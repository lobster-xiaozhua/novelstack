package server_test

// 第九阶段集成测试：服务端授时（忽略客户端时间）、阅读时长累计、
// 正文净化、数据导出二次验证、软删除后的音频缓存回收。

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"novelstack/internal/tts"
)

func doJSON(t *testing.T, client *http.Client, method, url string, payload any) *http.Response {
	t.Helper()
	var body io.Reader
	if payload != nil {
		raw, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, url, body)
	if err != nil {
		t.Fatal(err)
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

// 验收 A2：篡改客户端时间不影响服务端记录的更新时间，且阅读时长被累计。
func TestProgressIgnoresClientClock(t *testing.T) {
	requireFullStack(t)
	s := newStack(t)
	client := loginOK(t, s)
	bookID := uploadTTSBook(t, client, s.ts.URL)

	resp := doJSON(t, client, http.MethodPut,
		fmt.Sprintf("%s/api/books/%s/progress", s.ts.URL, bookID),
		map[string]any{
			"chapter_index": 1,
			"char_offset":   10,
			"percent":       0.42,
			"device_id":     "dev-clock-test",
			// 客户端自称的时间是 2000 年：服务端必须完全忽略。
			"updated_at":   "2000-01-01T00:00:00Z",
			"read_seconds": 120,
		})
	body := bodyBytes(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d body=%s", resp.StatusCode, body)
	}
	var out struct {
		Progress struct {
			UpdatedAt string `json:"updated_at"`
		} `json:"progress"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatal(err)
	}
	got, err := time.Parse(time.RFC3339Nano, out.Progress.UpdatedAt)
	if err != nil {
		t.Fatalf("updated_at 格式异常 %q: %v", out.Progress.UpdatedAt, err)
	}
	if d := time.Since(got); d < -5*time.Second || d > 60*time.Second {
		t.Fatalf("updated_at 不是服务器当前时间: %s (差 %s)", out.Progress.UpdatedAt, d)
	}

	// GET 回来的也必须是服务端时间。
	resp2 := doJSON(t, client, http.MethodGet,
		fmt.Sprintf("%s/api/books/%s/progress", s.ts.URL, bookID), nil)
	body2 := bodyBytes(t, resp2)
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("get status=%d body=%s", resp2.StatusCode, body2)
	}
	if !strings.Contains(string(body2), out.Progress.UpdatedAt) {
		t.Fatalf("GET 返回的更新时间与 PUT 不一致: %s vs %s", body2, out.Progress.UpdatedAt)
	}

	// 阅读时长进入统计。
	resp3 := doJSON(t, client, http.MethodGet, s.ts.URL+"/api/stats/reading", nil)
	body3 := bodyBytes(t, resp3)
	if resp3.StatusCode != http.StatusOK {
		t.Fatalf("stats status=%d body=%s", resp3.StatusCode, body3)
	}
	var st struct {
		TodaySeconds int64 `json:"today_seconds"`
		TotalSeconds int64 `json:"total_seconds"`
		StreakDays   int   `json:"streak_days"`
		Days         []struct {
			Day     string `json:"day"`
			Seconds int64  `json:"seconds"`
		} `json:"days"`
	}
	if err := json.Unmarshal(body3, &st); err != nil {
		t.Fatal(err)
	}
	if st.TodaySeconds < 120 {
		t.Fatalf("今日时长 = %d，应 >= 120", st.TodaySeconds)
	}
	if st.StreakDays != 1 {
		t.Fatalf("连续天数 = %d", st.StreakDays)
	}
	if len(st.Days) != 90 {
		t.Fatalf("日历窗口 = %d，应为 90", len(st.Days))
	}
}

// 验收 B1：配置一条正则后，解密输出里该句消失，其余正文不受影响。
func TestPurgeRemovesAdsFromChapterPlain(t *testing.T) {
	requireFullStack(t)
	s := newStackWith(t, stackOpts{
		provider:   tts.NewMock(0),
		purgeRules: []string{`请记住本站[^\n]*`},
	})
	client := loginOK(t, s)
	text := "第一章 开端\n他推开门，屋里很暗。\n请记住本站域名 novel.example.com\n他坐下了。\n"
	resp := uploadBook(t, client, s.ts.URL, "净化测试", []byte(text), "purge.txt")
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("upload: %d %s", resp.StatusCode, bodyString(t, resp))
	}
	var meta struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(bodyBytes(t, resp), &meta); err != nil {
		t.Fatal(err)
	}

	plain := doJSON(t, client, http.MethodGet,
		fmt.Sprintf("%s/api/books/%s/chapters/0/plain", s.ts.URL, meta.ID), nil)
	body := bodyString(t, plain)
	if strings.Contains(body, "请记住本站") {
		t.Fatalf("广告句未被净化:\n%s", body)
	}
	if !strings.Contains(body, "他推开门") || !strings.Contains(body, "他坐下了") {
		t.Fatalf("正文被误删:\n%s", body)
	}
	if strings.Contains(body, "\n\n\n") {
		t.Fatalf("净化后残留多余空行:\n%q", body)
	}
}

// 验收 C3：导出必须二次验证密码；正确密码返回 ZIP，内容是解密后的正文。
func TestExportRequiresPasswordAndProducesZip(t *testing.T) {
	requireFullStack(t)
	s := newStackWith(t, stackOpts{
		provider:   tts.NewMock(0),
		purgeRules: []string{`请记住本站[^\n]*`},
	})
	client := loginOK(t, s)
	text := "第一章 开端\n他推开门。\n请记住本站广告\n风很大。\n"
	resp := uploadBook(t, client, s.ts.URL, "导出测试", []byte(text), "export.txt")
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("upload: %d %s", resp.StatusCode, bodyString(t, resp))
	}

	denied := doJSON(t, client, http.MethodPost, s.ts.URL+"/api/admin/export",
		map[string]any{"password": "wrong-password", "format": "txt"})
	denied.Body.Close()
	if denied.StatusCode != http.StatusUnauthorized {
		t.Fatalf("错误密码应 401，实际 %d", denied.StatusCode)
	}

	ok := doJSON(t, client, http.MethodPost, s.ts.URL+"/api/admin/export",
		map[string]any{"password": testPassword, "format": "txt"})
	raw := bodyBytes(t, ok)
	if ok.StatusCode != http.StatusOK {
		t.Fatalf("status=%d body=%s", ok.StatusCode, raw)
	}
	if ct := ok.Header.Get("Content-Type"); ct != "application/zip" {
		t.Fatalf("Content-Type = %s", ct)
	}
	zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		t.Fatalf("zip: %v", err)
	}
	var found bool
	for _, f := range zr.File {
		if !strings.HasSuffix(f.Name, ".txt") {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		content, _ := io.ReadAll(rc)
		rc.Close()
		found = true
		if !strings.Contains(string(content), "他推开门") {
			t.Fatalf("导出内容缺正文: %s", content)
		}
		if strings.Contains(string(content), "请记住本站") {
			t.Fatalf("导出内容未净化: %s", content)
		}
	}
	if !found {
		t.Fatal("ZIP 里没有 .txt 条目")
	}
}

// 导出格式非法 → 400（而不是生成损坏的包）。
func TestExportRejectsUnknownFormat(t *testing.T) {
	requireFullStack(t)
	s := newStack(t)
	client := loginOK(t, s)
	uploadTTSBook(t, client, s.ts.URL)

	resp := doJSON(t, client, http.MethodPost, s.ts.URL+"/api/admin/export",
		map[string]any{"password": testPassword, "format": "pdf"})
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d", resp.StatusCode)
	}
}

// 验收 A1：软删除后 manifest 下发墓碑，客户端据此清理本地数据。
func TestSoftDeleteSendsTombstone(t *testing.T) {
	requireFullStack(t)
	s := newStack(t)
	client := loginOK(t, s)
	bookID := uploadTTSBook(t, client, s.ts.URL)

	// 先取一次游标，后面用增量方式观察墓碑。
	first := doJSON(t, client, http.MethodGet, s.ts.URL+"/api/sync/manifest?since=0", nil)
	var m1 struct {
		Cursor int64 `json:"cursor"`
	}
	if err := json.Unmarshal(bodyBytes(t, first), &m1); err != nil {
		t.Fatal(err)
	}
	if m1.Cursor <= 0 {
		t.Fatalf("cursor = %d", m1.Cursor)
	}

	del := doJSON(t, client, http.MethodDelete, s.ts.URL+"/api/books/"+bookID, nil)
	del.Body.Close()
	if del.StatusCode != http.StatusNoContent {
		t.Fatalf("delete status = %d", del.StatusCode)
	}

	inc := doJSON(t, client, http.MethodGet,
		fmt.Sprintf("%s/api/sync/manifest?since=%d", s.ts.URL, m1.Cursor), nil)
	body := bodyBytes(t, inc)
	if inc.StatusCode != http.StatusOK {
		t.Fatalf("manifest status=%d body=%s", inc.StatusCode, body)
	}
	var m2 struct {
		Deleted []string `json:"deleted"`
		Books   []struct {
			ID string `json:"id"`
		} `json:"books"`
	}
	if err := json.Unmarshal(body, &m2); err != nil {
		t.Fatal(err)
	}
	if len(m2.Deleted) != 1 || m2.Deleted[0] != bookID {
		t.Fatalf("墓碑 = %v，期望 [%s]", m2.Deleted, bookID)
	}
	if len(m2.Books) != 0 {
		t.Fatalf("已删除书籍不应出现在增量列表: %+v", m2.Books)
	}

	// 软删除后章节接口应 404（不可再读）。
	plain := doJSON(t, client, http.MethodGet,
		fmt.Sprintf("%s/api/books/%s/chapters/0/plain", s.ts.URL, bookID), nil)
	plain.Body.Close()
	if plain.StatusCode != http.StatusNotFound {
		t.Fatalf("软删除后读取正文 status = %d", plain.StatusCode)
	}
}

// 验收 A1：定时清理任务发现书籍已软删除时，立即回收其全部音频缓存。
func TestSoftDeleteReclaimsAudioCache(t *testing.T) {
	requireFullStack(t)
	s := newStack(t)
	client := loginOK(t, s)
	bookID := uploadTTSBook(t, client, s.ts.URL)

	prep := doJSON(t, client, http.MethodPost,
		fmt.Sprintf("%s/api/books/%s/chapters/0/audio/prepare?speed=1.0", s.ts.URL, bookID), nil)
	prep.Body.Close()
	if prep.StatusCode != http.StatusOK {
		t.Fatalf("prepare status = %d", prep.StatusCode)
	}
	var before int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM tts_segments WHERE book_id = ?`, bookID).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if before == 0 {
		t.Fatal("准备音频后应写入片段索引")
	}

	del := doJSON(t, client, http.MethodDelete, s.ts.URL+"/api/books/"+bookID, nil)
	del.Body.Close()
	if del.StatusCode != http.StatusNoContent {
		t.Fatalf("delete status = %d", del.StatusCode)
	}

	// 零策略：只做软删除回收，不按容量/时间淘汰。
	res, err := s.tts.PruneCache(context.Background(), tts.PrunePolicy{})
	if err != nil {
		t.Fatal(err)
	}
	if res.RemovedDeleted != before {
		t.Fatalf("回收片段数 = %d，期望 %d", res.RemovedDeleted, before)
	}
	var after int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM tts_segments WHERE book_id = ?`, bookID).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != 0 {
		t.Fatalf("回收后仍有 %d 条片段记录", after)
	}
}

// 未配置净化规则时输出保持原样（避免误伤）。
func TestNoPurgeRulesKeepsText(t *testing.T) {
	requireFullStack(t)
	s := newStack(t)
	client := loginOK(t, s)
	bookID := uploadTTSBook(t, client, s.ts.URL)

	plain := doJSON(t, client, http.MethodGet,
		fmt.Sprintf("%s/api/books/%s/chapters/0/plain", s.ts.URL, bookID), nil)
	body := bodyString(t, plain)
	if plain.StatusCode != http.StatusOK {
		t.Fatalf("status=%d body=%s", plain.StatusCode, body)
	}
	if !strings.Contains(body, "他推开门") {
		t.Fatalf("未配置规则时正文不应变化: %s", body)
	}
}
