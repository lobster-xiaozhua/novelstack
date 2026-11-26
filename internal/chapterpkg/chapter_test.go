package chapterpkg

import (
	"bytes"
	"crypto/rand"
	"testing"
)

func testDEK(t *testing.T) []byte {
	t.Helper()
	d := make([]byte, 32)
	if _, err := rand.Read(d); err != nil {
		t.Fatal(err)
	}
	return d
}

func TestSealOpenRoundtrip(t *testing.T) {
	dek := testDEK(t)
	plain := []byte("第一章 测试\n这是一段正文，包含中文与 emoji 🙂。")
	pkg, err := Seal(dek, "book-1", 3, plain)
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	if len(pkg) != ChapterHeader+len(plain)+16 {
		t.Fatalf("package size = %d", len(pkg))
	}
	back, err := Open(dek, "book-1", 3, pkg)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if !bytes.Equal(back, plain) {
		t.Fatal("roundtrip mismatch")
	}
}

func TestOpenRejectsTamperAndWrongContext(t *testing.T) {
	dek := testDEK(t)
	pkg, err := Seal(dek, "book-1", 0, []byte("secret body"))
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < len(pkg); i++ {
		bad := bytes.Clone(pkg)
		bad[i] ^= 0x01
		if _, err := Open(dek, "book-1", 0, bad); err == nil {
			t.Fatalf("tampered byte %d accepted", i)
		}
	}
	if _, err := Open(dek, "book-2", 0, pkg); err == nil {
		t.Fatal("wrong bookID accepted")
	}
	if _, err := Open(dek, "book-1", 1, pkg); err == nil {
		t.Fatal("wrong chapter index accepted")
	}
	other := testDEK(t)
	if _, err := Open(other, "book-1", 0, pkg); err == nil {
		t.Fatal("wrong DEK accepted")
	}
}

func TestSealRejectsBadInput(t *testing.T) {
	if _, err := Seal(make([]byte, 16), "b", 0, []byte("x")); err == nil {
		t.Fatal("16-byte DEK should be rejected")
	}
	if _, err := Open(testDEK(t), "b", 0, []byte("NSCH")); err == nil {
		t.Fatal("truncated package should be rejected")
	}
}
