package blocklist

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func testDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "b.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE ip_blocklist (
		ip TEXT PRIMARY KEY, reason TEXT NOT NULL, hits INTEGER NOT NULL DEFAULT 0,
		created_at TEXT NOT NULL, expires_at TEXT NOT NULL, unblocked_at TEXT
	)`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestBurstTriggersBan(t *testing.T) {
	db := testDB(t)
	s := New(db, true, time.Hour, 3, time.Hour, 5000)
	ip := "203.0.113.9"
	for i := 0; i < 2; i++ {
		if n := s.RecordViolation(ip); n != i+1 {
			t.Fatalf("hit %d count=%d", i+1, n)
		}
		if s.IsBlocked(ip) {
			t.Fatalf("banned too early at hit %d", i+1)
		}
	}
	if n := s.RecordViolation(ip); n != 3 {
		t.Fatalf("third count=%d", n)
	}
	// 达到阈值不自动封禁（封禁由中间件在写响应后触发），这里手动模拟。
	if !s.Ban(context.Background(), ip, "401/404 burst", 3) {
		t.Fatal("ban failed")
	}
	if !s.IsBlocked(ip) {
		t.Fatal("not blocked after ban")
	}
	// 封禁后不再累计违规。
	if n := s.RecordViolation(ip); n != 0 {
		t.Fatalf("blocked ip still recorded: %d", n)
	}
}

func TestLoopbackNeverBanned(t *testing.T) {
	db := testDB(t)
	s := New(db, true, time.Hour, 2, time.Hour, 5000)
	for i := 0; i < 10; i++ {
		s.RecordViolation("127.0.0.1")
		s.RecordViolation("::1")
	}
	if s.IsBlocked("127.0.0.1") || s.IsBlocked("::1") {
		t.Fatal("loopback got banned")
	}
}

func TestDisabledStoreIgnoresViolations(t *testing.T) {
	db := testDB(t)
	s := New(db, false, time.Hour, 2, time.Hour, 5000)
	for i := 0; i < 5; i++ {
		if n := s.RecordViolation("198.51.100.7"); n != 0 {
			t.Fatalf("disabled store recorded %d", n)
		}
	}
	if s.IsBlocked("198.51.100.7") {
		t.Fatal("disabled store banned")
	}
}

func TestUnblockClearsMemoryAndDB(t *testing.T) {
	db := testDB(t)
	s := New(db, true, time.Hour, 2, time.Hour, 5000)
	ip := "198.51.100.8"
	_ = s.Ban(context.Background(), ip, "test", 2)
	if !s.IsBlocked(ip) {
		t.Fatal("not blocked")
	}
	if err := s.Unblock(context.Background(), ip); err != nil {
		t.Fatal(err)
	}
	if s.IsBlocked(ip) {
		t.Fatal("still blocked after unblock")
	}
	entries, err := s.List(context.Background(), 10)
	if err != nil || len(entries) != 1 {
		t.Fatalf("list=%d err=%v", len(entries), err)
	}
	if entries[0].Active || entries[0].UnblockedAt == "" {
		t.Fatalf("entry should be inactive: %+v", entries[0])
	}
}

func TestBanPersistsAcrossRestart(t *testing.T) {
	db := testDB(t)
	s1 := New(db, true, time.Hour, 2, time.Hour, 5000)
	ip := "198.51.100.9"
	_ = s1.Ban(context.Background(), ip, "test", 2)
	s2 := New(db, true, time.Hour, 2, time.Hour, 5000)
	if !s2.IsBlocked(ip) {
		t.Fatal("ban not loaded after restart")
	}
}

func TestMiddlewareBlocksAndCounts(t *testing.T) {
	db := testDB(t)
	s := New(db, true, time.Hour, 2, time.Hour, 5000)
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	})
	h := s.Middleware(inner)

	// 第一次 401 放行。
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/x", nil).WithContext(context.Background()))
	req := httptest.NewRequest(http.MethodGet, "/api/x", nil)
	req.RemoteAddr = "203.0.113.20:1234"
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("first status=%d", rec.Code)
	}

	// 第二次 401 后触发封禁，第三次直接 403。
	h.ServeHTTP(rec, req)
	if !s.IsBlocked("203.0.113.20") {
		t.Fatal("not banned after threshold")
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("after ban status=%d", rec.Code)
	}
	if rec.Body.Len() == 0 || !contains(rec.Body.String(), "ip blocked") {
		t.Fatalf("403 body=%q", rec.Body.String())
	}
}

func TestBanRespectsMaxEntries(t *testing.T) {
	db := testDB(t)
	s := New(db, true, time.Hour, 2, time.Hour, 2)
	ctx := context.Background()
	if !s.Ban(ctx, "203.0.113.41", "t", 2) || !s.Ban(ctx, "203.0.113.42", "t", 2) {
		t.Fatal("first two bans should succeed")
	}
	if s.Ban(ctx, "203.0.113.43", "t", 2) {
		t.Fatal("ban beyond max entries should be rejected")
	}
	if s.IsBlocked("203.0.113.43") {
		t.Fatal("rejected ip must not be blocked")
	}
}

func TestViolationMapStopsGrowingAtLimit(t *testing.T) {
	db := testDB(t)
	s := New(db, true, time.Hour, 1000, time.Hour, 4)
	// 超过上限的来源 IP 只计数到上限，之后放弃计数（防探测把内存撑爆）。
	for i := 0; i < 20; i++ {
		s.RecordViolation("198.51.100." + strconv.Itoa(i+1))
	}
	s.mu.RLock()
	n := len(s.hits)
	s.mu.RUnlock()
	if n > 4 {
		t.Fatalf("hits map grew to %d, want <= 4", n)
	}
}

func TestClientIPPrefersXFF(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = "10.0.0.1:9999"
	r.Header.Set("X-Forwarded-For", "203.0.113.30, 10.0.0.1")
	if got := ClientIP(r); got != "203.0.113.30" {
		t.Fatalf("ClientIP=%q", got)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
