package telemetry

import (
	"context"
	"log"
	"sync"
	"time"
)

// Scheduler 每小时聚合一次 P50/P95，每天清理一次 90 天前的明细。
//
// 设计取舍：
//   - 用「重算上一小时」而不是「增量累加」，所以重复执行天然幂等；
//   - 每次运行回补最近 3 小时，进程重启/休眠导致的漏跑会被自动补上；
//   - 清理按「日期」去重，避免每 10 分钟扫一遍大表。
type Scheduler struct {
	store     *Store
	retention time.Duration
	interval  time.Duration

	mu      sync.Mutex
	lastDay string
	now     func() time.Time
	logf    func(string, ...any)

	stop chan struct{}
	wg   sync.WaitGroup
}

// SchedulerOptions 便于测试注入时间与日志。
type SchedulerOptions struct {
	Store     *Store
	Retention time.Duration // 默认 90 天
	Interval  time.Duration // 默认 10 分钟
	Now       func() time.Time
	Logf      func(string, ...any)
}

func NewScheduler(o SchedulerOptions) *Scheduler {
	if o.Retention <= 0 {
		o.Retention = 90 * 24 * time.Hour
	}
	if o.Interval <= 0 {
		o.Interval = 10 * time.Minute
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Logf == nil {
		o.Logf = log.Printf
	}
	return &Scheduler{
		store: o.Store, retention: o.Retention, interval: o.Interval,
		now: o.Now, logf: o.Logf, stop: make(chan struct{}),
	}
}

// Start 启动后台循环（只应在 serve 命令中调用一次）。
func (s *Scheduler) Start() {
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		// 启动即跑一次：把停机期间的小时补上。
		if _, err := s.RunOnce(context.Background(), s.now()); err != nil {
			s.logf("telemetry: 启动聚合失败: %v", err)
		}
		t := time.NewTicker(s.interval)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				if _, err := s.RunOnce(context.Background(), s.now()); err != nil {
					s.logf("telemetry: 聚合失败: %v", err)
				}
			case <-s.stop:
				return
			}
		}
	}()
}

func (s *Scheduler) Close() {
	close(s.stop)
	s.wg.Wait()
}

// Result 记录一次运行的产出，便于测试断言与日志观测。
type Result struct {
	Hours   int   // 聚合了几个小时桶
	Rollups int   // 写入/更新的聚合行数
	Deleted int64 // 清理的明细行数
}

// RunOnce 执行一次：回补最近 3 小时聚合 + 每天一次过期清理。
func (s *Scheduler) RunOnce(ctx context.Context, now time.Time) (Result, error) {
	var res Result
	if err := EnsureStore(s.store); err != nil {
		return res, err
	}
	base := now.UTC().Truncate(time.Hour)
	for i := 1; i <= 3; i++ {
		n, err := s.store.AggregateHour(ctx, base.Add(-time.Duration(i)*time.Hour))
		if err != nil {
			return res, err
		}
		res.Hours++
		res.Rollups += n
	}

	day := now.UTC().Format("2006-01-02")
	s.mu.Lock()
	needCleanup := s.lastDay != day
	s.mu.Unlock()
	if needCleanup {
		n, err := s.store.Cleanup(ctx, now.Add(-s.retention))
		if err != nil {
			return res, err
		}
		s.mu.Lock()
		s.lastDay = day
		s.mu.Unlock()
		res.Deleted = n
		if n > 0 {
			s.logf("telemetry: 清理 %d 条超过 %d 天的明细", n, int(s.retention.Hours()/24))
		}
	}
	return res, nil
}
