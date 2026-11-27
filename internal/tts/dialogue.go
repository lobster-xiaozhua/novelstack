package tts

import (
	"regexp"
	"strings"
)

// Span 是一段「同一角色」的文本。
type Span struct {
	Text string
	// Dialogue 为 true 表示这段是引号内的对话，应使用对话音色。
	Dialogue bool
}

// SplitDialogue 用 pattern 把章节文本切成「旁白 / 对话」交替的片段。
//
// 约定：pattern 的第一个捕获组是对话正文（引号本身不朗读）。
// 没有捕获组时，整个匹配作为对话。未匹配的部分一律视为旁白。
//
// 返回的片段按原文顺序拼接后与输入等价（引号本身被丢弃，其余字符保留）。
func SplitDialogue(text string, pattern *regexp.Regexp) []Span {
	if text == "" {
		return nil
	}
	if pattern == nil {
		return []Span{{Text: text}}
	}
	matches := pattern.FindAllStringSubmatchIndex(text, -1)
	if len(matches) == 0 {
		return []Span{{Text: text}}
	}
	out := make([]Span, 0, len(matches)*2+1)
	cursor := 0
	appendSpan := func(s string, dialogue bool) {
		if s == "" {
			return
		}
		if n := len(out); n > 0 && out[n-1].Dialogue == dialogue {
			out[n-1].Text += s
			return
		}
		out = append(out, Span{Text: s, Dialogue: dialogue})
	}
	for _, m := range matches {
		if m[0] < cursor {
			continue // 防御：正则出现重叠匹配时只取不重叠部分
		}
		appendSpan(text[cursor:m[0]], false)
		// 捕获组 1 存在则只朗读组内内容，否则朗读整个匹配。
		gs, ge := m[0], m[1]
		if len(m) >= 4 && m[2] >= 0 {
			gs, ge = m[2], m[3]
		}
		appendSpan(text[gs:ge], true)
		cursor = m[1]
	}
	if cursor < len(text) {
		appendSpan(text[cursor:], false)
	}
	return out
}

// isSilentSpan 判断整段是否只有空白与标点（无需分配音色）。
func isSilentSpan(s string) bool {
	return strings.TrimSpace(s) == ""
}
