// Package backup 实现加密备份包：Argon2id 派生备份 KEK，分块 AES-256-GCM 加密 tar.gz。
//
// 备份口令/备份密钥与主 KEK 完全分离：本包从不读取、也从不写入主 KEK。
// KEK 的离线备份是人工步骤（打印或存入硬件密钥），绝不进入备份包。
//
// 包格式（v1）：
//
//	offset 0   magic "NSBKP"        5B
//	offset 5   version 0x01         1B
//	offset 6   kdf 0x01 (argon2id)  1B
//	offset 7   time    uint32 LE    4B
//	offset 11  memory  uint32 LE    4B (KiB)
//	offset 15  threads uint8        1B
//	offset 16  compression uint8    1B (0=无, 1=gzip)
//	offset 17  salt                 16B
//	offset 33  chunkSize uint32 LE  4B
//	offset 37  chunks: [nonce(12B) || ciphertext+GCM tag]...
//
// 每块 AAD = 包头(37B) || chunkIndex(4B LE)。包头任一字节被改动都会导致解密失败。
package backup

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"

	"golang.org/x/crypto/argon2"
)

const (
	// Magic 是备份包标识。
	Magic = "NSBKP"
	// FormatVersion 是包格式版本。
	FormatVersion = 1
	// KDFArgon2id 是唯一支持的 KDF 标识。
	KDFArgon2id = 1

	CompressionNone = 0
	CompressionGzip = 1

	saltSize   = 16
	nonceSize  = 12
	keySize    = 32
	headerSize = 5 + 1 + 1 + 4 + 4 + 1 + 1 + saltSize + 4 // 37
)

var (
	// ErrFormat 表示包结构不合法（magic/版本/长度异常）。
	ErrFormat = errors.New("backup: bad package format")
	// ErrDecrypt 表示口令错误或包被篡改（GCM 校验失败）。
	ErrDecrypt = errors.New("backup: wrong passphrase or tampered package")
)

// KDFParams 是 Argon2id 参数。参数会写入包头，恢复时无需外部配置。
type KDFParams struct {
	Time    uint32
	Memory  uint32 // KiB
	Threads uint8
}

// DefaultKDFParams 是备份命令的默认参数（64MiB / 3 轮 / 2 线程）。
var DefaultKDFParams = KDFParams{Time: 3, Memory: 64 * 1024, Threads: 2}

// Validate 检查参数是否在安全且可用的范围内。
func (p KDFParams) Validate() error {
	if p.Time < 1 || p.Time > 10 {
		return fmt.Errorf("backup: argon2 time 必须在 1..10")
	}
	if p.Memory < 8*1024 || p.Memory > 512*1024 {
		return fmt.Errorf("backup: argon2 memory 必须在 8MiB..512MiB")
	}
	if p.Threads < 1 || p.Threads > 8 {
		return fmt.Errorf("backup: argon2 threads 必须在 1..8")
	}
	return nil
}

// Header 是解密所需的全部公开参数（不含密钥）。
type Header struct {
	Version     byte
	KDF         byte
	Params      KDFParams
	Compression byte
	Salt        [saltSize]byte
	ChunkSize   uint32
}

func (h *Header) marshal() []byte {
	b := make([]byte, headerSize)
	copy(b[0:5], Magic)
	b[5] = h.Version
	b[6] = h.KDF
	binary.LittleEndian.PutUint32(b[7:11], h.Params.Time)
	binary.LittleEndian.PutUint32(b[11:15], h.Params.Memory)
	b[15] = h.Params.Threads
	b[16] = h.Compression
	copy(b[17:33], h.Salt[:])
	binary.LittleEndian.PutUint32(b[33:37], h.ChunkSize)
	return b
}

func parseHeaderBytes(raw []byte) (*Header, error) {
	if len(raw) < headerSize {
		return nil, ErrFormat
	}
	if string(raw[0:5]) != Magic {
		return nil, fmt.Errorf("%w: magic 不匹配", ErrFormat)
	}
	h := &Header{
		Version:     raw[5],
		KDF:         raw[6],
		Compression: raw[16],
		ChunkSize:   binary.LittleEndian.Uint32(raw[33:37]),
	}
	h.Params = KDFParams{
		Time:    binary.LittleEndian.Uint32(raw[7:11]),
		Memory:  binary.LittleEndian.Uint32(raw[11:15]),
		Threads: raw[15],
	}
	if h.Version != FormatVersion {
		return nil, fmt.Errorf("%w: 不支持的版本 %d", ErrFormat, h.Version)
	}
	if h.KDF != KDFArgon2id {
		return nil, fmt.Errorf("%w: 不支持的 KDF %d", ErrFormat, h.KDF)
	}
	if err := h.Params.Validate(); err != nil {
		return nil, err
	}
	if h.Compression != CompressionNone && h.Compression != CompressionGzip {
		return nil, fmt.Errorf("%w: 不支持的压缩方式 %d", ErrFormat, h.Compression)
	}
	if h.ChunkSize < 64<<10 || h.ChunkSize > 8<<20 {
		return nil, fmt.Errorf("%w: chunkSize 异常", ErrFormat)
	}
	copy(h.Salt[:], raw[17:33])
	return h, nil
}

func deriveKey(passphrase []byte, h *Header) []byte {
	return argon2.IDKey(passphrase, h.Salt[:], h.Params.Time, h.Params.Memory, h.Params.Threads, keySize)
}

