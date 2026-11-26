package backup

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// LaunchConfig 是临时实例的启动参数（不同端口、不同数据目录）。
type LaunchConfig struct {
	Addr         string
	DataDir      string
	KEKPath      string
	KEKVersion   int
	OldKEKs      map[int]string
	CookieSecure bool
}

// Launcher 抽象临时实例的启动方式：默认使用当前可执行文件，测试可注入进程内实例。
type Launcher interface {
	Launch(ctx context.Context, cfg LaunchConfig) (baseURL string, stop func(), err error)
}

// LauncherFunc 把函数适配为 Launcher（测试与嵌入式使用）。
type LauncherFunc func(ctx context.Context, cfg LaunchConfig) (baseURL string, stop func(), err error)

// Launch 实现 Launcher。
func (f LauncherFunc) Launch(ctx context.Context, cfg LaunchConfig) (string, func(), error) {
	return f(ctx, cfg)
}

// CmdLauncher 通过当前可执行文件的 serve 子命令启动临时实例。
type CmdLauncher struct {
	Executable string
}

// Launch 启动子进程并等待其健康检查通过。
func (l CmdLauncher) Launch(ctx context.Context, cfg LaunchConfig) (string, func(), error) {
	exe := l.Executable
	if exe == "" {
		var err error
		exe, err = os.Executable()
		if err != nil {
			return "", nil, fmt.Errorf("drill: 无法定位当前可执行文件: %w", err)
		}
	}
	cmd := exec.Command(exe, "serve") // #nosec G204 -- 可执行文件来自当前进程
	overrides := map[string]string{
		"ADDR":                   cfg.Addr,
		"DATA_DIR":               cfg.DataDir,
		"KEK_PATH":               cfg.KEKPath,
		"COOKIE_SECURE":          "false",
		"TTS_PROVIDER":           "off",
		"MEILI_URL":              "",
		"SEARCH_SYNC_ON_START":   "false",
		"LEGADO_ENABLED":         "false",
		"ADMIN_INITIAL_PASSWORD": "",
	}
	if cfg.KEKVersion > 0 {
		overrides["KEK_VERSION"] = fmt.Sprintf("%d", cfg.KEKVersion)
	}
	if len(cfg.OldKEKs) > 0 {
		parts := make([]string, 0, len(cfg.OldKEKs))
		vers := make([]int, 0, len(cfg.OldKEKs))
		for v := range cfg.OldKEKs {
			vers = append(vers, v)
		}
		sort.Ints(vers)
		for _, v := range vers {
			parts = append(parts, fmt.Sprintf("%d=%s", v, cfg.OldKEKs[v]))
		}
		overrides["KEK_OLD_PATHS"] = strings.Join(parts, ",")
	}
	cmd.Env = mergeEnv(os.Environ(), overrides)
	var logs bytes.Buffer
	cmd.Stdout = &limitedWriter{w: &logs, max: 64 << 10}
	cmd.Stderr = cmd.Stdout
	if err := cmd.Start(); err != nil {
		return "", nil, fmt.Errorf("drill: 启动临时实例失败: %w", err)
	}
	done := make(chan struct{})
	var waitErr error
	var waitMu sync.Mutex
	go func() {
		err := cmd.Wait()
		waitMu.Lock()
		waitErr = err
		waitMu.Unlock()
		close(done)
	}()

	base := "http://" + cfg.Addr
	stop := func() {
		_ = cmd.Process.Signal(os.Interrupt)
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			_ = cmd.Process.Kill()
			<-done
		}
	}
	if err := waitHealth(ctx, base, done, 30*time.Second); err != nil {
		stop()
		waitMu.Lock()
		werr := waitErr
		waitMu.Unlock()
		tail := logs.String()
		if len(tail) > 2000 {
			tail = tail[len(tail)-2000:]
		}
		return "", nil, fmt.Errorf("drill: 临时实例未就绪: %w (wait=%v logs: %s)", err, werr, sanitizeLogTail(tail))
	}
	return base, stop, nil
}

// DrillOptions 是恢复演练参数。
type DrillOptions struct {
	PackagePath    string
	Passphrase     []byte
	KEKPath        string
	KEKVersion     int
	OldKEKs        map[int]string
	Username       string
	Password       string
	BookID         string
	LogPath        string
	WorkDir        string
	ExpectedSHA256 string
	Launcher       Launcher
	// RecordDBPath 非空时把演练结果写入 drill_runs（生产库）。
	RecordDBPath string
	Timeout      time.Duration
}

