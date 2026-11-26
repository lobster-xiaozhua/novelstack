package backup

import (
	"bytes"
	"crypto/rand"
	"errors"
	"io"
	"testing"
)

func testKDF() KDFParams { return KDFParams{Time: 1, Memory: 8 * 1024, Threads: 1} }

func TestFormatRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	w, h, err := NewWriter(&buf, []byte("correct horse battery staple"), testKDF(), CompressionNone, 64<<10)
	if err != nil {
		t.Fatal(err)
	}
	if h.ChunkSize != 64<<10 {
		t.Fatalf("chunk = %d", h.ChunkSize)
	}
	payload := make([]byte, 150_000)
	if _, err := rand.Read(payload); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(payload[:70_000]); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(payload[70_000:]); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	r, hdr, err := NewReader(bytes.NewReader(buf.Bytes()), []byte("correct horse battery staple"))
	if err != nil {
		t.Fatal(err)
	}
	if hdr.Compression != CompressionNone || hdr.Params != testKDF() {
		t.Fatalf("header mismatch: %+v", hdr)
	}
	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("roundtrip mismatch: %d vs %d", len(got), len(payload))
	}
}

func TestFormatWrongPassphrase(t *testing.T) {
	raw := sealForTest(t, []byte("payload"), []byte("passphrase-one"))
	r, _, err := NewReader(bytes.NewReader(raw), []byte("passphrase-two"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadAll(r); !errors.Is(err, ErrDecrypt) {
		t.Fatalf("err = %v, want ErrDecrypt", err)
	}
}

func TestFormatTamperAnyByteFails(t *testing.T) {
	raw := sealForTest(t, bytes.Repeat([]byte("x"), 5000), []byte("passphrase"))
	// 头部、密文区各取一个字节翻转
	for _, off := range []int{0, 6, 20, len(raw) / 2, len(raw) - 1} {
		mod := append([]byte(nil), raw...)
		mod[off] ^= 0x01
		r, _, err := NewReader(bytes.NewReader(mod), []byte("passphrase"))
		if err == nil {
			_, err = io.ReadAll(r)
		}
		if err == nil {
			t.Fatalf("offset %d 篡改未被发现", off)
		}
		if !errors.Is(err, ErrDecrypt) && !errors.Is(err, ErrFormat) {
			t.Fatalf("offset %d err = %v", off, err)
		}
	}
}

func TestFormatTruncatedFails(t *testing.T) {
	raw := sealForTest(t, bytes.Repeat([]byte("x"), 5000), []byte("passphrase"))
	cut := raw[:len(raw)-5]
	r, _, err := NewReader(bytes.NewReader(cut), []byte("passphrase"))
	if err == nil {
		_, err = io.ReadAll(r)
	}
	if err == nil {
		t.Fatal("截断未被发现")
	}
}

func sealForTest(t *testing.T, payload, pass []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	w, _, err := NewWriter(&buf, pass, testKDF(), CompressionGzip, 64<<10)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(payload); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}
