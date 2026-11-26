// Package devices 管理移动端长期凭证（device token）与一次性配对码。
//
// 与浏览器 session 的关键区别：
//   - token 有效期长（默认 180 天，可单独撤销），不依赖 Cookie；
//   - 每个设备可携带一把 RSA 公钥，服务端用它包装书籍 DEK——
//     服务端只搬运「被设备公钥加密的 DEK」，从不输出明文密钥；
//   - device token 只用于 /api/* 的阅读接口，绝不能访问 /admin 或 /api/admin/*。
//
// 数据库只存 token 的 SHA-256，泄露库文件也无法反推凭证。
package devices

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strings"
	"time"
)

// TokenPrefix 是 device token 的固定前缀，便于日志脱敏与误用识别。
const TokenPrefix = "nsd_"

// PairingPrefix 是配对码前缀。
const PairingPrefix = "nsp_"

// Errors 是设备凭证相关错误（handler 据此映射 HTTP 状态）。
var (
	ErrNotFound  = errors.New("devices: not found")
	ErrRevoked   = errors.New("devices: revoked")
	ErrExpired   = errors.New("devices: expired")
	ErrBadToken  = errors.New("devices: bad token")
	ErrBadPair   = errors.New("devices: bad pairing code")
	ErrPairUsed  = errors.New("devices: pairing code already used")
	ErrNoOwner   = errors.New("devices: owner mismatch")
	ErrBadPubKey = errors.New("devices: invalid public key")
)

// Device 是设备令牌的元数据（不含 token 本身）。
type Device struct {
	ID         string  `json:"id"`
	Name       string  `json:"name"`
	Prefix     string  `json:"token_prefix"`
	PublicKey  string  `json:"-"` // PEM，不返回给管理接口
	HasKey     bool    `json:"has_public_key"`
	CreatedAt  string  `json:"created_at"`
	LastUsedAt *string `json:"last_used_at,omitempty"`
	RevokedAt  *string `json:"revoked_at,omitempty"`
	ExpiresAt  *string `json:"expires_at,omitempty"`
}

// Store 封装 device_tokens / pairing_codes 表。
type Store struct {
	db *sql.DB
}

// NewStore 构造设备存储。
func NewStore(db *sql.DB) *Store { return &Store{db: db} }

// hashToken 返回 token 的 SHA-256 hex（与 session 相同的策略）。
func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// randomToken 生成带前缀的随机凭证；返回值只在此刻出现一次。
func randomToken(prefix string, n int) (token, hash, short string, err error) {
	b := make([]byte, n)
	if _, err = rand.Read(b); err != nil {
		return "", "", "", err
	}
	token = prefix + base64.RawURLEncoding.EncodeToString(b)
	hash = hashToken(token)
	short = token
	if len(short) > 12 {
		short = short[:12]
	}
	return token, hash, short, nil
}

