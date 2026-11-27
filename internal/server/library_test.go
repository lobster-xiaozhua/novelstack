package server_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// 第八阶段验收：发现/足迹接口不得包含任何正文字段；收藏开关状态正确。

// noPlaintextFields 是绝不允许出现在元数据接口响应中的字段名。
var noPlaintextFields = []string{
	`"text"`, `"ciphertext"`, `"content"`, `"storage_path"`,
	`"wrapped_dek"`, `"dek_nonce"`, `"plain"`,
}

func assertNoPlaintext(t *testing.T, where string, body []byte) {
	t.Helper()
	s := string(body)
	for _, f := range noPlaintextFields {
		if strings.Contains(s, f) {
			t.Fatalf("%s 响应出现正文字段 %s: %.200s", where, f, s)
		}
	}
}

func TestDiscoverAndHistoryNoPlaintext(t *testing.T) {
	requireFullStack(t)
	s := newStack(t)
	bookID := uploadThreeChapters(t, s)
	dev, _ := registerDevice(t, s, "发现页测试机")

	// 写一条进度，足迹才有数据。
	code, body := doBearer(t, http.MethodPut,
		s.ts.URL+"/api/books/"+bookID+"/progress", dev.token, map[string]any{
			"chapter_index": 1, "char_offset": 10, "percent": 0.55,
		})
	if code != http.StatusOK {
		t.Fatalf("写进度 status=%d body=%s", code, body)
	}

	// 发现页
	code, body = doBearer(t, http.MethodGet, s.ts.URL+"/api/discover", dev.token, nil)
	if code != http.StatusOK {
		t.Fatalf("discover status=%d body=%s", code, body)
	}
	assertNoPlaintext(t, "discover", body)
	var disc struct {
		Recent []map[string]any `json:"recent"`
		Tags   []map[string]any `json:"tags"`
		Random []map[string]any `json:"random"`
	}
	if err := json.Unmarshal(body, &disc); err != nil {
		t.Fatalf("discover json: %v body=%s", err, body)
	}
	if len(disc.Recent) == 0 {
		t.Fatal("discover.recent 为空")
	}
	if len(disc.Random) == 0 || len(disc.Random) > 5 {
		t.Fatalf("discover.random 数量异常: %d", len(disc.Random))
	}
	if _, ok := disc.Recent[0]["title"]; !ok {
		t.Fatalf("discover.recent 缺少 title: %v", disc.Recent[0])
	}

	// 足迹
	code, body = doBearer(t, http.MethodGet, s.ts.URL+"/api/history", dev.token, nil)
	if code != http.StatusOK {
		t.Fatalf("history status=%d body=%s", code, body)
	}
	assertNoPlaintext(t, "history", body)
	var hist struct {
		Items []struct {
			BookID       string  `json:"book_id"`
			Title        string  `json:"title"`
			ChapterIndex int     `json:"chapter_index"`
			ChapterTitle string  `json:"chapter_title"`
			Percent      float64 `json:"percent"`
			UpdatedAt    string  `json:"updated_at"`
		} `json:"items"`
	}
	if err := json.Unmarshal(body, &hist); err != nil {
		t.Fatalf("history json: %v body=%s", err, body)
	}
	if len(hist.Items) != 1 {
		t.Fatalf("history 条目数=%d 期望 1", len(hist.Items))
	}
	it := hist.Items[0]
	if it.BookID != bookID {
		t.Fatalf("history book_id=%s 期望 %s", it.BookID, bookID)
	}
	if it.Title == "" || it.UpdatedAt == "" {
		t.Fatalf("history 缺少 title/updated_at: %+v", it)
	}
	if it.ChapterIndex != 1 {
		t.Fatalf("history chapter_index=%d 期望 1", it.ChapterIndex)
	}
	if it.Percent < 0.54 || it.Percent > 0.56 {
		t.Fatalf("history percent=%.3f 期望 0.55", it.Percent)
	}
}

