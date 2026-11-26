package tts

import (
	"strings"
	"testing"
)

// 1. 切分后片段有序、不丢字符（拼回等于原文）、不超长。
func TestSegmentPreservesAllCharacters(t *testing.T) {
	texts := []string{
		"第一句。第二句！第三句？第四句；第五句…结束。",
		"“引号里的句子。”他说，“然后呢？”她笑了笑。\n\n换行后的段落，继续讲述一个很长很长的故事，没有标点也要能切开的超长句子" + strings.Repeat("很长", 300) + "。",
		strings.Repeat("春江潮水连海平，海上明月共潮生。", 120),
		"  空白开头\n第二段。  ",
	}
	for _, text := range texts {
		for _, max := range []int{20, 50, 100, 120} {
			segs := Segment(text, max)
			if len(segs) == 0 {
				t.Fatalf("no segments for max=%d", max)
			}
			if got := strings.Join(segs, ""); got != text {
				t.Fatalf("字符丢失：max=%d\n原文长度=%d 拼回长度=%d", max, len([]rune(text)), len([]rune(got)))
			}
			for i, s := range segs {
				if n := len([]rune(s)); n > max+1 { // +1 允许收尾标点并入
					t.Fatalf("片段 %d 超长: %d > %d", i, n, max)
				}
			}
		}
	}
}

// 片段必须在句子/分句边界断开，不能切在句子中间（除非超长句硬切）。
func TestSegmentStopsAtSentenceBoundary(t *testing.T) {
	text := "他推开门。屋里很暗！有人在吗？没有人回答；只有风。"
	segs := Segment(text, 8)
	for i, s := range segs {
		if i == len(segs)-1 {
			continue
		}
		last := []rune(s)[len([]rune(s))-1]
		if !sentenceEnd(last) && !clauseEnd(last) && !closingPunct(last) {
			t.Fatalf("片段 %d 未在标点处结束: %q", i, s)
		}
	}
	if len(segs) < 2 {
		t.Fatalf("预期被切分为多片, got %v", segs)
	}
}

// 超长无标点句：硬切但不丢字符。
func TestSegmentHardCutLongSentence(t *testing.T) {
	text := strings.Repeat("无标点", 200) // 600 字符
	segs := Segment(text, 50)
	if strings.Join(segs, "") != text {
		t.Fatal("硬切丢字符")
	}
	if len(segs) < 10 {
		t.Fatalf("预期至少 10 片, got %d", len(segs))
	}
}

// 章节切分：标题边界、索引连续、字数合计等于全文。
func TestSplitChapters(t *testing.T) {
	text := `楔子
很久以前，有座山。

第一章 夜航船
他推开门，屋里很暗。

第二章 归途
后来他回来了。

后记
故事结束了。`
	chs := SplitChapters(text)
	if len(chs) != 4 {
		t.Fatalf("预期 4 章, got %d: %+v", len(chs), chs)
	}
	if chs[0].Title != "楔子" || chs[1].Title != "第一章 夜航船" {
		t.Fatalf("标题解析异常: %+v", chs)
	}
	total := 0
	var joined strings.Builder
	runes := []rune(text)
	for i, c := range chs {
		if c.Index != i {
			t.Fatalf("章节索引不连续: %+v", chs)
		}
		if c.End <= c.Start {
			t.Fatalf("章节范围异常: %+v", c)
		}
		total += c.Chars
		joined.WriteString(string(runes[c.Start:c.End]))
	}
	if joined.String() != text {
		t.Fatal("章节拼接与原文不一致（丢字符）")
	}
	if total != len(runes) {
		t.Fatalf("字数合计 %d != %d", total, len(runes))
	}
}

// 无章节标题时整本作为单章。
func TestSplitChaptersNoHeading(t *testing.T) {
	text := "只有一段普通正文，没有章节标题。"
	chs := SplitChapters(text)
	if len(chs) != 1 || chs[0].Title != "全文" || chs[0].Chars != len([]rune(text)) {
		t.Fatalf("异常: %+v", chs)
	}
}

// 规范化只影响空白，不改变文字。
func TestNormalizeText(t *testing.T) {
	in := "  他\u3000说：\u200b“你好”  \n\n 世界 "
	got := NormalizeText(in)
	if got != "他 说：“你好” 世界" {
		t.Fatalf("got %q", got)
	}
}
