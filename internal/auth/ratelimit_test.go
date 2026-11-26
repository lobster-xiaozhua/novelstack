package auth

import (
	"testing"
	"time"
)

// 登录限流：容量 5，第 6 次必须拒绝。
func TestIPLimiterSixthAttemptBlocked(t *testing.T) {
	l := NewIPLimiter(5)
	ip := "10.0.0.1"
	for i := 0; i < 5; i++ {
		if !l.Allow(ip) {
			t.Fatalf("attempt %d unexpectedly blocked", i+1)
		}
	}
	if l.Allow(ip) {
		t.Fatal("6th attempt should be blocked")
	}
	// 其他 IP 不受影响。
	if !l.Allow("10.0.0.2") {
		t.Fatal("other ip should not be affected")
	}
}

// 令牌随时间恢复（用极短 refill 周期模拟，仅验证恢复逻辑路径）。
func TestIPLimiterRefill(t *testing.T) {
	l := NewIPLimiter(1)
	l.refill = 5 * time.Millisecond
	ip := "10.0.0.3"
	if !l.Allow(ip) {
		t.Fatal("first should pass")
	}
	if l.Allow(ip) {
		t.Fatal("second immediately should be blocked")
	}
	time.Sleep(8 * time.Millisecond)
	if !l.Allow(ip) {
		t.Fatal("after refill should pass")
	}
}

func TestNormalizeIP(t *testing.T) {
	if got := NormalizeIP("192.168.1.5:4433", ""); got != "192.168.1.5" {
		t.Fatalf("got %q", got)
	}
	if got := NormalizeIP("10.0.0.9:1234", "1.2.3.4, 10.0.0.9"); got != "1.2.3.4" {
		t.Fatalf("got %q", got)
	}
	if got := NormalizeIP("badaddr", ""); got != "badaddr" {
		t.Fatalf("got %q", got)
	}
}
