// Package purge 实现「解密后、输出前」的正文本净化。
//
// 设计约束：
//   - 只做删除，不做替换，因此可以把多条规则合并成一个 alternation 一次扫描；
//   - 全部正则使用 Go 的 RE2 引擎，最坏情况线性时间，不会出现回溯爆炸；
//   - 规则在启动时编译并校验，运行期只有一次 ReplaceAllString + 一次空行折叠；
//   - 规则数量与单条长度都有上限，避免运维误配导致每章都要跑几十 KB 的正则。
//
// 安全边界：规则文本来自运维配置，可能包含站点广告词，但不含书籍正文；
// 本包不记录任何被处理文本，也不返回命中位置（避免把正文写进日志）。
package purge

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"
)

const (
	// MaxRules 是单次可配置的规则条数上限。
	MaxRules = 64
	// MaxRuleBytes 是单条规则的长度上限（正则源码字节数）。
	MaxRuleBytes = 512
	// MaxJoinedBytes 是合并后表达式长度上限（防止单次扫描的 DFA 过大）。
	MaxJoinedBytes = 16 << 10
)

// Engine 是只读的净化引擎；并发安全（regexp 本身并发安全）。
type Engine struct {
	// joined 是所有规则的合并表达式；为 nil 表示无规则。
	joined *regexp.Regexp
	// each 是逐条回退路径：合并编译失败时使用。
	each []*regexp.Regexp
	// blankLines 折叠删除后留下的多余空行。
	blankLines  *regexp.Regexp
	fingerprint string
	count       int
}

// New 编译规则集合。任一规则非法即返回 error，由启动流程 fail-fast。
func New(patterns []string) (*Engine, error) {
	cleaned := make([]string, 0, len(patterns))
	for i, p := range patterns {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if len(p) > MaxRuleBytes {
			return nil, fmt.Errorf("第 %d 条净化规则过长（%d 字节，上限 %d）", i+1, len(p), MaxRuleBytes)
		}
		cleaned = append(cleaned, p)
	}
	if len(cleaned) > MaxRules {
		return nil, fmt.Errorf("净化规则过多（%d 条，上限 %d）", len(cleaned), MaxRules)
	}
	e := &Engine{
		blankLines: regexp.MustCompile(`\n[ \t]*(?:\n[ \t]*){2,}`),
		count:      len(cleaned),
	}
	if len(cleaned) == 0 {
		e.fingerprint = "none"
		return e, nil
	}
	// 先逐条编译：既做校验，也保留回退路径。
	for i, p := range cleaned {
		re, err := regexp.Compile(p)
		if err != nil {
			return nil, fmt.Errorf("第 %d 条净化规则无效：%w", i+1, err)
		}
		e.each = append(e.each, re)
	}
	quoted := make([]string, 0, len(cleaned))
	for _, re := range e.each {
		quoted = append(quoted, "(?:"+re.String()+")")
	}
	joined := strings.Join(quoted, "|")
	if len(joined) <= MaxJoinedBytes {
		if re, err := regexp.Compile(joined); err == nil {
			e.joined = re
		}
	}
	sum := sha256.Sum256([]byte(joined))
	// 指纹参与章节 ETag：改了规则，客户端缓存必须失效。
	e.fingerprint = hex.EncodeToString(sum[:6])
	return e, nil
}

// Count 返回生效的规则条数。
func (e *Engine) Count() int {
	if e == nil {
		return 0
	}
	return e.count
}

// Fingerprint 返回规则指纹（无规则时为 "none"）。
func (e *Engine) Fingerprint() string {
	if e == nil {
		return "none"
	}
	return e.fingerprint
}

// Apply 返回净化后的文本。没有命中时原样返回同一个字符串（零拷贝）。
func (e *Engine) Apply(text string) string {
	if e == nil || e.count == 0 || text == "" {
		return text
	}
	var out string
	if e.joined != nil {
		out = e.joined.ReplaceAllString(text, "")
	} else {
		out = text
		for _, re := range e.each {
			out = re.ReplaceAllString(out, "")
		}
	}
	if out == text {
		return text
	}
	// 删掉整句后常留下连续空行；折叠成最多一个空行，避免阅读页出现大片留白。
	out = e.blankLines.ReplaceAllString(out, "\n\n")
	return out
}
