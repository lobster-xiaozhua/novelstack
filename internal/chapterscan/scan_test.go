package chapterscan

import (
	"math/rand"
	"strings"
	"testing"
	"unicode/utf8"
)

// 流式扫描结果必须与整文本切分完全一致，否则 /chapters 与 /plain 的 index 会错位。
func TestScannerMatchesSplit(t *testing.T) {
	cases := []string{
		"",
		"只有一行正文。",
		"第一章 夜航船\n他推开门。\n第二章 归途\n天亮的时候。\n",
		"前言正文\n第一章 起\n正文\n",
		"   \n\n第一章 起\n正文",
		"第一章 起\n\n\n",
		"第一章 起\r\n他推开门。\r\n第二章 归\r\n雨停了。\r\n",
		"序章\n甲\n正文\n尾声\n乙\n",
		"第1章 a\nX\n第2章 b\nY",
		"很长的一段没有章节标题的文字，" + strings.Repeat("他说。", 50),
	}
	for i, text := range cases {
		want := Split(text)
		got := scanAll(text)
		if len(want) != len(got) {
			t.Fatalf("case %d: 章节数不一致 want=%d got=%d\nwant=%+v\ngot=%+v", i, len(want), len(got), want, got)
		}
		for j := range want {
			if want[j] != got[j] {
				t.Fatalf("case %d 第 %d 章不一致\nwant=%+v\ngot =%+v", i, j, want[j], got[j])
			}
		}
	}
}

// 随机文本回归：任意组合的标题行/正文行/空行/CRLF 都必须一致。
func TestScannerMatchesSplitRandom(t *testing.T) {
	heads := []string{"第一章 夜航船", "第十二节 归途", "序章", "尾声", "第3章 风", "第一章   ", "番外 一年后"}
	bodies := []string{"他推开门，屋里很暗。", "雨还在下。", "", "   ", "没有标点的一行字", "她笑了，说：走吧。"}
	r := rand.New(rand.NewSource(20261125))
	for iter := 0; iter < 300; iter++ {
		var b strings.Builder
		n := 1 + r.Intn(12)
		for i := 0; i < n; i++ {
			switch r.Intn(4) {
			case 0:
				b.WriteString(heads[r.Intn(len(heads))])
			default:
				b.WriteString(bodies[r.Intn(len(bodies))])
			}
			if r.Intn(3) == 0 {
				b.WriteString("\r")
			}
			if i < n-1 || r.Intn(2) == 0 {
				b.WriteString("\n")
			}
		}
		text := b.String()
		want := Split(text)
		got := scanAll(text)
		if len(want) != len(got) {
			t.Fatalf("iter %d: 章节数不一致 want=%d got=%d text=%q", iter, len(want), len(got), text)
		}
		for j := range want {
			if want[j] != got[j] {
				t.Fatalf("iter %d 第 %d 章不一致 want=%+v got=%+v text=%q", iter, j, want[j], got[j], text)
			}
		}
	}
}

// 扫描结果只保留标题与偏移：不含正文内容。
func TestScannerKeepsNoContent(t *testing.T) {
	text := "第一章 夜航船\n" + strings.Repeat("他推开门，屋里很暗。", 200) + "\n第二章 归途\n雨停了。\n"
	got := scanAll(text)
	if len(got) != 2 {
		t.Fatalf("want 2 chapters, got %d", len(got))
	}
	for _, c := range got {
		if strings.Contains(c.Title, "他推开") || strings.Contains(c.Title, "雨停") {
			t.Fatalf("标题中不应包含正文: %q", c.Title)
		}
		if utf8.RuneCountInString(c.Title) > maxTitleRunes {
			t.Fatalf("标题超长: %d", utf8.RuneCountInString(c.Title))
		}
	}
	// 偏移连续覆盖全文（拼回即原文）
	if got[0].Start != 0 || got[0].End != got[1].Start || got[1].End != utf8.RuneCountInString(text) {
		t.Fatalf("偏移不连续: %+v total=%d", got, utf8.RuneCountInString(text))
	}
}

// 分块写入（模拟 1KiB 流式读取）结果与一次写入相同。
func TestScannerChunkedWrite(t *testing.T) {
	text := "第一章 夜航船\n" + strings.Repeat("他推开门，屋里很暗。窗外的雨还在下。", 40) +
		"\n第二章 归途\n天亮的时候，雨停了。\n"
	want := Split(text)
	sc := New(0)
	for i := 0; i < len(text); i += 1024 {
		end := i + 1024
		if end > len(text) {
			end = len(text)
		}
		if _, err := sc.Write([]byte(text[i:end])); err != nil {
			t.Fatal(err)
		}
	}
	got := sc.Close()
	if len(got) != len(want) {
		t.Fatalf("chunked: want %d got %d", len(want), len(got))
	}
	for i := range want {
		if want[i] != got[i] {
			t.Fatalf("chunked 第 %d 章不一致 want=%+v got=%+v", i, want[i], got[i])
		}
	}
}

func scanAll(text string) []Chapter {
	sc := New(0)
	_, _ = sc.Write([]byte(text))
	return sc.Close()
}
