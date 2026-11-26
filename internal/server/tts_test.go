package server_test

// 第二阶段 TTS 集成测试：缓存命中/未命中、缓存键隔离、篡改、flush、指标、鉴权。

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"novelstack/internal/tts"
)

// countingProvider 在 mock 基础上统计调用次数，用于验证缓存命中不重复合成。
type countingProvider struct {
	inner *tts.Mock
	calls int64
}

func (c *countingProvider) Name() string                     { return "mock:counting-v1" }
func (c *countingProvider) Health(ctx context.Context) error { return nil }
func (c *countingProvider) Synthesize(ctx context.Context, text, voice string, speed float64) ([]byte, error) {
	atomic.AddInt64(&c.calls, 1)
	return c.inner.Synthesize(ctx, text, voice, speed)
}

func (c *countingProvider) Calls() int64 { return atomic.LoadInt64(&c.calls) }

// newStackWith 在临时目录准备好 KEK 后，按自定义 TTS 配置装配测试栈。
func newStackWith(t *testing.T, opts stackOpts) *stack {
	t.Helper()
	dir := t.TempDir()
	kek := make([]byte, 32)
	if _, err := rand.Read(kek); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "kek"), kek, 0o600); err != nil {
		t.Fatal(err)
	}
	return buildStackWith(t, dir, opts)
}

// 两个章节，每章 3 个以上片段（默认 100 字/片）。
const ttsBookText = `第一章 夜航船
他推开门，屋里很暗。窗外的雨还在下，檐角的水一滴一滴落进石臼里。他摸到桌边坐下，把湿透的外衣挂在椅背上，听见楼上有人咳嗽。那声音很轻，像是怕惊动了谁。他抬起头，看见楼梯转角处有一盏灯，灯下站着一个人影，正低头看着他。
他没有说话，只是把手伸进口袋，摸到那枚冰凉的铜钥匙。钥匙上刻着三个字：夜航船。那是父亲留下的东西，也是他此行的全部理由。
第二章 归途
天亮的时候，雨停了。他沿着河堤往回走，河面上浮着一层薄雾。远处有人在撑船，船桨划开水面的声音很轻，一下，又一下。
他想起昨晚那个人影说过的话：船不开往对岸，只开往从前。他笑了笑，觉得这话很像父亲的口气。父亲一生都在水上，最后也留在水上。
走到渡口时，他把铜钥匙扔进了河里。水面荡开一圈涟漪，很快就恢复了平静。
`

// speak 发起一次朗读请求。
func speak(t *testing.T, client *http.Client, baseURL, bookID string, chapter int, query string) *http.Response {
	t.Helper()
	url := fmt.Sprintf("%s/api/books/%s/chapters/%d/speak%s", baseURL, bookID, chapter, query)
	resp, err := client.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func uploadTTSBook(t *testing.T, client *http.Client, baseURL string) string {
	t.Helper()
	resp := uploadBook(t, client, baseURL, "夜航船（TTS）", []byte(ttsBookText), "tts.txt")
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("upload: %d %s", resp.StatusCode, bodyString(t, resp))
	}
	var meta struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(bodyBytes(t, resp), &meta); err != nil {
		t.Fatal(err)
	}
	return meta.ID
}

// 6. 未登录访问朗读接口返回 401。
func TestSpeakRequiresAuth(t *testing.T) {
	requireFullStack(t)
	s := newStack(t)
	client := loginOK(t, s)
	bookID := uploadTTSBook(t, client, s.ts.URL)

	anon := &http.Client{}
	for _, path := range []string{
		fmt.Sprintf("/api/books/%s/chapters", bookID),
		fmt.Sprintf("/api/books/%s/chapters/1/speak", bookID),
	} {
		resp, err := anon.Get(s.ts.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("%s: want 401, got %d", path, resp.StatusCode)
		}
	}
}

