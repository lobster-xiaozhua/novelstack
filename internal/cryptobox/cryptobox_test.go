package cryptobox

import (
	"bytes"
	"crypto/rand"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newKEK(t *testing.T) []byte {
	t.Helper()
	k := make([]byte, KeySize)
	if _, err := rand.Read(k); err != nil {
		t.Fatal(err)
	}
	return k
}

func newDEKForTest(t *testing.T) []byte {
	t.Helper()
	d, err := NewDEK()
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// 1. 加密 roundtrip：加密后解密内容一致（覆盖单块与多块）。
func TestEncryptDecryptRoundtrip(t *testing.T) {
	kek := newKEK(t)
	dek := newDEKForTest(t)
	wrapped, nonce, err := WrapDEK(kek, dek)
	if err != nil {
		t.Fatalf("wrap: %v", err)
	}
	got, err := UnwrapDEK(kek, wrapped, nonce)
	if err != nil {
		t.Fatalf("unwrap: %v", err)
	}
	if !bytes.Equal(got, dek) {
		t.Fatal("DEK unwrap mismatch")
	}

	for _, size := range []int{1, 1024, 1 << 20, (1 << 20) + 123, (3 << 20) + 7} {
		plain := make([]byte, size)
		if _, err := rand.Read(plain); err != nil {
			t.Fatal(err)
		}
		var ct bytes.Buffer
		sum, err := EncryptStream(bytes.NewReader(plain), &ct, dek, "book-1", 1<<20)
		if err != nil {
			t.Fatalf("encrypt size=%d: %v", size, err)
		}
		var out bytes.Buffer
		gotSum, gotSize, err := DecryptStream(bytes.NewReader(ct.Bytes()), &out, dek, "book-1", 1<<20, int64(size)+1024)
		if err != nil {
			t.Fatalf("decrypt size=%d: %v", size, err)
		}
		if gotSize != int64(size) {
			t.Fatalf("size mismatch: got %d want %d", gotSize, size)
		}
		if !bytes.Equal(out.Bytes(), plain) {
			t.Fatalf("roundtrip mismatch size=%d", size)
		}
		if !bytes.Equal(gotSum, sum) {
			t.Fatalf("sha mismatch size=%d", size)
		}
	}
}

// 2. 篡改密文任意一个字节，解密必须失败（逐字节扫描最后一块与中间块）。
func TestTamperDetection(t *testing.T) {
	dek := newDEKForTest(t)
	plain := bytes.Repeat([]byte("A novel body line.\n"), 200000) // ~3.4MB，多块
	var ct bytes.Buffer
	if _, err := EncryptStream(bytes.NewReader(plain), &ct, dek, "book-2", 1<<20); err != nil {
		t.Fatal(err)
	}
	orig := ct.Bytes()

	// 篡改头部 → 格式错误。
	bad := append([]byte(nil), orig...)
	bad[0] ^= 0xFF
	if _, _, err := DecryptStream(bytes.NewReader(bad), io.Discard, dek, "book-2", 1<<20, 1<<30); err == nil {
		t.Fatal("tampered header accepted")
	}

	// 篡改每个块的第 20 字节（密文区）→ ErrTampered。
	chunkCT := 1<<20 + 16 + NonceSize
	nChunks := (len(orig) - HeaderSize + chunkCT - 1) / chunkCT
	for c := 0; c < nChunks; c++ {
		bad := append([]byte(nil), orig...)
		pos := HeaderSize + c*chunkCT + NonceSize + 20
		if pos >= len(bad) {
			pos = len(bad) - 1
		}
		bad[pos] ^= 0x01
		_, _, err := DecryptStream(bytes.NewReader(bad), io.Discard, dek, "book-2", 1<<20, 1<<30)
		if err != ErrTampered {
			t.Fatalf("tampered chunk %d not detected: %v", c, err)
		}
	}

	// 篡改 nonce → ErrTampered。
	bad = append([]byte(nil), orig...)
	bad[HeaderSize+1] ^= 0x01
	if _, _, err := DecryptStream(bytes.NewReader(bad), io.Discard, dek, "book-2", 1<<20, 1<<30); err != ErrTampered {
		t.Fatalf("tampered nonce not detected: %v", err)
	}
}

// AAD 绑定：用错误 bookID 解密必须失败。
func TestAADBinding(t *testing.T) {
	dek := newDEKForTest(t)
	var ct bytes.Buffer
	if _, err := EncryptStream(strings.NewReader("hello"), &ct, dek, "book-A", 1<<20); err != nil {
		t.Fatal(err)
	}
	if _, _, err := DecryptStream(bytes.NewReader(ct.Bytes()), io.Discard, dek, "book-B", 1<<20, 1<<10); err != ErrTampered {
		t.Fatalf("wrong bookID accepted: %v", err)
	}
}

// 错误 KEK 无法解包 DEK。
func TestWrongKEK(t *testing.T) {
	kek1, kek2 := newKEK(t), newKEK(t)
	dek := newDEKForTest(t)
	wrapped, nonce, err := WrapDEK(kek1, dek)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := UnwrapDEK(kek2, wrapped, nonce); err != ErrTampered {
		t.Fatalf("wrong KEK accepted: %v", err)
	}
}

// 3. KEK 文件：原始 32 字节与 base64 均可，格式错误 fail-fast。
func TestLoadKEK(t *testing.T) {
	raw := make([]byte, KeySize)
	rand.Read(raw)

	cases := []struct {
		name    string
		content []byte
		want    []byte
		wantErr bool
	}{
		{"raw32", raw, raw, false},
		{"raw32-with-newline", append(append([]byte(nil), raw...), '\n'), raw, false},
		{"base64-std", []byte(encodeStd(raw) + "\n"), raw, false},
		{"garbage", []byte("short"), nil, true},
		{"wrong-size", make([]byte, 31), nil, true},
	}
	dir := t.TempDir()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := filepath.Join(dir, "kek")
			if err := os.WriteFile(p, tc.content, 0o600); err != nil {
				t.Fatal(err)
			}
			got, err := LoadKEK(p)
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, tc.want) {
				t.Fatal("kek mismatch")
			}
		})
	}
}

func encodeStd(b []byte) string {
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"
	var out strings.Builder
	for i := 0; i < len(b); i += 3 {
		var v uint32
		n := len(b) - i
		v = uint32(b[i]) << 16
		if n > 1 {
			v |= uint32(b[i+1]) << 8
		}
		if n > 2 {
			v |= uint32(b[i+2])
		}
		out.WriteByte(alphabet[(v>>18)&63])
		out.WriteByte(alphabet[(v>>12)&63])
		if n > 1 {
			out.WriteByte(alphabet[(v>>6)&63])
		} else {
			out.WriteByte('=')
		}
		if n > 2 {
			out.WriteByte(alphabet[v&63])
		} else {
			out.WriteByte('=')
		}
	}
	return out.String()
}

// 空明文拒绝加密。
func TestEmptyInputRejected(t *testing.T) {
	dek := newDEKForTest(t)
	var ct bytes.Buffer
	if _, err := EncryptStream(strings.NewReader(""), &ct, dek, "b", 1<<20); err != ErrBadFormat {
		t.Fatalf("empty input accepted: %v", err)
	}
}
