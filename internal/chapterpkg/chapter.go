// Package chapterpkg 定义移动端单章密文包（NSCH）。
//
// 边界：本包只做「明文 → 密文」与逆操作，不接触 HTTP、不落盘、不写日志。
// 服务端在内存中用书籍 DEK 封装章节，客户端拿到的始终是密文。
package chapterpkg

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
)

// 章节密文包（NSCH）布局：
//
//	offset 0  magic "NSCH"        (4)
//	offset 4  version 1           (1)
//	offset 5  reserved 0          (1)
//	offset 6  flags uint16 LE     (2)  bit0=1 表示正文为 UTF-8
//	offset 8  chapterIndex uint32 (4)
//	offset 12 plainLen uint32     (4)
//	offset 16 nonce               (12)
//	offset 28 ciphertext+tag      (plainLen+16)
//
// AAD = "novelstack-mobile-chapter-v1|" + bookID + "|" + header[0:16]。
// 把 magic/version/flags/index/plainLen 全部纳入认证，任何头部篡改都会解密失败。
// 与书籍分块格式（NSBK）的区别：章节包按「章」独立封装，客户端可单章缓存、
// 单章校验，不需要理解整本书的分块边界。
const (
	ChapterMagic    = "NSCH"
	ChapterVersion  = 1
	ChapterHeader   = 28
	ChapterAADPref  = "novelstack-mobile-chapter-v1|"
	chapterMaxPlain = 64 << 20 // 单章上限，防御异常输入
)

// ErrChapterFormat 表示章节包格式非法。
var ErrChapterFormat = errors.New("chapterpkg: invalid chapter package")

func chapterAAD(bookID string, header []byte) []byte {
	out := make([]byte, 0, len(ChapterAADPref)+len(bookID)+1+16)
	out = append(out, ChapterAADPref...)
	out = append(out, bookID...)
	out = append(out, '|')
	return append(out, header[:16]...)
}

// Seal 用书籍 DEK 封装单章正文，返回完整 NSCH 包。
func Seal(dek []byte, bookID string, index int, plain []byte) ([]byte, error) {
	if len(dek) != 32 {
		return nil, errors.New("chapterpkg: DEK must be 32 bytes")
	}
	if len(plain) > chapterMaxPlain {
		return nil, errors.New("chapterpkg: chapter too large")
	}
	block, err := aes.NewCipher(dek)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	out := make([]byte, ChapterHeader, ChapterHeader+len(plain)+aead.Overhead())
	copy(out[0:4], ChapterMagic)
	out[4] = ChapterVersion
	// out[5] reserved = 0
	binary.LittleEndian.PutUint16(out[6:8], 1) // flags: UTF-8
	binary.LittleEndian.PutUint32(out[8:12], uint32(index))
	binary.LittleEndian.PutUint32(out[12:16], uint32(len(plain)))
	nonce := out[16:28]
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return aead.Seal(out, nonce, plain, chapterAAD(bookID, out[:16])), nil
}

// Open 解开 NSCH 包（客户端算法参考实现 + 服务端测试用）。
func Open(dek []byte, bookID string, index int, pkg []byte) ([]byte, error) {
	if len(pkg) < ChapterHeader+16 || string(pkg[0:4]) != ChapterMagic {
		return nil, ErrChapterFormat
	}
	if pkg[4] != ChapterVersion {
		return nil, fmt.Errorf("%w: version %d", ErrChapterFormat, pkg[4])
	}
	if int(binary.LittleEndian.Uint32(pkg[8:12])) != index {
		return nil, fmt.Errorf("%w: index mismatch", ErrChapterFormat)
	}
	n := int(binary.LittleEndian.Uint32(pkg[12:16]))
	if n < 0 || n > chapterMaxPlain || len(pkg) != ChapterHeader+n+16 {
		return nil, fmt.Errorf("%w: length mismatch", ErrChapterFormat)
	}
	block, err := aes.NewCipher(dek)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	plain, err := aead.Open(nil, pkg[16:28], pkg[ChapterHeader:], chapterAAD(bookID, pkg[:16]))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrChapterFormat, err)
	}
	return plain, nil
}

// Sealed 是单章密文包及其定位信息（不含正文）。
type Sealed struct {
	Index   int
	Title   string
	ETag    string
	Package []byte
}
