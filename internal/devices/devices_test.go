package devices_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"novelstack/internal/db"
	"novelstack/internal/devices"
)

func newStore(t *testing.T) *devices.Store {
	t.Helper()
	s, _ := newStoreDB(t)
	return s
}

func newStoreDB(t *testing.T) (*devices.Store, *sql.DB) {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatalf("db: %v", err)
	}
	t.Cleanup(func() { d.Close() })
	return devices.NewStore(d), d
}

func newKeyPEM(t *testing.T) (*rsa.PrivateKey, string) {
	t.Helper()
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	pemStr, err := devices.EncodePublicKeyPEM(&priv.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	return priv, pemStr
}

func TestCreateAuthenticateRevoke(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	token, dev, err := s.Create(ctx, "admin", "Pixel 8", "", time.Hour)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if len(token) < 30 || token[:4] != devices.TokenPrefix {
		t.Fatalf("unexpected token shape: %q", token[:min(8, len(token))])
	}
	if dev.ID == "" || dev.Prefix == "" {
		t.Fatalf("device metadata incomplete: %+v", dev)
	}
	if dev.HasKey {
		t.Fatal("empty public key should mark HasKey=false")
	}

	got, owner, err := s.Authenticate(ctx, token)
	if err != nil {
		t.Fatalf("auth: %v", err)
	}
	if owner != "admin" || got.ID != dev.ID {
		t.Fatalf("auth mismatch: owner=%q id=%q", owner, got.ID)
	}

	list, err := s.List(ctx, "admin")
	if err != nil || len(list) != 1 {
		t.Fatalf("list: %v len=%d", err, len(list))
	}
	active, revoked, err := s.Count(ctx, "admin")
	if err != nil || active != 1 || revoked != 0 {
		t.Fatalf("count: %v %d/%d", err, active, revoked)
	}

	ok, err := s.Revoke(ctx, "admin", dev.ID)
	if err != nil || !ok {
		t.Fatalf("revoke: %v ok=%t", err, ok)
	}
	if _, _, err := s.Authenticate(ctx, token); err != devices.ErrRevoked {
		t.Fatalf("revoked token err = %v, want ErrRevoked", err)
	}
	// 幂等：再次撤销仍成功。
	if ok, err := s.Revoke(ctx, "admin", dev.ID); err != nil || !ok {
		t.Fatalf("second revoke: %v ok=%t", err, ok)
	}
	// 别的用户不能撤销。
	if ok, err := s.Revoke(ctx, "someone", dev.ID); err != nil || ok {
		t.Fatalf("cross-owner revoke: %v ok=%t", err, ok)
	}
	active, revoked, _ = s.Count(ctx, "admin")
	if active != 0 || revoked != 1 {
		t.Fatalf("count after revoke = %d/%d", active, revoked)
	}
}

func TestAuthenticateRejectsBadTokens(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	for _, tok := range []string{"", "abc", "nsd_short", "Bearer x", "nsd_" + string(make([]byte, 40))} {
		if _, _, err := s.Authenticate(ctx, tok); err == nil {
			t.Fatalf("token %q should be rejected", tok)
		}
	}
}

func TestExpiredToken(t *testing.T) {
	s, database := newStoreDB(t)
	ctx := context.Background()
	token, dev, err := s.Create(ctx, "admin", "old", "", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Authenticate(ctx, token); err != nil {
		t.Fatalf("fresh token should work: %v", err)
	}
	if _, err := database.Exec(`UPDATE device_tokens SET expires_at = ? WHERE id = ?`,
		time.Now().UTC().Add(-time.Hour).Format(time.RFC3339Nano), dev.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Authenticate(ctx, token); err != devices.ErrExpired {
		t.Fatalf("expired token err = %v, want ErrExpired", err)
	}
}

func TestPairingCodeOneShot(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	pc, err := s.CreatePairing(ctx, "admin", time.Minute)
	if err != nil {
		t.Fatalf("pairing: %v", err)
	}
	if len(pc.Code) < 10 || pc.Code[:4] != devices.PairingPrefix {
		t.Fatalf("bad pairing code %q", pc.Code)
	}
	owner, err := s.RedeemPairing(ctx, pc.Code)
	if err != nil || owner != "admin" {
		t.Fatalf("redeem: %v owner=%q", err, owner)
	}
	if _, err := s.RedeemPairing(ctx, pc.Code); err != devices.ErrPairUsed {
		t.Fatalf("second redeem err = %v, want ErrPairUsed", err)
	}
	if _, err := s.RedeemPairing(ctx, "nsp_nope"); err != devices.ErrBadPair {
		t.Fatalf("bad code err = %v", err)
	}
}

func TestPublicKeyAndDEKWrap(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	priv, pemStr := newKeyPEM(t)

	token, dev, err := s.Create(ctx, "admin", "Pixel", pemStr, time.Hour)
	if err != nil {
		t.Fatalf("create with key: %v", err)
	}
	if !dev.HasKey {
		t.Fatal("HasKey should be true")
	}
	got, _, err := s.Authenticate(ctx, token)
	if err != nil || got.PublicKey == "" {
		t.Fatalf("auth with key: %v", err)
	}

	dek := make([]byte, 32)
	if _, err := rand.Read(dek); err != nil {
		t.Fatal(err)
	}
	wrapped, err := devices.WrapDEK(&priv.PublicKey, dek)
	if err != nil {
		t.Fatalf("wrap: %v", err)
	}
	back, err := devices.UnwrapDEK(priv, wrapped)
	if err != nil || string(back) != string(dek) {
		t.Fatalf("unwrap: %v", err)
	}
	if _, err := devices.WrapDEK(&priv.PublicKey, make([]byte, 16)); err == nil {
		t.Fatal("16-byte DEK should be rejected")
	}
	if _, err := devices.ParseRSAPublicKey("not a pem"); err == nil {
		t.Fatal("garbage PEM should be rejected")
	}

	// 1024 位太短。
	weak, _ := rsa.GenerateKey(rand.Reader, 1024)
	weakPEM, _ := devices.EncodePublicKeyPEM(&weak.PublicKey)
	if _, _, err := s.Create(ctx, "admin", "weak", weakPEM, time.Hour); err == nil {
		t.Fatal("1024-bit key should be rejected")
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
