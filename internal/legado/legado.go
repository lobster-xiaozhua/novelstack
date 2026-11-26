// Package legado 实现「Legado 书源网关」。
//
// 安全模型：网关是主后端唯一向第三方阅读 App 暴露的接口，因此认证分两层：
//  1. 身份层：网关会话（scope=legado，独立 cookie/头部，不能用于 /api/*）；
//  2. 书源令牌层：X-Legado-Token 对应的长期令牌（存哈希、可单独撤销）。
//
// 两层都通过才放行；令牌泄露只能读写网关，无法登录管理接口。
// 网关内部解密章节后按 Legado 书源期望的 JSON 返回，明文只在响应体内出现。
package legado

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"novelstack/internal/audit"
	"novelstack/internal/auth"
	"novelstack/internal/books"
	"novelstack/internal/chapterscan"
	"novelstack/internal/search"
)

// Service 持有网关依赖。
type Service struct {
	Sessions     *auth.Store
	Books        *books.Service
	Search       *search.Service
	Audit        *audit.Logger
	Tokens       *TokenStore
	SessionTTL   time.Duration
	CookieSecure bool
}

var tokenNamePattern = regexp.MustCompile(`^[A-Za-z0-9 _.@\-]{0,40}$`)

// ---- 令牌存储 ----

// TokenStore 管理书源令牌（只存 SHA-256 哈希）。
type TokenStore struct {
	db *sql.DB
}

func NewTokenStore(db *sql.DB) *TokenStore { return &TokenStore{db: db} }

// TokenView 是令牌的展示形态（不含令牌本身）。
type TokenView struct {
	ID         int64   `json:"id"`
	Name       string  `json:"name"`
	CreatedAt  string  `json:"created_at"`
	LastUsedAt *string `json:"last_used_at,omitempty"`
	RevokedAt  *string `json:"revoked_at,omitempty"`
	Active     bool    `json:"active"`
}

