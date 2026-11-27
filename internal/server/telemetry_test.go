package server_test

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// 第九阶段验收：
//  1. /api/telemetry/batch 能解压 gzip 并解析批量事件；
//  2. 入口清洗必须丢弃正文与凭证（即使客户端没清）；
//  3. 控制台遥测接口只接受浏览器会话，device token 一律 403；
//  4. 崩溃堆栈可完整返回，供控制台展开。

func postGzip(t *testing.T, url, token, contentType string, payload []byte) (int, []byte) {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(payload); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPost, url, &buf)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Encoding", "gzip")
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out bytes.Buffer
	_, _ = out.ReadFrom(resp.Body)
	return resp.StatusCode, out.Bytes()
}

func TestTelemetryBatchAcceptsGzipAndSanitizes(t *testing.T) {
	requireFullStack(t)
	s := newStack(t)
	dev, _ := registerDevice(t, s, "遥测测试机")

	stack := strings.Repeat("  at com.example.Foo.run(Foo.kt:42)\n", 3)
	payload, _ := json.Marshal(map[string]any{
		"device_id":   "dev-telemetry-1",
		"app_version": "0.2.1",
		"os":          "14",
		"model":       "Xiaomi 14",
		"events": []map[string]any{
			{
				"id": "e1", "kind": "CRASH", "name": "java.lang.IllegalStateException",
				"ts": "2026-11-27T10:00:00Z", "stack": stack, "count": 3,
				"context": map[string]any{
					"screen": "Reader",
					"text":   "这是正文，服务端必须丢弃",
					"token":  "nsd_leaked",
				},
			},
			{
				"id": "e2", "kind": "API", "name": "/api/discover",
				"ts": "2026-11-27T10:01:00Z", "duration_ms": 240,
			},
		},
	})

	code, body := postGzip(t, s.ts.URL+"/api/telemetry/batch", dev.token, "application/json", payload)
	if code != http.StatusOK {
		t.Fatalf("batch status=%d body=%s", code, body)
	}
	var res struct {
		OK       bool `json:"ok"`
		Accepted int  `json:"accepted"`
		Dropped  int  `json:"dropped"`
	}
	if err := json.Unmarshal(body, &res); err != nil {
		t.Fatalf("batch json: %v body=%s", err, body)
	}
	if !res.OK || res.Accepted != 2 || res.Dropped != 0 {
		t.Fatalf("accepted=%d dropped=%d body=%s", res.Accepted, res.Dropped, body)
	}

	// 控制台视角校验：崩溃能查到，且正文/凭证没有落库。
	admin := loginOK(t, s)
	resp, err := admin.Get(s.ts.URL + "/api/admin/telemetry/crashes?days=7")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var crashBody bytes.Buffer
	_, _ = crashBody.ReadFrom(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("crashes status=%d body=%s", resp.StatusCode, crashBody.String())
	}
	raw := crashBody.String()
	if strings.Contains(raw, "正文") || strings.Contains(raw, "nsd_leaked") {
		t.Fatalf("崩溃明细泄露敏感内容: %s", raw)
	}
	if !strings.Contains(raw, "IllegalStateException") {
		t.Fatalf("崩溃名称缺失: %s", raw)
	}
	if !strings.Contains(raw, `"screen":"Reader"`) {
		t.Fatalf("白名单上下文丢失: %s", raw)
	}
	var list struct {
		Items []struct {
			Occurrence int64  `json:"occurrence"`
			Stack      string `json:"stack"`
		} `json:"items"`
	}
	if err := json.Unmarshal(crashBody.Bytes(), &list); err != nil {
		t.Fatalf("crashes json: %v", err)
	}
	if len(list.Items) != 1 || list.Items[0].Occurrence != 3 {
		t.Fatalf("崩溃条数或 occurrence 不对: %+v", list.Items)
	}
	if !strings.Contains(list.Items[0].Stack, "Foo.kt:42") {
		t.Fatalf("堆栈不完整: %q", list.Items[0].Stack)
	}
}

func TestTelemetryBatchRejectsBadGzip(t *testing.T) {
	requireFullStack(t)
	s := newStack(t)
	dev, _ := registerDevice(t, s, "坏包测试机")

	req, _ := http.NewRequest(http.MethodPost, s.ts.URL+"/api/telemetry/batch", strings.NewReader("not-gzip"))
	req.Header.Set("Content-Encoding", "gzip")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+dev.token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("坏 gzip 应 400，实际 %d", resp.StatusCode)
	}
}

func TestTelemetryAdminRejectsDeviceToken(t *testing.T) {
	requireFullStack(t)
	s := newStack(t)
	dev, _ := registerDevice(t, s, "越权测试机")

	code, body := doBearer(t, http.MethodGet, s.ts.URL+"/api/admin/telemetry/overview", dev.token, nil)
	if code != http.StatusForbidden {
		t.Fatalf("device token 访问控制台应 403，实际 %d body=%s", code, body)
	}
}

func TestTelemetryOverviewEmptyIsZero(t *testing.T) {
	requireFullStack(t)
	s := newStack(t)
	admin := loginOK(t, s)

	resp, err := admin.Get(s.ts.URL + "/api/admin/telemetry/overview?days=7")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out struct {
		ActiveDevices int64 `json:"active_devices"`
		Crashes       int64 `json:"crashes"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.ActiveDevices != 0 || out.Crashes != 0 {
		t.Fatalf("空库应全 0，实际 %+v", out)
	}
}
