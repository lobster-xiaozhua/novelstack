package lan

import (
	"sync"
	"time"
)

// attemptLimiter 是短码兑换的每 IP 滑动窗口限流。
// 与 devices 的登录限流分开：短码熵低，兑换接口需要更保守的独立计数。
type attemptLimiter struct {
	mu      sync.Mutex
	perMin  int
	windows map[string]*attemptWindow
}

type attemptWindow struct {
	start time.Time
	count int
}

func newAttemptLimiter(perMinute int) *attemptLimiter {
	if perMinute <= 0 {
		perMinute = 8
	}
	return &attemptLimiter{perMin: perMinute, windows: make(map[string]*attemptWindow)}
}

// Allow 判断某 IP 本分钟内是否还能尝试。
func (l *attemptLimiter) Allow(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	w := l.windows[ip]
	if w == nil || now.Sub(w.start) >= time.Minute {
		l.windows[ip] = &attemptWindow{start: now, count: 1}
		l.gcLocked(now)
		return true
	}
	if w.count >= l.perMin {
		return false
	}
	w.count++
	return true
}

// gcLocked 清理过期窗口，避免长期运行后 map 无限增长（调用方已持锁）。
func (l *attemptLimiter) gcLocked(now time.Time) {
	if len(l.windows) < 256 {
		return
	}
	for k, w := range l.windows {
		if now.Sub(w.start) >= time.Minute {
			delete(l.windows, k)
		}
	}
}
