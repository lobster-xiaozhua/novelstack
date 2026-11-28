// Package chapterscan 以流式方式从明文流中扫描章节标题。
//
// 为什么需要流式扫描：上传时明文只经过内存管道（边读边加密，不落盘），
// 若为建立目录而缓存整本书的明文，等于把正文在内存里多存一份。本包只保留
// 「标题字符串 + 字数 + rune 偏移」，正文一个字节都不留。
//
// 语义必须与整文本切分完全一致（见 Split），否则 /chapters 列表（来自扫描结果）
// 与 /plain（按整文本切分）的 index 会错位。chapterscan_test.go 用随机文本
// 做一致性回归。
//
// 真实网文有两种常见排版，都必须支持：
//
//	A. 标题独占一行（经典格式）：「第一章 夜航船\n正文…」
//	B. 标题与正文粘连在同一行（更常见，尤其从网页/APP 导出的 TXT）：
//	   「第1章 叶无忧淑女服光滑丰腴的曲面…」（整章一行，几千字）
//
// 旧实现只支持 A（正则要求行尾），导致 B 类文本整本正文被塞进"前言"，
// 而书前的目录表短行被切成一堆只有标题的碎片章。本实现用「行首匹配 +
// 短章合并 + 目录标题回填」同时覆盖两种排版。
package chapterscan

import (
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

// HeadingPattern 只检查「行首」是否出现章节标记，不要求整行都是标题——
// 这正是兼容"标题与正文粘连"的关键。锚点是 ^ 而不是 $。
//
// 兼容格式（均由真实语料归纳）：
//
//	第1章 标题 / 第一章 标题 / 第 1 章 / 第1章标题 / 正文 第1章 标题 /
//	第1节 / 第12回 / 第三卷 / 第5部 / 序章 / 楔子 / 引子 / 前言 / 序言 /
//	尾声 / 终章 / 后记 / 番外 / 正文
//
// 允许标记前后出现空格、全角空格与常见装饰符号（·、—、-、:、：、[]、【】）。
var HeadingPattern = regexp.MustCompile(
	`(?m)^[ \t　]*[·•\-—\[\]【】]*[ \t　]*` + // 前置装饰与空白（(?m) 让 ^ 匹配每行行首）
		`(?:正文[ \t　]*)?` + // 「正文 第1章」的前缀
		`(?:` +
		`第[ \t　]*[0-9零〇一二三四五六七八九十百千万两]{1,12}[ \t　]*[章节回卷篇集部][^\n]*` + // 第N章 …
		`|` +
		`(?:序章|楔子|引子|前言|序言|尾声|终章|后记|番外)[^\n]*` + // 独立书名式标题
		`)`,
)

// headingNumRe 从标题中提取章号（用于目录标题回填时的键）。
var headingNumRe = regexp.MustCompile(`第[ \t　]*([0-9零〇一二三四五六七八九十百千万两]{1,12})[ \t　]*[章节回卷篇集部]`)

// minChapterRunes 是"碎片章"阈值：正文少于这个字数的章，视为目录表残留或
// 误识别的标题行，合并到上一章。用户反馈的碎片章只有 8~12 字，100 是安全余量。
const minChapterRunes = 100

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

// IsHeading 判断一行（已去掉行尾换行）的行首是否为章节标题标记。
// 注意：不做 TrimSpace，保持与整文本切分（HeadingPattern 直接作用于原文）完全一致；
// 行首的空白由正则自己吸收。CRLF 文本的 \r 落在行尾，不影响行首匹配。
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
	// 去掉行尾 CR（CRLF 文本）
	t = strings.TrimRight(t, "\r")
	if utf8.RuneCountInString(t) > maxTitleRunes {
		r := []rune(t)
		t = string(r[:maxTitleRunes])
	}
	return t
}

// cleanShortTitle 清理目录表里的短标题（用于回填正文章标题）。
func cleanShortTitle(s string) string {
	t := strings.TrimSpace(strings.TrimRight(s, "\r"))
	if utf8.RuneCountInString(t) > maxTitleRunes {
		r := []rune(t)
		t = string(r[:maxTitleRunes])
	}
	return t
}

// headingKey 返回标题的章号键（归一化后用于目录回填匹配）。
// 中文数字与阿拉伯数字统一成整数字符串；无章号（如"前言"）返回空串。
func headingKey(title string) string {
	m := headingNumRe.FindStringSubmatch(title)
	if len(m) < 2 {
		return ""
	}
	if n, err := strconv.Atoi(m[1]); err == nil {
		return strconv.Itoa(n)
	}
	return cnNumToArabic(m[1])
}