func TestFavoriteToggle(t *testing.T) {
	requireFullStack(t)
	s := newStack(t)
	bookID := uploadThreeChapters(t, s)
	dev, _ := registerDevice(t, s, "收藏测试机")

	// 初始未收藏
	fav := favOfBook(t, s, dev.token, bookID)
	if fav {
		t.Fatal("新建书不应是收藏状态")
	}

	// 收藏
	code, body := doBearer(t, http.MethodPatch,
		s.ts.URL+"/api/books/"+bookID+"/favorite", dev.token,
		map[string]any{"is_favorite": true})
	if code != http.StatusOK {
		t.Fatalf("收藏 status=%d body=%s", code, body)
	}
	if !strings.Contains(string(body), `"is_favorite":true`) {
		t.Fatalf("收藏响应未回显 true: %s", body)
	}
	if !favOfBook(t, s, dev.token, bookID) {
		t.Fatal("收藏后列表未反映 is_favorite=true")
	}

	// 取消收藏
	code, body = doBearer(t, http.MethodPatch,
		s.ts.URL+"/api/books/"+bookID+"/favorite", dev.token,
		map[string]any{"is_favorite": false})
	if code != http.StatusOK {
		t.Fatalf("取消收藏 status=%d body=%s", code, body)
	}
	if favOfBook(t, s, dev.token, bookID) {
		t.Fatal("取消收藏后列表仍为 true")
	}

	// 不存在的书 → 404
	code, _ = doBearer(t, http.MethodPatch,
		s.ts.URL+"/api/books/does-not-exist/favorite", dev.token,
		map[string]any{"is_favorite": true})
	if code != http.StatusNotFound {
		t.Fatalf("未知书收藏 status=%d 期望 404", code)
	}

	// 缺少字段 → 400
	code, _ = doBearer(t, http.MethodPatch,
		s.ts.URL+"/api/books/"+bookID+"/favorite", dev.token, map[string]any{})
	if code != http.StatusBadRequest {
		t.Fatalf("缺字段收藏 status=%d 期望 400", code)
	}
}

// favOfBook 从 GET /api/books 列表中读取指定书的 is_favorite。
func favOfBook(t *testing.T, s *stack, token, bookID string) bool {
	t.Helper()
	code, body := doBearer(t, http.MethodGet, s.ts.URL+"/api/books", token, nil)
	if code != http.StatusOK {
		t.Fatalf("book list status=%d body=%s", code, body)
	}
	var list []struct {
		ID         string `json:"id"`
		IsFavorite bool   `json:"is_favorite"`
	}
	if err := json.Unmarshal(body, &list); err != nil {
		t.Fatalf("book list json: %v", err)
	}
	for _, b := range list {
		if b.ID == bookID {
			return b.IsFavorite
		}
	}
	t.Fatalf("列表中找不到书 %s", bookID)
	return false
}

func TestAppVersionEndpoints(t *testing.T) {
	requireFullStack(t)
	s := newStack(t)
	dev, _ := registerDevice(t, s, "版本测试机")

	code, body := doBearer(t, http.MethodGet, s.ts.URL+"/api/app/version", dev.token, nil)
	if code != http.StatusOK {
		t.Fatalf("version status=%d body=%s", code, body)
	}
	var v struct {
		Version   string   `json:"version"`
		Changelog string   `json:"changelog"`
		Mirrors   []string `json:"mirrors"`
	}
	if err := json.Unmarshal(body, &v); err != nil {
		t.Fatalf("version json: %v body=%s", err, body)
	}
	if v.Version == "" {
		t.Fatal("version 为空")
	}
	if v.Mirrors == nil {
		t.Fatal("mirrors 应为空数组而不是 null")
	}

	// 未配置哈希时返回 404（客户端据此提示未提供校验值）。
	code, _ = doBearer(t, http.MethodGet, s.ts.URL+"/api/app/version/latest-hash", dev.token, nil)
	if code != http.StatusNotFound {
		t.Fatalf("未配置哈希 status=%d 期望 404", code)
	}
}
