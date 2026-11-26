// Package auth 实现单用户认证：Argon2id 口令哈希、session 管理、登录限流。
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"golang.org/x/crypto/argon2"
)

// Argon2id 参数：在内存量受约束（<256MB）的容器上取保守值。
// m=64MiB, t=2, p=1 是 OWASP 推荐的最低组合之一。
const (
	argonMemory  uint32 = 64 * 1024 // KiB
	argonTime    uint32 = 2
	argonThreads uint8  = 1
	argonKeyLen  uint32 = 32
	argonSaltLen        = 16
)

// SessionCookieName 是 session cookie 名。
const SessionCookieName = "ns_session"

// GatewayCookieName 是书源网关会话 cookie 名（独立 Path，不参与 /api 认证）。
const GatewayCookieName = "ns_gw_session"

// 会话作用域：网关会话只对 /gateway/legado/* 有效，令牌泄露的爆炸半径因此被限制。
const (
	ScopeAPI    = "api"
	ScopeLegado = "legado"
)

// ErrInvalidCredentials 表示用户名或密码错误（对外模糊）。
var ErrInvalidCredentials = errors.New("invalid credentials")

// HashPassword 用 Argon2id 哈希口令，返回 PHC 格式编码串。
func HashPassword(password string) (string, error) {
	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("生成盐失败: %w", err)
	}
	key := argon2.IDKey([]byte(password), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, argonMemory, argonTime, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key)), nil
}

// VerifyPassword 校验口令与 PHC 格式哈希是否匹配（常数时间比较）。
func VerifyPassword(password, encoded string) (bool, error) {
	parts := strings.Split(encoded, "$")
	// "", "argon2id", "v=19", "m=..,t=..,p=..", salt, hash
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false, errors.New("哈希格式无效")
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil {
		return false, fmt.Errorf("解析版本失败: %w", err)
	}
	var m uint32
	var t uint32
	var p uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &m, &t, &p); err != nil {
		return false, fmt.Errorf("解析参数失败: %w", err)
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false, fmt.Errorf("解码盐失败: %w", err)
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return false, fmt.Errorf("解码哈希失败: %w", err)
	}
	got := argon2.IDKey([]byte(password), salt, t, m, p, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}

// Store 封装 users / sessions 表操作。
type Store struct {
	db *sql.DB
}

func NewStore(db *sql.DB) *Store { return &Store{db: db} }

// UserCount 返回用户数，用于判断是否需要初始化管理员。
func (s *Store) UserCount(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&n)
	return n, err
}

// PasswordHash 返回用户的 Argon2id 哈希；用户不存在返回空串。
func (s *Store) PasswordHash(ctx context.Context, username string) (string, error) {
	var hash string
	err := s.db.QueryRowContext(ctx, `SELECT password_hash FROM users WHERE username = ?`, username).Scan(&hash)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return hash, nil
}

// EnsureAdmin 首次启动（users 表为空）时用初始口令创建唯一用户。
// 返回是否执行了初始化。
func (s *Store) EnsureAdmin(ctx context.Context, username, initialPassword string) (bool, error) {
	n, err := s.UserCount(ctx)
	if err != nil {
		return false, err
	}
	if n > 0 {
		return false, nil
	}
	if initialPassword == "" {
		return false, errors.New("users 表为空且未设置 ADMIN_INITIAL_PASSWORD，拒绝启动")
	}
	hash, err := HashPassword(initialPassword)
	if err != nil {
		return false, err
	}
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO users(username, password_hash) VALUES (?,?)`, username, hash); err != nil {
		return false, fmt.Errorf("创建初始用户失败: %w", err)
	}
	return true, nil
}

// Session 描述一个已登录会话（不含原始 token）。
type Session struct {
	TokenHash string
	Username  string
	ExpiresAt time.Time
	Scope     string
}

// newToken 生成 32 字节随机 token 及其 SHA-256 hex。
func newToken() (token, tokenHash string, err error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", "", fmt.Errorf("生成 session token 失败: %w", err)
	}
	token = base64.RawURLEncoding.EncodeToString(b)
	sum := sha256.Sum256([]byte(token))
	tokenHash = hex.EncodeToString(sum[:])
	return token, tokenHash, nil
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// CreateSession 创建管理接口会话（scope=api）。
func (s *Store) CreateSession(ctx context.Context, username string, ttl time.Duration) (token string, exp time.Time, err error) {
	return s.CreateSessionScoped(ctx, username, ttl, ScopeAPI)
}

// CreateSessionScoped 登录成功后创建指定作用域的会话，数据库只存 token 哈希。
func (s *Store) CreateSessionScoped(ctx context.Context, username string, ttl time.Duration, scope string) (token string, exp time.Time, err error) {
	if scope == "" {
		scope = ScopeAPI
	}
	var userID int64
	err = s.db.QueryRowContext(ctx, `SELECT id FROM users WHERE username = ?`, username).Scan(&userID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", time.Time{}, ErrInvalidCredentials
	}
	if err != nil {
		return "", time.Time{}, err
	}
	token, tokenHash, err := newToken()
	if err != nil {
		return "", time.Time{}, err
	}
	exp = time.Now().UTC().Add(ttl)
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO sessions(token_hash, user_id, expires_at, scope) VALUES (?,?,?,?)`,
		tokenHash, userID, exp.Format(time.RFC3339), scope); err != nil {
		return "", time.Time{}, fmt.Errorf("写入 session 失败: %w", err)
	}
	return token, exp, nil
}

