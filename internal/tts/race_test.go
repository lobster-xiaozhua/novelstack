package tts

import (
	"fmt"
	"sync"
	"testing"
)

// TestConcurrentKeyRefsUnderRace 在 -race 下验证朗读引用计数的共享 map。
// acquireKeys/activeKeys/release 会被流式朗读与缓存淘汰并发调用。
func TestConcurrentKeyRefsUnderRace(t *testing.T) {
	svc := &Service{}
	const workers = 24
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			key := fmt.Sprintf("key-%02d", i%4)
			release := svc.acquireKeys([]seg{{key: key}})
			_ = svc.activeKeys()
			release()
		}(i)
	}
	wg.Wait()
	if left := svc.activeKeys(); len(left) != 0 {
		t.Fatalf("所有引用释放后仍有活跃键: %v", left)
	}
}
