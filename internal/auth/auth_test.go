package auth

import "testing"

// Argon2id 哈希/校验 roundtrip 与错误口令拒绝。
func TestHashVerifyPassword(t *testing.T) {
	h, err := HashPassword("s3cret-passw0rd")
	if err != nil {
		t.Fatal(err)
	}
	if len(h) < 60 || h[:9] != "$argon2id" {
		t.Fatalf("bad hash encoding: %q", h[:min(20, len(h))])
	}
	ok, err := VerifyPassword("s3cret-passw0rd", h)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("correct password rejected")
	}
	ok, err = VerifyPassword("wrong-password", h)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("wrong password accepted")
	}
	// 篡改哈希串必须失败（格式校验/解码错误或比较不等）。
	ok, _ = VerifyPassword("s3cret-passw0rd", h+"x")
	if ok {
		t.Fatal("tampered hash accepted")
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
