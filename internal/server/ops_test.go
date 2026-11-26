package server_test

// 第六阶段集成验收：手动触发自动备份 → 控制台可见记录 → 失败场景 → 健康横幅变红 → webhook 收到 JSON。

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"novelstack/internal/backup"
	"novelstack/internal/cryptobox"
)

func stackWithKEK(t *testing.T, opts stackOpts) *stack {
	t.Helper()
	dir := t.TempDir()
	kek := make([]byte, cryptobox.KeySize)
	if _, err := rand.Read(kek); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "kek"), kek, 0o600); err != nil {
		t.Fatal(err)
	}
	return buildStackWith(t, dir, opts)
}

func dashboardJSON(t *testing.T, client *http.Client, base string) map[string]any {
	t.Helper()
	resp, err := client.Get(base + "/api/admin/dashboard")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("dashboard status=%d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestBackupRunEndpointAndDashboardRecord(t *testing.T) {
	requireFullStack(t)
	s := stackWithKEK(t, stackOpts{provider: nil, autoBackup: true})
	client := loginOK(t, s)
	dev, _ := registerDevice(t, s, "phone-phase6")

	// 设备令牌不能触发备份。
	st, body := doBearer(t, http.MethodPost, s.ts.URL+"/api/admin/backup/run", dev.token, nil)
	if st != http.StatusForbidden {
		t.Fatalf("device token backup status=%d body=%s", st, body)
	}

	// 浏览器会话触发：返回包信息，落盘文件名含日期与 SHA-256 前 8 位。
	st, body = doSession(t, client, http.MethodPost, s.ts.URL+"/api/admin/backup/run", nil)
	if st != http.StatusOK {
		t.Fatalf("backup status=%d body=%s", st, body)
	}
	var res struct {
		OK         bool   `json:"ok"`
		Path       string `json:"path"`
		SHA256     string `json:"sha256"`
		SizeBytes  int64  `json:"size_bytes"`
		DurationMS int64  `json:"duration_ms"`
	}
	if err := json.Unmarshal(body, &res); err != nil || !res.OK {
		t.Fatalf("backup response=%s err=%v", body, err)
	}
	if !backup.IsAutoBackupName(res.Path) || !strings.HasSuffix(res.Path, "-"+res.SHA256[:8]+".nsb") {
		t.Fatalf("包命名不含校验和前缀: %s", res.Path)
	}
	if res.SizeBytes <= 0 {
		t.Fatalf("包大小异常: %d", res.SizeBytes)
	}
	if _, err := os.Stat(filepath.Join(s.dataDir, "backups", res.Path+".sha256")); err != nil {
		t.Fatalf("缺少 .sha256 伴生文件: %v", err)
	}

	d := dashboardJSON(t, client, s.ts.URL)
	if n, ok := d["backups_dir_bytes"].(float64); !ok || n <= 0 {
		t.Fatalf("dashboard 未反映备份目录占用: %v", d["backups_dir_bytes"])
	}
	bk := d["backup"].(map[string]any)
	last := bk["last"].(map[string]any)
	if last["path"] != res.Path || last["sha256"] != res.SHA256 {
		t.Fatalf("dashboard 未反映备份记录: %+v", last)
	}
	h := d["health"].(map[string]any)
	if h["last_backup_at"] == nil || h["backup_ok"] != true {
		t.Fatalf("health 未反映成功备份: %+v", h)
	}
	alerts, _ := h["alerts"].([]any)
	for _, a := range alerts {
		m := a.(map[string]any)
		if m["type"] == "backup_stale" || m["type"] == "backup_failed" {
			t.Fatalf("刚备份完不应告警: %+v", m)
		}
	}
	recent := d["audit"].(map[string]any)["recent"].([]any)
	sawAudit := false
	for _, raw := range recent {
		e := raw.(map[string]any)
		if e["action"] == "backup_scheduled" && strings.Contains(e["detail"].(string), "sha256="+res.SHA256) {
			sawAudit = true
		}
	}
	if !sawAudit {
		t.Fatalf("审计缺少 backup_scheduled 完整校验和: %+v", recent)
	}
}

func doSession(t *testing.T, client *http.Client, method, url string, body any) (int, []byte) {
	t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, url, rd)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

func TestFailureTurnsBannerRedAndSendsWebhook(t *testing.T) {
	requireFullStack(t)
	received := make(chan []byte, 8)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		received <- b
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	// 备份目录被一个普通文件占用 → MkdirAll 失败，且不会影响已有备份包。
	dir := t.TempDir()
	kek := make([]byte, cryptobox.KeySize)
	if _, err := rand.Read(kek); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "kek"), kek, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "backups"), []byte("not a dir"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := buildStackWith(t, dir, stackOpts{autoBackup: true, webhook: srv.URL})
	client := loginOK(t, s)

	st, body := doSession(t, client, http.MethodPost, s.ts.URL+"/api/admin/backup/run", nil)
	if st != http.StatusInternalServerError {
		t.Fatalf("期望备份失败 500, got %d body=%s", st, body)
	}
	if strings.Contains(string(body), "passphrase") || strings.Contains(string(body), "/tmp/") {
		t.Fatalf("错误响应泄露内部信息: %s", body)
	}

	// 每次 dashboard 轮询都会重算健康并异步发 webhook（同类型有冷却）。
	deadline := time.Now().Add(5 * time.Second)
	var h map[string]any
	for time.Now().Before(deadline) {
		d := dashboardJSON(t, client, s.ts.URL)
		h = d["health"].(map[string]any)
		if h["level"] == "bad" {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if h["level"] != "bad" {
		t.Fatalf("失败后横幅应变红: %+v", h)
	}
	found := false
	for _, a := range h["alerts"].([]any) {
		if a.(map[string]any)["type"] == "backup_failed" {
			found = true
		}
	}
	if !found {
		t.Fatalf("缺少 backup_failed 告警: %+v", h)
	}

	select {
	case payload := <-received:
		var msg map[string]any
		if err := json.Unmarshal(payload, &msg); err != nil {
			t.Fatal(err)
		}
		if len(msg) != 4 || msg["type"] != "backup_failed" || msg["level"] != "bad" {
			t.Fatalf("webhook 请求体异常: %s", payload)
		}
		lower := strings.ToLower(string(payload))
		for _, bad := range []string{"token", "password", "kek", "dek", "query", "content", "nsd_"} {
			if strings.Contains(lower, bad) {
				t.Fatalf("webhook 含敏感字段 %q: %s", bad, payload)
			}
		}
	case <-time.After(5 * time.Second):
		t.Fatal("未收到 webhook")
	}
}
