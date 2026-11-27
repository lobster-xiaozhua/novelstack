package tts

import (
	"regexp"
	"testing"
)

func TestSplitDialogueSeparatesNarration(t *testing.T) {
	text := `他抬起头，低声说：“我们走吧。”然后推开了门。`
	re := regexp.MustCompile(`[“"「『]([^”"」』\n]{1,400})[”"」』]`)
	spans := SplitDialogue(text, re)
	if len(spans) != 3 {
		t.Fatalf("spans = %d: %+v", len(spans), spans)
	}
	if spans[0].Dialogue || spans[0].Text != "他抬起头，低声说：" {
		t.Fatalf("旁白 1 = %+v", spans[0])
	}
	if !spans[1].Dialogue || spans[1].Text != "我们走吧。" {
		t.Fatalf("对话 = %+v", spans[1])
	}
	if spans[2].Dialogue || spans[2].Text != "然后推开了门。" {
		t.Fatalf("旁白 2 = %+v", spans[2])
	}
}

func TestSplitDialogueWithoutPattern(t *testing.T) {
	spans := SplitDialogue("纯旁白文本", nil)
	if len(spans) != 1 || spans[0].Dialogue {
		t.Fatalf("spans = %+v", spans)
	}
}

func TestSplitDialogueMergesAdjacent(t *testing.T) {
	text := `“第一句”“第二句”`
	re := regexp.MustCompile(`[“"]([^”"]+)[”"]`)
	spans := SplitDialogue(text, re)
	if len(spans) != 1 || !spans[0].Dialogue {
		t.Fatalf("相邻对话应合并为一段: %+v", spans)
	}
	if spans[0].Text != "第一句第二句" {
		t.Fatalf("text = %q", spans[0].Text)
	}
}

func TestBuildSegmentsUsesDifferentVoices(t *testing.T) {
	svc := &Service{
		Provider:        NewMock(0),
		MaxSegmentRunes: 20,
		DialogueVoice:   "dialogue-x",
		DialoguePattern: `[“"]([^”"]+)[”"]`,
	}
	segs := svc.buildSegments(`旁白一句话。“对白一句话。”`, "narration-x", "dialogue-x", 1.0)
	if len(segs) != 2 {
		t.Fatalf("segs = %d: %+v", len(segs), segs)
	}
	if segs[0].voice != "narration-x" {
		t.Fatalf("旁白音色 = %s", segs[0].voice)
	}
	if segs[1].voice != "dialogue-x" {
		t.Fatalf("对话音色 = %s", segs[1].voice)
	}
	if segs[0].key == segs[1].key {
		t.Fatal("不同文本/音色的缓存键不应相同")
	}
}

func TestBuildSegmentsSingleVoiceWhenDialogueEmpty(t *testing.T) {
	svc := &Service{Provider: NewMock(0), MaxSegmentRunes: 20}
	segs := svc.buildSegments(`旁白。“对白。”`, "only-voice", "", 1.0)
	for _, sg := range segs {
		if sg.voice != "only-voice" {
			t.Fatalf("应全部使用旁白音色: %+v", sg)
		}
	}
}

func TestBuildSegmentsKeepsOrderAndText(t *testing.T) {
	svc := &Service{Provider: NewMock(0), MaxSegmentRunes: 5, DialogueVoice: "d", DialoguePattern: `“([^”]+)”`}
	text := `一二三四五六七八九十。“甲乙丙丁戊己庚辛。”`
	segs := svc.buildSegments(text, "n", "d", 1.0)
	var joined string
	for _, sg := range segs {
		joined += sg.text
	}
	// 引号被丢弃，其余字符必须完整保留且顺序不变。
	want := `一二三四五六七八九十。甲乙丙丁戊己庚辛。`
	if joined != want {
		t.Fatalf("拼接结果 = %q, want %q", joined, want)
	}
	for i := 1; i < len(segs); i++ {
		if segs[i].index <= segs[i-1].index {
			t.Fatalf("片段序号必须严格递增: %+v", segs)
		}
	}
}
