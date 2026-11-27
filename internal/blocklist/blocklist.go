// Package blocklist 提供 IP 级熔断保护。
//
// 统计每个来源 IP 在滑动窗口（默认 1 小时）内的 401/404 响应次数；
// 达到阈值（默认 10）后写入 SQLite 黑名单并封禁 24 小时，期间所有请求直接 403。
//
// 设计要点：
//   - 违规计数只在内存，进程重启后重新计数（黑名单本身持久化，不受影响）；
//   - 黑名单在内存里缓存一份，启动加载未过期条目，避免每请求查库；
//   - 回环地址永不封禁（否则本机自测/健康检查会互相污染）；
//   - 只统计 401/404：403（已封禁响应）、429、5xx 都不计数，避免自我放大；
//   - 中间件透传 Flusher/Unwrap，不破坏 TTS 等流式输出。
package blocklist

import (
	"context"
	"database/sql"
	"encoding/json"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Store 是黑名单存储与中间件状态。
type Store struct {
	db         *sql.DB
	enabled    bool
	window     time.Duration
	threshold  int
	banTTL     time.Duration
	maxEntries int

	mu      sync.RWMutex
	blocked map[string]time.Time   // IP -> 封禁到期时间（UTC）
	hits    map[string][]time.Time // IP -> 窗口内违规时刻（UTC，升序）
}

// New 创建 Store，并从数据库加载未过期的黑名单。
func New(db *sql.DB, enabled bool, window time.Duration, threshold int, banTTL time.Duration, maxEntries int) *Store {
	if window <= 0 {
		window = time.Hour
	}
	if threshold < 2 {
		threshold = 10
	}
	if banTTL <= 0 {
		banTTL = 24 * time.Hour
	}
	if maxEntries < 1 {
		maxEntries = 5000
	}
	s := &Store{
		db: db, enabled: enabled, window: window, threshold: threshold,
		banTTL: banTTL, maxEntries: maxEntries,
		blocked: make(map[string]time.Time),
		hits:    make(map[string][]time.Time),
	}
	if db != nil {
		_ = s.load()
	}
	return s
}

// load 从数据库加载仍然生效的封禁条目（unblocked_at IS NULL 且未过期）。
func (s *Store) load() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	rows, err := s.db.QueryContext(ctx,
		`SELECT ip, expires_at FROM ip_blocklist
		 WHERE (unblocked_at IS NULL OR unblocked_at = '') AND expires_at > ?`,
		nowUTC().Format(time.RFC3339Nano))
	if err != nil {
		return err
	}
	defer rows.Close()
	now := nowUTC()
	for rows.Next() {
		var ip, expStr string
		if err := rows.Scan(&ip, &expStr); err != nil {
			continue
		}
		exp, err := time.Parse(time.RFC3339Nano, expStr)
		if err != nil || !exp.After(now) {
			continue
		}
		s.blocked[ip] = exp
	}
	return rows.Err()
}

// Entry 是控制台展示的一条黑名单记录。
type Entry struct {
	IP          string `json:"ip"`
	Reason      string `json:"reason"`
	Hits        int    `json:"hits"`
	CreatedAt   string `json:"created_at"`
	ExpiresAt   string `json:"expires_at"`
	UnblockedAt string `json:"unblocked_at,omitempty"`
	Active      bool   `json:"active"`
}

// List 返回最近的黑名单记录（含已解封/已过期的，便于审计追溯）。
func (s *Store) List(ctx context.Context, limit int) ([]Entry, error) {
	if s.db == nil {
		return []Entry{}, nil
	}
	if limit < 1 || limit > 500 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT ip, reason, hits, created_at, expires_at, COALESCE(unblocked_at,'')
		 FROM ip_blocklist ORDER BY created_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	now := nowUTC()
	out := make([]Entry, 0)
	for rows.Next() {
		var e Entry
		var expStr string
		if err := rows.Scan(&e.IP, &e.Reason, &e.Hits, &e.CreatedAt, &expStr, &e.UnblockedAt); err != nil {
			continue
		}
		e.ExpiresAt = expStr
		exp, perr := time.Parse(time.RFC3339Nano, expStr)
		e.Active = perr == nil && exp.After(now) && e.UnblockedAt == ""
		out = append(out, e)
	}
	return out, rows.Err()
}

// IsBlocked 判断 IP 当前是否被封禁（内存缓存）。过期条目惰性清理。
func (s *Store) IsBlocked(ip string) bool {
	now := nowUTC()
	s.mu.RLock()
	exp, ok := s.blocked[ip]
	s.mu.RUnlock()
	if !ok {
		return false
	}
	if !exp.After(now) {
		s.mu.Lock()
		delete(s.blocked, ip)
		s.mu.Unlock()
		return false
	}
	return true
}

