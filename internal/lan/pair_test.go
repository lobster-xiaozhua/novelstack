package lan

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "lan.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`CREATE TABLE lan_pair_codes (
		id TEXT PRIMARY KEY, owner_id TEXT NOT NULL, code_hash TEXT NOT NULL UNIQUE,
		client_ip TEXT NOT NULL DEFAULT '', created_at TEXT NOT NULL,
		expires_at TEXT NOT NULL, used_at TEXT)`); err != nil {
		t.Fatal(err)
	}
	return NewStore(db, 5*time.Minute, 8)
}

func TestCodeIsSixDigits(t *testing.T) {
	s := newTestStore(t)
	c, err := s.Create(context.Background(), "alice", "192.168.1.10", "http://192.168.1.5:8080")
	if err != nil {
		t.Fatal(err)
	}
	if !isSixDigits(c.Code) {
		t.Fatalf("code = %q, want 6 digits", c.Code)
	}
	if c.Endpoint != "http://192.168.1.5:8080" {
		t.Fatalf("endpoint = %q", c.Endpoint)
	}
}

func TestRedeemOnce(t *testing.T) {
	s := newTestStore(t)
	c, _ := s.Create(context.Background(), "alice", "192.168.1.10", "")
	owner, err := s.Redeem(context.Background(), c.Code, "192.168.1.10")
	if err != nil || owner != "alice" {
		t.Fatalf("redeem: owner=%q err=%v", owner, err)
	}
	if _, err := s.Redeem(context.Background(), c.Code, "192.168.1.10"); err != ErrBadCode {
		t.Fatalf("second redeem err = %v, want ErrBadCode", err)
	}
}

func TestRedeemRejectsDifferentIP(t *testing.T) {
	s := newTestStore(t)
	c, _ := s.Create(context.Background(), "alice", "192.168.1.10", "")
	if _, err := s.Redeem(context.Background(), c.Code, "10.0.0.9"); err != ErrBadCode {
		t.Fatalf("cross-ip redeem err = %v, want ErrBadCode", err)
	}
	// 正确 IP 仍可用（失败尝试不应消费短码）。
	if _, err := s.Redeem(context.Background(), c.Code, "192.168.1.10"); err != nil {
		t.Fatalf("correct-ip redeem after failed attempt: %v", err)
	}
}

func TestRedeemRejectsMalformed(t *testing.T) {
	s := newTestStore(t)
	for _, code := range []string{"", "12345", "abcdef", "1234567", "12 456"} {
		if _, err := s.Redeem(context.Background(), code, "1.2.3.4"); err != ErrBadCode {
			t.Fatalf("code %q: err = %v, want ErrBadCode", code, err)
		}
	}
}

func TestRedeemExpired(t *testing.T) {
	s := newTestStore(t)
	c, _ := s.Create(context.Background(), "alice", "192.168.1.10", "")
	// 直接改库把过期时间提前。
	if _, err := s.db.Exec(`UPDATE lan_pair_codes SET expires_at = ? WHERE code_hash = ?`,
		time.Now().UTC().Add(-time.Minute).Format(time.RFC3339Nano), hashCode(c.Code)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Redeem(context.Background(), c.Code, "192.168.1.10"); err != ErrBadCode {
		t.Fatalf("expired redeem err = %v, want ErrBadCode", err)
	}
}

func TestRedeemRateLimited(t *testing.T) {
	s := newTestStore(t)
	lim := s.attempts
	// 先把窗口打满。
	for i := 0; i < lim.perMin; i++ {
		if !lim.Allow("9.9.9.9") {
			t.Fatalf("allow %d failed", i)
		}
	}
	if _, err := s.Redeem(context.Background(), "123456", "9.9.9.9"); err != ErrRateLimited {
		t.Fatalf("rate limited err = %v, want ErrRateLimited", err)
	}
}

func TestHashDoesNotContainCode(t *testing.T) {
	h := hashCode("123456")
	if len(h) != 64 || contains(h, "123456") {
		t.Fatalf("hash = %q", h)
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