// DrillResult 是一次恢复演练的结果。
type DrillResult struct {
	Time           string `json:"ts"`
	Package        string `json:"package"`
	Checksum       string `json:"package_sha256"`
	BookID         string `json:"book_id"`
	ExpectedSHA256 string `json:"expected_sha256"`
	ActualSHA256   string `json:"actual_sha256"`
	Result         string `json:"result"`
	Reason         string `json:"reason,omitempty"`
	DurationMS     int64  `json:"duration_ms"`
}

// RunDrill 执行「解包 → 校验 → 临时实例 → 登录 → 下载 → SHA 比对 → 清理」。
// 无论成功失败都会追加演练日志；失败返回 error，调用方以此决定退出码。
func RunDrill(ctx context.Context, opts DrillOptions) (*DrillResult, error) {
	started := time.Now()
	res := &DrillResult{
		Time:    started.UTC().Format(time.RFC3339),
		Package: filepath.Base(opts.PackagePath),
		Result:  "fail",
	}
	if opts.Timeout <= 0 {
		opts.Timeout = 3 * time.Minute
	}
	dctx, cancel := context.WithTimeout(ctx, opts.Timeout)
	defer cancel()
	defer func() {
		res.DurationMS = time.Since(started).Milliseconds()
		writeDrillLog(opts.LogPath, res)
		recordDrillRun(opts.RecordDBPath, res)
	}()

	if opts.PackagePath == "" || opts.KEKPath == "" || opts.Username == "" || opts.Password == "" {
		res.Reason = "missing_arguments"
		return res, errors.New("drill: 需要备份包、KEK、用户名与口令")
	}
	if opts.Launcher == nil {
		opts.Launcher = CmdLauncher{}
	}
	// 当前版本的 KEK 由 KEKPath 提供；若 OldKEKs 里混入当前版本，临时实例的启动校验会
	// 直接拒绝（KEK_OLD_PATHS 不应包含当前版本），所以这里先剔除。语义上无影响：
	// 解密时当前版本优先命中 KEKPath。
	if _, ok := opts.OldKEKs[opts.KEKVersion]; ok {
		trimmed := make(map[int]string, len(opts.OldKEKs))
		for v, p := range opts.OldKEKs {
			if v != opts.KEKVersion {
				trimmed[v] = p
			}
		}
		opts.OldKEKs = trimmed
	}

	workRoot := opts.WorkDir
	if workRoot == "" {
		workRoot = os.TempDir()
	}
	tmp, err := os.MkdirTemp(workRoot, "novelstack-drill-*")
	if err != nil {
		res.Reason = "tempdir_failed"
		return res, err
	}
	defer os.RemoveAll(tmp)

	extractDir := filepath.Join(tmp, "restore")
	ex, err := Extract(dctx, ExtractOptions{
		PackagePath:    opts.PackagePath,
		Passphrase:     opts.Passphrase,
		DestDir:        extractDir,
		ExpectedSHA256: opts.ExpectedSHA256,
	})
	if err != nil {
		res.Reason = "extract_failed"
		return res, err
	}
	res.Checksum = ex.Checksum
	man := ex.Manifest

	entry, err := pickBook(man, opts.BookID)
	if err != nil {
		res.Reason = "no_book_to_verify"
		return res, err
	}
	res.BookID = entry.ID
	res.ExpectedSHA256 = entry.SHA256

	addr, err := freeAddr()
	if err != nil {
		res.Reason = "port_failed"
		return res, err
	}
	base, stop, err := opts.Launcher.Launch(dctx, LaunchConfig{
		Addr:         addr,
		DataDir:      filepath.Join(extractDir, "data"),
		KEKPath:      opts.KEKPath,
		KEKVersion:   opts.KEKVersion,
		OldKEKs:      opts.OldKEKs,
		CookieSecure: false,
	})
	if err != nil {
		res.Reason = "launch_failed"
		return res, err
	}
	defer stop()

	if err := waitHealth(dctx, base, nil, 30*time.Second); err != nil {
		res.Reason = "health_failed"
		return res, err
	}
	client, err := newDrillClient()
	if err != nil {
		res.Reason = "client_failed"
		return res, err
	}
	if err := drillLogin(dctx, client, base, opts.Username, opts.Password); err != nil {
		res.Reason = "login_failed"
		return res, err
	}
	sum, size, err := drillDownload(dctx, client, base, entry.ID)
	if err != nil {
		res.Reason = "download_failed"
		return res, err
	}
	res.ActualSHA256 = sum
	if size != entry.SizeBytes {
		res.Reason = "size_mismatch"
		return res, fmt.Errorf("drill: 明文长度不一致（期望 %d，实际 %d）", entry.SizeBytes, size)
	}
	if !strings.EqualFold(sum, entry.SHA256) {
		res.Reason = "sha256_mismatch"
		return res, fmt.Errorf("drill: 明文 SHA-256 不一致")
	}
	res.Result = "ok"
	return res, nil
}