// RecordViolation 记录一次 401/404，返回窗口内的违规次数。
// 回环地址与已封禁 IP 不计入（防止自我放大）。
func (s *Store) RecordViolation(ip string) int {
	if !s.enabled || isLoopback(ip) || s.IsBlocked(ip) {
		return 0
	}
	now := nowUTC()
	cutoff := now.Add(-s.window)
	s.mu.Lock()
	defer s.mu.Unlock()
	// 防内存膨胀：来源 IP 数量达到上限时先清理过期计数，仍满则放弃本轮计数。
	if len(s.hits) >= s.maxEntries {
		s.pruneLocked(now)
		if len(s.hits) >= s.maxEntries {
			return 0
		}
	}
	old := s.hits[ip]
	kept := old[:0]
	for _, t := range old {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	kept = append(kept, now)
	s.hits[ip] = kept
	return len(kept)
}

// Ban 将 IP 写入黑名单（幂等；已封禁时延长到当前到期时间之后不缩短）。
// 返回是否实际新增封禁。
func (s *Store) Ban(ctx context.Context, ip, reason string, hits int) bool {
	now := nowUTC()
	exp := now.Add(s.banTTL)
	s.mu.Lock()
	// 黑名单条数上限：先剔过期条目，仍满则拒绝新增（保护数据库不被探测撑爆）。
	if len(s.blocked) >= s.maxEntries {
		s.pruneLocked(now)
		if len(s.blocked) >= s.maxEntries {
			s.mu.Unlock()
			return false
		}
	}
	if old, ok := s.blocked[ip]; ok && old.After(exp) {
		exp = old
	}
	s.blocked[ip] = exp
	delete(s.hits, ip)
	s.mu.Unlock()

	if s.db == nil {
		return true
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO ip_blocklist(ip, reason, hits, created_at, expires_at, unblocked_at)
		 VALUES (?,?,?,?,?,'')
		 ON CONFLICT(ip) DO UPDATE SET
		   reason=excluded.reason, hits=excluded.hits, created_at=excluded.created_at,
		   expires_at=excluded.expires_at, unblocked_at=''`,
		ip, reason, hits, now.Format(time.RFC3339Nano), exp.Format(time.RFC3339Nano))
	return err == nil
}

// pruneLocked 清理窗口外的违规时刻与已过期的封禁。调用方必须持有 s.mu 写锁。
func (s *Store) pruneLocked(now time.Time) {
	cutoff := now.Add(-s.window)
	for ip, ts := range s.hits {
		kept := ts[:0]
		for _, t := range ts {
			if t.After(cutoff) {
				kept = append(kept, t)
			}
		}
		if len(kept) == 0 {
			delete(s.hits, ip)
		} else {
			s.hits[ip] = kept
		}
	}
	for ip, exp := range s.blocked {
		if !exp.After(now) {
			delete(s.blocked, ip)
		}
	}
}

// Unblock 手动解封：同时更新数据库与内存缓存。
func (s *Store) Unblock(ctx context.Context, ip string) error {
	s.mu.Lock()
	delete(s.blocked, ip)
	delete(s.hits, ip)
	s.mu.Unlock()
	if s.db == nil {
		return nil
	}
	_, err := s.db.ExecContext(ctx,
		`UPDATE ip_blocklist SET unblocked_at=? WHERE ip=?`, nowUTC().Format(time.RFC3339Nano), ip)
	return err
}

// PurgeExpired 清理数据库里过期很久的条目（由后台调度调用，非必需）。
func (s *Store) PurgeExpired(ctx context.Context, retention time.Duration) (int64, error) {
	if s.db == nil {
		return 0, nil
	}
	cutoff := nowUTC().Add(-retention).Format(time.RFC3339Nano)
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM ip_blocklist WHERE expires_at < ? AND unblocked_at != ''`, cutoff)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// Enabled 返回中间件是否启用。
func (s *Store) Enabled() bool { return s != nil && s.enabled }

// Middleware 是 IP 熔断中间件：封禁命中直接 403；否则透传并记录 401/404。
func (s *Store) Middleware(next http.Handler) http.Handler {
	if s == nil || !s.enabled {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip := ClientIP(r)
		if s.IsBlocked(ip) {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.Header().Set("Cache-Control", "no-store")
			w.WriteHeader(http.StatusForbidden)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "ip blocked"})
			return
		}
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		if rec.status != http.StatusUnauthorized && rec.status != http.StatusNotFound {
			return
		}
		if s.RecordViolation(ip) >= s.threshold {
			_ = s.Ban(r.Context(), ip, "401/404 burst", s.threshold)
		}
	})
}

// statusRecorder 捕获响应状态码，同时透传 Flusher/Unwrap 以免破坏流式输出。
type statusRecorder struct {
	http.ResponseWriter
	status int
	wrote  bool
}

func (rw *statusRecorder) WriteHeader(code int) {
	if rw.wrote {
		return
	}
	rw.status = code
	rw.wrote = true
	rw.ResponseWriter.WriteHeader(code)
}

func (rw *statusRecorder) Write(b []byte) (int, error) {
	if !rw.wrote {
		rw.WriteHeader(http.StatusOK)
	}
	return rw.ResponseWriter.Write(b)
}

func (rw *statusRecorder) Flush() {
	if f, ok := rw.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (rw *statusRecorder) Unwrap() http.ResponseWriter { return rw.ResponseWriter }

// ClientIP 提取客户端 IP：XFF 第一跳，否则 RemoteAddr。
// 与 server.clientIP 使用同一归一化规则，避免两处判定不一致。
func ClientIP(r *http.Request) string {
	xff := strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-For"), ",")[0])
	if xff != "" {
		return xff
	}
	host, _, err := net.SplitHostPort(strings.TrimSpace(r.RemoteAddr))
	if err != nil {
		return strings.TrimSpace(r.RemoteAddr)
	}
	return host
}

// isLoopback 判断回环地址（IPv4 127/8、IPv6 ::1），这些地址永不封禁。
func isLoopback(ip string) bool {
	parsed := net.ParseIP(ip)
	if parsed == nil {
		return ip == "localhost"
	}
	return parsed.IsLoopback()
}

func nowUTC() time.Time { return time.Now().UTC() }
