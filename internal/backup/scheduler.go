// Package backup 的每日调度器。
//
// 设计约束：
//   - 不引入 cron 库：time.Ticker 每分钟巡检一次，命中配置的时分才执行；
//   - 复用 Create()，不另写加密/快照逻辑，备份包格式与第四阶段完全一致；
//   - 保留策略只删除「自动生成且命名合规」的包，其他文件一律不动；
//   - 失败不删除任何包，只写 backup_failed 审计（摘要，不含明文/口令）。
package backup

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"novelstack/internal/audit"
)

// autoNamePattern 只匹配本调度器生成的包：日期-时间-SHA256前8位.nsb。
// 手工备份（无校验和后缀）与其他文件永不进入保留删除范围。
var autoNamePattern = regexp.MustCompile(`^novelstack-(\d{8})-(\d{6})-([0-9a-f]{8})\.nsb$`)

// SchedulerOptions 是调度器依赖；Now 可注入以测试到点触发与保留策略。
type SchedulerOptions struct {
	Enabled       bool
	Hour          int
	Minute        int
	RetentionDays int
	DataDir       string
	OutDir        string
	Passphrase    []byte
	Params        KDFParams
	ChunkSize     int
	OmitTTSCache  bool
	DB            *sql.DB
	Audit         *audit.Logger
	Now           func() time.Time
	// CheckInterval 为巡检周期，默认 1 分钟（仅用于测试加速）。
	CheckInterval time.Duration
}

// Scheduler 串行执行每日备份，避免控制台手动触发与定时器重叠。
type Scheduler struct {
	opts SchedulerOptions
	mu   sync.Mutex
	// lifeMu 串行化 Start/Close：ticker 与 stop 的生命周期、以及 wg.Add/Wait 必须成对，
	// 否则并发启停会出现数据竞争或 WaitGroup 误用（见 race_test.go）。
	lifeMu     sync.Mutex
	lastRunDay string
	ticker     *time.Ticker
	stop       chan struct{}
	wg         sync.WaitGroup
}

// NewScheduler 构造调度器（尚未启动）。
func NewScheduler(opts SchedulerOptions) *Scheduler {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.RetentionDays <= 0 {
		opts.RetentionDays = 7
	}
	if opts.CheckInterval <= 0 {
		opts.CheckInterval = time.Minute
	}
	if opts.Params == (KDFParams{}) {
		opts.Params = DefaultKDFParams
	}
	if opts.ChunkSize <= 0 {
		opts.ChunkSize = 1 << 20
	}
	if opts.OutDir == "" {
		opts.OutDir = filepath.Join(opts.DataDir, "backups")
	}
	return &Scheduler{opts: opts}
}

// Start 启动后台巡检；配置关闭或缺少口令时不启动（不阻塞主程序）。
func (s *Scheduler) Start() {
	if !s.opts.Enabled || len(s.opts.Passphrase) == 0 {
		return
	}
	s.lifeMu.Lock()
	defer s.lifeMu.Unlock()
	s.mu.Lock()
	if s.stop != nil {
		s.mu.Unlock()
		return
	}
	s.stop = make(chan struct{})
	s.lastRunDay = s.lastSuccessDay()
	s.mu.Unlock()

	s.ticker = time.NewTicker(s.opts.CheckInterval)
	ticker := s.ticker // 捕获局部引用：goroutine 不再读共享字段，Close 可安全清空 s.ticker
	stop := s.stop     // 必须捕获旧 channel：Close 会把 s.stop 置 nil，直接读字段会永远等不到信号
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.tick(s.opts.Now()) // 启动时补一次：容器恰好在 03:00 之后重启也不会漏
		for {
			select {
			case <-ticker.C:
				s.tick(s.opts.Now())
			case <-stop:
				return
			}
		}
	}()
}

