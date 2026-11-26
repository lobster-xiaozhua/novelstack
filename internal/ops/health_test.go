package ops

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"novelstack/internal/audit"
	"novelstack/internal/db"
)

func healthFixture(t *testing.T, now time.Time) (*sql.DB, *audit.Logger, string, Input) {
	t.Helper()
	database, err := db.Open(filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	marker := filepath.Join(t.TempDir(), ".kek-offline-backup-ok")
	if err := os.WriteFile(marker, []byte("ok"), 0o600); err != nil {
		t.Fatal(err)
	}
	return database, audit.New(database), marker, Input{
		DB: database, BooksTotal: 10, SearchDocuments: 10, KekMarkerPath: marker, Now: now,
	}
}

func hasAlert(h Health, typ string) bool {
	for _, a := range h.Alerts {
		if a.Type == typ {
			return true
		}
	}
	return false
}

func alertSummary(h Health, typ string) string {
	for _, a := range h.Alerts {
		if a.Type == typ {
			return a.Summary
		}
	}
	return ""
}

func TestEvaluateBackupFailureAndStale(t *testing.T) {
	now := time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)
	database, _, marker, in := healthFixture(t, now)
	_, _ = database.Exec(`INSERT INTO backup_runs(result,checksum,size_bytes,ts) VALUES('ok','abc',100,?)`,
		now.Add(-3*time.Hour).Format(time.RFC3339))
	_, _ = database.Exec(`INSERT INTO audit_logs(action,result,detail,ts) VALUES('backup_failed','error','reason=write_failed',?)`,
		now.Add(-time.Hour).Format(time.RFC3339))
	in.KekMarkerPath = marker

	h := Evaluate(context.Background(), in)
	if h.Level != "bad" || !hasAlert(h, alertBackupFailed) || hasAlert(h, alertBackupStale) {
		t.Fatalf("health = %+v", h)
	}

	// 失败发生在成功之前 → 不算失败；但 49 小时没有成功 → 过时
	_, _ = database.Exec(`UPDATE audit_logs SET ts=? WHERE action='backup_failed'`, now.Add(-50*time.Hour).Format(time.RFC3339))
	_, _ = database.Exec(`UPDATE backup_runs SET ts=?`, now.Add(-49*time.Hour).Format(time.RFC3339))
	h = Evaluate(context.Background(), in)
	if h.Level != "bad" || !hasAlert(h, alertBackupStale) || hasAlert(h, alertBackupFailed) {
		t.Fatalf("stale health = %+v", h)
	}

	// 有成功备份且无失败 → 无备份类告警；标记缺失只降级为 warn
	_, _ = database.Exec(`UPDATE backup_runs SET ts=?`, now.Add(-time.Hour).Format(time.RFC3339))
	_, _ = database.Exec(`DELETE FROM audit_logs WHERE action='backup_failed'`)
	in.KekMarkerPath = filepath.Join(t.TempDir(), "missing")
	h = Evaluate(context.Background(), in)
	if h.Level != "warn" || !hasAlert(h, alertKEKMarker) || h.BackupOK != true {
		t.Fatalf("marker health = %+v", h)
	}
}

func TestEvaluateIntegrityAndDrift(t *testing.T) {
	now := time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)
	database, _, marker, in := healthFixture(t, now)
	_, _ = database.Exec(`INSERT INTO backup_runs(result,checksum,size_bytes,ts) VALUES('ok','abc',100,?)`,
		now.Add(-time.Hour).Format(time.RFC3339))
	for _, action := range []string{"decrypt_failed", "book_download_tamper", "tts_cache_tamper"} {
		_, _ = database.Exec(`INSERT INTO audit_logs(action,result,detail,ts) VALUES(?,'denied','SECRET-DETAIL',?)`,
			action, now.Add(-30*time.Minute).Format(time.RFC3339))
	}
	in.SearchDocuments = 15 // 多 5 篇
	h := Evaluate(context.Background(), in)
	if !hasAlert(h, alertTamper) || h.Integrity24h != 3 {
		t.Fatalf("integrity health = %+v", h)
	}
	if hasAlert(h, alertIndexDrift) || h.IndexDrift != 5 {
		t.Fatalf("drift should not alarm before 1h: %+v", h)
	}

	// 59 分钟仍不告警，61 分钟才升级
	in.Now = now.Add(59 * time.Minute)
	if hasAlert(Evaluate(context.Background(), in), alertIndexDrift) {
		t.Fatal("偏差未满 1 小时不应告警")
	}
	in.Now = now.Add(61 * time.Minute)
	h = Evaluate(context.Background(), in)
	if !hasAlert(h, alertIndexDrift) {
		t.Fatalf("超过 1 小时应告警: %+v", h)
	}
	if summary := alertSummary(h, alertIndexDrift); !strings.Contains(summary, "多 5") {
		t.Fatalf("正偏差摘要应区分方向: %s", summary)
	}
	// 偏差归零后清除计时器，下次再偏差要重新计时
	in.Now = now.Add(62 * time.Minute)
	in.SearchDocuments = 10
	if hasAlert(Evaluate(context.Background(), in), alertIndexDrift) {
		t.Fatal("偏差归零后不应继续告警")
	}
	_ = marker
}

