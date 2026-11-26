package server_test

import (
	"bytes"
	"encoding/json"
	"io"
	"io/fs"
	"net/http"
	"strings"
	"testing"

	"novelstack/internal/adminui"
)

func TestAdminLoginGateAndEmbeddedAssets(t *testing.T) {
	s := sharedFullStack(t)

	// 未登录：/admin 跳转登录页
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Get(s.ts.URL + "/admin")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/admin/login" {
		t.Fatalf("unauth /admin = %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}

	// 登录页不依赖前端构建
	resp, err = client.Get(s.ts.URL + "/admin/login")
	if err != nil {
		t.Fatal(err)
	}
	body := bodyString(t, resp)
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, "运营控制台") || !strings.Contains(body, "/api/login") {
		t.Fatalf("login page: %d %s", resp.StatusCode, body)
	}

	// 登录后 /admin 返回内嵌 SPA 入口
	authClient := loginOK(t, s)
	resp, err = authClient.Get(s.ts.URL + "/admin")
	if err != nil {
		t.Fatal(err)
	}
	body = bodyString(t, resp)
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, `id="app"`) || !strings.Contains(body, "/admin/assets/") {
		t.Fatalf("admin index: %d %s", resp.StatusCode, body)
	}

	// go:embed 产物确实存在且可访问（资源本身公开，不含数据）
	sub, err := adminui.FS()
	if err != nil {
		t.Fatal(err)
	}
	matches, err := fs.Glob(sub, "assets/*.js")
	if err != nil || len(matches) == 0 {
		t.Fatalf("embedded assets = %v err=%v", matches, err)
	}
	resp, err = client.Get(s.ts.URL + "/admin/" + matches[0])
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("asset %s = %d", matches[0], resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "javascript") {
		t.Fatalf("asset content-type = %q", ct)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	// SPA 子路由回落到 index.html
	resp, err = authClient.Get(s.ts.URL + "/admin/overview")
	if err != nil {
		t.Fatal(err)
	}
	body = bodyString(t, resp)
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, `id="app"`) {
		t.Fatalf("spa route: %d", resp.StatusCode)
	}
}

func TestDashboardRequiresAuthAndLeaksNothing(t *testing.T) {
	s := sharedFullStack(t)
	resp, err := http.Get(s.ts.URL + "/api/admin/dashboard")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauth dashboard = %d", resp.StatusCode)
	}

	client := loginOK(t, s)
	resp, err = client.Get(s.ts.URL + "/api/admin/dashboard")
	if err != nil {
		t.Fatal(err)
	}
	body := bodyBytes(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("dashboard = %d %s", resp.StatusCode, body)
	}
	lower := strings.ToLower(string(body))
	for _, bad := range []string{`"content"`, `"plaintext"`, `"body"`, `"password"`, `"token"`, `"query"`, `"dek"`} {
		if strings.Contains(lower, bad) {
			t.Fatalf("dashboard 泄漏字段 %s: %s", bad, body)
		}
	}
	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"generated_at", "books", "kek", "tts", "search", "audit", "backup", "cache"} {
		if _, ok := out[k]; !ok {
			t.Fatalf("dashboard 缺少 %s", k)
		}
	}
}

func TestCacheClearAuthMetricsAndAudit(t *testing.T) {
	s := sharedFullStack(t)
	client := loginOK(t, s)
	bookID := uploadTTSBook(t, client, s.ts.URL)

	resp := speak(t, client, s.ts.URL, bookID, 0, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("speak = %d", resp.StatusCode)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	before := dashboardCacheBytes(t, client, s.ts.URL)
	if before <= 0 {
		t.Fatal("朗读后缓存大小应大于 0")
	}

	// 未确认 → 400
	req, _ := http.NewRequest(http.MethodPost, s.ts.URL+"/api/admin/cache/clear",
		strings.NewReader(`{"all":true}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("no confirm = %d", resp.StatusCode)
	}

	// 未认证 → 401
	resp, err = http.Post(s.ts.URL+"/api/admin/cache/clear", "application/json",
		strings.NewReader(`{"all":true,"confirm":true}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauth clear = %d", resp.StatusCode)
	}

	// 正式清理
	req, _ = http.NewRequest(http.MethodPost, s.ts.URL+"/api/admin/cache/clear",
		bytes.NewReader([]byte(`{"all":true,"confirm":true}`)))
	req.Header.Set("Content-Type", "application/json")
	resp, err = client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body := bodyBytes(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("clear = %d %s", resp.StatusCode, body)
	}
	var result struct {
		Removed int   `json:"removed"`
		After   int64 `json:"bytes_after"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatal(err)
	}
	if result.Removed == 0 {
		t.Fatalf("removed = 0 (%s)", body)
	}
	after := dashboardCacheBytes(t, client, s.ts.URL)
	if after >= before {
		t.Fatalf("缓存未下降: before=%d after=%d", before, after)
	}

	// 审计中可见 cache_cleared
	resp, err = client.Get(s.ts.URL + "/api/admin/audit?action=cache_cleared&limit=10")
	if err != nil {
		t.Fatal(err)
	}
	auditBody := bodyBytes(t, resp)
	if !strings.Contains(string(auditBody), "cache_cleared") || !strings.Contains(string(auditBody), `"result":"ok"`) {
		t.Fatalf("audit 缺少 cache_cleared: %s", auditBody)
	}
}

func TestAuditFilterByAction(t *testing.T) {
	s := sharedFullStack(t)
	client := loginOK(t, s)
	bookID := uploadTTSBook(t, client, s.ts.URL)
	_ = bookID

	resp, err := client.Get(s.ts.URL + "/api/admin/audit?action=book_upload&limit=50")
	if err != nil {
		t.Fatal(err)
	}
	body := bodyBytes(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("audit = %d", resp.StatusCode)
	}
	if !strings.Contains(string(body), `"action":"book_upload"`) {
		t.Fatalf("缺少 book_upload: %s", body)
	}
	if strings.Contains(string(body), `"action":"login_success"`) {
		t.Fatalf("过滤失效: %s", body)
	}
}

func dashboardCacheBytes(t *testing.T, client *http.Client, base string) int64 {
	t.Helper()
	resp, err := client.Get(base + "/api/admin/dashboard")
	if err != nil {
		t.Fatal(err)
	}
	body := bodyBytes(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("dashboard = %d", resp.StatusCode)
	}
	var out struct {
		Cache struct {
			Bytes int64 `json:"bytes"`
		} `json:"cache"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatal(err)
	}
	return out.Cache.Bytes
}