// LookupSession 按 cookie 中的原始 token 查会话（比对哈希），并校验过期时间。
// 只接受 scope=api 的会话：网关会话不能用于管理接口。
func (s *Store) LookupSession(ctx context.Context, token string) (*Session, error) {
	return s.LookupSessionScoped(ctx, token, ScopeAPI)
}

// LookupSessionScoped 按作用域查会话。
// scope 为空表示不限制（仅内部使用，避免把网关会话误当管理会话）。
func (s *Store) LookupSessionScoped(ctx context.Context, token, scope string) (*Session, error) {
	if token == "" {
		return nil, nil
	}
	var sess Session
	var expStr string
	q := `SELECT s.token_hash, u.username, s.expires_at, s.scope
	      FROM sessions s JOIN users u ON u.id = s.user_id
	      WHERE s.token_hash = ?`
	args := []any{hashToken(token)}
	if scope != "" {
		q += ` AND s.scope = ?`
		args = append(args, scope)
	}
	err := s.db.QueryRowContext(ctx, q, args...).Scan(&sess.TokenHash, &sess.Username, &expStr, &sess.Scope)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	exp, err := time.Parse(time.RFC3339, expStr)
	if err != nil {
		return nil, nil // 过期时间损坏视为无效会话
	}
	if time.Now().UTC().After(exp) {
		_ = s.DeleteSessionByHash(ctx, sess.TokenHash)
		return nil, nil
	}
	sess.ExpiresAt = exp
	return &sess, nil
}

// DeleteSession 删除当前 token 对应的会话（登出）。
func (s *Store) DeleteSession(ctx context.Context, token string) error {
	return s.DeleteSessionByHash(ctx, hashToken(token))
}

// DeleteSessionByHash 按哈希删除会话。
func (s *Store) DeleteSessionByHash(ctx context.Context, tokenHash string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE token_hash = ?`, tokenHash)
	return err
}

// SetSessionCookie 写入 HttpOnly + SameSite=Strict cookie（Secure 由配置决定）。
func SetSessionCookie(w http.ResponseWriter, token string, exp time.Time, secure bool) {
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookieName,
		Value:    token,
		Path:     "/",
		Expires:  exp,
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteStrictMode,
	})
}

// SetGatewaySessionCookie 写入书源网关会话 cookie。
// Path 限定在 /gateway/legado，管理接口永远不会收到它。
func SetGatewaySessionCookie(w http.ResponseWriter, token string, exp time.Time, secure bool) {
	http.SetCookie(w, &http.Cookie{
		Name:     GatewayCookieName,
		Value:    token,
		Path:     "/gateway/legado",
		Expires:  exp,
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteStrictMode,
	})
}

// ClearGatewaySessionCookie 清除网关会话 cookie。
func ClearGatewaySessionCookie(w http.ResponseWriter, secure bool) {
	http.SetCookie(w, &http.Cookie{
		Name:     GatewayCookieName,
		Value:    "",
		Path:     "/gateway/legado",
		Expires:  time.Unix(0, 0),
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteStrictMode,
	})
}

// ClearSessionCookie 清除 cookie。
func ClearSessionCookie(w http.ResponseWriter, secure bool) {
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookieName,
		Value:    "",
		Path:     "/",
		Expires:  time.Unix(0, 0),
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteStrictMode,
	})
}
