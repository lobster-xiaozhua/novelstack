package server_test

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"novelstack/internal/chapterpkg"
	"novelstack/internal/devices"
)

// deviceClient 是一组「device token + 设备私钥」的测试夹具。
type deviceClient struct {
	token string
	key   *rsa.PrivateKey
	id    string
}

func newDeviceKey(t *testing.T) (*rsa.PrivateKey, string) {
	t.Helper()
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	pemStr, err := devices.EncodePublicKeyPEM(&priv.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	return priv, pemStr
}

// doBearer 发送带 device token 的请求。
func doBearer(t *testing.T, method, url, token string, body any) (int, []byte) {
	t.Helper()
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, url, rd)
	if err != nil {
		t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

// registerDevice 用用户名密码换 device token。
func registerDevice(t *testing.T, s *stack, name string) (*deviceClient, *rsa.PrivateKey) {
	t.Helper()
	priv, pemStr := newDeviceKey(t)
	code, body := doBearer(t, http.MethodPost, s.ts.URL+"/api/device/token", "", map[string]any{
		"username": "admin", "password": testPassword,
		"device_name": name, "public_key": pemStr,
	})
	if code != http.StatusOK {
		t.Fatalf("device token status=%d body=%s", code, body)
	}
	var out struct {
		Token  string         `json:"token"`
		Device map[string]any `json:"device"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatal(err)
	}
	if out.Token == "" || out.Device["id"] == nil {
		t.Fatalf("bad token response: %s", body)
	}
	return &deviceClient{token: out.Token, key: priv, id: out.Device["id"].(string)}, priv
}

func uploadThreeChapters(t *testing.T, s *stack) string {
	t.Helper()
	client := loginOK(t, s)
	content := "第一章 开始\n正文甲，用于校验第一章。\n" +
		"第二章 继续\n正文乙，用于校验第二章。\n" +
		"第三章 结束\n正文丙，用于校验第三章。\n"
	resp := uploadBook(t, client, s.ts.URL, "同步测试书", []byte(content), "sync.txt")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		t.Fatalf("upload status=%d body=%s", resp.StatusCode, bodyString(t, resp))
	}
	var out struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out.ID == "" {
		t.Fatal("upload response missing id")
	}
	return out.ID
}

func TestDeviceTokenLifecycle(t *testing.T) {
	requireFullStack(t)
	s := newStack(t)
	bookID := uploadThreeChapters(t, s)

	// 密码错误
	code, _ := doBearer(t, http.MethodPost, s.ts.URL+"/api/device/token", "", map[string]any{
		"username": "admin", "password": "wrong",
	})
	if code != http.StatusUnauthorized {
		t.Fatalf("wrong password status=%d", code)
	}

	dev, _ := registerDevice(t, s, "Pixel 8")

	// token 可用；/api/me 标记为 device
	code, body := doBearer(t, http.MethodGet, s.ts.URL+"/api/me", dev.token, nil)
	if code != http.StatusOK || !bytes.Contains(body, []byte(`"device"`)) {
		t.Fatalf("/api/me status=%d body=%s", code, body)
	}

	// device token 不能进控制台
	code, _ = doBearer(t, http.MethodGet, s.ts.URL+"/admin", dev.token, nil)
	if code != http.StatusForbidden {
		t.Fatalf("/admin with device token status=%d, want 403", code)
	}
	code, _ = doBearer(t, http.MethodGet, s.ts.URL+"/api/admin/dashboard", dev.token, nil)
	if code != http.StatusForbidden {
		t.Fatalf("dashboard with device token status=%d, want 403", code)
	}
	// 也不能用明文/写接口
	code, _ = doBearer(t, http.MethodGet, s.ts.URL+"/api/books/"+bookID+"/content", dev.token, nil)
	if code != http.StatusForbidden {
		t.Fatalf("content with device token status=%d, want 403", code)
	}
	code, _ = doBearer(t, http.MethodDelete, s.ts.URL+"/api/books/"+bookID, dev.token, nil)
	if code != http.StatusForbidden {
		t.Fatalf("delete with device token status=%d, want 403", code)
	}
	// 但书库与目录可读
	code, _ = doBearer(t, http.MethodGet, s.ts.URL+"/api/books", dev.token, nil)
	if code != http.StatusOK {
		t.Fatalf("book list with device token status=%d", code)
	}
	code, _ = doBearer(t, http.MethodGet, s.ts.URL+"/api/books/"+bookID+"/chapters", dev.token, nil)
	if code != http.StatusOK {
		t.Fatalf("chapters with device token status=%d", code)
	}

	// 管理端能看到设备
	client := loginOK(t, s)
	resp, err := client.Get(s.ts.URL + "/api/admin/devices")
	if err != nil {
		t.Fatal(err)
	}
	body = []byte(bodyString(t, resp))
	if resp.StatusCode != http.StatusOK || !bytes.Contains(body, []byte(dev.id)) {
		t.Fatalf("device list status=%d body=%s", resp.StatusCode, body)
	}
	if bytes.Contains(body, []byte(dev.token)) {
		t.Fatal("device list must not contain raw token")
	}

	// 撤销 → 401
	req, _ := http.NewRequest(http.MethodDelete, s.ts.URL+"/api/admin/devices/"+dev.id, nil)
	r2, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	r2.Body.Close()
	if r2.StatusCode != http.StatusOK {
		t.Fatalf("revoke status=%d", r2.StatusCode)
	}
	code, _ = doBearer(t, http.MethodGet, s.ts.URL+"/api/sync/manifest", dev.token, nil)
	if code != http.StatusUnauthorized {
		t.Fatalf("revoked token status=%d, want 401", code)
	}
	// 撤销不影响浏览器 session
	resp3, err := client.Get(s.ts.URL + "/api/me")
	if err != nil || resp3.StatusCode != http.StatusOK {
		t.Fatalf("session should survive device revoke: %v %d", err, resp3.StatusCode)
	}
	resp3.Body.Close()

	// 审计动作可见
	resp4, err := client.Get(s.ts.URL + "/api/admin/audit?action=device_&limit=20")
	if err != nil {
		t.Fatal(err)
	}
	ab := bodyString(t, resp4)
	if !strings.Contains(ab, "device_token_created") || !strings.Contains(ab, "device_revoked") {
		t.Fatalf("audit missing device actions: %s", ab)
	}
}

func TestPairingCodeFlow(t *testing.T) {
	requireFullStack(t)
	s := newStack(t)
	client := loginOK(t, s)
	resp, err := client.Post(s.ts.URL+"/api/admin/pairing", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("pairing status=%d", resp.StatusCode)
	}
	var pc struct {
		Code string `json:"code"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&pc); err != nil || pc.Code == "" {
		t.Fatalf("pairing decode: %v %+v", err, pc)
	}
	// device token 不能生成配对码
	if code, _ := doBearer(t, http.MethodPost, s.ts.URL+"/api/admin/pairing", "nsd_fake", nil); code != http.StatusForbidden {
		t.Fatalf("pairing with device token status=%d, want 403", code)
	}

	code, body := doBearer(t, http.MethodPost, s.ts.URL+"/api/device/token", "", map[string]any{
		"pairing_code": pc.Code, "device_name": "配对设备",
	})
	if code != http.StatusOK {
		t.Fatalf("redeem status=%d body=%s", code, body)
	}
	// 一次性
	code, _ = doBearer(t, http.MethodPost, s.ts.URL+"/api/device/token", "", map[string]any{
		"pairing_code": pc.Code,
	})
	if code != http.StatusUnauthorized {
		t.Fatalf("second redeem status=%d, want 401", code)
	}
}

func TestSyncManifestAndPrefetch(t *testing.T) {
	requireFullStack(t)
	s := newStack(t)
	bookID := uploadThreeChapters(t, s)
	dev, _ := registerDevice(t, s, "离线设备")

	code, body := doBearer(t, http.MethodGet, s.ts.URL+"/api/sync/manifest", dev.token, nil)
	if code != http.StatusOK {
		t.Fatalf("manifest status=%d body=%s", code, body)
	}
	var m struct {
		Cursor int64 `json:"cursor"`
		Full   bool  `json:"full"`
		Count  int   `json:"count"`
		Books  []struct {
			ID         string `json:"id"`
			WrappedDEK string `json:"wrapped_dek"`
			Chapters   []struct {
				Index int    `json:"index"`
				ETag  string `json:"etag"`
			} `json:"chapters"`
		} `json:"books"`
		Deleted []string `json:"deleted"`
	}
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatal(err)
	}
	if !m.Full || m.Count != 1 || len(m.Books) != 1 || len(m.Books[0].Chapters) != 3 {
		t.Fatalf("manifest = %+v", m)
	}
	if m.Books[0].WrappedDEK == "" {
		t.Fatal("wrapped_dek missing for device with public key")
	}
	dek, err := devices.UnwrapDEK(dev.key, m.Books[0].WrappedDEK)
	if err != nil || len(dek) != 32 {
		t.Fatalf("unwrap dek: %v len=%d", err, len(dek))
	}

	// 无变更
	code, body = doBearer(t, http.MethodGet,
		fmt.Sprintf("%s/api/sync/manifest?since=%d", s.ts.URL, m.Cursor), dev.token, nil)
	if code != http.StatusOK {
		t.Fatalf("incremental status=%d", code)
	}
	var m2 struct {
		Count   int      `json:"count"`
		Deleted []string `json:"deleted"`
		Cursor  int64    `json:"cursor"`
	}
	if err := json.Unmarshal(body, &m2); err != nil {
		t.Fatal(err)
	}
	if m2.Count != 0 || len(m2.Deleted) != 0 || m2.Cursor != m.Cursor {
		t.Fatalf("incremental should be empty: %+v", m2)
	}

	// 预取 3 章
	code, body = doBearer(t, http.MethodPost, s.ts.URL+"/api/books/"+bookID+"/chapters/prefetch", dev.token,
		map[string]any{"indexes": []int{0, 1, 2}})
	if code != http.StatusOK {
		t.Fatalf("prefetch status=%d body=%s", code, body)
	}
	var p struct {
		Count     int    `json:"count"`
		Bytes     int64  `json:"bytes"`
		Wrapped   string `json:"wrapped_dek"`
		Truncated bool   `json:"truncated"`
		Chapters  []struct {
			Index      int    `json:"index"`
			ETag       string `json:"etag"`
			Ciphertext string `json:"ciphertext"`
		} `json:"chapters"`
	}
	if err := json.Unmarshal(body, &p); err != nil {
		t.Fatal(err)
	}
	if p.Count != 3 || p.Bytes <= 0 || p.Truncated {
		t.Fatalf("prefetch = %+v", p)
	}
	if p.Wrapped == "" {
		t.Fatal("prefetch should include wrapped_dek")
	}
	pdek, err := devices.UnwrapDEK(dev.key, p.Wrapped)
	if err != nil || !bytes.Equal(pdek, dek) {
		t.Fatalf("prefetch dek differs: %v", err)
	}
	for _, ch := range p.Chapters {
		pkg, err := base64.StdEncoding.DecodeString(ch.Ciphertext)
		if err != nil {
			t.Fatal(err)
		}
		plain, err := chapterpkg.Open(dek, bookID, ch.Index, pkg)
		if err != nil {
			t.Fatalf("open chapter %d: %v", ch.Index, err)
		}
		want := []string{"正文甲", "正文乙", "正文丙"}[ch.Index]
		if !bytes.Contains(plain, []byte(want)) {
			t.Fatalf("chapter %d plaintext unexpected: %q", ch.Index, plain)
		}
		if ch.ETag != m.Books[0].Chapters[ch.Index].ETag {
			t.Fatalf("etag mismatch for chapter %d", ch.Index)
		}
	}

	// 超过单次上限 → 400
	code, body = doBearer(t, http.MethodPost, s.ts.URL+"/api/books/"+bookID+"/chapters/prefetch", dev.token,
		map[string]any{"indexes": []int{0, 1, 2, 3, 4, 5, 6, 7, 8, 9}})
	if code != http.StatusBadRequest {
		t.Fatalf("over-limit status=%d body=%s", code, body)
	}
	// 越界章节 → 400
	code, _ = doBearer(t, http.MethodPost, s.ts.URL+"/api/books/"+bookID+"/chapters/prefetch", dev.token,
		map[string]any{"indexes": []int{99}})
	if code != http.StatusBadRequest {
		t.Fatalf("out-of-range status=%d", code)
	}
}

func TestPrefetchRateLimit(t *testing.T) {
	requireFullStack(t)
	dir := t.TempDir()
	// buildStackWith 需要可用的 KEK 文件（NewStack 会写，这里手动准备）。
	kek := make([]byte, 32)
	if _, err := rand.Read(kek); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "kek"), kek, 0o600); err != nil {
		t.Fatal(err)
	}
	s := buildStackWith(t, dir, stackOpts{provider: nil, prefetchPerMinute: 1})
	bookID := uploadThreeChapters(t, s)
	dev, _ := registerDevice(t, s, "限流设备")

	code, _ := doBearer(t, http.MethodPost, s.ts.URL+"/api/books/"+bookID+"/chapters/prefetch", dev.token,
		map[string]any{"indexes": []int{0}})
	if code != http.StatusOK {
		t.Fatalf("first prefetch status=%d", code)
	}
	code, body := doBearer(t, http.MethodPost, s.ts.URL+"/api/books/"+bookID+"/chapters/prefetch", dev.token,
		map[string]any{"indexes": []int{1}})
	if code != http.StatusTooManyRequests {
		t.Fatalf("second prefetch status=%d body=%s, want 429", code, body)
	}
	// session 使用独立桶
	client := loginOK(t, s)
	req, _ := http.NewRequest(http.MethodPost, s.ts.URL+"/api/books/"+bookID+"/chapters/prefetch",
		strings.NewReader(`{"indexes":[0]}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("session prefetch status=%d, want 200", resp.StatusCode)
	}
}

func TestProgressVisibleOnDashboard(t *testing.T) {
	requireFullStack(t)
	s := newStack(t)
	bookID := uploadThreeChapters(t, s)
	dev, _ := registerDevice(t, s, "阅读器")

	code, body := doBearer(t, http.MethodPut, s.ts.URL+"/api/books/"+bookID+"/progress", dev.token,
		map[string]any{"chapter_index": 2, "char_offset": 7, "percent": 0.42})
	if code != http.StatusOK {
		t.Fatalf("put progress status=%d body=%s", code, body)
	}
	code, body = doBearer(t, http.MethodGet, s.ts.URL+"/api/books/"+bookID+"/progress", dev.token, nil)
	if code != http.StatusOK {
		t.Fatalf("get progress status=%d", code)
	}
	var got struct {
		ChapterIndex int     `json:"chapter_index"`
		CharOffset   int     `json:"char_offset"`
		Percent      float64 `json:"percent"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	if got.ChapterIndex != 2 || got.CharOffset != 7 || got.Percent < 0.41 || got.Percent > 0.43 {
		t.Fatalf("progress = %+v", got)
	}
	// 越界
	code, _ = doBearer(t, http.MethodPut, s.ts.URL+"/api/books/"+bookID+"/progress", dev.token,
		map[string]any{"chapter_index": 3, "char_offset": 1, "percent": 1.4})
	if code != http.StatusBadRequest {
		t.Fatalf("invalid progress status=%d", code)
	}

	client := loginOK(t, s)
	resp, err := client.Get(s.ts.URL + "/api/admin/dashboard")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var d struct {
		Mobile struct {
			ProgressBooks  int `json:"progress_books"`
			RecentProgress []struct {
				BookID string `json:"book_id"`
				Title  string `json:"title"`
			} `json:"recent_progress"`
			DevicesActive int `json:"devices_active"`
		} `json:"mobile"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&d); err != nil {
		t.Fatal(err)
	}
	if d.Mobile.ProgressBooks != 1 || len(d.Mobile.RecentProgress) != 1 ||
		d.Mobile.RecentProgress[0].BookID != bookID || d.Mobile.DevicesActive != 1 {
		t.Fatalf("dashboard mobile block = %+v", d.Mobile)
	}
}

func TestAudioCipherPath(t *testing.T) {
	requireFullStack(t)
	s := newStack(t)
	bookID := uploadThreeChapters(t, s)
	dev, _ := registerDevice(t, s, "听书设备")

	// 先合成（服务端加密落盘，不返回明文）
	code, body := doBearer(t, http.MethodPost,
		s.ts.URL+"/api/books/"+bookID+"/chapters/1/audio/prepare", dev.token, nil)
	if code != http.StatusOK {
		t.Fatalf("prepare status=%d body=%s", code, body)
	}
	var prep struct {
		Segments int `json:"segments"`
	}
	if err := json.Unmarshal(body, &prep); err != nil || prep.Segments == 0 {
		t.Fatalf("prepare = %+v %v", prep, err)
	}

	code, body = doBearer(t, http.MethodGet,
		s.ts.URL+"/api/books/"+bookID+"/chapters/1/audio", dev.token, nil)
	if code != http.StatusOK {
		t.Fatalf("audio manifest status=%d body=%s", code, body)
	}
	var m struct {
		Ready    bool `json:"ready"`
		Count    int  `json:"count"`
		Segments []struct {
			Key           string `json:"key"`
			CTOffset      int    `json:"ct_offset"`
			CTLen         int    `json:"ct_len"`
			WrappedDEK    string `json:"wrapped_dek"`
			AudioNonceB64 string `json:"audio_nonce"`
		} `json:"segments"`
	}
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatal(err)
	}
	if !m.Ready || m.Count == 0 {
		t.Fatalf("audio manifest not ready: %+v", m)
	}

	for _, seg := range m.Segments {
		audioDEK, err := devices.UnwrapDEK(dev.key, seg.WrappedDEK)
		if err != nil {
			t.Fatalf("unwrap audio dek: %v", err)
		}
		nonce, _ := base64.StdEncoding.DecodeString(seg.AudioNonceB64)
		code, raw := doBearer(t, http.MethodGet, s.ts.URL+"/api/tts/segments/"+seg.Key, dev.token, nil)
		if code != http.StatusOK {
			t.Fatalf("segment %s status=%d", seg.Key, code)
		}
		if len(raw) < seg.CTOffset+seg.CTLen {
			t.Fatalf("segment file too short: %d < %d", len(raw), seg.CTOffset+seg.CTLen)
		}
		block, _ := aes.NewCipher(audioDEK)
		aead, _ := cipher.NewGCM(block)
		wav, err := aead.Open(nil, nonce, raw[seg.CTOffset:seg.CTOffset+seg.CTLen],
			[]byte("novelstack-tts-audio-v1|"+seg.Key))
		if err != nil {
			t.Fatalf("decrypt audio: %v", err)
		}
		if len(wav) < 44 || string(wav[0:4]) != "RIFF" || string(wav[8:12]) != "WAVE" {
			t.Fatalf("decrypted audio is not WAV: %x", wav[:min(12, len(wav))])
		}
	}

	// 无设备公钥时：段仍可列出，但不给 wrapped_dek（无法解密）
	client := loginOK(t, s)
	resp, err := client.Get(s.ts.URL + "/api/books/" + bookID + "/chapters/1/audio")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var sm struct {
		Segments []struct {
			WrappedDEK string `json:"wrapped_dek"`
		} `json:"segments"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&sm); err != nil {
		t.Fatal(err)
	}
	if len(sm.Segments) == 0 || sm.Segments[0].WrappedDEK != "" {
		t.Fatalf("session manifest should omit wrapped_dek: %+v", sm.Segments)
	}

	// 未授权段 key 不能取
	code, _ = doBearer(t, http.MethodGet, s.ts.URL+"/api/tts/segments/not-a-real-key", dev.token, nil)
	if code != http.StatusNotFound && code != http.StatusBadRequest {
		t.Fatalf("unknown segment status=%d", code)
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
