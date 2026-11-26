package ttscache

import (
	"bytes"
	"crypto/rand"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func newStore(t *testing.T) (*Store, []byte) {
	t.Helper()
	kek := make([]byte, 32)
	if _, err := rand.Read(kek); err != nil {
		t.Fatal(err)
	}
	st, err := New(filepath.Join(t.TempDir(), "tts-cache"), kek, 4<<20)
	if err != nil {
		t.Fatal(err)
	}
	return st, kek
}

func TestPutGetRoundtrip(t *testing.T) {
	st, _ := newStore(t)
	key := CacheKeyForTest("hello")
	wav := []byte("RIFF\x00\x00\x00\x00WAVEfmt data-plain-payload")
	if err := st.Put(key, wav); err != nil {
		t.Fatal(err)
	}
	got, err := st.Get(key)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, wav) {
		t.Fatal("roundtrip mismatch")
	}
	if err := st.Verify(key); err != nil {
		t.Fatalf("verify: %v", err)
	}
}

// 3. 落盘必须是密文：文件头不是 RIFF，且不含明文 WAV 标识。
func TestCiphertextNotPlainWAVOnDisk(t *testing.T) {
	st, _ := newStore(t)
	key := CacheKeyForTest("plain-check")
	wav := []byte("RIFF\x24\x08\x00\x00WAVEfmt \x10\x00\x00\x00unique-plain-marker")
	if err := st.Put(key, wav); err != nil {
		t.Fatal(err)
	}
	p, err := st.path(key)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.HasPrefix(raw, []byte("RIFF")) {
		t.Fatal("缓存文件以 RIFF 开头，疑似明文落盘")
	}
	if bytes.Contains(raw, []byte("unique-plain-marker")) {
		t.Fatal("缓存文件包含明文内容")
	}
	if !bytes.HasPrefix(raw, []byte("NSAC")) {
		t.Fatalf("缺少缓存 magic: %q", raw[:4])
	}
	if len(raw) != len(wav)+HeaderSize+16 {
		t.Fatalf("密文长度异常: %d", len(raw))
	}
}

