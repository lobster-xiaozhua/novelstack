package ttscache

import (
	"bytes"
	"fmt"
	"sync"
	"testing"
)

// TestStoreConcurrentAccessUnderRace 在 -race 下验证缓存 Put/Get/Verify/Stats 并发访问。
// 每个 goroutine 使用独立 key，最后逐一回读校验内容未被并发写破坏。
func TestStoreConcurrentAccessUnderRace(t *testing.T) {
	st, _ := newStore(t)
	const workers = 16
	payload := []byte("RIFF-test-concurrent-audio-payload")
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			key := CacheKeyForTest(fmt.Sprintf("race-key-%02d", i))
			if err := st.Put(key, payload); err != nil {
				t.Errorf("Put(%s): %v", key, err)
				return
			}
			got, err := st.Get(key)
			if err != nil || !bytes.Equal(got, payload) {
				t.Errorf("Get(%s): len=%d err=%v", key, len(got), err)
			}
			if err := st.Verify(key); err != nil {
				t.Errorf("Verify(%s): %v", key, err)
			}
			if _, _, err := st.Stats(); err != nil {
				t.Errorf("Stats: %v", err)
			}
		}(i)
	}
	wg.Wait()
	if segments, _, err := st.Stats(); err != nil || segments != workers {
		t.Fatalf("stats = %d err=%v, want %d", segments, err, workers)
	}
}
