package server

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"novelstack/internal/auth"
	"novelstack/internal/books"
	"novelstack/internal/devices"
	"novelstack/internal/mobile"
)

// 批量同步的单次上限：私人站书架规模远小于此，设上限只为防滥用。
const syncBatchMax = 200

// handleSyncProgressGet 拉取阅读进度。
//
// 支持两种凭证：浏览器 session 与 device token。客户端前台每 5 分钟调用一次，
// 携带上次的 cursor；服务端只回增量（含边界重复，客户端按 book_id 幂等覆盖）。
func (s *Server) handleSyncProgressGet(w http.ResponseWriter, r *http.Request, id identity) {
	if s.db == nil {
		writeErr(w, http.StatusServiceUnavailable, "database not ready")
		return
	}
	since := strings.TrimSpace(r.URL.Query().Get("since"))
	if since != "" {
		if _, err := time.Parse(time.RFC3339Nano, since); err != nil {
			writeErr(w, http.StatusBadRequest, "invalid since cursor")
			return
		}
	}
	items, err := mobile.ListProgress(r.Context(), s.db, id.username, since, 500)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"cursor": mobile.ProgressCursor(),
		"count":  len(items),
		"items":  items,
	})
}

// handleSyncProgressPost 批量上报并合并进度。
//
// 合并规则见 mobile.MergeProgress：同一本书保留最靠后的位置，
// 因此「多端同时看一本书」最终会对齐到读得最远的那一端。
func (s *Server) handleSyncProgressPost(w http.ResponseWriter, r *http.Request, id identity) {
	if s.db == nil {
		writeErr(w, http.StatusServiceUnavailable, "database not ready")
		return
	}
	if !isJSONContentType(r.Header.Get("Content-Type")) {
		writeErr(w, http.StatusUnsupportedMediaType, "application/json required")
		return
	}
	var req struct {
		Items []mobile.Progress `json:"items"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json body")
		return
	}
	applied, err := mobile.MergeProgress(r.Context(), s.db, id.username, req.Items, id.deviceID, syncBatchMax)
	if err != nil {
		switch {
		case errors.Is(err, mobile.ErrProgressInvalid):
			writeErr(w, http.StatusBadRequest, "invalid progress fields")
		case errors.Is(err, mobile.ErrTooManyItems):
			writeErr(w, http.StatusRequestEntityTooLarge,
				fmt.Sprintf("too many items (max %d)", syncBatchMax))
		default:
			writeErr(w, http.StatusInternalServerError, "internal error")
		}
		return
	}
	// 直接返回合并后的全量，客户端一次往返即可对齐，不用再发 GET。
	items, err := mobile.ListProgress(r.Context(), s.db, id.username, "", 500)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	s.audit.Record(r.Context(), id.username, id.ip, "progress_merged", "", "ok",
		fmt.Sprintf("reported=%d applied=%d", len(req.Items), applied))
	writeJSON(w, http.StatusOK, map[string]any{
		"cursor":  mobile.ProgressCursor(),
		"applied": applied,
		"count":   len(items),
		"items":   items,
	})
}

// handleWebGrant 给已登录的客户端签发一次性 Web 登录票据。
//
// 典型调用方是 Android：它只有 device token，无法直接写浏览器的 HttpOnly cookie。
func (s *Server) handleWebGrant(w http.ResponseWriter, r *http.Request, id identity) {
	if s.webGrants == nil {
		writeErr(w, http.StatusServiceUnavailable, "grant disabled")
		return
	}
	token, exp, err := s.webGrants.Issue(id.username)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	base := s.publicBaseURL(r)
	s.audit.Record(r.Context(), id.username, id.ip, "web_grant_issued", "", "ok", "ttl=60s")
	writeJSON(w, http.StatusOK, map[string]any{
		"grant":      token,
		"expires_at": exp.UTC().Format(time.RFC3339),
		"url":        base + "/admin/login?grant=" + token,
	})
}

// handleGrantLogin 兑换票据并写入 session cookie。
//
// 不需要既有会话（这正是它的用途），因此必须限流：与登录共用 IP 限流器，
// 且票据是 256 位随机数，猜中概率可忽略。
func (s *Server) handleGrantLogin(w http.ResponseWriter, r *http.Request) {
	if s.webGrants == nil {
		writeErr(w, http.StatusServiceUnavailable, "grant disabled")
		return
	}
	if !isJSONContentType(r.Header.Get("Content-Type")) {
		writeErr(w, http.StatusUnsupportedMediaType, "application/json required")
		return
	}
	if !s.limiter.Allow(s.clientIP(r)) {
		writeErr(w, http.StatusTooManyRequests, "too many attempts, try later")
		return
	}
	var req struct {
		Grant string `json:"grant"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json body")
		return
	}
	username, ok := s.webGrants.Redeem(strings.TrimSpace(req.Grant))
	if !ok {
		s.audit.Record(r.Context(), "", s.clientIP(r), "web_grant_redeem", "", "denied", "invalid or expired")
		writeErr(w, http.StatusUnauthorized, "invalid grant")
		return
	}
	token, exp, err := s.sessions.CreateSession(r.Context(), username, s.cfg.SessionTTL)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	auth.SetSessionCookie(w, token, exp, s.cfg.CookieSecure)
	s.audit.Record(r.Context(), username, s.clientIP(r), "web_grant_redeem", "", "ok", "webview login")
	writeJSON(w, http.StatusOK, map[string]any{"username": username})
}