// Create 生成新令牌并返回明文（仅此一次可见）。
func (t *TokenStore) Create(ctx context.Context, name, username string) (string, int64, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		name = "legado"
	}
	if !tokenNamePattern.MatchString(name) {
		return "", 0, errors.New("invalid token name")
	}
	var userID int64
	if err := t.db.QueryRowContext(ctx, `SELECT id FROM users WHERE username = ?`, username).Scan(&userID); err != nil {
		return "", 0, err
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", 0, err
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	res, err := t.db.ExecContext(ctx,
		`INSERT INTO legado_tokens(name, token_hash, user_id) VALUES (?,?,?)`,
		name, hashToken(token), userID)
	if err != nil {
		return "", 0, err
	}
	id, _ := res.LastInsertId()
	return token, id, nil
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// Verify 校验令牌并返回绑定用户名；过期/撤销/未知一律视为无效。
func (t *TokenStore) Verify(ctx context.Context, token string) (string, bool, error) {
	if token == "" || len(token) > 128 {
		return "", false, nil
	}
	var username string
	err := t.db.QueryRowContext(ctx, `SELECT u.username FROM legado_tokens t
		JOIN users u ON u.id = t.user_id
		WHERE t.token_hash = ? AND t.revoked_at IS NULL`, hashToken(token)).Scan(&username)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	_, _ = t.db.ExecContext(ctx,
		`UPDATE legado_tokens SET last_used_at = strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE token_hash = ?`,
		hashToken(token))
	return username, true, nil
}

// List 返回全部令牌（含已撤销）。
func (t *TokenStore) List(ctx context.Context) ([]TokenView, error) {
	rows, err := t.db.QueryContext(ctx, `SELECT id, name, created_at, last_used_at, revoked_at
		FROM legado_tokens ORDER BY id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []TokenView{}
	for rows.Next() {
		var v TokenView
		if err := rows.Scan(&v.ID, &v.Name, &v.CreatedAt, &v.LastUsedAt, &v.RevokedAt); err != nil {
			return nil, err
		}
		v.Active = v.RevokedAt == nil
		out = append(out, v)
	}
	return out, rows.Err()
}

// Revoke 立即失效一个令牌（幂等）。
func (t *TokenStore) Revoke(ctx context.Context, id int64) (bool, error) {
	res, err := t.db.ExecContext(ctx,
		`UPDATE legado_tokens SET revoked_at = strftime('%Y-%m-%dT%H:%M:%fZ','now')
		 WHERE id = ? AND revoked_at IS NULL`, id)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// ---- 会话（登录）----

// Login 处理 POST /gateway/legado/login：口令换网关会话（scope=legado）。
// 该会话不能访问 /api/*；返回体里的 session_token 供书源以头部方式携带。
func (s *Service) Login(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json body")
		return
	}
	hash, err := s.Sessions.PasswordHash(ctx, req.Username)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	ok := false
	if hash != "" {
		ok, err = auth.VerifyPassword(req.Password, hash)
		if err != nil {
			ok = false
		}
	} else {
		_, _ = auth.HashPassword(req.Password) // 时间对齐
	}
	if !ok {
		s.Audit.Record(ctx, req.Username, clientIP(r), "legado_login_failed", "", "denied", "bad credentials")
		writeErr(w, http.StatusUnauthorized, "invalid credentials")
		return
	}
	token, exp, err := s.Sessions.CreateSessionScoped(ctx, req.Username, s.SessionTTL, auth.ScopeLegado)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	auth.SetGatewaySessionCookie(w, token, exp, s.CookieSecure)
	s.Audit.Record(ctx, req.Username, clientIP(r), "legado_login", "", "ok",
		fmt.Sprintf("ttl_hours=%d", int(s.SessionTTL.Hours())))
	writeJSON(w, http.StatusOK, map[string]any{
		"username":      req.Username,
		"expires_at":    exp.UTC().Format(time.RFC3339),
		"session_token": token, // 供 X-Legado-Session 头部使用
	})
}

// Logout 处理 POST /gateway/legado/logout。
func (s *Service) Logout(w http.ResponseWriter, r *http.Request, username, sessionToken string) {
	if sessionToken != "" {
		_ = s.Sessions.DeleteSession(r.Context(), sessionToken)
	}
	auth.ClearGatewaySessionCookie(w, s.CookieSecure)
	s.Audit.Record(r.Context(), username, clientIP(r), "legado_logout", "", "ok", "")
	writeJSON(w, http.StatusOK, map[string]string{"status": "logged_out"})
}

// ---- 书源接口 ----

// SearchBooks 处理 GET /gateway/legado/search?key=&page=。
func (s *Service) SearchBooks(w http.ResponseWriter, r *http.Request, username string) {
	ctx := r.Context()
	q := strings.TrimSpace(r.URL.Query().Get("key"))
	if q == "" {
		q = strings.TrimSpace(r.URL.Query().Get("q"))
	}
	res, err := s.Search.Search(ctx, q, 20)
	if err != nil {
		s.Audit.Record(ctx, username, clientIP(r), "legado_search", "", "error", "search failed")
		writeErr(w, http.StatusServiceUnavailable, "search unavailable")
		return
	}
	base := baseURL(r)
	items := make([]map[string]any, 0, len(res.Hits))
	for _, h := range res.Hits {
		items = append(items, map[string]any{
			"name":     h.Title,
			"author":   h.Author,
			"intro":    "",
			"kind":     "",
			"bookUrl":  base + "/gateway/legado/book/" + h.BookID,
			"coverUrl": "",
		})
	}
	s.Audit.Record(ctx, username, clientIP(r), "legado_search", "", "ok",
		fmt.Sprintf("q_len=%d hits=%d", res.QueryChars, len(items)))
	writeJSON(w, http.StatusOK, map[string]any{"data": items})
}

// Book 处理 GET /gateway/legado/book/{id}：书籍详情。
func (s *Service) Book(w http.ResponseWriter, r *http.Request, bookID, username string) {
	ctx := r.Context()
	meta, err := s.Books.Store.GetOwned(ctx, bookID, username, false)
	if err != nil {
		s.Audit.Record(ctx, username, clientIP(r), "legado_book", bookID, "denied", "not found")
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	chapters, _ := s.Books.Store.ListChapters(ctx, bookID)
	last := ""
	if len(chapters) > 0 {
		last = chapters[len(chapters)-1].Title
	}
	base := baseURL(r)
	s.Audit.Record(ctx, username, clientIP(r), "legado_book", bookID, "ok", "")
	writeJSON(w, http.StatusOK, map[string]any{
		"name":         meta.Title,
		"author":       meta.Author,
		"intro":        meta.Description,
		"kind":         strings.Join(meta.Tags, ","),
		"bookUrl":      base + "/gateway/legado/book/" + meta.ID,
		"tocUrl":       base + "/gateway/legado/toc/" + meta.ID,
		"lastChapter":  last,
		"chapterCount": len(chapters),
		"wordCount":    meta.SizeBytes,
	})
}

// Toc 处理 GET /gateway/legado/toc/{id}：目录（不解密正文）。
func (s *Service) Toc(w http.ResponseWriter, r *http.Request, bookID, username string) {
	ctx := r.Context()
	if _, err := s.Books.Store.GetOwned(ctx, bookID, username, false); err != nil {
		s.Audit.Record(ctx, username, clientIP(r), "legado_toc", bookID, "denied", "not found")
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	rows, err := s.Books.Store.ListChapters(ctx, bookID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	base := baseURL(r)
	items := make([]map[string]any, 0, len(rows))
	for _, c := range rows {
		items = append(items, map[string]any{
			"title": c.Title,
			"index": c.Index,
			"url":   fmt.Sprintf("%s/gateway/legado/content/%s/%d", base, bookID, c.Index),
		})
	}
	s.Audit.Record(ctx, username, clientIP(r), "legado_toc", bookID, "ok",
		fmt.Sprintf("chapters=%d", len(items)))
	writeJSON(w, http.StatusOK, map[string]any{"chapters": items})
}

// Content 处理 GET /gateway/legado/content/{id}/{index}：解密并返回单章正文。
// 明文只出现在响应体；审计只记章节号与字节数。
func (s *Service) Content(w http.ResponseWriter, r *http.Request, bookID string, index int, username string) {
	ctx := r.Context()
	meta, plain, err := s.Books.LoadPlaintextFor(ctx, bookID, username)
	if err != nil {
		s.Audit.Record(ctx, username, clientIP(r), "legado_content", bookID, "denied", "read failed")
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	chapters := chapterscan.Split(string(plain))
	if index < 0 || index >= len(chapters) {
		writeErr(w, http.StatusNotFound, "chapter not found")
		return
	}
	ch := chapters[index]
	runes := []rune(string(plain))
	text := string(runes[ch.Start:ch.End])

	// Legado 正文按段落渲染，保留换行即可
	s.Audit.Record(ctx, username, clientIP(r), "legado_content", bookID, "ok",
		fmt.Sprintf("chapter=%d chars=%d", index, ch.Chars))
	writeJSON(w, http.StatusOK, map[string]any{
		"title":   ch.Title,
		"index":   ch.Index,
		"bookId":  meta.ID,
		"content": text,
	})
}

// ---- 令牌管理（由 /api 管理接口调用，需要 api 会话）----

// CreateToken 处理 POST /api/admin/legado/tokens。
func (s *Service) CreateToken(w http.ResponseWriter, r *http.Request, username string) {
	ctx := r.Context()
	var req struct {
		Name string `json:"name"`
	}
	_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req)
	token, id, err := s.Tokens.Create(ctx, req.Name, username)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid token request")
		return
	}
	s.Audit.Record(ctx, username, clientIP(r), "legado_token_create", "", "ok", fmt.Sprintf("id=%d", id))
	writeJSON(w, http.StatusCreated, map[string]any{
		"id":    id,
		"token": token, // 只在此次响应中出现
		"note":  "仅显示一次，请立即保存到书源 header",
	})
}

// ListTokens 处理 GET /api/admin/legado/tokens。
func (s *Service) ListTokens(w http.ResponseWriter, r *http.Request, username string) {
	list, err := s.Tokens.List(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	s.Audit.Record(r.Context(), username, clientIP(r), "legado_token_list", "", "ok", fmt.Sprintf("count=%d", len(list)))
	writeJSON(w, http.StatusOK, list)
}

// RevokeToken 处理 DELETE /api/admin/legado/tokens/{id}。
func (s *Service) RevokeToken(w http.ResponseWriter, r *http.Request, username string) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeErr(w, http.StatusBadRequest, "invalid id")
		return
	}
	ok, err := s.Tokens.Revoke(r.Context(), id)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	if !ok {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	s.Audit.Record(r.Context(), username, clientIP(r), "legado_token_revoke", "", "ok", fmt.Sprintf("id=%d", id))
	w.WriteHeader(http.StatusNoContent)
}

// ---- 辅助 ----

func baseURL(r *http.Request) string {
	scheme := "https"
	if p := r.Header.Get("X-Forwarded-Proto"); p != "" {
		scheme = p
	} else if r.TLS == nil {
		scheme = "http"
	}
	host := r.Host
	if h := r.Header.Get("X-Forwarded-Host"); h != "" {
		host = h
	}
	return scheme + "://" + host
}

func clientIP(r *http.Request) string {
	return auth.NormalizeIP(r.RemoteAddr, r.Header.Get("X-Forwarded-For"))
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
