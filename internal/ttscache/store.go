// Package ttscache 实现片段级加密音频缓存。
//
// 密钥层次（与书籍存储一致的信封原则，密钥不落盘）：
//
//	主 KEK（只读 secret 文件，仅内存）
//	  └─ HKDF-SHA256(info="novelstack-tts-audio-kek-v1") → 音频 KEK（仅内存，不落盘）
//	       └─ 每个片段独立随机 DEK（32B，仅内存）
//	            └─ AES-256-GCM 加密该片段 WAV
//
// 缓存文件格式（版本化）：
//
//	v1:
//	  offset 0   magic "NSAC"      4B
//	  offset 4   version 0x01      1B
//	  offset 5   wrapNonce        12B
//	  offset 17  wrappedDEK       48B
//	  offset 65  audioNonce       12B
//	  offset 77  ciphertext+tag   N+16B
//
//	v2（第四阶段，带 KEK 版本，支持轮换窗口期读取旧缓存）:
//	  offset 0   magic "NSAC"      4B
//	  offset 4   version 0x02      1B
//	  offset 5   keyVersion       1B
//	  offset 6   wrapNonce        12B
//	  ... 其余同 v1
//
// 两处 AAD 都绑定 cacheKey，防止缓存文件被改名/挪用；篡改任意字节都会导致解密失败。
package ttscache

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"golang.org/x/crypto/hkdf"
)

var (
	// ErrNotFound 表示缓存未命中。
	ErrNotFound = errors.New("ttscache: not found")
	// ErrTampered 表示密文被篡改或密钥不匹配。
	ErrTampered = errors.New("ttscache: tampered or key mismatch")
	// ErrTooLarge 表示缓存明文超过上限。
	ErrTooLarge = errors.New("ttscache: plaintext too large")
	// ErrKeyVersion 表示本进程没有该 KEK 版本。
	ErrKeyVersion = errors.New("ttscache: kek version not loaded")
)

const (
	magic        = "NSAC"
	formatV1     = 1
	formatV2     = 2
	nonceSize    = 12
	keySize      = 32
	wrappedSize  = keySize + 16 // DEK + GCM tag
	headerV1     = 4 + 1 + nonceSize + wrappedSize + nonceSize
	headerV2     = 4 + 1 + 1 + nonceSize + wrappedSize + nonceSize
	wrapAADPref  = "novelstack-tts-dek-wrap-v1|"
	audioAADPref = "novelstack-tts-audio-v1|"
)

// Store 是片段级加密缓存。所有方法并发安全（文件系统级隔离 + 原子 rename）。
type Store struct {
	dir      string
	keys     map[int][]byte // KEK 版本 → 派生后的音频 KEK
	current  int
	maxPlain int64
}

// New 用主 KEK 派生音频 KEK（版本 1）并准备缓存目录。
func New(dir string, masterKEK []byte, maxPlain int64) (*Store, error) {
	return NewWithKeyRing(dir, map[int][]byte{1: masterKEK}, 1, maxPlain)
}

// NewWithKeyRing 支持多版本主 KEK：写入使用 current，读取按文件内 keyVersion 选择。
// 轮换窗口期内同时提供旧/新主 KEK，服务可继续读旧缓存。
func NewWithKeyRing(dir string, masterKEKs map[int][]byte, current int, maxPlain int64) (*Store, error) {
	if dir == "" {
		return nil, fmt.Errorf("ttscache: 缓存目录不能为空")
	}
	if current <= 0 {
		current = 1
	}
	if len(masterKEKs) == 0 {
		return nil, fmt.Errorf("ttscache: 至少需要一个主 KEK")
	}
	derived := make(map[int][]byte, len(masterKEKs))
	for ver, master := range masterKEKs {
		if ver <= 0 {
			return nil, fmt.Errorf("ttscache: 非法的 KEK 版本 %d", ver)
		}
		if len(master) != keySize {
			return nil, fmt.Errorf("ttscache: 主 KEK 长度必须为 %d 字节", keySize)
		}
		k := make([]byte, keySize)
		// HKDF-SHA256：与书籍加密使用的 KEK 隔离，即使缓存 KEK 泄露也不影响书籍密文
		if _, err := io.ReadFull(hkdf.New(sha256.New, master, nil, []byte("novelstack-tts-audio-kek-v1")), k); err != nil {
			return nil, fmt.Errorf("ttscache: 派生音频 KEK 失败: %w", err)
		}
		derived[ver] = k
	}
	if _, ok := derived[current]; !ok {
		return nil, fmt.Errorf("ttscache: 当前 KEK 版本 %d 未提供", current)
	}
	if maxPlain <= 0 {
		maxPlain = 16 << 20
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("ttscache: 创建缓存目录失败: %w", err)
	}
	return &Store{dir: dir, keys: derived, current: current, maxPlain: maxPlain}, nil
}

