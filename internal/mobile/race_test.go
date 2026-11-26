package mobile

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestLimiterConcurrentAllow 在 -race 下暴露令牌桶共享状态的竞争。
// 注入固定时钟：burst=8，8 个并发请求应恰好放行 8 次，且 buckets/lastGC 的读写安全。
func TestLimiterConcurrentAllow(t *testing.T) {
	l := NewLimiter(60, 8)
	now := time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)
	l.now = func() time.Time { return now }

	const workers = 32
	var allowed atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			key := []string{"phone-a", "phone-b"}[i%2]
			if ok, _ := l.Allow(key); ok {
				allowed.Add(1)
			}
		}(i)
	}
	wg.Wait()
	if got := allowed.Load(); got != 16 {
		t.Fatalf("allowed = %d, want 16 (两个 key 各 burst 8)", got)
	}
}