// webLibrary 是 Web 阅读器的书架响应。
type webLibrary struct {
	Manifest *mobile.Manifest  `json:"manifest"`
	Progress []mobile.Progress `json:"progress"`
	Cursor   string            `json:"progress_cursor"`
}

// handleWebLibrary 返回 Web 阅读器所需的全部元数据：书籍、章节索引、进度，
// 以及用调用方公钥包装的书籍 DEK。
//
// 公钥由浏览器用 Web Crypto 生成（RSA-OAEP-256，私钥只留在本标签页的
// sessionStorage），服务端从不输出明文 DEK——这一点与移动端设备完全同构。
func (s *Server) handleWebLibrary(w http.ResponseWriter, r *http.Request, id identity) {
	if s.db == nil {
		writeErr(w, http.StatusServiceUnavailable, "database not ready")
		return
	}
	if !isJSONContentType(r.Header.Get("Content-Type")) {
		writeErr(w, http.StatusUnsupportedMediaType, "application/json required")
		return
	}
	var req struct {
		PublicKey string `json:"public_key"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json body")
		return
	}
	pub, err := devices.ParseRSAPublicKey(req.PublicKey)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid public key")
		return
	}
	wrap := func(bookID string) (string, error) {
		return s.books.WrapDEKFor(r.Context(), bookID, id.username, func(dek []byte) (string, error) {
			return devices.WrapDEK(pub, dek)
		})
	}
	m, err := mobile.BuildManifestWithVariant(r.Context(), s.db, id.username, 0, wrap, s.books.PurgeVariant())
	if err != nil {
		if errors.Is(err, books.ErrKeyVersion) {
			writeErr(w, http.StatusServiceUnavailable, "kek version not available")
			return
		}
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	progress, err := mobile.ListProgress(r.Context(), s.db, id.username, "", 500)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	s.audit.Record(r.Context(), id.username, id.ip, "web_library", "", "ok",
		fmt.Sprintf("books=%d progress=%d", m.Count, len(progress)))
	writeJSON(w, http.StatusOK, webLibrary{
		Manifest: m, Progress: progress, Cursor: mobile.ProgressCursor(),
	})
}

// handleWebChapters 批量返回章节密文（NSCH），并附上用调用方公钥包装的书籍 DEK。
//
// 与 /api/books/{id}/chapters/prefetch 的区别：这里接受请求体里的公钥，
// 供浏览器在没有设备注册的前提下解密；权限边界与 /chapters/{i}/plain 相同
// （都是 session 级），但传输层不出现明文 DEK。
func (s *Server) handleWebChapters(w http.ResponseWriter, r *http.Request, id identity) {
	if !isJSONContentType(r.Header.Get("Content-Type")) {
		writeErr(w, http.StatusUnsupportedMediaType, "application/json required")
		return
	}
	var req struct {
		BookID    string `json:"book_id"`
		Indexes   []int  `json:"indexes"`
		PublicKey string `json:"public_key"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32<<10)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json body")
		return
	}
	if strings.TrimSpace(req.BookID) == "" {
		writeErr(w, http.StatusBadRequest, "book_id required")
		return
	}
	pub, err := devices.ParseRSAPublicKey(req.PublicKey)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid public key")
		return
	}
	key := "s:" + id.username
	if ok, wait := s.prefetch.Allow(key); !ok {
		w.Header().Set("Retry-After", fmt.Sprintf("%d", int(wait.Seconds())+1))
		writeErr(w, http.StatusTooManyRequests, "prefetch rate limit exceeded")
		return
	}
	maxCh := prefetchMaxChapters(s.cfg)
	if len(req.Indexes) == 0 {
		writeErr(w, http.StatusBadRequest, "indexes required")
		return
	}
	if len(req.Indexes) > maxCh {
		writeErr(w, http.StatusBadRequest, fmt.Sprintf("too many chapters in one request (max %d)", maxCh))
		return
	}
	sealed, err := s.books.SealChapters(r.Context(), req.BookID, id.username, req.Indexes, maxCh)
	if err != nil {
		switch {
		case errors.Is(err, books.ErrBadRequest):
			writeErr(w, http.StatusBadRequest, "invalid chapter index")
		case errors.Is(err, books.ErrNotFound):
			writeErr(w, http.StatusNotFound, "book not found")
		case errors.Is(err, books.ErrKeyVersion):
			writeErr(w, http.StatusServiceUnavailable, "kek version not available")
		default:
			writeErr(w, http.StatusInternalServerError, "internal error")
		}
		return
	}
	maxBytes := prefetchMaxBytes(s.cfg)
	var total int64
	truncated := false
	type item struct {
		Index      int    `json:"index"`
		Title      string `json:"title"`
		ETag       string `json:"etag"`
		Ciphertext string `json:"ciphertext"`
	}
	out := make([]item, 0, len(sealed))
	for _, sc := range sealed {
		if total+int64(len(sc.Package)) > maxBytes && len(out) > 0 {
			truncated = true
			break
		}
		total += int64(len(sc.Package))
		out = append(out, item{
			Index: sc.Index, Title: sc.Title, ETag: sc.ETag,
			Ciphertext: base64.StdEncoding.EncodeToString(sc.Package),
		})
	}
	wrapped, err := s.books.WrapDEKFor(r.Context(), req.BookID, id.username,
		func(dek []byte) (string, error) { return devices.WrapDEK(pub, dek) })
	if err != nil {
		if errors.Is(err, books.ErrKeyVersion) {
			writeErr(w, http.StatusServiceUnavailable, "kek version not available")
			return
		}
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	s.audit.Record(r.Context(), id.username, id.ip, "web_chapter_seal", req.BookID, "ok",
		fmt.Sprintf("chapters=%d bytes=%d", len(out), total))
	writeJSON(w, http.StatusOK, map[string]any{
		"book_id": req.BookID, "count": len(out), "bytes": total,
		"truncated": truncated, "wrapped_dek": wrapped, "chapters": out,
	})
}

// publicBaseURL 推断对外的根地址，用于拼 WebView 登录链接。
// 优先反向代理的 X-Forwarded-Proto/Host，其次用请求 Host。
func (s *Server) publicBaseURL(r *http.Request) string {
	scheme := strings.TrimSpace(r.Header.Get("X-Forwarded-Proto"))
	if scheme == "" {
		if r.TLS != nil {
			scheme = "https"
		} else {
			scheme = "http"
		}
	}
	host := strings.TrimSpace(r.Header.Get("X-Forwarded-Host"))
	if host == "" {
		host = r.Host
	}
	return scheme + "://" + host
}