func TestEvaluateNegativeIndexDrift(t *testing.T) {
	now := time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)
	_, _, _, in := healthFixture(t, now)
	in.SearchDocuments = 5 // 比书籍数少 5 篇

	h := Evaluate(context.Background(), in)
	if hasAlert(h, alertIndexDrift) || h.IndexDrift != -5 {
		t.Fatalf("负偏差未满 1 小时不应告警: %+v", h)
	}
	in.Now = now.Add(59 * time.Minute)
	if hasAlert(Evaluate(context.Background(), in), alertIndexDrift) {
		t.Fatal("负偏差未满 1 小时不应告警")
	}
	in.Now = now.Add(61 * time.Minute)
	h = Evaluate(context.Background(), in)
	if !hasAlert(h, alertIndexDrift) {
		t.Fatalf("负偏差超过 1 小时应告警: %+v", h)
	}
	summary := alertSummary(h, alertIndexDrift)
	if !strings.Contains(summary, "少 5") || strings.Contains(summary, "多 5") {
		t.Fatalf("负偏差摘要应区分方向: %s", summary)
	}

	// 归零清除计时；再次出现负偏差要重新计时。
	in.Now = now.Add(62 * time.Minute)
	in.SearchDocuments = 10
	if hasAlert(Evaluate(context.Background(), in), alertIndexDrift) {
		t.Fatal("偏差归零后不应继续告警")
	}
	in.SearchDocuments = 8
	if hasAlert(Evaluate(context.Background(), in), alertIndexDrift) {
		t.Fatal("新偏差未满 1 小时不应告警")
	}
	in.Now = now.Add(123 * time.Minute)
	if !hasAlert(Evaluate(context.Background(), in), alertIndexDrift) {
		t.Fatal("新偏差超过 1 小时应告警")
	}
}

func TestNotifyWebhookBodyAndRetry(t *testing.T) {
	var calls atomic.Int32
	var body []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		b, _ := io.ReadAll(r.Body)
		body = b
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	auditLog := audit.New(mustDB(t))
	store := NewHealthStore(HealthOptions{Webhook: srv.URL, Audit: auditLog, Cooldown: time.Hour})
	health := Health{Level: "bad", CheckedAt: "2026-11-26T12:00:00Z", Alerts: []Alert{
		{Type: alertBackupFailed, Summary: "最近一次自动备份失败：请查看审计 backup_failed，并确认云盘可写与备份口令有效。"},
	}}
	store.Notify(context.Background(), health)
	if calls.Load() != 1 {
		t.Fatalf("webhook calls = %d", calls.Load())
	}
	var decoded map[string]any
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"time", "level", "type", "summary"} {
		if _, ok := decoded[k]; !ok {
			t.Fatalf("webhook 缺少字段 %s: %s", k, body)
		}
	}
	if len(decoded) != 4 {
		t.Fatalf("webhook 字段过多（可能泄漏）: %s", body)
	}
	lower := strings.ToLower(string(body))
	for _, bad := range []string{"token", "password", "kek", "dek", "content", "query", "nsd_", "secret"} {
		if strings.Contains(lower, bad) {
			t.Fatalf("webhook 含敏感字段 %q: %s", bad, body)
		}
	}
	// 同一告警冷却期内不重复推送
	store.Notify(context.Background(), health)
	if calls.Load() != 1 {
		t.Fatalf("冷却期内不应重复推送: %d", calls.Load())
	}

	// 500 → 首次 + 2 次重试 = 3 次，随后写 error 审计并放弃。
	var failed atomic.Int32
	badSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		failed.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer badSrv.Close()
	db, a, _, _ := healthFixture(t, time.Now())
	_ = db
	retryStore := NewHealthStore(HealthOptions{Webhook: badSrv.URL, Audit: a, Cooldown: time.Hour,
		Client: &http.Client{Timeout: time.Second}})
	retryStore.Notify(context.Background(), health)
	if failed.Load() != 3 {
		t.Fatalf("失败重试次数 = %d, want 3", failed.Load())
	}
	entries, err := a.Query(context.Background(), audit.Filter{Action: "health_webhook"})
	if err != nil || len(entries) != 1 || entries[0].Result != "error" ||
		!strings.Contains(entries[0].Detail, "attempts=3") {
		t.Fatalf("失败审计 = %+v err=%v", entries, err)
	}
	if strings.Contains(entries[0].Detail, badSrv.URL) {
		t.Fatal("审计不应记录 webhook URL")
	}
}

func mustDB(t *testing.T) *sql.DB {
	t.Helper()
	database, err := db.Open(filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	return database
}