// Dir 返回缓存根目录（用于指标展示，不含密钥）。
func (s *Store) Dir() string { return s.dir }

// CurrentVersion 返回写入使用的 KEK 版本。
func (s *Store) CurrentVersion() int { return s.current }

// Versions 返回已加载的 KEK 版本（升序）。
func (s *Store) Versions() []int {
	out := make([]int, 0, len(s.keys))
	for v := range s.keys {
		out = append(out, v)
	}
	sort.Ints(out)
	return out
}

// path 返回分片路径：<dir>/<key 前 2 位>/<key>.bin，避免单目录文件过多。
func (s *Store) path(key string) (string, error) {
	if len(key) < 3 || strings.ContainsAny(key, "/\\") {
		return "", fmt.Errorf("ttscache: 非法缓存键")
	}
	return filepath.Join(s.dir, key[:2], key+".bin"), nil
}

// PathFor 返回缓存键对应的文件路径（用于清理，不涉及密钥）。
func (s *Store) PathFor(key string) (string, error) { return s.path(key) }

func gcmFor(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// Put 把明文音频加密后原子写入缓存（格式 v2，带当前 KEK 版本）。
func (s *Store) Put(key string, plain []byte) error {
	if int64(len(plain)) > s.maxPlain {
		return ErrTooLarge
	}
	p, err := s.path(key)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return fmt.Errorf("ttscache: 创建分片目录失败: %w", err)
	}

	dek := make([]byte, keySize)
	if _, err := rand.Read(dek); err != nil {
		return fmt.Errorf("ttscache: 生成片段 DEK 失败: %w", err)
	}
	wrapAEAD, err := gcmFor(s.keys[s.current])
	if err != nil {
		return err
	}
	wrapNonce := make([]byte, nonceSize)
	if _, err := rand.Read(wrapNonce); err != nil {
		return err
	}
	wrapped := wrapAEAD.Seal(nil, wrapNonce, dek, []byte(wrapAADPref+key))

	dataAEAD, err := gcmFor(dek)
	if err != nil {
		return err
	}
	audioNonce := make([]byte, nonceSize)
	if _, err := rand.Read(audioNonce); err != nil {
		return err
	}
	ct := dataAEAD.Seal(nil, audioNonce, plain, []byte(audioAADPref+key))

	out := make([]byte, 0, headerV2+len(ct))
	out = append(out, magic...)
	out = append(out, formatV2, byte(s.current))
	out = append(out, wrapNonce...)
	out = append(out, wrapped...)
	out = append(out, audioNonce...)
	out = append(out, ct...)

	return writeAtomic(p, out)
}

// Get 读取并解密缓存片段。命中返回明文 WAV；未命中返回 ErrNotFound；被篡改返回 ErrTampered。
func (s *Store) Get(key string) ([]byte, error) {
	return s.load(key, true)
}

// Verify 只校验可解密性（解密到 io.Discard），用于流式输出前的全量预检。
func (s *Store) Verify(key string) error {
	_, err := s.load(key, false)
	return err
}

type fileLayout struct {
	keyVersion int
	wrapNonce  []byte
	wrapped    []byte
	audioNonce []byte
	ct         []byte
	raw        []byte
}

// parseFile 解析 v1/v2 缓存文件布局。
func parseFile(raw []byte, key string) (*fileLayout, error) {
	if len(raw) < headerV1+16 {
		return nil, ErrTampered
	}
	if string(raw[0:4]) != magic {
		return nil, fmt.Errorf("ttscache: 不支持的缓存格式")
	}
	l := &fileLayout{raw: raw}
	off := 4
	switch raw[off] {
	case formatV1:
		off++
		l.keyVersion = 1
	case formatV2:
		off++
		if off >= len(raw) {
			return nil, ErrTampered
		}
		l.keyVersion = int(raw[off])
		off++
	default:
		return nil, fmt.Errorf("ttscache: 不支持的缓存格式版本 %d", raw[4])
	}
	if len(raw) < off+nonceSize+wrappedSize+nonceSize+16 {
		return nil, ErrTampered
	}
	l.wrapNonce = raw[off : off+nonceSize]
	off += nonceSize
	l.wrapped = raw[off : off+wrappedSize]
	off += wrappedSize
	l.audioNonce = raw[off : off+nonceSize]
	off += nonceSize
	l.ct = raw[off:]
	return l, nil
}

