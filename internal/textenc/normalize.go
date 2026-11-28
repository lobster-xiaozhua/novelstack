// Package textenc 把上传的 TXT 明文统一成 UTF-8，供章节扫描与加密使用。
//
// 为什么要在流式管道里做：GBK/GB18030 文本的字节序列里根本没有 UTF-8 的
// 「第」「章」，章节正则会全部失效；而明文不落盘、不缓存整本，是本项目
// 的硬约束。因此实现成一个 io.Reader：先 peek 开头少量字节判断编码，
// 再把整个流按需转码，内存占用与单块大小同阶，不随文件大小增长。
package textenc

import (
	"bufio"
	"bytes"
	"io"
	"unicode/utf8"

	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/encoding/unicode"
	"golang.org/x/text/transform"
)

// peekBytes 是判断编码所需的前缀长度。BOM 只需 3 字节；无 BOM 时取 4KB
// 做 UTF-8 合法性统计——足够可靠，且不会因超大文件而占用内存。
const peekBytes = 4096

// Normalize 返回一个读作 UTF-8 的 Reader：
//   - UTF-8 BOM：去掉 BOM；
//   - UTF-16 BOM：转 UTF-8；
//   - 合法 UTF-8：原样透传（最常见路径，零拷贝开销）；
//   - 非 UTF-8（按 GBK/GB18030 处理）：流式转码。
func Normalize(r io.Reader) io.Reader {
	br := bufio.NewReaderSize(r, peekBytes+32)
	head, _ := br.Peek(peekBytes) // Peek 不消费，错误（EOF/短读）可忽略

	switch {
	case bytes.HasPrefix(head, []byte{0xEF, 0xBB, 0xBF}):
		_, _ = br.Discard(3) // 去掉 UTF-8 BOM
		return br
	case bytes.HasPrefix(head, []byte{0xFF, 0xFE}):
		_, _ = br.Discard(2)
		return transform.NewReader(br, unicodeUTF16LE())
	case bytes.HasPrefix(head, []byte{0xFE, 0xFF}):
		_, _ = br.Discard(2)
		return transform.NewReader(br, unicodeUTF16BE())
	case looksUTF8(head):
		return br
	default:
		// GBK / GB18030：simplifiedchinese.GB18030 向下兼容 GBK/GB2312。
		return transform.NewReader(br, simplifiedchinese.GB18030.NewDecoder())
	}
}

// looksUTF8 判断前缀是否"看起来是合法 UTF-8"。
//
// 注意：peek 的 4096 字节可能恰好截断一个多字节字符（如 3 字节的汉字），
// 直接 utf8.Valid 会把合法 UTF-8 误判成 GBK，导致整本书被错误转码。
// 因此先向前回退，找到最后一个完整 UTF-8 边界再验证；真正的 GBK 字节
// 序列在开头就会出现非法前导字节，回退 3 字节不足以"洗白"。
func looksUTF8(b []byte) bool {
	end := len(b)
	for i := 0; i < 3 && end > 0; i++ {
		if utf8.Valid(b[:end]) {
			return true
		}
		end--
	}
	return utf8.Valid(b[:end])
}

// unicodeUTF16LE/BE 返回 UTF-16 → UTF-8 的 transformer。
func unicodeUTF16LE() transform.Transformer {
	return unicode.UTF16(unicode.LittleEndian, unicode.IgnoreBOM).NewDecoder()
}
func unicodeUTF16BE() transform.Transformer {
	return unicode.UTF16(unicode.BigEndian, unicode.IgnoreBOM).NewDecoder()
}
