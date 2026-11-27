package purge

import (
	"fmt"
	"strings"
	"testing"
)

func TestApplyRemovesAds(t *testing.T) {
	e, err := New([]string{`请记住本站.*`, `https?://\S+`})
	if err != nil {
		t.Fatal(err)
	}
	in := "第一章 开端\n请记住本站域名 novel.example.com\n他推开门，走了出去。\n访问 https://spam.example/x 获取更多"
	out := e.Apply(in)
	if strings.Contains(out, "请记住本站") {
		t.Fatalf("广告句未删除: %q", out)
	}
	if strings.Contains(out, "spam.example") {
		t.Fatalf("URL 未删除: %q", out)
	}
	if !strings.Contains(out, "他推开门，走了出去。") {
		t.Fatalf("正文被误删: %q", out)
	}
	if e.Count() != 2 {
		t.Fatalf("Count = %d", e.Count())
	}
}

func TestApplyNoRulesIsIdentity(t *testing.T) {
	e, err := New(nil)
	if err != nil {
		t.Fatal(err)
	}
	in := "  原样返回\n\n\n不动  "
	if out := e.Apply(in); out != in {
		t.Fatalf("无规则时不应修改文本: %q", out)
	}
	if e.Fingerprint() != "none" {
		t.Fatalf("fingerprint = %s", e.Fingerprint())
	}
}

func TestApplyCollapsesBlankLines(t *testing.T) {
	e, err := New([]string{`广告行`})
	if err != nil {
		t.Fatal(err)
	}
	out := e.Apply("第一段\n广告行\n\n\n\n第二段")
	if strings.Contains(out, "\n\n\n") {
		t.Fatalf("多余空行未折叠: %q", out)
	}
	if !strings.Contains(out, "第一段") || !strings.Contains(out, "第二段") {
		t.Fatalf("段落丢失: %q", out)
	}
}

func TestFingerprintChangesWithRules(t *testing.T) {
	a, _ := New([]string{`甲`})
	b, _ := New([]string{`乙`})
	if a.Fingerprint() == b.Fingerprint() {
		t.Fatal("不同规则的指纹不应相同")
	}
	c, _ := New([]string{`甲`})
	if a.Fingerprint() != c.Fingerprint() {
		t.Fatal("相同规则的指纹应相同")
	}
}

func TestInvalidRuleRejected(t *testing.T) {
	if _, err := New([]string{`(未闭合`}); err == nil {
		t.Fatal("非法正则应返回错误")
	}
}

func TestTooManyRulesRejected(t *testing.T) {
	rules := make([]string, 0, MaxRules+1)
	for i := 0; i <= MaxRules; i++ {
		rules = append(rules, `x`)
	}
	if _, err := New(rules); err == nil {
		t.Fatal("超过上限应返回错误")
	}
}

func TestLongRuleRejected(t *testing.T) {
	if _, err := New([]string{strings.Repeat("a", MaxRuleBytes+1)}); err == nil {
		t.Fatal("超长规则应返回错误")
	}
}

// TestManyRulesStillLinear 用 40 条规则跑一段 20 万字符文本，
// 目的是确保合并后的单次扫描没有出现病态慢路径（不校验具体毫秒数）。
func TestManyRulesStillLinear(t *testing.T) {
	rules := make([]string, 0, 40)
	for i := 0; i < 40; i++ {
		rules = append(rules, fmt.Sprintf(`广告词%d.*`, i))
	}
	e, err := New(rules)
	if err != nil {
		t.Fatal(err)
	}
	body := strings.Repeat("正文内容，正常阅读。\n", 20000)
	out := e.Apply(body)
	if len(out) != len(body) {
		t.Fatalf("未命中的文本长度不应变化: %d != %d", len(out), len(body))
	}
}
