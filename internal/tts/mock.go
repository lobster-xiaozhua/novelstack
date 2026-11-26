package tts

import (
	"context"
	"strings"
	"time"
)

// Mock 是开发/演示/测试用的 Provider：不依赖任何外部引擎，
// 生成确定性正弦波 WAV，时长随文本长度变化，便于验证流式与缓存行为。
// 生产环境不建议启用（TTS_PROVIDER=mock 需显式设置）。
type Mock struct {
	// Latency 模拟合成延迟（测试首片 flush 行为用）。
	Latency time.Duration
	// FailOn 包含该子串的文本会返回错误（测试失败路径用）；默认空表示不失败。
	FailOn string
}

// NewMock 创建 mock Provider。
func NewMock(latency time.Duration) *Mock { return &Mock{Latency: latency} }

// Name 实现 Provider。
func (m *Mock) Name() string { return "mock:v1" }

// Health 永远可用。
func (m *Mock) Health(ctx context.Context) error { return nil }

// Synthesize 生成正弦波 WAV；Latency 通过 select 保证可被 ctx 取消。
func (m *Mock) Synthesize(ctx context.Context, text, voice string, speed float64) ([]byte, error) {
	n := len([]rune(NormalizeText(text)))
	if n == 0 {
		return nil, ErrEmptyText
	}
	if m.FailOn != "" && strings.Contains(text, m.FailOn) {
		return nil, ErrUnavailable
	}
	if m.Latency > 0 {
		t := time.NewTimer(m.Latency)
		defer t.Stop()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-t.C:
		}
	}
	freq := 220.0
	switch voice {
	case "low":
		freq = 160
	case "high":
		freq = 320
	}
	// speed 不影响 mock 的时长/音高：保证缓存键变化时音频仍可区分（键不同即视为不同片段）
	_ = speed
	return ToneWAV(n, freq), nil
}
