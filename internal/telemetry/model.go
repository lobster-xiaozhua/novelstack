// Package telemetry 负责客户端遥测数据的接收、清洗、落库、聚合与过期清理。
//
// 安全红线（本包所有代码必须遵守）：
//   - 只接受运行状况、设备信息与耗时数据；
//   - 书籍正文、音频明文、Token/密钥一律在入口丢弃，不写入数据库；
//   - 客户端已经清洗过一遍，服务端仍做防御性二次清洗（不信任客户端）。
package telemetry

import (
	"encoding/json"
	"strings"
	"time"
	"unicode/utf8"
)

// 允许的事件类型白名单。不在白名单内的一律丢弃，避免客户端把任意内容塞进 kind 字段。
const (
	KindCrash          = "CRASH"           // 未捕获异常（含完整堆栈）
	KindANR            = "ANR"             // 主线程卡死（看门狗捕获）
	KindSlowChapter    = "SLOW_CHAPTER"    // 解密 + 渲染 > 500ms
	KindTTSFirstFrame  = "TTS_FIRST_FRAME" // 首帧音频耗时
	KindJank           = "JANK"            // 连续 3 帧 > 32ms
	KindPreload        = "PRELOAD_STATS"   // 章节预读结果
	KindSearch         = "SEARCH_STATS"    // 搜索耗时与命中数
	KindAPI            = "API"             // 接口耗时（无 body、无 Authorization）
	KindColdStart      = "COLD_START"      // 冷启动到首帧
	KindPlaybackError  = "PLAYBACK_ERROR"  // TTS 播放失败
	KindForegroundSync = "FOREGROUND_SYNC"
)

var allowedKinds = map[string]struct{}{
	KindCrash: {}, KindANR: {}, KindSlowChapter: {}, KindTTSFirstFrame: {},
	KindJank: {}, KindPreload: {}, KindSearch: {}, KindAPI: {},
	KindColdStart: {}, KindPlaybackError: {}, KindForegroundSync: {},
}

// 清洗上限。客户端也有同样的限制，这里再兜一层，防止被改包绕过。
const (
	maxStackLines = 100
	maxStackBytes = 16 << 10 // 16KB
	maxNameLen    = 160
	maxContextKey = 16
	maxValueLen   = 200
	maxEvents     = 500
)

// 敏感字段名片段：命中即整条丢弃。宁可少收一个指标，也不冒险收正文或凭证。
var sensitiveKeyParts = []string{
	"token", "password", "passwd", "secret", "authorization", "cookie",
	"ciphertext", "cipher", "wrapped_dek", "dek", "key",
	"content", "text", "body", "plain", "chapter_body", "audio",
}

// Event 是单条遥测事件。字段刻意保持扁平，便于 SQL 聚合。
type Event struct {
	ID         string         `json:"id"`
	Kind       string         `json:"kind"`
	Name       string         `json:"name"`
	Severity   string         `json:"severity"`
	TS         string         `json:"ts"` // RFC3339，客户端本地时间
	DurationMS int64          `json:"duration_ms"`
	Stack      string         `json:"stack"`
	Context    map[string]any `json:"context"`
	Count      int64          `json:"count"` // 客户端合并后的重复次数
}

// Envelope 是一次批量上报的载荷。
type Envelope struct {
	DeviceID   string  `json:"device_id"`
	AppVersion string  `json:"app_version"`
	OS         string  `json:"os"`
	Model      string  `json:"model"`
	Events     []Event `json:"events"`
}

// Cleaned 是清洗后的结果，可直接落库。
type Cleaned struct {
	DeviceID   string
	AppVersion string
	OS         string
	Model      string
	Events     []CleanEvent
	Dropped    int
}

// CleanEvent 是清洗后的事件（时间已归一化为 UTC 毫秒）。
type CleanEvent struct {
	Kind        string
	Name        string
	Severity    string
	EventTime   time.Time
	DurationMS  int64
	Stack       string
	ContextJSON string
	Count       int64
}

// Clean 对整批载荷做防御性清洗：丢弃非法事件、截断堆栈、剥离敏感上下文。
// 返回的 Cleaned 永不为 nil；Dropped 记录被丢弃的事件数（便于服务端日志观测）。
func Clean(env Envelope, now time.Time) Cleaned {
	out := Cleaned{
		DeviceID:   clip(env.DeviceID, 64),
		AppVersion: clip(env.AppVersion, 32),
		OS:         clip(env.OS, 32),
		Model:      clip(env.Model, 64),
	}
	for _, e := range env.Events {
		if len(out.Events) >= maxEvents {
			out.Dropped++
			continue
		}
		ce, ok := cleanEvent(e, now)
		if !ok {
			out.Dropped++
			continue
		}
		out.Events = append(out.Events, ce)
	}
	return out
}

