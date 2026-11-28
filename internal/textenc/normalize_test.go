package textenc

import (
	"bytes"
	"io"
	"strings"
	"testing"

	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/encoding/unicode"
	"golang.org/x/text/transform"
)

func readAll(t *testing.T, r io.Reader) string {
	t.Helper()
	b, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestNormalizeUTF8(t *testing.T) {
	in := "第一章 夜航船\n他推开门。"
	got := readAll(t, Normalize(strings.NewReader(in)))
	if got != in {
		t.Fatalf("utf8 passthrough: %q", got)
	}
}

func TestNormalizeUTF8BOM(t *testing.T) {
	in := "\xEF\xBB\xBF第一章\n正文"
	got := readAll(t, Normalize(strings.NewReader(in)))
	if got != "第一章\n正文" {
		t.Fatalf("bom: %q", got)
	}
}

func TestNormalizeGBK(t *testing.T) {
	plain := "第一章 夜航船\n他推开门。"
	enc, _, err := transform.Bytes(simplifiedchinese.GB18030.NewEncoder(), []byte(plain))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(enc, []byte(plain)) {
		t.Fatal("test setup: gk encoded == utf8?")
	}
	got := readAll(t, Normalize(bytes.NewReader(enc)))
	if got != plain {
		t.Fatalf("gbk decode: want %q got %q", plain, got)
	}
}

func TestNormalizeUTF16LE(t *testing.T) {
	plain := "第一章\n正文"
	enc, _, err := transform.Bytes(unicode_utf16le_encoder(), []byte(plain))
	if err != nil {
		t.Fatal(err)
	}
	withBOM := append([]byte{0xFF, 0xFE}, enc...)
	got := readAll(t, Normalize(bytes.NewReader(withBOM)))
	if got != plain {
		t.Fatalf("utf16le: want %q got %q", plain, got)
	}
}

func unicode_utf16le_encoder() transform.Transformer {
	return unicode.UTF16(unicode.LittleEndian, unicode.IgnoreBOM).NewEncoder()
}
