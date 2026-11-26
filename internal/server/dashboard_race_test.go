package server_test

import (
	"io"
	"net/http"
	"sync"
	"testing"
)

// TestConcurrentDashboardPollingUnderRace 在 -race 下验证控制台轮询 handler。
// 控制台每 15s 轮询一次 /api/admin/dashboard；这里并发多次请求，覆盖聚合层的共享状态。
func TestConcurrentDashboardPollingUnderRace(t *testing.T) {
	s := sharedFullStack(t)
	client := loginOK(t, s)
	const workers = 12
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp, err := client.Get(s.ts.URL + "/api/admin/dashboard")
			if err != nil {
				t.Errorf("dashboard: %v", err)
				return
			}
			defer resp.Body.Close()
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
			if resp.StatusCode != http.StatusOK {
				t.Errorf("dashboard status = %d", resp.StatusCode)
			}
		}()
	}
	wg.Wait()
}