// Create 为某用户签发 device token。
// publicKeyPEM 可为空（纯 API 客户端不参与密文解密），但移动端必须提供。
// 返回的 token 只在此处出现一次，调用方不得写日志。
func (s *Store) Create(ctx context.Context, ownerID, name, publicKeyPEM string, ttl time.Duration) (string, *Device, error) {
	ownerID = strings.TrimSpace(ownerID)
	if ownerID == "" {
		return "", nil, ErrNoOwner
	}
	name = strings.TrimSpace(name)
	if name == "" {
		name = "未命名设备"
	}
	if len([]rune(name)) > 64 {
		name = string([]rune(name)[:64])
	}
	if publicKeyPEM != "" {
		if _, err := ParseRSAPublicKey(publicKeyPEM); err != nil {
			return "", nil, err
		}
	}
	token, hash, short, err := randomToken(TokenPrefix, 32)
	if err != nil {
		return "", nil, err
	}
	idBytes := make([]byte, 8)
	if _, err := rand.Read(idBytes); err != nil {
		return "", nil, err
	}
	id := hex.EncodeToString(idBytes)

	var expires any
	if ttl > 0 {
		expires = time.Now().UTC().Add(ttl).Format(time.RFC3339Nano)
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO device_tokens
		(id, owner_id, name, token_hash, token_prefix, public_key_pem, created_at, expires_at)
		VALUES (?,?,?,?,?,?,?,?)`,
		id, ownerID, name, hash, short, publicKeyPEM, now(), expires)
	if err != nil {
		return "", nil, err
	}
	d := &Device{ID: id, Name: name, Prefix: short, PublicKey: publicKeyPEM, HasKey: publicKeyPEM != "",
		CreatedAt: now()}
	if ttl > 0 {
		v := time.Now().UTC().Add(ttl).Format(time.RFC3339Nano)
		d.ExpiresAt = &v
	}
	return token, d, nil
}

// Authenticate 校验 Bearer token，返回设备与所属用户。
// 每次成功校验会更新 last_used_at，但只在距上次超过 60 秒时写库（降低写放大）。
func (s *Store) Authenticate(ctx context.Context, token string) (*Device, string, error) {
	token = strings.TrimSpace(token)
	if !strings.HasPrefix(token, TokenPrefix) || len(token) < 20 {
		return nil, "", ErrBadToken
	}
	hash := hashToken(token)
	var d Device
	var pub string
	var revoked, expires, lastUsed sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT id, name, token_prefix, public_key_pem,
		created_at, last_used_at, revoked_at, expires_at
		FROM device_tokens WHERE token_hash = ?`, hash).
		Scan(&d.ID, &d.Name, &d.Prefix, &pub, &d.CreatedAt, &lastUsed, &revoked, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, "", ErrBadToken
	}
	if err != nil {
		return nil, "", err
	}
	if revoked.Valid {
		return nil, "", ErrRevoked
	}
	if expires.Valid && expires.String != "" {
		if t, perr := time.Parse(time.RFC3339Nano, expires.String); perr == nil && time.Now().After(t) {
			return nil, "", ErrExpired
		}
	}
	d.PublicKey = pub
	d.HasKey = pub != ""
	if lastUsed.Valid {
		v := lastUsed.String
		d.LastUsedAt = &v
	}
	if expires.Valid {
		v := expires.String
		d.ExpiresAt = &v
	}

	// 节流写入：60 秒内重复请求不更新。
	needTouch := true
	if lastUsed.Valid {
		if t, perr := time.Parse(time.RFC3339Nano, lastUsed.String); perr == nil && time.Since(t) < time.Minute {
			needTouch = false
		}
	}
	if needTouch {
		_, _ = s.db.ExecContext(ctx, `UPDATE device_tokens SET last_used_at = ? WHERE id = ?`, now(), d.ID)
	}
	return &d, ownerOf(ctx, s.db, d.ID), nil
}

// ownerOf 单独查询 owner_id（避免把 owner 暴露在 Device JSON 中）。
func ownerOf(ctx context.Context, db *sql.DB, id string) string {
	var owner string
	_ = db.QueryRowContext(ctx, `SELECT owner_id FROM device_tokens WHERE id = ?`, id).Scan(&owner)
	return owner
}

// List 返回某用户的全部设备（含已撤销，便于管理界面判断）。
func (s *Store) List(ctx context.Context, ownerID string) ([]Device, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, token_prefix, public_key_pem,
		created_at, last_used_at, revoked_at, expires_at
		FROM device_tokens WHERE owner_id = ? ORDER BY created_at DESC`, ownerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Device{}
	for rows.Next() {
		var d Device
		var pub string
		var lastUsed, revoked, expires sql.NullString
		if err := rows.Scan(&d.ID, &d.Name, &d.Prefix, &pub, &d.CreatedAt, &lastUsed, &revoked, &expires); err != nil {
			return nil, err
		}
		d.PublicKey = pub
		d.HasKey = pub != ""
		if lastUsed.Valid {
			v := lastUsed.String
			d.LastUsedAt = &v
		}
		if revoked.Valid {
			v := revoked.String
			d.RevokedAt = &v
		}
		if expires.Valid {
			v := expires.String
			d.ExpiresAt = &v
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// Revoke 撤销某用户的设备；返回是否命中（已撤销视为命中，幂等）。
func (s *Store) Revoke(ctx context.Context, ownerID, id string) (bool, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE device_tokens SET revoked_at = ?
		WHERE id = ? AND owner_id = ? AND revoked_at IS NULL`, now(), id, ownerID)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	if n > 0 {
		return true, nil
	}
	// 已经撤销过也算成功（幂等），但不存在于该用户则返回 false。
	var one int
	err = s.db.QueryRowContext(ctx, `SELECT 1 FROM device_tokens WHERE id = ? AND owner_id = ?`,
		id, ownerID).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// Count 返回某用户未撤销的设备数（dashboard 用）。
