package backup

import (
	"sync"
	"testing"
	"time"
)

// TestSchedulerConcurrentLifecycleUnderRace 在 -race 下验证 Start/Close 生命周期。
// 控制台手动备份与每日 ticker 可能在同一进程内并发触发，这里反复并发启动/停止调度器。
func TestSchedulerConcurrentLifecycleUnderRace(t *testing.T) {
	now := time.Date(2026, 3, 10, 3, 0, 0, 0, time.UTC)
	s, _, _ := schedulerFixture(t, now)
	const rounds = 8
	var wg sync.WaitGroup
	for i := 0; i < rounds; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.Start()
			s.Close()
		}()
	}
	wg.Wait()
	// 生命周期结束后再完整走一轮，确认状态未损坏。
	s.Start()
	s.Close()
}