// 章节索引接口：返回章节且不含正文。
func TestChaptersEndpoint(t *testing.T) {
	requireFullStack(t)
	s := newStack(t)
	client := loginOK(t, s)
	bookID := uploadTTSBook(t, client, s.ts.URL)

	resp, err := client.Get(s.ts.URL + "/api/books/" + bookID + "/chapters")
	if err != nil {
		t.Fatal(err)
	}
	body := bodyBytes(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d body=%s", resp.StatusCode, body)
	}
	var out struct {
		Chapters []struct {
			Index int    `json:"index"`
			Title string `json:"title"`
			Chars int    `json:"chars"`
		} `json:"chapters"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Chapters) != 2 {
		t.Fatalf("预期 2 章, got %d", len(out.Chapters))
	}
	if out.Chapters[0].Title != "第一章 夜航船" || out.Chapters[1].Title != "第二章 归途" {
		t.Fatalf("标题异常: %+v", out.Chapters)
	}
	if strings.Contains(string(body), "他推开门") {
		t.Fatal("章节接口不应返回正文")
	}
}

// 2. 同一片段连续请求两次：第二次全部命中缓存，Provider 调用次数不增加。
// 同时校验输出为 WAV 片段流、磁盘为密文（非 RIFF）。
func TestSpeakCacheHitAvoidsResynthesis(t *testing.T) {
	requireFullStack(t)
	cp := &countingProvider{inner: tts.NewMock(0)}
	s := newStackWith(t, stackOpts{provider: cp})
	client := loginOK(t, s)
	bookID := uploadTTSBook(t, client, s.ts.URL)

	first := speak(t, client, s.ts.URL, bookID, 1, "")
	firstBody := bodyBytes(t, first)
	if first.StatusCode != http.StatusOK {
		t.Fatalf("first speak: %d %s", first.StatusCode, firstBody)
	}
	if !bytes.HasPrefix(firstBody, []byte("RIFF")) {
		t.Fatalf("响应不是 WAV：前 8 字节 %q", firstBody[:8])
	}
	segs := bytes.Count(firstBody, []byte("RIFF"))
	if segs < 2 {
		t.Fatalf("预期多个 WAV 片段, got %d", segs)
	}
	callsAfterFirst := cp.Calls()
	if callsAfterFirst != int64(segs) {
		t.Fatalf("首轮合成次数 %d 与片段数 %d 不一致", callsAfterFirst, segs)
	}
	if got := first.Header.Get("X-TTS-Stream"); got != "wav-segments" {
		t.Fatalf("缺少流式标记: %q", got)
	}

	second := speak(t, client, s.ts.URL, bookID, 1, "")
	secondBody := bodyBytes(t, second)
	if second.StatusCode != http.StatusOK {
		t.Fatalf("second speak: %d", second.StatusCode)
	}
	if cp.Calls() != callsAfterFirst {
		t.Fatalf("命中缓存仍触发合成: %d -> %d", callsAfterFirst, cp.Calls())
	}
	// 命中缓存时返回的音频应与首次完全一致
	if !bytes.Equal(firstBody, secondBody) {
		t.Fatal("缓存命中返回的音频与首次不一致")
	}

	// 3. 缓存目录中必须是密文，不能出现 RIFF 明文头
	files := listCacheFiles(t, s.dataDir)
	if len(files) == 0 {
		t.Fatal("缓存目录为空")
	}
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.HasPrefix(raw, []byte("RIFF")) {
			t.Fatalf("缓存文件为明文 WAV: %s", f)
		}
		if !bytes.HasPrefix(raw, []byte("NSAC")) {
			t.Fatalf("缓存文件缺少密文 magic: %s", f)
		}
	}
}

// 4. 错误 voice / speed 不命中其他缓存。
func TestSpeakCacheKeyIsolation(t *testing.T) {
	requireFullStack(t)
	cp := &countingProvider{inner: tts.NewMock(0)}
	s := newStackWith(t, stackOpts{provider: cp})
	client := loginOK(t, s)
	bookID := uploadTTSBook(t, client, s.ts.URL)

	base := speak(t, client, s.ts.URL, bookID, 1, "?voice=low&speed=1.0")
	baseBody := bodyBytes(t, base)
	n1 := cp.Calls()
	if base.StatusCode != http.StatusOK || n1 == 0 {
		t.Fatalf("base: %d", base.StatusCode)
	}

	// 换 voice：应重新合成
	v := speak(t, client, s.ts.URL, bookID, 1, "?voice=high&speed=1.0")
	vBody := bodyBytes(t, v)
	if v.StatusCode != http.StatusOK {
		t.Fatalf("voice=high: %d", v.StatusCode)
	}
	n2 := cp.Calls()
	if n2 <= n1 {
		t.Fatalf("换 voice 未重新合成: %d -> %d", n1, n2)
	}
	if bytes.Equal(baseBody, vBody) {
		t.Fatal("不同 voice 的音频不应相同")
	}

	// 换 speed：应重新合成
	sp := speak(t, client, s.ts.URL, bookID, 1, "?voice=high&speed=1.5")
	bodyBytes(t, sp)
	n3 := cp.Calls()
	if n3 <= n2 {
		t.Fatalf("换 speed 未重新合成: %d -> %d", n2, n3)
	}

	// 重复相同参数：全部命中
	again := speak(t, client, s.ts.URL, bookID, 1, "?voice=high&speed=1.5")
	bodyBytes(t, again)
	if cp.Calls() != n3 {
		t.Fatalf("相同参数应命中缓存: %d -> %d", n3, cp.Calls())
	}

	// 缓存文件数量应为三组参数的并集
	files := listCacheFiles(t, s.dataDir)
	if len(files) < 3 {
		t.Fatalf("缓存片段数异常: %d", len(files))
	}
}

// 5. 首片优先：模拟每片 120ms 合成延迟，首字节应在 300ms 内到达。
func TestSpeakFirstByteArrivesBeforeWholeChapter(t *testing.T) {
	requireFullStack(t)
	cp := &countingProvider{inner: tts.NewMock(120 * time.Millisecond)}
	s := newStackWith(t, stackOpts{provider: cp})
	client := loginOK(t, s)
	bookID := uploadTTSBook(t, client, s.ts.URL)

	start := time.Now()
	resp := speak(t, client, s.ts.URL, bookID, 1, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	buf := make([]byte, 1)
	n, err := io.ReadFull(resp.Body, buf)
	firstByteAt := time.Since(start)
	if err != nil || n != 1 {
		t.Fatalf("读取首字节失败: %v", err)
	}
	// 读完全部
	rest, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	total := time.Since(start)

	if firstByteAt > 300*time.Millisecond {
		t.Fatalf("首字节过慢: %v（整章 %v）", firstByteAt, total)
	}
	if total < 240*time.Millisecond {
		t.Fatalf("总耗时异常（延迟未生效？）: %v", total)
	}
	// 首片之后仍有片段在合成：说明是增量推送而非整章攒完再发
	if total-firstByteAt < 80*time.Millisecond {
		t.Fatalf("首字节与整章完成过于接近，疑似未增量推送: first=%v total=%v", firstByteAt, total)
	}
	if len(rest) == 0 {
		t.Fatal("响应体为空")
	}
}

// 7. 篡改缓存片段 → 朗读返回 409，且不输出任何音频字节。
func TestSpeakDetectsTamperedCache(t *testing.T) {
	requireFullStack(t)
	cp := &countingProvider{inner: tts.NewMock(0)}
	s := newStackWith(t, stackOpts{provider: cp})
	client := loginOK(t, s)
	bookID := uploadTTSBook(t, client, s.ts.URL)

	r := speak(t, client, s.ts.URL, bookID, 1, "")
	bodyBytes(t, r)
	if r.StatusCode != http.StatusOK {
		t.Fatalf("warmup: %d", r.StatusCode)
	}

	files := listCacheFiles(t, s.dataDir)
	if len(files) == 0 {
		t.Fatal("无缓存文件")
	}
	p := files[0]
	raw, _ := os.ReadFile(p)
	raw[len(raw)-3] ^= 0x01 // 篡改密文尾部
	if err := os.WriteFile(p, raw, 0o600); err != nil {
		t.Fatal(err)
	}

	before := cp.Calls()
	bad := speak(t, client, s.ts.URL, bookID, 1, "")
	badBody := bodyBytes(t, bad)
	if bad.StatusCode != http.StatusConflict {
		t.Fatalf("篡改后应 409, got %d body=%s", bad.StatusCode, badBody)
	}
	if bytes.Contains(badBody, []byte("RIFF")) {
		t.Fatal("响应中不应出现任何音频字节")
	}
	if cp.Calls() != before {
		t.Fatal("篡改不应静默重新合成（会掩盖破坏行为）")
	}

	// 审计留痕
	au, _ := client.Get(s.ts.URL + "/api/admin/audit?limit=50")
	auBody := bodyString(t, au)
	au.Body.Close()
	if !strings.Contains(auBody, "tts_cache_tamper") {
		t.Fatalf("审计缺少篡改记录: %s", auBody)
	}
}

// 指标接口：缓存规模、成功率、Provider 健康。
func TestTTSStats(t *testing.T) {
	requireFullStack(t)
	cp := &countingProvider{inner: tts.NewMock(0)}
	s := newStackWith(t, stackOpts{provider: cp})
	client := loginOK(t, s)
	bookID := uploadTTSBook(t, client, s.ts.URL)
	bodyBytes(t, speak(t, client, s.ts.URL, bookID, 0, ""))

	resp, err := client.Get(s.ts.URL + "/api/admin/tts/stats?limit=50")
	if err != nil {
		t.Fatal(err)
	}
	body := bodyBytes(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d body=%s", resp.StatusCode, body)
	}
	var st struct {
		Provider struct {
			Kind   string `json:"kind"`
			Name   string `json:"name"`
			Health string `json:"health"`
		} `json:"provider"`
		Cache struct {
			Segments    int   `json:"segments"`
			CipherBytes int64 `json:"cipher_bytes"`
		} `json:"cache"`
		Synth struct {
			Total       int     `json:"total"`
			SuccessRate float64 `json:"success_rate"`
			AvgMs       float64 `json:"avg_ms"`
		} `json:"synth"`
		Config struct {
			MaxSegmentChars int `json:"max_segment_chars"`
		} `json:"config"`
	}
	if err := json.Unmarshal(body, &st); err != nil {
		t.Fatal(err)
	}
	if st.Provider.Health != "ok" || st.Provider.Name == "" {
		t.Fatalf("provider 指标异常: %+v", st.Provider)
	}
	if st.Cache.Segments == 0 || st.Cache.CipherBytes == 0 {
		t.Fatalf("缓存指标异常: %+v", st.Cache)
	}
	if st.Synth.Total == 0 || st.Synth.SuccessRate != 1 {
		t.Fatalf("合成指标异常: %+v", st.Synth)
	}
	if st.Config.MaxSegmentChars <= 0 {
		t.Fatalf("配置指标异常: %+v", st.Config)
	}
	if strings.Contains(string(body), "他推开门") {
		t.Fatal("指标接口不应包含正文")
	}
}

// SSE 变体：事件流中包含 segment（base64 音频）与 done。
func TestSpeakSSEVariant(t *testing.T) {
	requireFullStack(t)
	cp := &countingProvider{inner: tts.NewMock(0)}
	s := newStackWith(t, stackOpts{provider: cp})
	client := loginOK(t, s)
	bookID := uploadTTSBook(t, client, s.ts.URL)

	resp := speak(t, client, s.ts.URL, bookID, 0, "?mode=sse")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("Content-Type=%q", ct)
	}
	sc := bufio.NewScanner(resp.Body)
	// SSE 单行包含整段 base64 音频，需放大扫描缓冲
	sc.Buffer(make([]byte, 0, 64<<10), 4<<20)
	var events []string
	var audio []byte
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "event: ") {
			events = append(events, strings.TrimPrefix(line, "event: "))
		}
		if strings.HasPrefix(line, "data: ") && strings.Contains(line, `"audio"`) {
			var payload struct {
				Audio string `json:"audio"`
			}
			if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &payload); err != nil {
				t.Fatalf("解析 segment 事件失败: %v", err)
			}
			b, err := base64.StdEncoding.DecodeString(payload.Audio)
			if err != nil {
				t.Fatal(err)
			}
			audio = append(audio, b...)
		}
	}
	if len(events) < 3 {
		t.Fatalf("事件过少: %v", events)
	}
	if events[0] != "start" || events[len(events)-1] != "done" {
		t.Fatalf("事件序列异常: %v", events)
	}
	if !bytes.HasPrefix(audio, []byte("RIFF")) {
		t.Fatal("SSE 音频不是 WAV")
	}
}

// 合成失败：对外返回通用错误，不泄漏引擎细节，审计落痕。
func TestSpeakSynthesisFailureIsOpaque(t *testing.T) {
	requireFullStack(t)
	cp := &countingProvider{inner: tts.NewMock(0)}
	cp.inner.FailOn = "父亲" // 第二章第 1 个片段含此词
	s := newStackWith(t, stackOpts{provider: cp})
	client := loginOK(t, s)
	bookID := uploadTTSBook(t, client, s.ts.URL)

	resp := speak(t, client, s.ts.URL, bookID, 1, "")
	body := bodyString(t, resp)
	if resp.StatusCode != http.StatusOK {
		// 首片即失败时允许直接错误码
		if resp.StatusCode < 500 && resp.StatusCode != http.StatusBadGateway {
			t.Fatalf("异常状态: %d %s", resp.StatusCode, body)
		}
		if strings.Contains(body, "engine") || strings.Contains(body, "mock") {
			t.Fatalf("错误信息泄漏引擎细节: %s", body)
		}
	}

	au, _ := client.Get(s.ts.URL + "/api/admin/audit?limit=50")
	auBody := bodyString(t, au)
	au.Body.Close()
	if !strings.Contains(auBody, "tts_speak_partial") && !strings.Contains(auBody, "tts_synth") {
		t.Fatalf("审计缺少合成失败记录: %s", auBody)
	}
	// 书名等元数据可以出现，但章节正文不得进入审计
	if strings.Contains(auBody, "他推开门") || strings.Contains(auBody, "父亲一生都在水上") {
		t.Fatal("审计不应包含章节正文")
	}
}

// 不存在的书/章节：404，且不泄漏内部信息。
func TestSpeakNotFound(t *testing.T) {
	requireFullStack(t)
	s := newStack(t)
	client := loginOK(t, s)
	for _, u := range []string{
		"/api/books/nonexistent/chapters/0/speak",
		"/api/books/nonexistent/chapters",
	} {
		resp, err := client.Get(s.ts.URL + u)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("%s: want 404, got %d", u, resp.StatusCode)
		}
	}
}

func listCacheFiles(t *testing.T, dataDir string) []string {
	t.Helper()
	root := filepath.Join(dataDir, "tts-cache")
	var out []string
	err := filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if !info.IsDir() && strings.HasSuffix(p, ".bin") {
			out = append(out, p)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}
