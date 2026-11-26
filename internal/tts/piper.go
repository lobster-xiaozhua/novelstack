package tts

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Piper 通过 os/exec 调用本地 piper 二进制（方式 A：进程桥接）。
// 模型在子进程内加载，主进程不引入 CGO/ONNX，内存占用随子进程释放。
//
// 调用形式：
//
//	piper --model <model.onnx> --output_file - [--length_scale <1/speed>]
//	（stdin 写入文本，stdout 输出 WAV）
type Piper struct {
	bin        string
	model      string
	modelDir   string
	timeout    time.Duration
	maxAudio   int64
	voiceCache map[string]string
}

// NewPiper 创建 piper Provider。bin 为空时使用 PATH 中的 piper。
func NewPiper(cfg ProviderConfig) (*Piper, error) {
	if cfg.PiperModel == "" && cfg.PiperModelDir == "" {
		return nil, fmt.Errorf("piper: 需要 PIPER_MODEL 或 PIPER_MODEL_DIR")
	}
	bin := cfg.PiperBin
	if bin == "" {
		bin = "piper"
	}
	to := cfg.Timeout
	if to <= 0 {
		to = 20 * time.Second
	}
	maxAudio := cfg.MaxAudioBytes
	if maxAudio <= 0 {
		maxAudio = 16 << 20
	}
	return &Piper{
		bin:        bin,
		model:      cfg.PiperModel,
		modelDir:   cfg.PiperModelDir,
		timeout:    to,
		maxAudio:   maxAudio,
		voiceCache: make(map[string]string),
	}, nil
}

// Name 含模型名：换模型后缓存键随之变化，不会误用旧音频。
func (p *Piper) Name() string {
	m := p.model
	if m == "" && p.modelDir != "" {
		m = "dir"
	}
	return "piper:" + filepath.Base(strings.TrimSuffix(m, ".onnx"))
}

// Health 只检查二进制与模型文件是否存在（不加载模型）。
func (p *Piper) Health(ctx context.Context) error {
	if _, err := exec.LookPath(p.bin); err != nil {
		return fmt.Errorf("%w: 找不到 piper 可执行文件", ErrUnavailable)
	}
	if p.model != "" {
		if st, err := os.Stat(p.model); err != nil || st.IsDir() {
			return fmt.Errorf("%w: 模型文件不可读", ErrUnavailable)
		}
	}
	return nil
}

// resolveModel 根据 voice 选择模型：voice="xxx" 时尝试 <MODEL_DIR>/xxx.onnx。
func (p *Piper) resolveModel(voice string) string {
	if voice != "" && p.modelDir != "" {
		if cached, ok := p.voiceCache[voice]; ok {
			return cached
		}
		cand := filepath.Join(p.modelDir, voice+".onnx")
		if st, err := os.Stat(cand); err == nil && !st.IsDir() {
			p.voiceCache[voice] = cand
			return cand
		}
	}
	return p.model
}

// Synthesize 执行一次合成：stdin 文本、stdout WAV、stderr 仅用于内部诊断。
func (p *Piper) Synthesize(ctx context.Context, text, voice string, speed float64) ([]byte, error) {
	model := p.resolveModel(voice)
	if model == "" {
		return nil, fmt.Errorf("%w: 未配置模型", ErrUnavailable)
	}
	ctx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()

	args := []string{"--model", model, "--output_file", "-"}
	if speed > 0 && speed != 1.0 {
		// piper 的 length_scale 与语速成反比
		args = append(args, "--length_scale", strconv.FormatFloat(1/speed, 'f', 3, 64))
	}
	cmd := exec.CommandContext(ctx, p.bin, args...)
	cmd.Stdin = strings.NewReader(text)
	var stdout bytes.Buffer
	var stderr limitedBuffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	// 子进程异常退出时确保被回收，不残留僵尸进程
	cmd.WaitDelay = 2 * time.Second

	err := cmd.Run()
	if ctx.Err() == context.DeadlineExceeded {
		return nil, ErrTimeout
	}
	if err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return nil, fmt.Errorf("%w: piper 未安装", ErrUnavailable)
		}
		// stderr 只进日志/审计摘要（由调用方记录），此处仅返回类别
		return nil, fmt.Errorf("%w: piper 退出异常 (stderr %d 字节)", ErrUnavailable, stderr.Len())
	}
	if int64(stdout.Len()) > p.maxAudio {
		return nil, fmt.Errorf("%w: 音频超过上限", ErrBadAudio)
	}
	out := stdout.Bytes()
	if err := ValidateWAV(out); err != nil {
		return nil, err
	}
	return out, nil
}

// limitedBuffer 只保留前 4KB stderr，避免异常引擎刷爆内存。
type limitedBuffer struct {
	buf bytes.Buffer
}

func (l *limitedBuffer) Write(p []byte) (int, error) {
	const cap = 4096
	if l.buf.Len() < cap {
		remain := cap - l.buf.Len()
		if len(p) > remain {
			l.buf.Write(p[:remain])
		} else {
			l.buf.Write(p)
		}
	}
	return len(p), nil
}

func (l *limitedBuffer) Len() int { return l.buf.Len() }

// String 只在内部日志使用，绝不写入 HTTP 响应。
func (l *limitedBuffer) String() string { return l.buf.String() }
