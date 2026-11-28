package server_test

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"novelstack/internal/auth"
	"novelstack/internal/chapterpkg"
	"novelstack/internal/devices"
)

// stage12 验收：多端进度合并、WebView 免密票据、浏览器 Web Crypto 解密链路。

func postJSON(t *testing.T, client *http.Client, url string, payload any) *http.Response {
	t.Helper()
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func uploadOneBook(t *testing.T, client *http.Client, baseURL, title string) string {
	t.Helper()
	content := []byte("第一章 起风\n山雨欲来。\n\n第二章 落雪\n雪落无声。\n\n第三章 归途\n长路漫漫。\n")
	resp := uploadBook(t, client, baseURL, title, content, "sync.txt")
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		t.Fatalf("upload status=%d body=%s", resp.StatusCode, string(bodyBytes(t, resp)))
	}
	var got struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(bodyBytes(t, resp), &got); err != nil {
		t.Fatal(err)
	}
	if got.ID == "" {
		t.Fatal("upload returned empty id")
	}
	return got.ID
}

// TestSyncProgressMergeKeepsFurthest 验证合并算法：位置最靠后的一页获胜，
// 与写入先后无关——这是「多端同时看一本书最终对齐」的核心保证。
func TestSyncProgressMergeKeepsFurthest(t *testing.T) {
	s := newStack(t)
	client := loginOK(t, s)
	bookID := uploadOneBook(t, client, s.ts.URL, "同步之书")

	push := func(chapter, offset int) (applied int, stored struct {
		ChapterIndex int `json:"chapter_index"`
		CharOffset   int `json:"char_offset"`
	}) {
		t.Helper()
		resp := postJSON(t, client, s.ts.URL+"/api/sync/progress", map[string]any{
			"items": []map[string]any{{
				"book_id": bookID, "chapter_index": chapter, "char_offset": offset,
				"percent": 0.1, "device_id": "phone",
			}},
		})
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("push status=%d body=%s", resp.StatusCode, string(bodyBytes(t, resp)))
		}
		var out struct {
			Applied int `json:"applied"`
			Items   []struct {
				ChapterIndex int `json:"chapter_index"`
				CharOffset   int `json:"char_offset"`
			} `json:"items"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			resp.Body.Close()
			t.Fatal(err)
		}
		resp.Body.Close()
		if len(out.Items) != 1 {
			t.Fatalf("want 1 item, got %d", len(out.Items))
		}
		stored.ChapterIndex = out.Items[0].ChapterIndex
		stored.CharOffset = out.Items[0].CharOffset
		return out.Applied, stored
	}

	// 手机先读到第 2 章。
	if applied, st := push(1, 40); applied != 1 || st.ChapterIndex != 1 || st.CharOffset != 40 {
		t.Fatalf("first push applied=%d stored=%+v", applied, st)
	}
	// 平板更晚上报，但位置更靠前（第 1 章 10 字）——必须被拒绝，不能把进度拽回去。
	if applied, st := push(0, 10); applied != 0 || st.ChapterIndex != 1 {
		t.Fatalf("older position overwrote: applied=%d stored=%+v", applied, st)
	}
	// 平板继续读到第 3 章——采纳。
	if applied, st := push(2, 5); applied != 1 || st.ChapterIndex != 2 || st.CharOffset != 5 {
		t.Fatalf("newer position not applied: applied=%d stored=%+v", applied, st)
	}
	// 同章更靠后的偏移——采纳。
	if applied, st := push(2, 30); applied != 1 || st.CharOffset != 30 {
		t.Fatalf("same chapter further offset not applied: applied=%d stored=%+v", applied, st)
	}
	// 同章更靠前——拒绝。
	if applied, _ := push(2, 12); applied != 0 {
		t.Fatalf("same chapter earlier offset applied=%d", applied)
	}

	// 增量拉取：给定一个很早的游标，应能拿到这条记录。
	resp, err := client.Get(s.ts.URL + "/api/sync/progress?since=" + time.Now().UTC().Add(-time.Hour).Format(time.RFC3339Nano))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var got struct {
		Cursor string `json:"cursor"`
		Items  []struct {
			BookID       string `json:"book_id"`
			ChapterIndex int    `json:"chapter_index"`
			CharOffset   int    `json:"char_offset"`
		} `json:"items"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.Cursor == "" || len(got.Items) != 1 || got.Items[0].ChapterIndex != 2 || got.Items[0].CharOffset != 30 {
		t.Fatalf("incremental pull = %+v", got)
	}
}

// TestSyncProgressRejectsUnknownBook 验证过期书架条目被静默跳过而不是整批报错。
func TestSyncProgressRejectsUnknownBook(t *testing.T) {
	s := newStack(t)
	client := loginOK(t, s)
	resp := postJSON(t, client, s.ts.URL+"/api/sync/progress", map[string]any{
		"items": []map[string]any{
			{"book_id": "no-such-book", "chapter_index": 0, "char_offset": 0, "percent": 0},
		},
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	var out struct {
		Applied int `json:"applied"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out.Applied != 0 {
		t.Fatalf("applied=%d for unknown book", out.Applied)
	}
}

// TestWebGrantSingleUse 验证一次性票据：可用一次、过期/重复兑换失败。
func TestWebGrantSingleUse(t *testing.T) {
	s := newStack(t)
	client := loginOK(t, s)

	resp := postJSON(t, client, s.ts.URL+"/api/device/web-grant", map[string]any{})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("grant status=%d", resp.StatusCode)
	}
	var grant struct {
		Grant string `json:"grant"`
		URL   string `json:"url"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&grant); err != nil {
		t.Fatal(err)
	}
	if grant.Grant == "" || grant.URL == "" {
		t.Fatalf("empty grant: %+v", grant)
	}

	// 新客户端（无 cookie）兑换票据。
	fresh := &http.Client{}
	rresp := postJSON(t, fresh, s.ts.URL+"/api/login/grant", map[string]string{"grant": grant.Grant})
	if rresp.StatusCode != http.StatusOK {
		t.Fatalf("redeem status=%d body=%s", rresp.StatusCode, string(bodyBytes(t, rresp)))
	}
	var cookie *http.Cookie
	for _, c := range rresp.Cookies() {
		if c.Name == auth.SessionCookieName {
			cookie = c
		}
	}
	rresp.Body.Close()
	if cookie == nil {
		t.Fatal("no session cookie issued")
	}

	// 用新会话访问 /api/me。
	req, _ := http.NewRequest(http.MethodGet, s.ts.URL+"/api/me", nil)
	req.AddCookie(cookie)
	me, err := fresh.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer me.Body.Close()
	if me.StatusCode != http.StatusOK {
		t.Fatalf("me status=%d", me.StatusCode)
	}

	// 重复兑换同一票据必须失败。
	again := postJSON(t, fresh, s.ts.URL+"/api/login/grant", map[string]string{"grant": grant.Grant})
	defer again.Body.Close()
	if again.StatusCode != http.StatusUnauthorized {
		t.Fatalf("reused grant status=%d, want 401", again.StatusCode)
	}
}

// TestWebLibraryAndChapterDecrypt 端到端验证浏览器路径：
// 生成 RSA-OAEP 密钥 → 服务端用公钥包装 DEK → 浏览器私钥解包 → AES-GCM 解章节。
func TestWebLibraryAndChapterDecrypt(t *testing.T) {
	s := newStack(t)
	client := loginOK(t, s)
	bookID := uploadOneBook(t, client, s.ts.URL, "浏览器之书")

	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	pubPEM, err := devices.EncodePublicKeyPEM(&priv.PublicKey)
	if err != nil {
		t.Fatal(err)
	}

	resp := postJSON(t, client, s.ts.URL+"/api/web/library", map[string]string{"public_key": pubPEM})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("library status=%d body=%s", resp.StatusCode, string(bodyBytes(t, resp)))
	}
	var lib struct {
		Manifest struct {
			Books []struct {
				ID         string `json:"id"`
				WrappedDEK string `json:"wrapped_dek"`
				Chapters   []struct {
					Index int `json:"index"`
				} `json:"chapters"`
			} `json:"books"`
		} `json:"manifest"`
		Progress []struct {
			BookID string `json:"book_id"`
		} `json:"progress"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&lib); err != nil {
		resp.Body.Close()
		t.Fatal(err)
	}
	resp.Body.Close()
	if len(lib.Manifest.Books) != 1 {
		t.Fatalf("books=%d", len(lib.Manifest.Books))
	}
	book := lib.Manifest.Books[0]
	if book.ID != bookID || book.WrappedDEK == "" || len(book.Chapters) != 3 {
		t.Fatalf("bad book entry: %+v", book)
	}

	// 私钥解包 DEK（与浏览器 Web Crypto 的 RSA-OAEP-SHA256 等价）。
	ct, err := base64.StdEncoding.DecodeString(book.WrappedDEK)
	if err != nil {
		t.Fatal(err)
	}
	dek, err := rsa.DecryptOAEP(sha256.New(), nil, priv, ct, nil)
	if err != nil {
		t.Fatalf("unwrap dek: %v", err)
	}
	if len(dek) != 32 {
		t.Fatalf("dek len=%d", len(dek))
	}

	// 拉章节密文并解密。
	cresp := postJSON(t, client, s.ts.URL+"/api/web/chapters", map[string]any{
		"book_id": bookID, "indexes": []int{0, 2}, "public_key": pubPEM,
	})
	if cresp.StatusCode != http.StatusOK {
		t.Fatalf("chapters status=%d body=%s", cresp.StatusCode, string(bodyBytes(t, cresp)))
	}
	var chap struct {
		Count    int `json:"count"`
		Chapters []struct {
			Index      int    `json:"index"`
			Ciphertext string `json:"ciphertext"`
		} `json:"chapters"`
	}
	if err := json.NewDecoder(cresp.Body).Decode(&chap); err != nil {
		cresp.Body.Close()
		t.Fatal(err)
	}
	cresp.Body.Close()
	if chap.Count != 2 {
		t.Fatalf("count=%d", chap.Count)
	}
	for _, c := range chap.Chapters {
		pkg, err := base64.StdEncoding.DecodeString(c.Ciphertext)
		if err != nil {
			t.Fatal(err)
		}
		plain, err := chapterpkg.Open(dek, bookID, c.Index, pkg)
		if err != nil {
			t.Fatalf("open chapter %d: %v", c.Index, err)
		}
		if len(plain) == 0 {
			t.Fatalf("chapter %d empty", c.Index)
		}
	}
}