// Close 停止后台巡检；不会中断已经开始的备份。
func (s *Scheduler) Close() {
	s.lifeMu.Lock()
	defer s.lifeMu.Unlock()
	s.mu.Lock()
	stop := s.stop
	ticker := s.ticker
	s.stop = nil
	s.ticker = nil
	s.mu.Unlock()
	if stop == nil {
		return
	}
	close(stop)
	if ticker != nil {
		ticker.Stop()
	}
	s.wg.Wait()
}

// tick 在到点且当天尚未执行时触发一次。
func (s *Scheduler) tick(now time.Time) {
	if !s.due(now) {
		return
	}
	s.mu.Lock()
	s.lastRunDay = now.Format("20060102")
	s.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Hour)
	defer cancel()
	_, _ = s.RunOnce(ctx)
}

func (s *Scheduler) due(now time.Time) bool {
	s.mu.Lock()
	last := s.lastRunDay
	s.mu.Unlock()
	if last == now.Format("20060102") {
		return false
	}
	cur := now.Hour()*60 + now.Minute()
	return cur >= s.opts.Hour*60+s.opts.Minute
}

// lastSuccessDay 从已有成功记录恢复「当天是否跑过」，避免重启后重复备份。
func (s *Scheduler) lastSuccessDay() string {
	if s.opts.DB == nil {
		return ""
	}
	var ts string
	if err := s.opts.DB.QueryRow(`SELECT ts FROM backup_runs WHERE result='ok' ORDER BY id DESC LIMIT 1`).Scan(&ts); err != nil {
		return ""
	}
	t, err := parseRunTS(ts)
	if err != nil {
		return ""
	}
	return t.Format("20060102")
}

// RunOnce 同步执行一次自动备份（控制台「立即备份」也走这里）。
// 成功：重命名包加入校验和前缀 → 按保留策略 pruning → 写 backup_scheduled 审计。
// 失败：只写 backup_failed 审计，不动任何已有备份包。
func (s *Scheduler) RunOnce(ctx context.Context) (*CreateResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.opts.Passphrase) == 0 {
		err := fmt.Errorf("backup: 缺少备份口令")
		s.recordFailure(ctx, "passphrase_missing", err)
		return nil, err
	}
	now := s.opts.Now()
	name := "novelstack-" + now.UTC().Format("20060102-150405") + ".nsb"
	res, err := Create(ctx, CreateOptions{
		DataDir:      s.opts.DataDir,
		OutDir:       s.opts.OutDir,
		Name:         name,
		Passphrase:   s.opts.Passphrase,
		Params:       s.opts.Params,
		ChunkSize:    s.opts.ChunkSize,
		OmitTTSCache: s.opts.OmitTTSCache,
		Now:          func() time.Time { return now },
	})
	if err != nil {
		s.recordFailure(ctx, reasonOf(err), err)
		return nil, err
	}

	renamed := true
	if err := s.nameWithChecksum(res); err != nil {
		// 改名失败不影响备份包本身，保留原文件名；保留策略不会碰它。
		renamed = false
	}
	deleted, pruneErr := s.prune(now)
	detail := fmt.Sprintf("sha256=%s size_bytes=%d duration_ms=%d retained_days=%d renamed=%t deleted=%d",
		res.Checksum, res.SizeBytes, res.DurationMS, s.opts.RetentionDays, renamed, deleted)
	if pruneErr != nil {
		detail += " prune_error=1"
	}
	if s.opts.Audit != nil {
		s.opts.Audit.Record(ctx, "system", "scheduler", "backup_scheduled", "", "ok", detail)
	}
	return res, nil
}

