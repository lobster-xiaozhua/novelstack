// Package tts 提供可替换的 TTS Provider 抽象、文本分片与流式朗读编排。
package tts

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
)

// 音频格式常量：本阶段只用 WAV(PCM)，避免引入编解码依赖。
const FormatWAV = "wav"

// 归一化错误类别（可安全写入审计/日志，不含引擎原始响应）。
var (
	ErrTimeout      = errors.New("tts: synthesize timeout")
	ErrUnavailable  = errors.New("tts: provider unavailable")
	ErrBadAudio     = errors.New("tts: engine returned invalid audio")
	ErrEmptyText    = errors.New("tts: empty text")
	ErrTextTooLarge = errors.New("tts: text too large")
)

// Provider 是可替换的 TTS 引擎抽象。
// 主进程不加载任何模型：实现只做进程桥接或 HTTP 调用。
type Provider interface {
	// Name 是引擎标识（含模型信息），参与缓存键，保证换引擎后不会误命中旧缓存。
	Name() string
	// Synthesize 合成一段文本，返回完整 WAV 字节。
	Synthesize(ctx context.Context, text, voice string, speed float64) ([]byte, error)
	// Health 做轻量健康探测（不加载模型、不合成）。
	Health(ctx context.Context) error
}

// StreamingProvider 为未来的端到端流式引擎预留（本阶段不实现：
// 片段级流式已由 handler 通过 HTTP chunked 完成）。
type StreamingProvider interface {
	Provider
	SynthesizeStream(ctx context.Context, text, voice string, speed float64) (<-chan []byte, <-chan error)
}

// ProviderConfig 汇总所有引擎的接入参数（来自环境变量）。
type ProviderConfig struct {
	Kind           string // off/mock/piper/openai
	DefaultVoice   string
	Timeout        time.Duration
	MaxAudioBytes  int64
	PiperBin       string
	PiperModel     string
	PiperModelDir  string
	HTTPBaseURL    string
	HTTPModel      string
	HTTPVoice      string
	HTTPAPIKey     string
	HTTPAPIKeyFile string
	MockLatency    time.Duration
}

// NewProvider 按配置构造 Provider；Kind=off 时返回 nil（朗读接口返回 503）。
func NewProvider(cfg ProviderConfig) (Provider, error) {
	switch strings.ToLower(cfg.Kind) {
	case "", "off":
		return nil, nil
	case "mock":
		return NewMock(cfg.MockLatency), nil
	case "piper":
		return NewPiper(cfg)
	case "openai":
		return NewOpenAI(cfg)
	default:
		return nil, fmt.Errorf("未知 TTS_PROVIDER: %q", cfg.Kind)
	}
}

// CacheKey 计算片段级缓存键（也是“片段指纹”）。
// 组成：加密格式版本 + 引擎标识 + voice + speed + 音频格式 + 规范化文本的 SHA-256。
// speed 用固定小数位格式化，避免 1 与 1.0 产生不同键。
func CacheKey(providerName, voice string, speed float64, format, text string) string {
	h := sha256.New()
	io := func(s string) { h.Write([]byte(s)) }
	io("novelstack-tts-cache-v1\n")
	io("provider=" + providerName + "\n")
	io("voice=" + voice + "\n")
	io(fmt.Sprintf("speed=%.3f\n", speed))
	io("format=" + format + "\n")
	io("text=" + NormalizeText(text) + "\n")
	return hex.EncodeToString(h.Sum(nil))
}

// NormalizeText 规范化片段文本：统一全角空格、压缩连续空白、去掉零宽字符。
// 只影响缓存键与送入 TTS 的文本，不改变章节原文（原文仍按片段顺序解密保留）。
func NormalizeText(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	lastSpace := false
	for _, r := range s {
		switch r {
		case '\u200b', '\u200c', '\u200d', '\ufeff':
			continue
		}
		if unicode.IsSpace(r) {
			if !lastSpace {
				b.WriteRune(' ')
				lastSpace = true
			}
			continue
		}
		lastSpace = false
		b.WriteRune(r)
	}
	return strings.TrimSpace(b.String())
}

// classifyErr 把 Provider 错误归一化为审计可用的类别与可返回给日志的摘要。
// 对外响应永远不包含这里的 detail。
func classifyErr(err error) (class string) {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, ErrTimeout), errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(err, ErrBadAudio):
		return "bad_audio"
	case errors.Is(err, ErrUnavailable):
		return "unavailable"
	case errors.Is(err, context.Canceled):
		return "canceled"
	default:
		return "engine_error"
	}
}
