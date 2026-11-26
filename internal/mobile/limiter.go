package mobile

import (
	"sync"
	"time"
)

// Limiter 是「按设备」的令牌桶，用于限制章节预取频率，
// 防止把预取接口当成整本下载通道。
//
// 选择内存实现而非数据库：限流是短时保护，进程重启后重新计数可接受；
// 单用户单进程，无需分布式协调。
type Limiter struct {
	mu      sync.Mutex
	rate    float64 // 每秒补充的令牌数
	burst   float64
	buckets map[string]*tokenBucket
	lastGC  time.Time
	now     func() time.Time // 测试可注入
}

type tokenBucket struct {
	tokens float64
	last   time.Time
}

// NewLimiter 创建限流器：perMinute 为每分钟允许的请求数，burst 为允许的突发量。
func NewLimiter(perMinute, burst int) *Limiter {
	if perMinute <= 0 {
		perMinute = 30
	}
	if burst <= 0 {
		burst = perMinute
	}
	return &Limiter{
		rate:    float64(perMinute) / 60,
		burst:   float64(burst),
		buckets: make(map[string]*tokenBucket),
		lastGC:  time.Now(),
		now:     time.Now,
	}
}

// Allow 判断 key 当前是否可放行；不允许时返回建议的重试等待时间。
func (l *Limiter) Allow(key string) (bool, time.Duration) {
	if l == nil {
		return true, 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	l.gcLocked(now)
	b, ok := l.buckets[key]
	if !ok {
		b = &tokenBucket{tokens: l.burst, last: now}
		l.buckets[key] = b
	}
	// 补充令牌（不超过桶容量）。
	elapsed := now.Sub(b.last).Seconds()
	if elapsed > 0 {
		b.tokens += elapsed * l.rate
		if b.tokens > l.burst {
			b.tokens = l.burst
		}
		b.last = now
	}
	if b.tokens >= 1 {
		b.tokens--
		return true, 0
	}
	need := (1 - b.tokens) / l.rate // 秒
	wait := time.Duration(need * float64(time.Second))
	if wait < time.Second {
		wait = time.Second
	}
	return false, wait
}

// gcLocked 清理 10 分钟未使用的桶，避免设备长期累积。
func (l *Limiter) gcLocked(now time.Time) {
	if now.Sub(l.lastGC) < 10*time.Minute {
		return
	}
	for k, b := range l.buckets {
		if now.Sub(b.last) > 10*time.Minute {
			delete(l.buckets, k)
		}
	}
	l.lastGC = now
}
