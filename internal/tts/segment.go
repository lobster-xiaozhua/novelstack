package tts

import (
	"strings"

	"novelstack/internal/chapterscan"
)

// Chapter 是章节切分结果。Start/End 是 rune 偏移（左闭右开）。
type Chapter struct {
	Index int    `json:"index"`
	Title string `json:"title"`
	Chars int    `json:"chars"`
	Start int    `json:"-"`
	End   int    `json:"-"`
}

// SplitChapters 按章节标题行切分整本书；没有标题行时整本作为单章。
// 实现委托给 chapterscan，保证「流式扫描建立的目录」与「整文本切分」完全一致。
func SplitChapters(text string) []Chapter {
	src := chapterscan.Split(text)
	out := make([]Chapter, 0, len(src))
	for _, c := range src {
		out = append(out, Chapter{Index: c.Index, Title: c.Title, Chars: c.Chars, Start: c.Start, End: c.End})
	}
	return out
}

// sentenceEnd 句末标点（可在此切分，且不破坏句子完整性）。
func sentenceEnd(r rune) bool {
	switch r {
	case '。', '！', '？', '；', '…', '!', '?', ';', '\n':
		return true
	}
	return false
}

// clauseEnd 分句标点（超长句的次级切分点）。
func clauseEnd(r rune) bool {
	switch r {
	case '，', '、', '：', '—', ',', ':':
		return true
	}
	return false
}

// closingPunct 句末标点后常见的收尾符号，应并入前一段，避免段落以引号开头。
func closingPunct(r rune) bool {
	switch r {
	case '”', '’', '"', '\'', '」', '』', '）', ')', '】', '》', '…':
		return true
	}
	return false
}

// Segment 按中文句子边界把章节文本切成不超过 maxRunes 个字符的片段。
// 保证：片段有序、拼回等于原文（不丢字符）、尽量在标点后断开、
// 单句超长时退化为分句/硬切，不截断在句中标点“中间”。
func Segment(text string, maxRunes int) []string {
	if maxRunes <= 0 {
		maxRunes = 100
	}
	runes := []rune(text)
	if len(runes) == 0 {
		return nil
	}
	var out []string
	start := 0
	for start < len(runes) {
		if len(runes)-start <= maxRunes {
			out = append(out, string(runes[start:]))
			break
		}
		end := start + maxRunes
		cut := -1
		// 1) 优先句末标点（从后往前找，保证长度不超限）
		for i := end - 1; i > start; i-- {
			if sentenceEnd(runes[i]) {
				cut = i + 1
				break
			}
		}
		// 2) 退化到分句标点
		if cut == -1 {
			for i := end - 1; i > start; i-- {
				if clauseEnd(runes[i]) {
					cut = i + 1
					break
				}
			}
		}
		// 3) 实在没有标点：硬切（保证不丢字符）
		if cut == -1 || cut <= start {
			cut = end
		}
		// 把紧跟的右引号/右括号并入本段
		for cut < len(runes) && closingPunct(runes[cut]) {
			cut++
		}
		out = append(out, string(runes[start:cut]))
		start = cut
	}
	return out
}

// IsSilent 判断片段是否只有空白（无需送入 TTS）。
func IsSilent(s string) bool {
	return strings.TrimSpace(s) == ""
}
