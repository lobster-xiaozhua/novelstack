package tts

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestMockProviderProducesValidWAV(t *testing.T) {
	p := NewMock(0)
	if p.Name() == "" || p.Health(context.Background()) != nil {
		t.Fatal("mock provider 元信息异常")
	}
	a, err := p.Synthesize(context.Background(), "第一章测试。", "", 1.0)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateWAV(a); err != nil {
		t.Fatalf("WAV 无效: %v", err)
	}
	b, _ := p.Synthesize(context.Background(), "第一章测试。", "", 1.0)
	if string(a) != string(b) {
		t.Fatal("mock 音频应确定性生成")
	}
	if _, err := p.Synthesize(context.Background(), "   ", "", 1); err != ErrEmptyText {
		t.Fatalf("空文本应报错, got %v", err)
	}
}

func TestMockRespectsTimeout(t *testing.T) {
	p := NewMock(200 * time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := p.Synthesize(ctx, "慢慢来。", "", 1); err == nil {
		t.Fatal("超时应返回错误")
	}
}

// OpenAI 兼容 Provider：请求字段、鉴权头、错误映射。
func TestOpenAIProvider(t *testing.T) {
	var gotBody map[string]any
	var gotAuth string
	wav := ToneWAV(10, 220)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/audio/speech":
			gotAuth = r.Header.Get("Authorization")
			b, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(b, &gotBody)
			w.Header().Set("Content-Type", "audio/wav")
			_, _ = w.Write(wav)
		case "/v1/models":
			_, _ = w.Write([]byte(`{"data":[]}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	p, err := NewOpenAI(ProviderConfig{
		Kind:          "openai",
		HTTPBaseURL:   srv.URL,
		HTTPModel:     "tts-1",
		HTTPVoice:     "alloy",
		HTTPAPIKey:    "test-key",
		Timeout:       5 * time.Second,
		MaxAudioBytes: 4 << 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Health(context.Background()); err != nil {
		t.Fatalf("health: %v", err)
	}
	out, err := p.Synthesize(context.Background(), "你好。", "nova", 1.25)
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != string(wav) {
		t.Fatal("返回音频不一致")
	}
	if gotAuth != "Bearer test-key" {
		t.Fatalf("鉴权头异常: %q", gotAuth)
	}
	if gotBody["response_format"] != "wav" || gotBody["voice"] != "nova" {
		t.Fatalf("请求体异常: %v", gotBody)
	}
	if gotBody["speed"].(float64) != 1.25 {
		t.Fatalf("speed 未透传: %v", gotBody["speed"])
	}
	if gotBody["input"] != "你好。" {
		t.Fatalf("input 异常: %v", gotBody["input"])
	}
}

func TestOpenAIProviderErrors(t *testing.T) {
	cases := []struct {
		name    string
		status  int
		body    string
		wantErr error
	}{
		{"upstream500", 500, "boom: internal trace", ErrUnavailable},
		{"ratelimit", 429, "", ErrUnavailable},
		{"notWAV", 200, "not-a-wav-body", ErrBadAudio},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()
			p, err := NewOpenAI(ProviderConfig{HTTPBaseURL: srv.URL, Timeout: 2 * time.Second, MaxAudioBytes: 1 << 20})
			if err != nil {
				t.Fatal(err)
			}
			_, err = p.Synthesize(context.Background(), "文本。", "", 1)
			if err == nil {
				t.Fatal("应返回错误")
			}
			// 错误信息不得包含上游原始响应
			if strings.Contains(err.Error(), "boom") {
				t.Fatalf("错误信息泄漏上游响应: %v", err)
			}
			if tc.wantErr != nil && !containsErr(err, tc.wantErr) {
				t.Fatalf("want %v, got %v", tc.wantErr, err)
			}
		})
	}
}

func containsErr(err, target error) bool {
	if err == target {
		return true
	}
	for e := err; e != nil; {
		if e == target {
			return true
		}
		u, ok := e.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		e = u.Unwrap()
	}
	return false
}

// piper 进程桥接：用一个假二进制验证 stdin/stdout 约定、参数与失败处理。
func TestPiperProviderWithFakeBinary(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script fake binary 仅支持类 Unix")
	}
	dir := t.TempDir()
	model := filepath.Join(dir, "voice.onnx")
	if err := os.WriteFile(model, []byte("fake-model"), 0o600); err != nil {
		t.Fatal(err)
	}
	wavPath := filepath.Join(dir, "sample.wav")
	if err := os.WriteFile(wavPath, ToneWAV(5, 220), 0o600); err != nil {
		t.Fatal(err)
	}

	// 假 piper：把 stdin 文本长度写入 stderr，stdout 输出预置 WAV
	bin := filepath.Join(dir, "fake-piper.sh")
	script := fmt.Sprintf(`#!/bin/sh
cat > /tmp/fake-piper-$$.txt
wc -c < /tmp/fake-piper-$$.txt >&2
cat %q
rm -f /tmp/fake-piper-$$.txt
`, wavPath)
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	p, err := NewPiper(ProviderConfig{
		Kind:          "piper",
		PiperBin:      bin,
		PiperModel:    model,
		PiperModelDir: dir,
		Timeout:       5 * time.Second,
		MaxAudioBytes: 4 << 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Health(context.Background()); err != nil {
		t.Fatalf("health: %v", err)
	}
	if name := p.Name(); !strings.HasPrefix(name, "piper:") {
		t.Fatalf("name=%q", name)
	}
	out, err := p.Synthesize(context.Background(), "测试文本。", "voice", 1.0)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateWAV(out); err != nil {
		t.Fatal(err)
	}

	// voice 对应 <MODEL_DIR>/voice.onnx 存在时应选用该模型（这里同名文件存在）
	if got := p.resolveModel("voice"); got != model {
		t.Fatalf("resolveModel=%q", got)
	}

	// 失败路径：退出码非 0 → ErrUnavailable，且不泄漏 stderr
	badBin := filepath.Join(dir, "bad-piper.sh")
	if err := os.WriteFile(badBin, []byte("#!/bin/sh\necho 'secret trace' >&2\nexit 3\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	p2, _ := NewPiper(ProviderConfig{PiperBin: badBin, PiperModel: model, Timeout: 2 * time.Second})
	_, err = p2.Synthesize(context.Background(), "x", "", 1)
	if err == nil || !containsErr(err, ErrUnavailable) {
		t.Fatalf("got %v", err)
	}
	if strings.Contains(err.Error(), "secret trace") {
		t.Fatal("错误信息泄漏 stderr")
	}

	// 超时路径
	slowBin := filepath.Join(dir, "slow-piper.sh")
	if err := os.WriteFile(slowBin, []byte("#!/bin/sh\nsleep 5\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	p3, _ := NewPiper(ProviderConfig{PiperBin: slowBin, PiperModel: model, Timeout: 100 * time.Millisecond})
	if _, err := p3.Synthesize(context.Background(), "x", "", 1); !containsErr(err, ErrTimeout) {
		t.Fatalf("want timeout, got %v", err)
	}
}

// 缓存键：文本/voice/speed/引擎/格式任一变化都必须产生不同键。
func TestCacheKeySensitivity(t *testing.T) {
	base := CacheKey("mock:v1", "v1", 1.0, FormatWAV, "他推开门。")
	same := CacheKey("mock:v1", "v1", 1.0, FormatWAV, "  他推开门。  ") // 规范化后相同
	if base != same {
		t.Fatal("规范化后相同文本应命中同一键")
	}
	cases := map[string]string{
		"text":     CacheKey("mock:v1", "v1", 1.0, FormatWAV, "他推开门！"),
		"voice":    CacheKey("mock:v1", "v2", 1.0, FormatWAV, "他推开门。"),
		"speed":    CacheKey("mock:v1", "v1", 1.1, FormatWAV, "他推开门。"),
		"provider": CacheKey("piper:voice", "v1", 1.0, FormatWAV, "他推开门。"),
		"format":   CacheKey("mock:v1", "v1", 1.0, "mp3", "他推开门。"),
	}
	for name, k := range cases {
		if k == base {
			t.Fatalf("%s 变化未改变缓存键", name)
		}
	}
}

func TestValidateWAV(t *testing.T) {
	good := ToneWAV(3, 220)
	if err := ValidateWAV(good); err != nil {
		t.Fatal(err)
	}
	for _, bad := range [][]byte{
		[]byte("RIFF"),
		[]byte("XXXX\x00\x00\x00\x00WAVE"),
		append([]byte(nil), good[:20]...),
	} {
		if err := ValidateWAV(bad); err == nil {
			t.Fatalf("畸形 WAV 未被拒绝: %q", bad)
		}
	}
}
