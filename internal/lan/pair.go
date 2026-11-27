// Package lan 实现第十阶段的局域网配对与发现。
//
// 安全边界：
//   - 6 位数字短码只有约 20 位熵，因此必须「绑定来源 IP + 短 TTL + 一次性 + 每 IP 限流」，
//     兑换成功后立即签发与浏览器无关的设备凭证；
//   - 数据库只存短码的 SHA-256，明文只在生成/兑换两次响应中出现；
//   - 本包绝不输出书籍正文、密钥或设备 token 的哈希以外信息。
package lan

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"
)

var (
	// ErrBadCode 表示短码不存在、过期、已使用或来源 IP 不匹配。
	ErrBadCode = errors.New("lan: invalid or expired code")
	// ErrRateLimited 表示短码兑换过于频繁。
	ErrRateLimited = errors.New("lan: rate limited")
	// ErrDisabled 表示局域网配对未启用。
	ErrDisabled = errors.New("lan: disabled")
)

// Code 是控制台生成的 6 位配对短码（给 App 输入或编码进二维码）。
type Code struct {
	Code      string `json:"code"`
	ExpiresAt string `json:"expires_at"`
	ExpiresIn int    `json:"expires_in_s"`
	// Endpoint 是 App 应连接的基础地址（由服务端按当前请求推断或配置覆盖）。
	Endpoint string `json:"endpoint"`
}

// Store 管理 lan_pair_codes 表。
type Store struct {
	db       *sql.DB
	ttl      time.Duration
	attempts *attemptLimiter
}

// NewStore 构造短码存储。ttl<=0 时用 5 分钟；perMinute<=0 时用 8。
func NewStore(db *sql.DB, ttl time.Duration, perMinute int) *Store {
	if ttl <= 0 {
		ttl = 5 * time.Minute
	}
	if perMinute <= 0 {
		perMinute = 8
	}
	return &Store{db: db, ttl: ttl, attempts: newAttemptLimiter(perMinute)}
}

// Create 为 owner 生成绑定 clientIP 的一次性短码。
// clientIP 为空时不绑定（仅用于测试/可信内网），生产应从请求推断。
func (s *Store) Create(ctx context.Context, ownerID, clientIP, endpoint string) (*Code, error) {
	ownerID = strings.TrimSpace(ownerID)
	if ownerID == "" {
		return nil, fmt.Errorf("lan: empty owner")
	}
	code, err := sixDigits()
	if err != nil {
		return nil, err
	}
	hash := hashCode(code)
	idBytes := make([]byte, 8)
	if _, err := rand.Read(idBytes); err != nil {
		return nil, err
	}
	exp := time.Now().UTC().Add(s.ttl)
	_, err = s.db.ExecContext(ctx, `INSERT INTO lan_pair_codes
		(id, owner_id, code_hash, client_ip, created_at, expires_at)
		VALUES (?,?,?,?,?,?)`,
		hex.EncodeToString(idBytes), ownerID, hash, clientIP, now(), exp.Format(time.RFC3339Nano))
	if err != nil {
		return nil, err
	}
	return &Code{
		Code: code, ExpiresAt: exp.Format(time.RFC3339Nano),
		ExpiresIn: int(s.ttl.Seconds()), Endpoint: endpoint,
	}, nil
}

// Redeem 校验并消费短码，返回所属用户。成功后短码立即失效。
// 校验顺序：限流 → 查表 → IP 绑定 → 过期 → 使用 → 原子置 used_at。
func (s *Store) Redeem(ctx context.Context, code, clientIP string) (string, error) {
	code = strings.TrimSpace(code)
	if !isSixDigits(code) {
		return "", ErrBadCode
	}
	if !s.attempts.Allow(clientIP) {
		return "", ErrRateLimited
	}
	var id, owner, expires, boundIP string
	var used sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT id, owner_id, expires_at, client_ip, used_at
		FROM lan_pair_codes WHERE code_hash = ?`, hashCode(code)).
		Scan(&id, &owner, &expires, &boundIP, &used)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrBadCode
	}
	if err != nil {
		return "", err
	}
	if used.Valid {
		return "", ErrBadCode
	}
	// IP 绑定：生成与兑换必须来自同一 IP（防止短码被截屏后异地使用）。
	if boundIP != "" && clientIP != "" && !strings.EqualFold(boundIP, clientIP) {
		return "", ErrBadCode
	}
	if t, perr := time.Parse(time.RFC3339Nano, expires); perr != nil || time.Now().After(t) {
		return "", ErrBadCode
	}
	res, err := s.db.ExecContext(ctx, `UPDATE lan_pair_codes SET used_at = ?
		WHERE id = ? AND used_at IS NULL`, now(), id)
	if err != nil {
		return "", err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return "", ErrBadCode
	}
	return owner, nil
}

// hashCode 把短码换成 SHA-256 hex（加固定域分隔，避免与其它 hash 用途碰撞）。
func hashCode(code string) string {
	sum := sha256.Sum256([]byte("novelstack-lan-code-v1:" + code))
	return hex.EncodeToString(sum[:])
}

// sixDigits 用 crypto/rand 生成均匀分布的 100000..999999。
func sixDigits() (string, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(900000))
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%06d", n.Int64()+100000), nil
}

func isSixDigits(s string) bool {
	if len(s) != 6 {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

func now() string { return time.Now().UTC().Format(time.RFC3339Nano) }