// 7. 篡改任意字节 → 解密失败。
func TestTamperDetected(t *testing.T) {
	st, _ := newStore(t)
	key := CacheKeyForTest("tamper")
	wav := bytes.Repeat([]byte("RIFFdata"), 100)
	if err := st.Put(key, wav); err != nil {
		t.Fatal(err)
	}
	p, _ := st.path(key)
	raw, _ := os.ReadFile(p)
	for _, off := range []int{5, 20, 60, len(raw) - 1} { // 覆盖 wrapNonce/wrappedDEK/audioNonce/密文
		bad := append([]byte(nil), raw...)
		bad[off] ^= 0x01
		if err := os.WriteFile(p, bad, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := st.Get(key); !errors.Is(err, ErrTampered) {
			t.Fatalf("offset %d 篡改未检出: %v", off, err)
		}
		if err := st.Verify(key); !errors.Is(err, ErrTampered) {
			t.Fatalf("offset %d Verify 未检出: %v", off, err)
		}
	}
}

// 用不同主 KEK 无法解密（密钥不匹配）。
func TestWrongMasterKEK(t *testing.T) {
	st, _ := newStore(t)
	key := CacheKeyForTest("wrong-kek")
	if err := st.Put(key, []byte("payload")); err != nil {
		t.Fatal(err)
	}
	other := make([]byte, 32)
	rand.Read(other)
	st2, err := New(st.Dir(), other, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st2.Get(key); !errors.Is(err, ErrTampered) {
		t.Fatalf("错误 KEK 应解密失败, got %v", err)
	}
}

// 键名换用到另一文件也会失败（AAD 绑定 cacheKey）。
func TestKeyBinding(t *testing.T) {
	st, _ := newStore(t)
	k1, k2 := CacheKeyForTest("a"), CacheKeyForTest("b")
	if err := st.Put(k1, []byte("payload-a")); err != nil {
		t.Fatal(err)
	}
	p1, _ := st.path(k1)
	p2, _ := st.path(k2)
	raw, _ := os.ReadFile(p1)
	if err := os.MkdirAll(filepath.Dir(p2), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p2, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Get(k2); !errors.Is(err, ErrTampered) {
		t.Fatalf("复制到其他键应解密失败, got %v", err)
	}
}

func TestNotFoundAndStats(t *testing.T) {
	st, _ := newStore(t)
	if _, err := st.Get(CacheKeyForTest("missing")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("got %v", err)
	}
	for i := 0; i < 3; i++ {
		k := CacheKeyForTest(string(rune('a' + i)))
		if err := st.Put(k, bytes.Repeat([]byte("x"), 1000)); err != nil {
			t.Fatal(err)
		}
	}
	n, bytesTotal, err := st.Stats()
	if err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Fatalf("segments=%d", n)
	}
	if bytesTotal <= 3000 {
		t.Fatalf("cipher bytes 过小: %d", bytesTotal)
	}
}

func TestTooLarge(t *testing.T) {
	kek := make([]byte, 32)
	st, err := New(filepath.Join(t.TempDir(), "c"), kek, 100)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Put(CacheKeyForTest("big"), bytes.Repeat([]byte("x"), 101)); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("got %v", err)
	}
}

func TestInvalidKey(t *testing.T) {
	st, _ := newStore(t)
	if err := st.Put("../evil", []byte("x")); err == nil {
		t.Fatal("非法键应被拒绝")
	}
}

// CacheKeyForTest 生成固定格式的测试键（不依赖 tts 包，避免循环依赖）。
func CacheKeyForTest(seed string) string {
	h := make([]byte, 32)
	copy(h, []byte(seed))
	for i := len(seed); i < 32; i++ {
		h[i] = byte(i)
	}
	const hexdigits = "0123456789abcdef"
	out := make([]byte, 64)
	for i, b := range h {
		out[i*2] = hexdigits[b>>4]
		out[i*2+1] = hexdigits[b&0xf]
	}
	return string(out)
}

// 第二阶段写入的缓存是 v1（无 keyVersion 字节），第四阶段必须仍能读取。
func TestReadLegacyV1AndRewrap(t *testing.T) {
	st, kek := newStore(t)
	key := "abcd1234legacy"
	plain := []byte("RIFF-legacy-pcm")

	dek := make([]byte, keySize)
	if _, err := rand.Read(dek); err != nil {
		t.Fatal(err)
	}
	wrap, err := gcmFor(st.keys[1])
	if err != nil {
		t.Fatal(err)
	}
	wrapNonce := make([]byte, nonceSize)
	if _, err := rand.Read(wrapNonce); err != nil {
		t.Fatal(err)
	}
	wrapped := wrap.Seal(nil, wrapNonce, dek, []byte(wrapAADPref+key))
	data, err := gcmFor(dek)
	if err != nil {
		t.Fatal(err)
	}
	audioNonce := make([]byte, nonceSize)
	if _, err := rand.Read(audioNonce); err != nil {
		t.Fatal(err)
	}
	ct := data.Seal(nil, audioNonce, plain, []byte(audioAADPref+key))

	raw := make([]byte, 0, headerV1+len(ct))
	raw = append(raw, magic...)
	raw = append(raw, formatV1)
	raw = append(raw, wrapNonce...)
	raw = append(raw, wrapped...)
	raw = append(raw, audioNonce...)
	raw = append(raw, ct...)
	if err := os.MkdirAll(filepath.Dir(filepath.Join(st.dir, "ab")), 0o700); err != nil {
		t.Fatal(err)
	}
	p, err := st.path(key)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, raw, 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := st.Get(key)
	if err != nil || !bytes.Equal(got, plain) {
		t.Fatalf("读取 v1 失败: %v", err)
	}
	st2, err := NewWithKeyRing(st.dir, map[int][]byte{1: kek, 2: make([]byte, 32)}, 2, 4<<20)
	if err != nil {
		t.Fatal(err)
	}
	res, err := st2.Rewrap(key, 2)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Rotated || res.OldVer != 1 || res.NewVer != 2 {
		t.Fatalf("rewrap = %+v", res)
	}
	raw2, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if raw2[4] != formatV2 || raw2[5] != 2 {
		t.Fatalf("重写后头部 = %v", raw2[:6])
	}
	if got, err := st2.Get(key); err != nil || !bytes.Equal(got, plain) {
		t.Fatalf("重包装后读取失败: %v", err)
	}
}