// nameWithChecksum 把包名改成 novelstack-日期-时间-SHA256前8位.nsb，并同步 .sha256 伴生文件与数据库记录。
func (s *Scheduler) nameWithChecksum(res *CreateResult) error {
	if len(res.Checksum) < 8 || !autoNamePattern.MatchString("novelstack-20060102-150405-"+res.Checksum[:8]+".nsb") {
		return fmt.Errorf("backup: 校验和非法，跳过重命名")
	}
	dir := filepath.Dir(res.Path)
	oldBase := filepath.Base(res.Path)
	newBase := strings.TrimSuffix(oldBase, ".nsb") + "-" + res.Checksum[:8] + ".nsb"
	if err := os.Rename(res.Path, filepath.Join(dir, newBase)); err != nil {
		return err
	}
	res.Path = filepath.Join(dir, newBase)
	oldSide := oldBase + ".sha256"
	newSide := newBase + ".sha256"
	if _, err := os.Stat(filepath.Join(dir, oldSide)); err == nil {
		if err := os.Rename(filepath.Join(dir, oldSide), filepath.Join(dir, newSide)); err != nil {
			return err
		}
	}
	if s.opts.DB != nil {
		_, err := s.opts.DB.Exec(`UPDATE backup_runs SET path = ? WHERE path = ? AND result='ok'
			AND id = (SELECT MAX(id) FROM backup_runs WHERE path = ?)`,
			filepath.Base(res.Path), oldBase, oldBase)
		if err != nil {
			return err
		}
	}
	return nil
}

// prune 只删除命名合规且日期早于保留窗口的自动备份包；返回删除数量。
// 失败不向上传播为「备份失败」：包已成功生成，清理失败只写提示。
func (s *Scheduler) prune(now time.Time) (int, error) {
	entries, err := os.ReadDir(s.opts.OutDir)
	if err != nil {
		return 0, err
	}
	cutoff := time.Date(now.UTC().Year(), now.UTC().Month(), now.UTC().Day(), 0, 0, 0, 0, time.UTC).
		AddDate(0, 0, -s.opts.RetentionDays)
	type stale struct{ name, path string }
	var staleList []stale
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		m := autoNamePattern.FindStringSubmatch(e.Name())
		if m == nil {
			continue
		}
		day, err := time.Parse("20060102", m[1])
		if err != nil || !day.Before(cutoff) {
			continue
		}
		staleList = append(staleList, stale{e.Name(), filepath.Join(s.opts.OutDir, e.Name())})
	}
	sort.Slice(staleList, func(i, j int) bool { return staleList[i].name < staleList[j].name })
	var firstErr error
	deleted := 0
	for _, item := range staleList {
		if err := os.Remove(item.path); err != nil {
			firstErr = err
			continue
		}
		_ = os.Remove(item.path + ".sha256")
		deleted++
		if s.opts.Audit != nil {
			s.opts.Audit.Record(context.Background(), "system", "scheduler", "backup_pruned", "", "ok", "file="+item.name)
		}
	}
	if firstErr != nil && s.opts.Audit != nil {
		s.opts.Audit.Record(context.Background(), "system", "scheduler", "backup_pruned", "", "error", "reason=remove_failed")
	}
	return deleted, firstErr
}

func (s *Scheduler) recordFailure(ctx context.Context, reason string, err error) {
	if s.opts.Audit == nil {
		return
	}
	msg := ""
	if err != nil {
		msg = truncate(err.Error(), 160)
		msg = strings.ReplaceAll(msg, "\n", " ")
	}
	s.opts.Audit.Record(ctx, "system", "scheduler", "backup_failed", "", "error",
		"reason="+reason+" detail="+msg)
}

func reasonOf(err error) string {
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "readonly"), strings.Contains(msg, "permission"), strings.Contains(msg, "落盘"):
		return "write_failed"
	case strings.Contains(msg, "vaccum"), strings.Contains(msg, "vacuum"), strings.Contains(msg, "sqlite"), strings.Contains(msg, "数据库"):
		return "snapshot_failed"
	case strings.Contains(msg, "missing ciphertext"), strings.Contains(msg, "密文"):
		return "missing_ciphertext"
	default:
		return "create_failed"
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

func parseRunTS(ts string) (time.Time, error) {
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05.000Z", "2006-01-02T15:04:05Z"} {
		if t, err := time.Parse(layout, ts); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("backup: 无法解析备份时间")
}

// IsAutoBackupName 供运维脚本/测试判断某个文件是否属于自动备份命名规范。
func IsAutoBackupName(name string) bool { return autoNamePattern.MatchString(name) }
