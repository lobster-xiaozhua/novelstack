package telemetry

import (
	"strings"
	"testing"
	"time"
)

// 第九阶段：服务端二次清洗的验收用例。
// 客户端已经清洗过，这里模拟「被改包的客户端」直接灌脏数据。

func TestCleanDropsSensitiveContext(t *testing.T) {
	now := time.Now().UTC()
	env := Envelope{
		DeviceID: "dev-1", AppVersion: "0.2.1", OS: "14", Model: "Xiaomi 14",
		Events: []Event{{
			Kind: KindSlowChapter, Name: "夜航船·第一章", DurationMS: 850,
			Context: map[string]any{
				"chapter_index": 3,
				"book_title":    "夜航船",
				"text":          "这是正文，绝不能入库",
				"ciphertext":    "AAAA",
				"authorization": "Bearer secret",
				"token":         "nsd_xxx",
				"empty":         "",
				"nested":        map[string]any{"text": "嵌套正文"},
			},
		}},
	}
	c := Clean(env, now)
	if len(c.Events) != 1 {
		t.Fatalf("事件应保留 1 条，实际 %d（dropped=%d）", len(c.Events), c.Dropped)
	}
	ctx := c.Events[0].ContextJSON
	for _, banned := range []string{"正文", "ciphertext", "authorization", "token", "nested", `"empty"`} {
		if strings.Contains(ctx, banned) {
			t.Fatalf("上下文仍含敏感内容 %q: %s", banned, ctx)
		}
	}
	if !strings.Contains(ctx, `"chapter_index":3`) || !strings.Contains(ctx, `"book_title":"夜航船"`) {
		t.Fatalf("白名单字段被误删: %s", ctx)
	}
}

func TestCleanRejectsUnknownKindAndClampsCount(t *testing.T) {
	now := time.Now().UTC()
	env := Envelope{DeviceID: "d", Events: []Event{
		{Kind: "CUSTOM_DEBUG", Name: "x"},
		{Kind: "crash", Name: "boom", Count: 999999},
	}}
	c := Clean(env, now)
	if len(c.Events) != 1 || c.Dropped != 1 {
		t.Fatalf("应丢弃 1 条未知类型，实际 events=%d dropped=%d", len(c.Events), c.Dropped)
	}
	e := c.Events[0]
	if e.Kind != KindCrash {
		t.Fatalf("kind 应归一化为 CRASH，实际 %s", e.Kind)
	}
	if e.Severity != "fatal" {
		t.Fatalf("崩溃 severity 应强制 fatal，实际 %s", e.Severity)
	}
	if e.Count != 10000 {
		t.Fatalf("count 应被钳到 10000，实际 %d", e.Count)
	}
}

func TestCleanTruncatesStackTo100Lines(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 300; i++ {
		b.WriteString("  at frame")
		b.WriteString(string(rune('A' + i%26)))
		b.WriteString("(File.kt:1)\n")
	}
	c := Clean(Envelope{DeviceID: "d", Events: []Event{{Kind: KindCrash, Stack: b.String()}}}, time.Now())
	lines := strings.Split(c.Events[0].Stack, "\n")
	if len(lines) > maxStackLines+1 {
		t.Fatalf("堆栈应截断到 %d 行（含提示行），实际 %d", maxStackLines+1, len(lines))
	}
	if !strings.HasSuffix(c.Events[0].Stack, "(truncated)") {
		t.Fatal("截断后应有 (truncated) 标记")
	}
}

func TestCleanClampsFutureTimestamp(t *testing.T) {
	now := time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)
	future := now.Add(48 * time.Hour).Format(time.RFC3339)
	c := Clean(Envelope{DeviceID: "d", Events: []Event{{Kind: KindAPI, TS: future}}}, now)
	if c.Events[0].EventTime.After(now.Add(time.Minute)) {
		t.Fatalf("未来时间应被钳到 now，实际 %s", c.Events[0].EventTime)
	}
}