func newAEAD(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func chunkAAD(header []byte, index uint32) []byte {
	aad := make([]byte, len(header)+4)
	copy(aad, header)
	binary.LittleEndian.PutUint32(aad[len(header):], index)
	return aad
}

// Writer 是流式加密写入器：数据可任意大小，按 chunkSize 分块加密。
type Writer struct {
	w      io.Writer
	aead   cipher.AEAD
	header []byte
	buf    []byte
	fill   int
	index  uint32
	closed bool
}

// NewWriter 生成随机盐、写入包头，返回加密写入器。
func NewWriter(w io.Writer, passphrase []byte, params KDFParams, compression byte, chunkSize int) (*Writer, *Header, error) {
	if len(passphrase) == 0 {
		return nil, nil, errors.New("backup: 备份口令不能为空")
	}
	if compression != CompressionNone && compression != CompressionGzip {
		return nil, nil, fmt.Errorf("backup: 不支持的压缩方式 %d", compression)
	}
	if chunkSize < 64<<10 || chunkSize > 8<<20 {
		return nil, nil, fmt.Errorf("backup: chunkSize 必须在 64KiB..8MiB")
	}
	if err := params.Validate(); err != nil {
		return nil, nil, err
	}
	h := &Header{
		Version:     FormatVersion,
		KDF:         KDFArgon2id,
		Params:      params,
		Compression: compression,
		ChunkSize:   uint32(chunkSize),
	}
	if _, err := rand.Read(h.Salt[:]); err != nil {
		return nil, nil, fmt.Errorf("backup: 生成盐失败: %w", err)
	}
	key := deriveKey(passphrase, h)
	aead, err := newAEAD(key)
	if err != nil {
		return nil, nil, err
	}
	header := h.marshal()
	if _, err := w.Write(header); err != nil {
		return nil, nil, err
	}
	return &Writer{w: w, aead: aead, header: header, buf: make([]byte, chunkSize)}, h, nil
}

func (w *Writer) Write(p []byte) (int, error) {
	if w.closed {
		return 0, errors.New("backup: writer closed")
	}
	total := len(p)
	for len(p) > 0 {
		n := copy(w.buf[w.fill:], p)
		w.fill += n
		p = p[n:]
		if w.fill == len(w.buf) {
			if err := w.flush(); err != nil {
				return total - len(p), err
			}
		}
	}
	return total, nil
}

func (w *Writer) flush() error {
	if w.fill == 0 {
		return nil
	}
	nonce := make([]byte, nonceSize)
	if _, err := rand.Read(nonce); err != nil {
		return err
	}
	ct := w.aead.Seal(nil, nonce, w.buf[:w.fill], chunkAAD(w.header, w.index))
	if _, err := w.w.Write(nonce); err != nil {
		return err
	}
	if _, err := w.w.Write(ct); err != nil {
		return err
	}
	w.index++
	w.fill = 0
	return nil
}

// Close 写出最后一块。调用方仍需关闭底层 writer/file。
func (w *Writer) Close() error {
	if w.closed {
		return nil
	}
	w.closed = true
	return w.flush()
}

// Reader 是流式解密读取器（实现 io.Reader）。
type Reader struct {
	r      io.Reader
	aead   cipher.AEAD
	header []byte
	h      *Header
	buf    []byte
	off    int
	index  uint32
	done   bool
}

// NewReader 解析包头并派生密钥；口令错误要到第一次 Read 才会暴露（GCM 校验）。
func NewReader(r io.Reader, passphrase []byte) (*Reader, *Header, error) {
	if len(passphrase) == 0 {
		return nil, nil, errors.New("backup: 备份口令不能为空")
	}
	raw := make([]byte, headerSize)
	if _, err := io.ReadFull(r, raw); err != nil {
		return nil, nil, fmt.Errorf("%w: 包头不完整", ErrFormat)
	}
	h, err := parseHeaderBytes(raw)
	if err != nil {
		return nil, nil, err
	}
	key := deriveKey(passphrase, h)
	aead, err := newAEAD(key)
	if err != nil {
		return nil, nil, err
	}
	return &Reader{r: r, aead: aead, header: raw, h: h}, h, nil
}

// Header 返回已解析的包头。
func (r *Reader) Header() *Header { return r.h }

func (r *Reader) Read(p []byte) (int, error) {
	for {
		if r.off < len(r.buf) {
			n := copy(p, r.buf[r.off:])
			r.off += n
			return n, nil
		}
		if r.done {
			return 0, io.EOF
		}
		if err := r.next(); err != nil {
			return 0, err
		}
	}
}

func (r *Reader) next() error {
	nonce := make([]byte, nonceSize)
	n, err := io.ReadFull(r.r, nonce)
	if err != nil {
		if errors.Is(err, io.EOF) && n == 0 {
			r.done = true
			return nil
		}
		return fmt.Errorf("%w: 块 nonce 不完整", ErrFormat)
	}
	// 最后一块可能短于 chunkSize+16：读到边界即停。
	lr := io.LimitReader(r.r, int64(r.h.ChunkSize)+16)
	ct, err := io.ReadAll(lr)
	if err != nil {
		return fmt.Errorf("backup: 读取密文块失败: %w", err)
	}
	if len(ct) < 16 {
		return fmt.Errorf("%w: 密文块过短", ErrFormat)
	}
	plain, err := r.aead.Open(nil, nonce, ct, chunkAAD(r.header, r.index))
	if err != nil {
		return ErrDecrypt
	}
	r.index++
	r.buf = plain
	r.off = 0
	return nil
}
