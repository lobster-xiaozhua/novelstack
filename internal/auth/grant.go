package auth

import (
	"crypto/rand"
	"encoding/base64"
	"sync"
	"time"
)

// GrantTTL 是 Web 登录票据的有效期。客户端拿到后应立即用 WebView 打开，
// 60 秒足够覆盖一次冷启动 + 网络握手，又能把泄露窗口压到最小。
const GrantTTL = 60 * time.Second

// 票据表上限：私人站单用户，正常并发极低；设上限防止被刷爆内存。
const grantMax = 64

type grantItem struct {
	username string
	expires  time.Time
}

// GrantStore 保存在内存里的一次性 Web 登录票据。
//
// 用途：Android 客户端只有 device token，浏览器里是 HttpOnly session cookie，
// 两者无法互相注入。客户端申请一张一次性票据，用 WebView 打开
// /admin/login?grant=...，由服务端兑换成 session cookie——token 不出现在
// URL 之外的任何地方，且关页即失效。
type GrantStore struct {
	mu    sync.Mutex
	items map[string]grantItem
	now   func() time.Time
}

// NewGrantStore 创建票据表。now 为 nil 时使用系统时钟（测试可注入）。
func NewGrantStore(now func() time.Time) *GrantStore {
	if now == nil {
		now = time.Now
	}
	return &GrantStore{items: make(map[string]grantItem, 8), now: now}
}

// Issue 为用户签发一张票据，返回票据与到期时间。
func (g *GrantStore) Issue(username string) (string, time.Time, error) {
	if g == nil {
		return "", time.Time{}, errGrantDisabled
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", time.Time{}, err
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	now := g.now().UTC()
	exp := now.Add(GrantTTL)

	g.mu.Lock()
	defer g.mu.Unlock()
	g.sweepLocked(now)
	if len(g.items) >= grantMax {
		// 已满：清掉最早到期的一张，避免直接拒绝服务。
		var oldestKey string
		var oldest time.Time
		for k, v := range g.items {
			if oldestKey == "" || v.expires.Before(oldest) {
				oldestKey, oldest = k, v.expires
			}
		}
		delete(g.items, oldestKey)
	}
	g.items[token] = grantItem{username: username, expires: exp}
	return token, exp, nil
}

// Redeem 兑换票据。成功返回用户名；票据不存在、过期或已使用都返回 false。
// 兑换即删除，保证「一次性」。
func (g *GrantStore) Redeem(token string) (string, bool) {
	if g == nil || token == "" {
		return "", false
	}
	now := g.now().UTC()
	g.mu.Lock()
	defer g.mu.Unlock()
	it, ok := g.items[token]
	if !ok {
		return "", false
	}
	delete(g.items, token)
	if !it.expires.After(now) {
		return "", false
	}
	return it.username, true
}

// Len 返回当前未兑换票据数（测试与健康检查用）。
func (g *GrantStore) Len() int {
	if g == nil {
		return 0
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.items)
}

func (g *GrantStore) sweepLocked(now time.Time) {
	for k, v := range g.items {
		if !v.expires.After(now) {
			delete(g.items, k)
		}
	}
}

type grantError string

func (e grantError) Error() string { return string(e) }

const errGrantDisabled = grantError("auth: grant store disabled")