// keyFor 取指定版本音频 KEK。
func (s *Store) keyFor(version int) ([]byte, error) {
	k, ok := s.keys[version]
	if !ok {
		return nil, fmt.Errorf("%w: v%d", ErrKeyVersion, version)
	}
	return k, nil
}

func (s *Store) load(key string, keep bool) ([]byte, error) {
	p, err := s.path(key)
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(p) // #nosec G304 -- 路径由缓存键推导，键已做字符校验
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("ttscache: 读取缓存失败: %w", err)
	}
	l, err := parseFile(raw, key)
	if err != nil {
		return nil, err
	}
	kek, err := s.keyFor(l.keyVersion)
	if err != nil {
		// 文件声称的 KEK 版本未加载：与无法解密同等对待，绝不输出可疑音频。
		return nil, ErrTampered
	}
	wrapAEAD, err := gcmFor(kek)
	if err != nil {
		return nil, err
	}
	dek, err := wrapAEAD.Open(nil, l.wrapNonce, l.wrapped, []byte(wrapAADPref+key))
	if err != nil {
		return nil, ErrTampered
	}
	dataAEAD, err := gcmFor(dek)
	if err != nil {
		return nil, err
	}
	plain, err := dataAEAD.Open(nil, l.audioNonce, l.ct, []byte(audioAADPref+key))
	if err != nil {
		return nil, ErrTampered
	}
	if int64(len(plain)) > s.maxPlain {
		return nil, ErrTooLarge
	}
	if !keep {
		return nil, nil // 校验模式：明文即弃，不返回
	}
	return plain, nil
}

// SegmentMeta 是移动端离线播放所需的段元信息。
// DEK 只在内存中返回给调用方用于「设备公钥再包装」，不得写日志或落盘。
type SegmentMeta struct {
	Key        string
	KeyVersion int
	FileBytes  int64
	CTOffset   int
	CTLen      int
	AudioNonce []byte
	DEK        []byte
}

// SegmentMeta 解析缓存文件，解出该段音频 DEK 与密文位置。
// 返回的 DEK 长度固定 32 字节；调用方用完后应清零。
func (s *Store) SegmentMeta(key string) (*SegmentMeta, error) {
	p, err := s.path(key)
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(p) // #nosec G304 -- 路径由缓存键推导，键已做字符校验
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	l, err := parseFile(raw, key)
	if err != nil {
		return nil, err
	}
	kek, err := s.keyFor(l.keyVersion)
	if err != nil {
		return nil, ErrTampered
	}
	wrapAEAD, err := gcmFor(kek)
	if err != nil {
		return nil, err
	}
	dek, err := wrapAEAD.Open(nil, l.wrapNonce, l.wrapped, []byte(wrapAADPref+key))
	if err != nil {
		return nil, ErrTampered
	}
	off := len(raw) - len(l.ct)
	if off < 0 || len(l.ct) == 0 {
		return nil, ErrTampered
	}
	nonce := make([]byte, len(l.audioNonce))
	copy(nonce, l.audioNonce)
	return &SegmentMeta{
		Key: key, KeyVersion: l.keyVersion, FileBytes: int64(len(raw)),
		CTOffset: off, CTLen: len(l.ct), AudioNonce: nonce, DEK: dek,
	}, nil
}

// Stats 统计缓存片段数与密文总字节数（遍历目录，权威数据在文件系统）。
func (s *Store) Stats() (segments int, bytes int64, err error) {
	err = s.WalkKeys(func(_ string, size int64, _ int) error {
		segments++
		bytes += size
		return nil
	})
	return segments, bytes, err
}

// WalkKeys 遍历全部缓存片段（只返回键、密文大小与 KEK 版本）。
func (s *Store) WalkKeys(fn func(key string, size int64, keyVersion int) error) error {
	var walkErr error
	err := filepath.Walk(s.dir, func(p string, info os.FileInfo, e error) error {
		if e != nil {
			if os.IsNotExist(e) {
				return nil
			}
			return e
		}
		if info.IsDir() || !strings.HasSuffix(p, ".bin") || strings.Contains(p, ".tmp") {
			return nil
		}
		name := filepath.Base(p)
		key := strings.TrimSuffix(name, ".bin")
		if len(key) < 3 {
			return nil
		}
		ver := 1
		if f, err := os.Open(p); err == nil { // 只读 6 字节头判断版本
			var hdr [6]byte
			if n, _ := io.ReadFull(f, hdr[:]); n == 6 && string(hdr[0:4]) == magic && hdr[4] == formatV2 {
				ver = int(hdr[5])
			}
			f.Close()
		}
		walkErr = fn(key, info.Size(), ver)
		return walkErr
	})
	if err != nil {
		return err
	}
	return walkErr
}

