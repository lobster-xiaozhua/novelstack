// Package cryptobox 实现信封加密：
//   - KEK：32 字节，来自只读 secret 文件（原始 32 字节或 base64），仅驻留内存；
//   - DEK：每本书随机 32 字节，仅内存短暂存在，用 KEK 以 AES-256-GCM 包装后入库；
//   - 书籍内容：DEK + AES-256-GCM 分块加密（默认 1MiB/块）。
//
// 密文文件格式（自定义，版本化）：
//
//	offset=0  magic     "NSBK"        (4 字节)
//	offset=4  version   0x01          (1 字节)
//	offset=5  chunkSize uint32 LE     (4 字节，明文分块大小)
//	offset=9  chunks: [nonce(12B) || ciphertext+GCM tag(plaintext+16B)] ...
//
// 每块 AAD：AADPrefix(version) + bookID + chunkIndex（固定长度前缀避免拼接歧义）。
package cryptobox

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
)

// FormatMagic / FormatVersion 是密文文件格式的标识，参与 AAD。
var FormatMagic = [4]byte{'N', 'S', 'B', 'K'}

const (
	FormatVersion   = 1
	NonceSize       = 12
	KeySize         = 32
	HeaderSize      = 9 // 4 magic + 1 version + 4 chunkSize
	aadPrefixLength = 8 // 2 magic + 2 version(LE) + 4 chunkIndex(LE)
	// ErrTampered 表示 GCM tag 校验失败或 AAD 不匹配（内容被篡改）。
	ErrTampered = cryptoboxError("content tampered or corrupted")
	// ErrBadFormat 表示文件头/长度等结构性错误。
	ErrBadFormat = cryptoboxError("bad ciphertext format")
)

type cryptoboxError string

func (e cryptoboxError) Error() string { return string(e) }

// aadPrefix 是固定 8 字节 AAD 前缀：magic(4) + formatVersion(2, LE)。
// 加上 chunkIndex(4, LE) 与 bookID 后构成完整 AAD。
func aadPrefix() []byte {
	p := make([]byte, 8)
	copy(p[0:4], FormatMagic[:])
	binary.LittleEndian.PutUint16(p[4:6], FormatVersion)
	return p
}

// buildAAD 构造第 index 块的 AAD：magic|version|chunkIndex|bookID。
func buildAAD(index uint32, bookID string) []byte {
	p := aadPrefix()
	binary.LittleEndian.PutUint32(p[4:8], index)
	aad := make([]byte, 0, len(p)+len(bookID))
	aad = append(aad, p...)
	aad = append(aad, bookID...)
	return aad
}

// LoadKEK 从只读 secret 文件加载 32 字节 KEK。
// 支持原始 32 字节或 base64（标准或 URL 编码，可带换行）。
// 任何失败都返回错误；错误信息不包含密钥内容。
func LoadKEK(path string) ([]byte, error) {
	raw, err := os.ReadFile(path) // #nosec G304 -- 路径来自受控配置，非用户输入
	if err != nil {
		return nil, fmt.Errorf("读取 KEK secret 失败: %w", err)
	}
	return parseKEK(raw)
}

func parseKEK(raw []byte) ([]byte, error) {
	if len(raw) == KeySize {
		return append([]byte(nil), raw...), nil
	}
	// 去除尾部换行（shell 重定向生成 KEK 时常见）。
	trimmed := bytes.TrimRight(raw, "\r\n")
	if len(trimmed) == KeySize {
		return append([]byte(nil), trimmed...), nil
	}
	// base64（标准或 URL 编码，允许任意空白）。
	compact := make([]byte, 0, len(raw))
	for _, b := range raw {
		switch b {
		case '\n', '\r', ' ', '\t':
			continue
		}
		compact = append(compact, b)
	}
	encodings := []*base64.Encoding{
		base64.StdEncoding,
		base64.RawStdEncoding,
		base64.URLEncoding,
		base64.RawURLEncoding,
	}
	for _, enc := range encodings {
		if key, err := enc.DecodeString(string(compact)); err == nil && len(key) == KeySize {
			return append([]byte(nil), key...), nil
		}
	}
	return nil, fmt.Errorf("KEK 文件格式无效：需要 32 字节原始数据或其 base64 编码（实际读取 %d 字节）", len(raw))
}

// NewDEK 生成 32 字节随机 DEK（仅内存使用）。
func NewDEK() ([]byte, error) {
	k := make([]byte, KeySize)
	if _, err := rand.Read(k); err != nil {
		return nil, fmt.Errorf("生成 DEK 失败: %w", err)
	}
	return k, nil
}

// NewBookID 生成 128 位随机 ID（hex，32 字符），同时作为密文文件名。
func NewBookID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("生成 bookID 失败: %w", err)
	}
	return fmt.Sprintf("%x", b), nil
}

// gcmOpen 返回以 KEK/DEK 初始化的 AES-256-GCM 实例。
func gcmOpen(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("初始化 AES 失败: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("初始化 GCM 失败: %w", err)
	}
	return aead, nil
}

// WrapDEK 用 KEK 包装 DEK，返回 (wrappedDEK, nonce)。密钥本身不出现在返回值之外的地方。
func WrapDEK(kek, dek []byte) (wrapped, nonce []byte, err error) {
	aead, err := gcmOpen(kek)
	if err != nil {
		return nil, nil, err
	}
	nonce = make([]byte, NonceSize)
	if _, err := rand.Read(nonce); err != nil {
		return nil, nil, fmt.Errorf("生成包装 nonce 失败: %w", err)
	}
	// AAD 绑定用途与 KEK 版本，防止 wrapped_dek 被挪用。
	wrapped = aead.Seal(nil, nonce, dek, []byte("novelstack-dek-wrap-v1"))
	return wrapped, nonce, nil
}

