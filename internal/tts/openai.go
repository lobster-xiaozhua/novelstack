package tts

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// OpenAI 通过 HTTP 调用独立的 OpenAI 兼容 TTS 端点（方式 B）。
// 端点可以是 FastKoko、本地 llama.cpp 系 TTS 网关或任何实现了
// POST /v1/audio/speech 的服务；主后端只持有 base_url，不依赖其内部实现。
type OpenAI struct {
	baseURL  string
	model    string
	voice    string
	apiKey   string
	apiKeyFn string
	timeout  time.Duration
	maxAudio int64
	client   *http.Client
}

// NewOpenAI 创建 HTTP Provider。API key 优先取环境变量，其次取只读 secret 文件。
func NewOpenAI(cfg ProviderConfig) (*OpenAI, error) {
	if cfg.HTTPBaseURL == "" {
		return nil, fmt.Errorf("openai: 需要 TTS_HTTP_BASE_URL")
	}
	to := cfg.Timeout
	if to <= 0 {
		to = 20 * time.Second
	}
	maxAudio := cfg.MaxAudioBytes
	if maxAudio <= 0 {
		maxAudio = 16 << 20
	}
	voice := cfg.HTTPVoice
	if voice == "" {
		voice = "alloy"
	}
	model := cfg.HTTPModel
	if model == "" {
		model = "tts-1"
	}
	return &OpenAI{
		baseURL:  strings.TrimRight(cfg.HTTPBaseURL, "/"),
		model:    model,
		voice:    voice,
		apiKey:   cfg.HTTPAPIKey,
		apiKeyFn: cfg.HTTPAPIKeyFile,
		timeout:  to,
		maxAudio: maxAudio,
		// 不使用环境代理的设置，避免内网请求被全局代理劫持
		client: &http.Client{Transport: &http.Transport{
			Proxy:               nil,
			MaxIdleConnsPerHost: 4,
			IdleConnTimeout:     30 * time.Second,
		}},
	}, nil
}

// Name 含模型标识，参与缓存键。
func (o *OpenAI) Name() string { return "openai:" + o.model }

// Health 轻量探测：GET {base}/v1/models（2 秒超时），不合成、不加载模型。
func (o *OpenAI) Health(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, o.baseURL+"/v1/models", nil)
	if err != nil {
		return err
	}
	o.addAuth(req)
	resp, err := o.client.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
	// 401/403 说明服务在线但凭证有问题：如实返回不可用
	if resp.StatusCode >= 500 || resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return fmt.Errorf("%w: HTTP %d", ErrUnavailable, resp.StatusCode)
	}
	return nil
}

// resolveKey 读取 API key：环境变量优先，其次 secret 文件（不写日志）。
func (o *OpenAI) resolveKey() string {
	if o.apiKey != "" {
		return o.apiKey
	}
	if o.apiKeyFn != "" {
		if b, err := os.ReadFile(o.apiKeyFn); err == nil {
			return strings.TrimSpace(string(b))
		}
	}
	return ""
}

func (o *OpenAI) addAuth(req *http.Request) {
	if k := o.resolveKey(); k != "" {
		req.Header.Set("Authorization", "Bearer "+k)
	}
}

// Synthesize 调用 /v1/audio/speech，强制 response_format=wav。
func (o *OpenAI) Synthesize(ctx context.Context, text, voice string, speed float64) ([]byte, error) {
	if strings.TrimSpace(text) == "" {
		return nil, ErrEmptyText
	}
	if voice == "" {
		voice = o.voice
	}
	if speed <= 0 {
		speed = 1.0
	}
	payload := map[string]any{
		"model":           o.model,
		"input":           text,
		"voice":           voice,
		"speed":           speed,
		"response_format": "wav",
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, o.timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.baseURL+"/v1/audio/speech", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	o.addAuth(req)

	resp, err := o.client.Do(req)
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return nil, ErrTimeout
		}
		return nil, fmt.Errorf("%w: 请求失败", ErrUnavailable)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		// 不把引擎原始错误体透出给客户端；丢弃有界样本
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 8<<10))
		if resp.StatusCode == http.StatusTooManyRequests {
			return nil, fmt.Errorf("%w: 上游限流", ErrUnavailable)
		}
		return nil, fmt.Errorf("%w: HTTP %d", ErrUnavailable, resp.StatusCode)
	}
	out, err := io.ReadAll(io.LimitReader(resp.Body, o.maxAudio+1))
	if err != nil {
		return nil, fmt.Errorf("%w: 读取响应失败", ErrUnavailable)
	}
	if int64(len(out)) > o.maxAudio {
		return nil, fmt.Errorf("%w: 音频超过上限", ErrBadAudio)
	}
	if err := ValidateWAV(out); err != nil {
		return nil, err
	}
	return out, nil
}
