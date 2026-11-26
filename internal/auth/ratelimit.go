package auth

import (
	"net"
	"sync"
	"time"
)

// ipLimiter 按 IP 的内存令牌桶，用于登录失败限流。
// 不引入 Redis：单进程内存实现，重启后清零（可接受，审计日志仍在）。
type IPLimiter struct {
	mu       sync.Mutex
	buckets  map[string]*bucket
	capacity int           // 桶容量
	refill   time.Duration // 每 time 单位补充 1 个令牌
	lastGC   time.Time
}

type bucket struct {
	tokens float64
	last   time.Time
}

// NewIPLimiter 创建限流器：每分钟 capacity 次。
func NewIPLimiter(capacity int) *IPLimiter {
	return &IPLimiter{
		buckets:  make(map[string]*bucket),
		capacity: capacity,
		refill:   time.Minute,
		lastGC:   time.Now(),
	}
}

// Allow 消耗 1 个令牌；无令牌返回 false。
func (l *IPLimiter) Allow(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	l.gcLocked(now)
	b, ok := l.buckets[ip]
	if !ok {
		l.buckets[ip] = &bucket{tokens: float64(l.capacity) - 1, last: now}
		return true
	}
	elapsed := now.Sub(b.last)
	b.tokens += elapsed.Seconds() / l.refill.Seconds()
	if b.tokens > float64(l.capacity) {
		b.tokens = float64(l.capacity)
	}
	b.last = now
	if b.tokens < 1 {
		return false
	}
	b.tokens -= 1
	return true
}

// gcLocked 清理长时间未活动的桶，避免内存增长。
func (l *IPLimiter) gcLocked(now time.Time) {
	if now.Sub(l.lastGC) < 5*time.Minute {
		return
	}
	for ip, b := range l.buckets {
		if now.Sub(b.last) > 10*time.Minute {
			delete(l.buckets, ip)
		}
	}
	l.lastGC = now
}

// NormalizeIP 从 RemoteAddr 或 X-Forwarded-For 提取客户端 IP。
// 本服务仅监听内网且只经 Caddy 反代到达，X-Forwarded-For 由 Caddy 设置，
// 因此取 XFF 的第一跳；否则回退 RemoteAddr。
func NormalizeIP(remoteAddr, xff string) string {
	if xff != "" {
		for _, part := range splitComma(xff) {
			if ip := trimHost(part); ip != "" {
				return ip
			}
		}
	}
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		return remoteAddr
	}
	return host
}

func splitComma(s string) []string {
	var out []string
	start := 0
	for i := 0; i <= len(s); i++ {
		if i == len(s) || s[i] == ',' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	return out
}

func trimHost(s string) string {
	for len(s) > 0 && (s[0] == ' ' || s[0] == '\t') {
		s = s[1:]
	}
	for len(s) > 0 && (s[len(s)-1] == ' ' || s[len(s)-1] == '\t') {
		s = s[:len(s)-1]
	}
	return s
}
