package server_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"novelstack/internal/cryptobox"
)

// newBlocklistStack 构建带 KEK 与熔断开启的测试栈。
func newBlocklistStack(t *testing.T, threshold int, loginLimit int) *stack {
	t.Helper()
	dir := t.TempDir()
	kek := make([]byte, cryptobox.KeySize)
	if _, err := rand.Read(kek); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "kek"), kek, 0o600); err != nil {
		t.Fatal(err)
	}
	return buildStackWith(t, dir, stackOpts{blocklistOn: true, blocklistThreshold: threshold, loginFailPerMinute: loginLimit})
}

// TestIPCircuitBreakerViaLogin 模拟脚本爆破：连续 10 次错密码（401），
// 第 11 次请求应被中间件直接 403 拒绝，且黑名单里出现该 IP。
func TestIPCircuitBreakerViaLogin(t *testing.T) {
	s := newBlocklistStack(t, 10, 50)
	ip := "203.0.113.77"
	login := func() int {
		t.Helper()
		body, _ := json.Marshal(map[string]string{"username": "admin", "password": "wrong"})
		req, _ := http.NewRequest(http.MethodPost, s.ts.URL+"/api/login", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Forwarded-For", ip) // 模拟来自同一来源 IP
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		return resp.StatusCode
	}
	for i := 1; i <= 10; i++ {
		if code := login(); code != http.StatusUnauthorized {
			t.Fatalf("第 %d 次登录 status=%d，期望 401", i, code)
		}
	}
	if !s.blocklist.IsBlocked(ip) {
		t.Fatal("10 次 401 后 IP 未进入黑名单")
	}
	if code := login(); code != http.StatusForbidden {
		t.Fatalf("第 11 次登录 status=%d，期望 403", code)
	}
	// 被封后连健康探测路径也被拦（兜底 404 不会绕过中间件）。
	req, _ := http.NewRequest(http.MethodGet, s.ts.URL+"/no-such-path", nil)
	req.Header.Set("X-Forwarded-For", ip)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("封禁后 404 探测 status=%d，期望 403", resp.StatusCode)
	}
	// 回环地址不受影响。
	resp2, err := http.Get(s.ts.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("回环健康检查 status=%d", resp2.StatusCode)
	}
}

// TestBlocklistAdminUnblock 验证控制台查看/解封接口与审计。
func TestBlocklistAdminUnblock(t *testing.T) {
	s := newBlocklistStack(t, 2, 50)
	ip := "198.51.100.55"
	// 直接注入黑名单（等价于熔断触发后的状态）。
	if !s.blocklist.Ban(context.Background(), ip, "test", 2) {
		t.Fatal("test ban failed")
	}

	client := loginOK(t, s)
	// 查看列表。
	resp, err := client.Get(s.ts.URL + "/api/admin/blocklist")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list status=%d", resp.StatusCode)
	}
	var list struct {
		Entries []struct {
			IP     string `json:"ip"`
			Active bool   `json:"active"`
		} `json:"entries"`
		Enabled bool `json:"enabled"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		t.Fatal(err)
	}
	if !list.Enabled || len(list.Entries) == 0 || list.Entries[0].IP != ip || !list.Entries[0].Active {
		t.Fatalf("list=%+v", list)
	}

	// 解封。
	req, _ := http.NewRequest(http.MethodDelete, s.ts.URL+"/api/admin/blocklist/"+ip, nil)
	uresp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer uresp.Body.Close()
	if uresp.StatusCode != http.StatusOK {
		t.Fatalf("unblock status=%d", uresp.StatusCode)
	}
	if s.blocklist.IsBlocked(ip) {
		t.Fatal("still blocked after unblock")
	}

	// 审计里出现 ip_unblocked。
	time.Sleep(20 * time.Millisecond)
	resp3, err := client.Get(s.ts.URL + "/api/admin/audit?action=ip_unblocked&limit=10")
	if err != nil {
		t.Fatal(err)
	}
	defer resp3.Body.Close()
	var audits []struct {
		Action string `json:"action"`
		Result string `json:"result"`
		Detail string `json:"detail"`
	}
	if err := json.NewDecoder(resp3.Body).Decode(&audits); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, a := range audits {
		if a.Action == "ip_unblocked" && a.Result == "ok" && a.Detail == "ip="+ip {
			found = true
		}
	}
	if !found {
		t.Fatalf("audit 缺少 ip_unblocked: %+v", audits)
	}
}
