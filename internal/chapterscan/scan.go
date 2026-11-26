// Package chapterscan 以流式方式从明文流中扫描章节标题。
//
// 为什么需要流式扫描：上传时明文只经过内存管道（边读边加密，不落盘），
// 若为建立目录而缓存整本书的明文，等于把正文在内存里多存一份。本包只保留
// 「标题字符串 + 字数 + rune 偏移」，正文一个字节都不留。
//
// 语义必须与整文本切分完全一致（见 Split），否则 /chapters 列表（来自扫描结果）
// 与 /plain（按整文本切分）的 index 会错位。chapterscan_test.go 用随机文本
// 做一致性回归。
package chapterscan

import (
	"regexp"
	"strings"
	"unicode/utf8"
)

// HeadingPattern 匹配常见中文章节标题行，例如「第一章」「第十二节」「序章」「后记」。
// 只匹配独立成行的短标题，避免误伤正文中的“第一章”叙述。
var HeadingPattern = regexp.MustCompile(`(?m)^[ \t　]*((?:第[0-9零一二三四五六七八九十百千万两]{1,10}[章节回卷篇集][^\n]{0,60})|(?:序章|楔子|引子|前言|序言|尾声|终章|后记|番外))[ \t　]*$`)

// maxTitleRunes 是标题保留的最大字符数（超出截断，只用于展示与索引）。
const maxTitleRunes = 60

// Chapter 是一章的定位信息（Start/End 为 rune 偏移，左闭右开）。
type Chapter struct {
	Index int
	Title string
	Chars int
	Start int
	End   int
}

// IsHeading 判断一行（已去掉行尾换行）是否为章节标题。
// 注意：不做 TrimSpace，保持与整文本切分（HeadingPattern 直接作用于原文）完全一致，
// 否则 CRLF 文本会出现「扫描得到标题、整文本却切不出」的偏差。
func IsHeading(line string) bool {
	if line == "" {
		return false
	}
	return HeadingPattern.MatchString(line)
}

// clipTitle 取标题首行并截断到 maxTitleRunes。
func clipTitle(s string) string {
	t := strings.TrimSpace(s)
	if i := strings.IndexByte(t, '\n'); i >= 0 {
		t = strings.TrimSpace(t[:i])
	}
	if utf8.RuneCountInString(t) > maxTitleRunes {
		r := []rune(t)
		t = string(r[:maxTitleRunes])
	}
	return t
}

// Split 对完整文本切分章节；没有标题行时整本作为单章。
// 所有字符（含标题行与空白）都会落入某一章，拼回即为原文。
func Split(text string) []Chapter {
	runes := []rune(text)
	if len(runes) == 0 {
		return nil
	}
	matches := HeadingPattern.FindAllStringIndex(text, -1)
	if len(matches) == 0 {
		return []Chapter{{Index: 0, Title: "全文", Chars: len(runes), Start: 0, End: len(runes)}}
	}
	runeAt := func(byteIdx int) int { return utf8.RuneCountInString(text[:byteIdx]) }

	var out []Chapter
	firstStart := runeAt(matches[0][0])
	if strings.TrimSpace(string(runes[:firstStart])) != "" {
		out = append(out, Chapter{Index: 0, Title: "前言", Chars: firstStart, Start: 0, End: firstStart})
	}
	for i, m := range matches {
		start := runeAt(m[0])
		end := len(runes)
		if i+1 < len(matches) {
			end = runeAt(matches[i+1][0])
		}
		out = append(out, Chapter{
			Index: len(out),
			Title: clipTitle(string(runes[start:end])),
			Chars: end - start,
			Start: start,
			End:   end,
		})
	}
	return out
}

// maxLineBytes 限制单行缓存；超过则视为超长正文行（不可能是标题）。
const maxLineBytes = 512

// Scanner 是 io.Writer：写入明文后，Close 返回与 Split 一致的章节列表。
// 只保留标题与偏移，不保留任何正文。
type Scanner struct {
	maxTitles int
	line      []byte
	lineOver  bool

	runes     int  // 已消费的 rune 数（含换行）
	lineStart int  // 当前行首的 rune 偏移
	nonBlank  bool // 是否出现过非空、非标题行（用于判断“前言”）

	pending *Chapter // 尚未确定结束位置的当前章
	out     []Chapter
	dropped int // 因 maxTitles 上限被丢弃的章节数（只影响目录，不影响正文）
}

// New 构造扫描器；maxTitles<=0 时使用默认上限。
func New(maxTitles int) *Scanner {
	if maxTitles <= 0 {
		maxTitles = 20000
	}
	return &Scanner{maxTitles: maxTitles, line: make([]byte, 0, 256)}
}

// Write 实现 io.Writer。返回值始终为 len(p)：扫描失败不应中断加密上传。
// rune 计数按 UTF-8 起始字节递增，超长行只放弃标题识别，不放弃偏移计算。
func (s *Scanner) Write(p []byte) (int, error) {
	for _, b := range p {
		if b == '\n' {
			s.flushLine()
			s.runes++ // 换行本身占一个字符，归属上一章
			s.lineStart = s.runes
			continue
		}
		if len(s.line) < maxLineBytes {
			s.line = append(s.line, b)
		} else {
			s.lineOver = true
		}
		if b < 0x80 || b&0xC0 != 0x80 {
			s.runes++
		}
	}
	return len(p), nil
}

func (s *Scanner) flushLine() {
	raw := string(s.line)
	over := s.lineOver
	s.line = s.line[:0]
	s.lineOver = false

	text := strings.TrimSpace(raw)
	if text == "" {
		return
	}
	if over || !IsHeading(raw) {
		s.nonBlank = true
		return
	}
	// 命中标题行：结束上一章（或补一个“前言”）
	if s.pending != nil {
		s.emit(s.pending.Start, s.lineStart, s.pending.Title)
	} else if s.nonBlank && s.lineStart > 0 {
		s.emit(0, s.lineStart, "前言")
	}
	s.pending = &Chapter{Title: clipTitle(text), Start: s.lineStart}
}

func (s *Scanner) emit(start, end int, title string) {
	if len(s.out) >= s.maxTitles {
		s.dropped++
		return
	}
	s.out = append(s.out, Chapter{Index: len(s.out), Title: title, Chars: end - start, Start: start, End: end})
}

// Close 结束扫描并返回章节列表。
func (s *Scanner) Close() []Chapter {
	if len(s.line) > 0 {
		// 末尾没有换行的最后一行
		s.flushLine()
	}
	if s.pending != nil {
		s.emit(s.pending.Start, s.runes, s.pending.Title)
		s.pending = nil
	}
	if len(s.out) == 0 {
		if s.runes == 0 {
			return nil
		}
		return []Chapter{{Index: 0, Title: "全文", Chars: s.runes, Start: 0, End: s.runes}}
	}
	return s.out
}

// Dropped 返回因上限被丢弃的章节数（用于审计/日志，不含正文）。
func (s *Scanner) Dropped() int { return s.dropped }