// CipherSize 返回缓存文件（密文）大小，用于指标记录；未命中返回 0。
func (s *Store) CipherSize(key string) int64 {
	p, err := s.path(key)
	if err != nil {
		return 0
	}
	st, err := os.Stat(p)
	if err != nil {
		return 0
	}
	return st.Size()
}

// Remove 删除一个缓存片段；不存在视为成功。
func (s *Store) Remove(key string) error {
	p, err := s.path(key)
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// RewrapResult 描述一次重包装的结果。
type RewrapResult struct {
	Key      string
	Rotated  bool
	OldVer   int
	NewVer   int
	FileSize int64
}

// Rewrap 把单个缓存片段的包装 DEK 从旧 KEK 版本换到新版本。
// 音频密文本身不变，只重写头部；已在新版本时返回 Rotated=false（幂等）。
func (s *Store) Rewrap(key string, newVersion int) (RewrapResult, error) {
	res := RewrapResult{Key: key, NewVer: newVersion}
	p, err := s.path(key)
	if err != nil {
		return res, err
	}
	raw, err := os.ReadFile(p) // #nosec G304
	if err != nil {
		if os.IsNotExist(err) {
			return res, ErrNotFound
		}
		return res, err
	}
	res.FileSize = int64(len(raw))
	l, err := parseFile(raw, key)
	if err != nil {
		return res, err
	}
	res.OldVer = l.keyVersion
	if l.keyVersion == newVersion {
		return res, nil
	}
	oldKEK, err := s.keyFor(l.keyVersion)
	if err != nil {
		return res, err
	}
	newKEK, err := s.keyFor(newVersion)
	if err != nil {
		return res, err
	}
	oldAEAD, err := gcmFor(oldKEK)
	if err != nil {
		return res, err
	}
	dek, err := oldAEAD.Open(nil, l.wrapNonce, l.wrapped, []byte(wrapAADPref+key))
	if err != nil {
		return res, ErrTampered
	}
	newAEAD, err := gcmFor(newKEK)
	if err != nil {
		return res, err
	}
	wrapNonce := make([]byte, nonceSize)
	if _, err := rand.Read(wrapNonce); err != nil {
		return res, err
	}
	wrapped := newAEAD.Seal(nil, wrapNonce, dek, []byte(wrapAADPref+key))

	out := make([]byte, 0, headerV2+len(l.ct))
	out = append(out, magic...)
	out = append(out, formatV2, byte(newVersion))
	out = append(out, wrapNonce...)
	out = append(out, wrapped...)
	out = append(out, l.audioNonce...)
	out = append(out, l.ct...)
	if err := writeAtomic(p, out); err != nil {
		return res, err
	}
	res.Rotated = true
	return res, nil
}

// RotateAll 遍历全部缓存，把非 newVersion 的片段重包装到新版本。
// 幂等：已在新版本的片段跳过；中途失败可重跑。
func (s *Store) RotateAll(newVersion int) (rotated int, skipped int, err error) {
	walkErr := s.WalkKeys(func(key string, _ int64, ver int) error {
		if ver == newVersion {
			skipped++
			return nil
		}
		res, err := s.Rewrap(key, newVersion)
		if err != nil {
			if errors.Is(err, ErrNotFound) {
				return nil
			}
			return fmt.Errorf("%s: %w", key, err)
		}
		if res.Rotated {
			rotated++
		} else {
			skipped++
		}
		return nil
	})
	return rotated, skipped, walkErr
}

// writeAtomic 写临时文件 → fsync → rename（临时文件也是密文，不含明文）。
func writeAtomic(p string, data []byte) error {
	tmp := p + ".tmp" + randSuffix()
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("ttscache: 创建临时文件失败: %w", err)
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(tmp)
		return fmt.Errorf("ttscache: 写入缓存失败: %w", err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(tmp)
		return fmt.Errorf("ttscache: 落盘失败: %w", err)
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, p); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("ttscache: 替换缓存失败: %w", err)
	}
	return nil
}

func randSuffix() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return fmt.Sprintf(".%x", b)
}

// HeaderSize 导出给测试断言缓存文件头长度（v2）。
const HeaderSize = headerV2