// cnNumToArabic 把常见的中文数字（最多到万位）转成十进制字符串。
// 只用于目录回填的键匹配，不追求处理"廿""卅"等古写。
func cnNumToArabic(s string) string {
	digits := map[rune]int{'零': 0, '〇': 0, '一': 1, '二': 2, '两': 2, '三': 3, '四': 4, '五': 5, '六': 6, '七': 7, '八': 8, '九': 9}
	units := map[rune]int{'十': 10, '百': 100, '千': 1000, '万': 10000}
	total, section, number := 0, 0, 0
	for _, r := range s {
		if d, ok := digits[r]; ok {
			number = d
			continue
		}
		if u, ok := units[r]; ok {
			if u == 10000 {
				section = (section + number) * u
				total += section
				section, number = 0, 0
			} else {
				if number == 0 {
					number = 1
				}
				section += number * u
				number = 0
			}
			continue
		}
		return "" // 含无法识别的字符，放弃
	}
	return strconv.Itoa(total + section + number)
}

// finalizeChapters 做三件后处理，Split 与 Scanner 共用，确保语义一致：
//  1. 识别书前目录表：连续短章，且其章号在后续长章里重复出现；
//  2. 把目录章合并掉（并入相邻正文章），并用其标题回填正文章标题；
//  3. 孤立的短章（如只有几行的短章节、末章）保留，不误伤。
//
// 判定目录表的关键是「章号重复」：书前目录行是「第1章 标题」（短章），
// 正文里又有一章「第1章 标题…正文…」（长章），同一章号出现两次。
// 而经典的短章节格式里，每个章号只出现一次，不能合并。
func finalizeChapters(chapters []Chapter) []Chapter {
	if len(chapters) == 0 {
		return nil
	}
	n := len(chapters)

	// 1. 收集长章（>=minChapterRunes 字）出现的章号集合。
	longKeys := map[string]bool{}
	for _, c := range chapters {
		if c.Chars >= minChapterRunes {
			if k := headingKey(c.Title); k != "" {
				longKeys[k] = true
			}
		}
	}

	// 2. 标记目录章：短章，且章号在长章里重复出现。
	toc := make([]bool, n)
	for i, c := range chapters {
		if c.Chars < minChapterRunes {
			if k := headingKey(c.Title); k != "" && longKeys[k] {
				toc[i] = true
			}
		}
	}

	// 3. 收集目录标题（章号 → 干净标题），用于回填正文长章。
	dir := map[string]string{}
	for i, c := range chapters {
		if toc[i] {
			if k := headingKey(c.Title); k != "" {
				if _, exists := dir[k]; !exists {
					dir[k] = cleanShortTitle(c.Title)
				}
			}
		}
	}

	// 4. 合并目录章到相邻正文章 + 回填标题。
	out := make([]Chapter, 0, n)
	pendingStart := -1 // 段首目录章无法并入上一章时，暂存起点给下一章
	for i, c := range chapters {
		if toc[i] {
			if len(out) > 0 {
				prev := &out[len(out)-1]
				prev.End = c.End
				prev.Chars = prev.End - prev.Start
			} else if pendingStart < 0 {
				pendingStart = c.Start
			}
			continue
		}
		if pendingStart >= 0 {
			c.Start = pendingStart
			c.Chars = c.End - c.Start
			pendingStart = -1
		}
		if k := headingKey(c.Title); k != "" {
			if t, ok := dir[k]; ok {
				c.Title = t
			}
		}
		c.Title = cleanShortTitle(c.Title)
		out = append(out, c)
	}
	// 极端情况：全文只有目录章——保留一段，避免空目录。
	if len(out) == 0 && len(chapters) > 0 {
		last := chapters[len(chapters)-1]
		out = append(out, Chapter{Title: "全文", Chars: last.End - chapters[0].Start,
			Start: chapters[0].Start, End: last.End})
	}
	for i := range out {
		out[i].Index = i
	}
	return out
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
	return finalizeChapters(out)
}

// maxLineBytes 限制单行缓存；超过则只放弃「整行标题」的判定，但行首缓存
// 仍可用于识别"标题与正文粘连"的长行（真实网文整章一行可达数千字节）。
const maxLineBytes = 512

// Scanner 是 io.Writer：写入明文后，Close 返回与 Split 一致的章节列表。
// 只保留标题与偏移，不保留任何正文。
type Scanner struct {
	maxTitles int
	line      []byte

	runes     int  // 已消费的 rune 数（含换行）
	lineStart int  // 当前行首的 rune 偏移
	nonBlank  bool // 是否出现过非空、非标题行（用于判断"前言"）

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
// rune 计数按 UTF-8 起始字节递增；超长行仍保留前 512 字节用于行首标题识别。
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
		}
		if b < 0x80 || b&0xC0 != 0x80 {
			s.runes++
		}
	}
	return len(p), nil
}

func (s *Scanner) flushLine() {
	raw := string(s.line)
	s.line = s.line[:0]

	text := strings.TrimSpace(raw)
	if text == "" {
		return
	}
	// 关键修复：即使整行超过 maxLineBytes，也检查行首缓存——
	// "标题与正文粘连"的长行正是这种情况。IsHeading 只看行首，前 512 字节足够。
	if !IsHeading(raw) {
		s.nonBlank = true
		return
	}
	// 命中标题行：结束上一章（或补一个"前言"）
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
	return finalizeChapters(s.out)
}

// Dropped 返回因上限被丢弃的章节数（用于审计/日志，不含正文）。
func (s *Scanner) Dropped() int { return s.dropped }