func (s *Store) Count(ctx context.Context, ownerID string) (active, revoked int, err error) {
	err = s.db.QueryRowContext(ctx, `SELECT
		COALESCE(SUM(CASE WHEN revoked_at IS NULL THEN 1 ELSE 0 END), 0),
		COALESCE(SUM(CASE WHEN revoked_at IS NOT NULL THEN 1 ELSE 0 END), 0)
		FROM device_tokens WHERE owner_id = ?`, ownerID).Scan(&active, &revoked)
	return active, revoked, err
}

// ---- 配对码 ----

// PairingCode 是一次性配对码（10 分钟有效）。
type PairingCode struct {
	Code      string `json:"code"`
	ExpiresAt string `json:"expires_at"`
	ExpiresIn int    `json:"expires_in_s"`
}

// CreatePairing 生成一次性配对码。明文只在本次响应出现，库里只存哈希。
func (s *Store) CreatePairing(ctx context.Context, ownerID string, ttl time.Duration) (*PairingCode, error) {
	if ttl <= 0 {
		ttl = 10 * time.Minute
	}
	// 8 字节 → 11 字符 base32，人工输入足够短，熵仍达 64 位。
	code, hash, _, err := randomToken(PairingPrefix, 8)
	if err != nil {
		return nil, err
	}
	idBytes := make([]byte, 8)
	if _, err := rand.Read(idBytes); err != nil {
		return nil, err
	}
	exp := time.Now().UTC().Add(ttl)
	_, err = s.db.ExecContext(ctx, `INSERT INTO pairing_codes
		(id, owner_id, code_hash, created_at, expires_at) VALUES (?,?,?,?,?)`,
		hex.EncodeToString(idBytes), ownerID, hash, now(), exp.Format(time.RFC3339Nano))
	if err != nil {
		return nil, err
	}
	return &PairingCode{Code: code, ExpiresAt: exp.Format(time.RFC3339Nano), ExpiresIn: int(ttl.Seconds())}, nil
}

// RedeemPairing 消费配对码并返回所属用户。一次性：成功后立即标记 used_at。
func (s *Store) RedeemPairing(ctx context.Context, code string) (string, error) {
	code = strings.TrimSpace(code)
	if !strings.HasPrefix(code, PairingPrefix) {
		return "", ErrBadPair
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback() //nolint:errcheck // 提交后回滚是空操作

	var id, owner, expires string
	var used sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT id, owner_id, expires_at, used_at
		FROM pairing_codes WHERE code_hash = ?`, hashToken(code)).Scan(&id, &owner, &expires, &used)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrBadPair
	}
	if err != nil {
		return "", err
	}
	if used.Valid {
		return "", ErrPairUsed
	}
	if t, perr := time.Parse(time.RFC3339Nano, expires); perr != nil || time.Now().After(t) {
		return "", ErrBadPair
	}
	res, err := tx.ExecContext(ctx, `UPDATE pairing_codes SET used_at = ?
		WHERE id = ? AND used_at IS NULL`, now(), id)
	if err != nil {
		return "", err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return "", ErrPairUsed
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	return owner, nil
}

// now 返回统一的 UTC 时间串（与数据库默认格式一致）。
func now() string { return time.Now().UTC().Format(time.RFC3339Nano) }