func cleanEvent(e Event, now time.Time) (CleanEvent, bool) {
	kind := strings.ToUpper(strings.TrimSpace(e.Kind))
	if _, ok := allowedKinds[kind]; !ok {
		return CleanEvent{}, false
	}
	ts := parseTime(e.TS, now)
	// 未来时间戳不可信（改系统时间或伪造），钳到 now，避免污染按时间聚合。
	if ts.After(now.Add(5 * time.Minute)) {
		ts = now
	}
	cnt := e.Count
	if cnt < 1 {
		cnt = 1
	}
	if cnt > 10000 {
		cnt = 10000
	}
	sev := strings.ToLower(strings.TrimSpace(e.Severity))
	switch sev {
	case "debug", "info", "warn", "error", "fatal":
	default:
		sev = "info"
	}
	if kind == KindCrash || kind == KindANR {
		sev = "fatal"
	}
	dur := e.DurationMS
	if dur < 0 {
		dur = 0
	}
	if dur > 24*60*60*1000 {
		dur = 24 * 60 * 60 * 1000
	}
	return CleanEvent{
		Kind:        kind,
		Name:        clip(e.Name, maxNameLen),
		Severity:    sev,
		EventTime:   ts.Truncate(time.Millisecond).UTC(),
		DurationMS:  dur,
		Stack:       cleanStack(e.Stack),
		ContextJSON: cleanContext(e.Context),
		Count:       cnt,
	}, true
}

// parseTime 解析客户端时间；失败或为零值时回落到 now（宁可时间偏差，也不要丢事件）。
func parseTime(s string, now time.Time) time.Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return now.UTC()
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05.000Z"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC()
		}
	}
	return now.UTC()
}

// cleanStack 只保留前 100 行 / 16KB。第三方库深栈往往是噪音，也是体积大头。
func cleanStack(s string) string {
	if s == "" {
		return ""
	}
	lines := strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n")
	if len(lines) > maxStackLines {
		lines = append(lines[:maxStackLines], "… (truncated)")
	}
	out := strings.Join(lines, "\n")
	if len(out) > maxStackBytes {
		cut := maxStackBytes
		for cut > 0 && !utf8.RuneStart(out[cut]) {
			cut--
		}
		out = out[:cut] + "\n… (truncated)"
	}
	return out
}

// cleanContext 是白名单式清洗：只保留标量值，敏感键整条删除，空值直接丢弃。
// 嵌套对象/数组一律不保留——它们最容易夹带正文。
func cleanContext(ctx map[string]any) string {
	if len(ctx) == 0 {
		return "{}"
	}
	kept := make(map[string]any, len(ctx))
	for k, v := range ctx {
		if len(kept) >= maxContextKey {
			break
		}
		lk := strings.ToLower(strings.TrimSpace(k))
		if lk == "" || isSensitiveKey(lk) {
			continue
		}
		switch val := v.(type) {
		case string:
			if strings.TrimSpace(val) == "" {
				continue
			}
			kept[lk] = clip(val, maxValueLen)
		case bool:
			kept[lk] = val
		case nil:
			continue
		default:
			// 数值统一按 float64 存：JSON 解码本来就是 float64，
			// 但内部调用可能传 int/int64，用反射兜一层，避免指标被静默丢弃。
			if n, ok := asNumber(v); ok {
				kept[lk] = n
				continue
			}
			// 对象/数组直接丢弃：无法保证其中没有正文片段。
			continue
		}
	}
	if len(kept) == 0 {
		return "{}"
	}
	b, err := json.Marshal(kept)
	if err != nil {
		return "{}"
	}
	return string(b)
}

func isSensitiveKey(k string) bool {
	for _, p := range sensitiveKeyParts {
		if strings.Contains(k, p) {
			return true
		}
	}
	return false
}

// asNumber 把各种整数/浮点类型归一为 float64；非数值返回 false。
func asNumber(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int32:
		return float64(n), true
	case int64:
		return float64(n), true
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	}
	return 0, false
}

// clip 按 rune 截断，避免把多字节字符切坏。
func clip(s string, max int) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	r := []rune(s)
	return string(r[:max])
}