// UnwrapDEK 还原 DEK。校验失败返回 ErrTampered。
func UnwrapDEK(kek, wrapped, nonce []byte) ([]byte, error) {
	if len(nonce) != NonceSize {
		return nil, ErrBadFormat
	}
	aead, err := gcmOpen(kek)
	if err != nil {
		return nil, err
	}
	dek, err := aead.Open(nil, nonce, wrapped, []byte("novelstack-dek-wrap-v1"))
	if err != nil {
		return nil, ErrTampered
	}
	return dek, nil
}

// EncryptStream 从 r 读取明文，边读边分块加密写入 w（密文文件格式见包注释）。
// 返回明文 SHA-256。r 与 w 之外不落任何盘。
func EncryptStream(r io.Reader, w io.Writer, dek []byte, bookID string, chunkSize int) ([]byte, error) {
	aead, err := gcmOpen(dek)
	if err != nil {
		return nil, err
	}
	header := make([]byte, 0, HeaderSize)
	header = append(header, FormatMagic[:]...)
	header = append(header, FormatVersion)
	var cs [4]byte
	binary.LittleEndian.PutUint32(cs[:], uint32(chunkSize))
	header = append(header, cs[:]...)
	if _, err := w.Write(header); err != nil {
		return nil, fmt.Errorf("写入密文头失败: %w", err)
	}

	hash := sha256.New()
	buf := make([]byte, chunkSize)
	nonce := make([]byte, NonceSize)
	var chunkIndex uint32
	tee := io.TeeReader(r, hash)
	for {
		n, rerr := io.ReadFull(tee, buf)
		if n > 0 {
			if _, err := rand.Read(nonce); err != nil {
				return nil, fmt.Errorf("生成块 nonce 失败: %w", err)
			}
			ct := aead.Seal(nil, nonce, buf[:n], buildAAD(chunkIndex, bookID))
			if _, err := w.Write(nonce); err != nil {
				return nil, fmt.Errorf("写入块 nonce 失败: %w", err)
			}
			if _, err := w.Write(ct); err != nil {
				return nil, fmt.Errorf("写入密文块失败: %w", err)
			}
			chunkIndex++
		}
		if rerr == nil {
			continue
		}
		if errors.Is(rerr, io.EOF) || errors.Is(rerr, io.ErrUnexpectedEOF) {
			if n == 0 && chunkIndex == 0 {
				return nil, ErrBadFormat // 空文件拒绝
			}
			return hash.Sum(nil), nil
		}
		return nil, fmt.Errorf("读取明文失败: %w", rerr)
	}
}

// DecryptStream 读取密文文件并流式解密，把明文写入 w。
// 每块独立校验 GCM tag 与 AAD；任何篡改立即返回 ErrTampered。
// 返回解密后明文的 SHA-256 与总字节数。
func DecryptStream(r io.Reader, w io.Writer, dek []byte, bookID string, expectChunkSize int, maxSize int64) (sum []byte, size int64, err error) {
	aead, err := gcmOpen(dek)
	if err != nil {
		return nil, 0, err
	}
	header := make([]byte, HeaderSize)
	if _, err := io.ReadFull(r, header); err != nil {
		return nil, 0, fmt.Errorf("%w: 头部不完整", ErrBadFormat)
	}
	if string(header[0:4]) != string(FormatMagic[:]) {
		return nil, 0, fmt.Errorf("%w: magic 不匹配", ErrBadFormat)
	}
	if header[4] != FormatVersion {
		return nil, 0, fmt.Errorf("%w: 不支持的格式版本 %d", ErrBadFormat, header[4])
	}
	chunkSize := int(binary.LittleEndian.Uint32(header[5:9]))
	if chunkSize < 64<<10 || chunkSize > 16<<20 || chunkSize != expectChunkSize {
		return nil, 0, fmt.Errorf("%w: chunkSize 异常", ErrBadFormat)
	}

	hash := sha256.New()
	ctr := &countingWriter{w: io.MultiWriter(w, hash)}
	var chunkIndex uint32
	for {
		nonce := make([]byte, NonceSize)
		if _, err := io.ReadFull(r, nonce); err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				return hash.Sum(nil), ctr.n, nil // 正常结束
			}
			return nil, 0, fmt.Errorf("%w: 读取 nonce 失败", ErrBadFormat)
		}
		// 该块密文长度未知（最后一块更短）：恰好读满 chunkSize+16 字节，
		// 剩余数据留给下一轮；末块自然读到边界。
		lr := io.LimitReader(r, int64(chunkSize)+16)
		ct, err := io.ReadAll(lr)
		if err != nil {
			return nil, 0, fmt.Errorf("读取密文块失败: %w", err)
		}
		if len(ct) == 0 || int64(len(ct)) > int64(chunkSize)+16 {
			return nil, 0, fmt.Errorf("%w: 密文块长度异常", ErrBadFormat)
		}
		plain, err := aead.Open(nil, nonce, ct, buildAAD(chunkIndex, bookID))
		if err != nil {
			return nil, 0, ErrTampered
		}
		if err := ctr.writeAll(plain); err != nil {
			return nil, 0, err
		}
		if ctr.n > maxSize {
			return nil, 0, fmt.Errorf("解密内容超过大小上限")
		}
		chunkIndex++
	}
}

// countingWriter 计数写入字节总数。
type countingWriter struct {
	w io.Writer
	n int64
}

func (c *countingWriter) writeAll(p []byte) error {
	n, err := c.w.Write(p)
	c.n += int64(n)
	return err
}
