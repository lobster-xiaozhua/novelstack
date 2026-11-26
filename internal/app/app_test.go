package app

import (
	"io"
	"os"
	"strings"
	"testing"

	"novelstack/internal/config"
)

// captureStdout 捕获同步函数调用期间写到 stdout 的内容；不并行运行以免互相干扰。
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	defer func() { os.Stdout = old }()
	fn()
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	b, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	_ = r.Close()
	return string(b)
}

func TestAutoBackupPassphraseWarning(t *testing.T) {
	t.Setenv("BACKUP_PASSPHRASE_FILE", "")
	t.Setenv("NOVELSTACK_BACKUP_PASSPHRASE", "")
	cfg := &config.Config{AutoBackup: true}

	out := captureStdout(t, func() {
		if got := newAutoBackupScheduler(cfg, nil, nil); got != nil {
			t.Error("缺口令时不应创建调度器")
		}
	})
	if !strings.Contains(out, "WARNING:") || !strings.Contains(out, "AUTO_BACKUP") {
		t.Fatalf("缺少启动 WARNING: %q", out)
	}
	lower := strings.ToLower(out)
	for _, bad := range []string{"kek", "dek", "password=", "passphrase=", "secret"} {
		if strings.Contains(lower, bad) {
			t.Fatalf("WARNING 含敏感信息 %q: %q", bad, out)
		}
	}

	// 已配置口令：正常装配，不输出警告。
	t.Setenv("NOVELSTACK_BACKUP_PASSPHRASE", "strong-passphrase-for-test")
	out = captureStdout(t, func() {
		if got := newAutoBackupScheduler(cfg, nil, nil); got == nil {
			t.Error("已配置口令时应创建调度器")
		}
	})
	if strings.Contains(out, "WARNING") {
		t.Fatalf("已有口令不应警告: %q", out)
	}

	// 自动备份关闭：既不警告，也不创建调度器。
	cfg.AutoBackup = false
	out = captureStdout(t, func() {
		if got := newAutoBackupScheduler(cfg, nil, nil); got != nil {
			t.Error("关闭自动备份时不应创建调度器")
		}
	})
	if out != "" {
		t.Fatalf("关闭自动备份不应输出任何内容: %q", out)
	}
}