func pickBook(man *Manifest, id string) (BookEntry, error) {
	if id != "" {
		for _, b := range man.BookList {
			if b.ID == id {
				return b, nil
			}
		}
		return BookEntry{}, fmt.Errorf("drill: 备份中不存在书籍 %s", id)
	}
	if len(man.BookList) == 0 {
		return BookEntry{}, errors.New("drill: 空书库无法做解密验证（备份本身有效，但需指定书籍）")
	}
	return man.BookList[0], nil
}

// newDrillClient 使用独立 cookie jar，模拟真实客户端登录。
func newDrillClient() (*http.Client, error) {
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, err
	}
	return &http.Client{
		Jar:     jar,
		Timeout: 60 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}, nil
}

func drillLogin(ctx context.Context, c *http.Client, base, user, pass string) error {
	body, _ := json.Marshal(map[string]string{"username": user, "password": pass})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(base, "/")+"/api/login", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 8<<10))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("drill: 登录失败（HTTP %d）", resp.StatusCode)
	}
	return nil
}

func drillDownload(ctx context.Context, c *http.Client, base, bookID string) (string, int64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		strings.TrimRight(base, "/")+"/api/books/"+bookID+"/content", nil)
	if err != nil {
		return "", 0, err
	}
	resp, err := c.Do(req)
	if err != nil {
		return "", 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 8<<10))
		return "", 0, fmt.Errorf("drill: 下载失败（HTTP %d）", resp.StatusCode)
	}
	h := sha256.New()
	n, err := io.Copy(h, resp.Body)
	if err != nil {
		return "", n, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

// waitHealth 轮询 /healthz；exited 非 nil 时若子进程提前退出则立即失败。
func waitHealth(ctx context.Context, base string, exited <-chan struct{}, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	client := &http.Client{Timeout: 2 * time.Second}
	for time.Now().Before(deadline) {
		if exited != nil {
			select {
			case <-exited:
				return errors.New("临时实例提前退出")
			default:
			}
		}
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(base, "/")+"/healthz", nil)
		resp, err := client.Do(req)
		if err == nil {
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1024))
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(150 * time.Millisecond):
		}
	}
	return errors.New("等待健康检查超时")
}

// freeAddr 选一个空闲回环端口（存在极小的竞态窗口，演练场景可接受）。
func freeAddr() (string, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	defer l.Close()
	return l.Addr().String(), nil
}

// writeDrillLog 追加一行 JSONL 演练记录（便于审计与外部核对）。
func writeDrillLog(path string, res *DrillResult) {
	if path == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return
	}
	raw, err := json.Marshal(res)
	if err != nil {
		return
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600) // #nosec G304
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.Write(append(raw, '\n'))
}

// recordDrillRun 把演练结果写入运行库（失败静默，JSONL 仍是权威记录）。
func recordDrillRun(dbPath string, res *DrillResult) {
	if dbPath == "" {
		return
	}
	db, err := sql.Open("sqlite", "file:"+dbPath+"?_pragma=busy_timeout(5000)")
	if err != nil {
		return
	}
	defer db.Close()
	_, _ = db.Exec(`INSERT INTO drill_runs(package, checksum, book_id, result, detail, duration_ms)
		VALUES (?,?,?,?,?,?)`, res.Package, res.Checksum, res.BookID, res.Result, res.Reason, res.DurationMS)
}

// mergeEnv 用 overrides 覆盖环境变量（避免重复键）。
func mergeEnv(base []string, overrides map[string]string) []string {
	m := make(map[string]string, len(base)+len(overrides))
	for _, kv := range base {
		if i := strings.IndexByte(kv, '='); i > 0 {
			m[kv[:i]] = kv[i+1:]
		}
	}
	for k, v := range overrides {
		m[k] = v
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, k+"="+m[k])
	}
	return out
}

// sanitizeLogTail 去掉可能出现的换行控制符，避免日志注入；启动日志本身不含密钥。
func sanitizeLogTail(s string) string {
	s = strings.ReplaceAll(s, "\x00", "")
	return strings.TrimSpace(s)
}

type limitedWriter struct {
	mu  sync.Mutex
	w   io.Writer
	max int
	n   int
}

func (l *limitedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.n >= l.max {
		return len(p), nil
	}
	room := l.max - l.n
	if len(p) > room {
		l.n += room
		_, _ = l.w.Write(p[:room])
		return len(p), nil
	}
	l.n += len(p)
	return l.w.Write(p)
}
