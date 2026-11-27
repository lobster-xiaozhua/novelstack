// Package server 组装 HTTP 路由、中间件与认证端点。
package server

import (
	"context"
	"crypto/rsa"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"novelstack/internal/audit"
	"novelstack/internal/auth"
	"novelstack/internal/backup"
	"novelstack/internal/books"
	"novelstack/internal/config"
	"novelstack/internal/devices"
	"novelstack/internal/legado"
	"novelstack/internal/mobile"
	"novelstack/internal/ops"
	"novelstack/internal/search"
	"novelstack/internal/stats"
	"novelstack/internal/telemetry"
	"novelstack/internal/tts"
)

// Deps 汇总服务端依赖，避免位置参数继续膨胀。
type Deps struct {
	Config   *config.Config
	Sessions *auth.Store
	Books    *books.Service
	TTS      *tts.Service
	Search   *search.Service
	Legado   *legado.Service
	Audit    *audit.Logger
	DB       *sql.DB
	// Devices 管理移动端长期凭证；为 nil 时移动端接口返回 503。
	Devices *devices.Store
	// Health 负责告警 webhook 去重与投递（判定在 dashboard/ops）。
	Health *ops.HealthStore
	// Scheduler 支撑控制台「立即备份」；为 nil 时该接口返回 503。
	Scheduler interface {
		RunOnce(ctx context.Context) (*backup.CreateResult, error)
	}
	// Telemetry 是遥测明细/聚合的查询与写入入口；为 nil 时相关接口返回 503。
	Telemetry *telemetry.Store
	// Stats 提供阅读统计（今日/累计/连续天数）；为 nil 时接口返回 503。
	Stats *stats.Service
}

// Server 持有全部依赖并暴露 http.Handler。
type Server struct {
	cfg       *config.Config
	sessions  *auth.Store
	books     *books.Service
	tts       *tts.Service
	search    *search.Service
	legado    *legado.Service
	audit     *audit.Logger
	db        *sql.DB
	devices   *devices.Store
	limiter   *auth.IPLimiter
	prefetch  *mobile.Limiter
	audioGen  *mobile.Limiter
	health    *ops.HealthStore
	telemetry *telemetry.Store
	stats     *stats.Service
	scheduler interface {
		RunOnce(ctx context.Context) (*backup.CreateResult, error)
	}
	mux *http.ServeMux
}

// New 构建路由（Go 1.22 net/http 方法路由）。
func New(d Deps) *Server {
	s := &Server{
		cfg:       d.Config,
		sessions:  d.Sessions,
		books:     d.Books,
		tts:       d.TTS,
		search:    d.Search,
		legado:    d.Legado,
		audit:     d.Audit,
		db:        d.DB,
		devices:   d.Devices,
		limiter:   auth.NewIPLimiter(d.Config.LoginFailPerMinute),
		prefetch:  mobile.NewLimiter(prefetchPerMinute(d.Config), 0),
		audioGen:  mobile.NewLimiter(max(2, prefetchPerMinute(d.Config)/4), 0),
		health:    d.Health,
		telemetry: d.Telemetry,
		stats:     d.Stats,
		scheduler: d.Scheduler,
		mux:       http.NewServeMux(),
	}
	s.routes()
	return s
}

// Handler 返回带安全响应头的根 handler。
func (s *Server) Handler() http.Handler {
	return s.securityHeaders(s.mux)
}

func (s *Server) routes() {
	// 健康检查：不鉴权。
	s.mux.HandleFunc("GET /healthz", s.handleHealthz)

	// 认证。
	s.mux.HandleFunc("POST /api/login", s.handleLogin)
	// 退出登录只针对浏览器会话；device token 的撤销走 /api/admin/devices。
	s.mux.HandleFunc("POST /api/logout", s.requireSession(s.handleLogout))
	s.mux.HandleFunc("GET /api/me", s.requireAuth(s.handleMe))

	// 第五阶段：移动端凭证与同步（device token 可用）。
	s.mux.HandleFunc("POST /api/device/token", s.handleDeviceToken)
	s.mux.HandleFunc("POST /api/admin/pairing", s.requireSession(s.handlePairingCreate))
	s.mux.HandleFunc("GET /api/admin/devices", s.requireSession(s.handleDeviceList))
	s.mux.HandleFunc("DELETE /api/admin/devices/{id}", s.requireSession(s.handleDeviceRevoke))
	s.mux.HandleFunc("GET /api/sync/manifest", s.requireAuth(s.handleSyncManifest))
	s.mux.HandleFunc("PUT /api/books/{id}/progress", s.requireAuth(s.handleProgressPut))
	s.mux.HandleFunc("GET /api/books/{id}/progress", s.requireAuth(s.handleProgressGet))
	s.mux.HandleFunc("POST /api/books/{id}/chapters/prefetch", s.requireAuth(s.handlePrefetch))
	s.mux.HandleFunc("GET /api/books/{id}/chapters/{index}/audio", s.requireAuth(s.handleAudioManifest))
	s.mux.HandleFunc("POST /api/books/{id}/chapters/{index}/audio/prepare", s.requireAuth(s.handleAudioPrepare))
	s.mux.HandleFunc("GET /api/tts/segments/{key}", s.requireAuth(s.handleAudioSegment))

	// 第八阶段：客户端体验与书库运营（只读元数据/进度，不含正文）。
	s.mux.HandleFunc("GET /api/discover", s.requireAuth(s.handleDiscover))
	s.mux.HandleFunc("GET /api/history", s.requireAuth(s.handleHistory))
	s.mux.HandleFunc("PATCH /api/books/{id}/favorite", s.requireAuth(s.handleFavoritePatch))
	s.mux.HandleFunc("GET /api/app/version", s.requireAuth(s.handleAppVersion))
	s.mux.HandleFunc("GET /api/app/version/latest-hash", s.requireAuth(s.handleAppLatestHash))
	s.mux.HandleFunc("GET /api/stats/reading", s.requireAuth(s.handleReadingStats))
	// 数据导出：管理员会话 + 密码二次验证（防设备被盗后一键拖库）。
	s.mux.HandleFunc("POST /api/admin/export", s.requireSession(s.handleExport))
	s.mux.HandleFunc("POST /api/admin/export/preview", s.requireSession(s.handleExportPreview))

	// 第九阶段：全链路遥测（客户端 device token 上报；控制台只读聚合与明细）。
	s.mux.HandleFunc("POST /api/telemetry/batch", s.requireAuth(s.handleTelemetryBatch))
	s.mux.HandleFunc("GET /api/admin/telemetry/overview", s.adminTelemetry(s.handleAdminTelemetryOverview))
	s.mux.HandleFunc("GET /api/admin/telemetry/heatmap", s.adminTelemetry(s.handleAdminTelemetryHeatmap))
	s.mux.HandleFunc("GET /api/admin/telemetry/slow-chapters", s.adminTelemetry(s.handleAdminTelemetrySlowChapters))
	s.mux.HandleFunc("GET /api/admin/telemetry/crashes", s.adminTelemetry(s.handleAdminTelemetryCrashes))
	s.mux.HandleFunc("GET /api/admin/telemetry/series", s.adminTelemetry(s.handleAdminTelemetrySeries))

	// 书籍：读接口允许 device token；写接口只允许浏览器会话。
	s.mux.HandleFunc("POST /api/books", s.requireSession(s.handleBookUpload))
	s.mux.HandleFunc("GET /api/books", s.requireAuth(s.handleBookList))
	s.mux.HandleFunc("GET /api/books/{id}", s.requireAuth(s.handleBookGet))
	s.mux.HandleFunc("PATCH /api/books/{id}", s.requireSession(s.handleBookPatch))
	s.mux.HandleFunc("GET /api/books/{id}/content", s.requireSession(s.handleBookContent))
	s.mux.HandleFunc("DELETE /api/books/{id}", s.requireSession(s.handleBookDelete))

	// 章节（第三阶段）：目录不解密；单章明文支持 ETag/304；回执用于覆盖度指标。
	// 目录允许 device token（同步需要）；明文与回执仅 session。
	s.mux.HandleFunc("GET /api/books/{id}/chapters", s.requireAuth(s.handleChapters))
	s.mux.HandleFunc("GET /api/books/{id}/chapters/{index}/plain", s.requireSession(s.handleChapterPlain))
	s.mux.HandleFunc("POST /api/books/{id}/chapters/{index}/ack", s.requireSession(s.handleChapterAck))

	// TTS 朗读（第二阶段）：服务端解密后流式输出，仅浏览器会话。
	// 移动端走 /chapters/{i}/audio 的密文路径，在设备内解密。
	s.mux.HandleFunc("GET /api/books/{id}/chapters/{index}/speak", s.requireSession(s.handleSpeak))

	// 元数据检索（只索引书名/作者/标签/简介/章节标题，正文不在服务端）。
	s.mux.HandleFunc("GET /api/search/metadata", s.requireSession(s.handleSearchMetadata))

	// 审计与指标（仅当前登录用户）。
	s.mux.HandleFunc("GET /api/admin/audit", s.requireSession(s.handleAuditFiltered))
	s.mux.HandleFunc("GET /api/admin/tts/stats", s.requireSession(s.handleTTSStats))
	s.mux.HandleFunc("GET /api/admin/metrics", s.requireSession(s.handleMetrics))

	// 第四阶段：运营控制台（静态资源 + 聚合指标 + 缓存治理）。
	s.registerAdmin()

	// 书源令牌管理（需要 api 会话；令牌本身只能用于 /gateway/legado/*）。
	s.mux.HandleFunc("POST /api/admin/legado/tokens", s.requireSession(s.handleLegadoTokenCreate))
	s.mux.HandleFunc("GET /api/admin/legado/tokens", s.requireSession(s.handleLegadoTokenList))
	s.mux.HandleFunc("DELETE /api/admin/legado/tokens/{id}", s.requireSession(s.handleLegadoTokenRevoke))

	// Legado 书源网关（路线 A）：登录 + 双层认证的业务接口。
	s.mux.HandleFunc("POST /gateway/legado/login", s.handleLegadoLogin)
	s.mux.HandleFunc("POST /gateway/legado/logout", s.handleLegadoLogout)
	s.mux.HandleFunc("GET /gateway/legado/search", s.gatewayAuth(s.handleLegadoSearch))
	s.mux.HandleFunc("GET /gateway/legado/book/{id}", s.gatewayAuth(s.handleLegadoBook))
	s.mux.HandleFunc("GET /gateway/legado/toc/{id}", s.gatewayAuth(s.handleLegadoToc))
	s.mux.HandleFunc("GET /gateway/legado/content/{id}/{index}", s.gatewayAuth(s.handleLegadoContent))

	// 兜底：未匹配路由统一 404 JSON。
	s.mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		writeErr(w, http.StatusNotFound, "not found")
	})
}

// clientIP 提取客户端 IP（XFF 第一跳，否则 RemoteAddr）。
func (s *Server) clientIP(r *http.Request) string {
	return auth.NormalizeIP(r.RemoteAddr, r.Header.Get("X-Forwarded-For"))
}

// identity 是 requireAuth 注入请求上下文的主体信息。
// session 与 device token 是两套凭证：sessionID 与 deviceID 不会同时非空。
type identity struct {
	username  string
	ip        string
	sessionID string // token 哈希前 12 位，用于审计关联，不含原始 token
	deviceID  string // 非空表示通过 device token 认证
	deviceKey *rsa.PublicKey
	viaDevice bool
}

// isDevice 判断本次请求是否使用 device token。
func (id identity) isDevice() bool { return id.viaDevice }

type ctxKey struct{}

func (s *Server) requireAuth(next func(w http.ResponseWriter, r *http.Request, id identity)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, status, msg := s.lookupIdentity(r)
		if status != 0 {
			writeErr(w, status, msg)
			return
		}
		next(w, r, id)
	}
}

// requireSession 只接受浏览器会话：device token 一律 403（而不是 401，
// 便于客户端区分「凭证无效」与「凭证类型不允许访问控制台」）。
func (s *Server) requireSession(next func(w http.ResponseWriter, r *http.Request, id identity)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if tok := bearerToken(r); strings.HasPrefix(tok, devices.TokenPrefix) {
			writeErr(w, http.StatusForbidden, "device token cannot access admin endpoints")
			return
		}
		c, err := r.Cookie(auth.SessionCookieName)
		if err != nil || c.Value == "" {
			writeErr(w, http.StatusUnauthorized, "authentication required")
			return
		}
		sess, err := s.sessions.LookupSession(r.Context(), c.Value)
		if err != nil || sess == nil {
			writeErr(w, http.StatusUnauthorized, "authentication required")
			return
		}
		next(w, r, identity{
			username:  sess.Username,
			ip:        s.clientIP(r),
			sessionID: shortHash(sess.TokenHash),
		})
	}
}

// adminTelemetry 给控制台遥测接口统一加「未装配则 503」的保护，
// 避免每个 handler 重复判空。
func (s *Server) adminTelemetry(next func(http.ResponseWriter, *http.Request)) http.HandlerFunc {
	return s.requireSession(func(w http.ResponseWriter, r *http.Request, _ identity) {
		if s.telemetry == nil {
			writeErr(w, http.StatusServiceUnavailable, "telemetry disabled")
			return
		}
		next(w, r)
	})
}

// lookupIdentity 先看浏览器 cookie，再看 Authorization: Bearer nsd_...。
// 返回 status=0 表示成功；否则 status/msg 直接写回客户端。
func (s *Server) lookupIdentity(r *http.Request) (identity, int, string) {
	if c, err := r.Cookie(auth.SessionCookieName); err == nil && c.Value != "" {
		if sess, err := s.sessions.LookupSession(r.Context(), c.Value); err == nil && sess != nil {
			return identity{
				username:  sess.Username,
				ip:        s.clientIP(r),
				sessionID: shortHash(sess.TokenHash),
			}, 0, ""
		}
	}

	tok := bearerToken(r)
	if tok == "" {
		return identity{}, http.StatusUnauthorized, "authentication required"
	}
	if !strings.HasPrefix(tok, devices.TokenPrefix) {
		return identity{}, http.StatusUnauthorized, "invalid credential"
	}
	if s.devices == nil {
		return identity{}, http.StatusServiceUnavailable, "device tokens not configured"
	}
	d, owner, err := s.devices.Authenticate(r.Context(), tok)
	if err != nil {
		return identity{}, http.StatusUnauthorized, "invalid or revoked device token"
	}
	id := identity{
		username:  owner,
		ip:        s.clientIP(r),
		deviceID:  d.ID,
		viaDevice: true,
	}
	if d.PublicKey != "" {
		if pub, perr := devices.ParseRSAPublicKey(d.PublicKey); perr == nil {
			id.deviceKey = pub
		}
	}
	return id, 0, ""
}

// bearerToken 提取 Authorization: Bearer 的 token（大小写不敏感）。
func bearerToken(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if len(h) < 7 || !strings.EqualFold(h[:7], "bearer ") {
		return ""
	}
	return strings.TrimSpace(h[7:])
}

// securityHeaders 设置基础安全响应头。
func (s *Server) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

// ---- handlers ----

func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleLogin 处理 POST /api/login。
// 仅接受 application/json；按 IP 限流；失败写审计。
func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	ip := s.clientIP(r)
	if !isJSONContentType(r.Header.Get("Content-Type")) {
		writeErr(w, http.StatusUnsupportedMediaType, "application/json required")
		return
	}
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json body")
		return
	}
	// 常数时间比较固定哨兵值，避免短用户名/空口令造成时间侧信道差异。
	sentinel := "novelstack-sentinel"
	_ = subtle.ConstantTimeCompare([]byte(req.Username), []byte(sentinel))
	_ = subtle.ConstantTimeCompare([]byte(req.Password), []byte(sentinel))

	if !s.limiter.Allow(ip) {
		s.audit.Record(r.Context(), "", ip, "login_ratelimited", "", "ratelimited", "")
		writeErr(w, http.StatusTooManyRequests, "too many attempts, try later")
		return
	}

	ok, err := verifyLogin(r.Context(), s.sessions, req.Username, req.Password)
	if err != nil || !ok {
		s.audit.Record(r.Context(), req.Username, ip, "login_failed", "", "denied", "bad credentials")
		// 对外统一模糊信息，不区分用户名不存在/密码错误。
		writeErr(w, http.StatusUnauthorized, "invalid credentials")
		return
	}

	token, exp, err := s.sessions.CreateSession(r.Context(), req.Username, s.cfg.SessionTTL)
	if err != nil {
		s.audit.Record(r.Context(), req.Username, ip, "login_failed", "", "error", "session create failed")
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	auth.SetSessionCookie(w, token, exp, s.cfg.CookieSecure)
	s.audit.Record(r.Context(), req.Username, ip, "login_success", "", "ok", "")
	writeJSON(w, http.StatusOK, map[string]any{
		"username":   req.Username,
		"expires_at": exp.UTC().Format(time.RFC3339),
	})
}

// verifyLogin 校验口令；用户不存在时也执行一次哈希，抹平时间差异。
func verifyLogin(ctx context.Context, st *auth.Store, username, password string) (bool, error) {
	hash, err := st.PasswordHash(ctx, username)
	if err != nil {
		return false, err
	}
	if hash == "" {
		_, _ = auth.HashPassword(password) // 耗时对齐
		return false, nil
	}
	return auth.VerifyPassword(password, hash)
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request, id identity) {
	if c, err := r.Cookie(auth.SessionCookieName); err == nil {
		_ = s.sessions.DeleteSession(r.Context(), c.Value)
	}
	auth.ClearSessionCookie(w, s.cfg.CookieSecure)
	s.audit.Record(r.Context(), id.username, id.ip, "logout", "", "ok", "")
	writeJSON(w, http.StatusOK, map[string]string{"status": "logged_out"})
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request, id identity) {
	// device token：不依赖 Cookie，返回设备身份（不含 token）。
	if id.viaDevice {
		writeJSON(w, http.StatusOK, map[string]any{
			"username":  id.username,
			"kind":      "device",
			"device_id": id.deviceID,
		})
		return
	}
	c, err := r.Cookie(auth.SessionCookieName)
	if err != nil {
		writeErr(w, http.StatusUnauthorized, "authentication required")
		return
	}
	sess, err := s.sessions.LookupSession(r.Context(), c.Value)
	if err != nil || sess == nil {
		writeErr(w, http.StatusUnauthorized, "authentication required")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"username":   sess.Username,
		"kind":       "session",
		"expires_at": sess.ExpiresAt.UTC().Format(time.RFC3339),
	})
}

func (s *Server) handleBookUpload(w http.ResponseWriter, r *http.Request, id identity) {
	// 上传体大小限制：明文上限 + multipart 边界开销余量（明文约 1MiB 块 + 表单字段）。
	r.Body = http.MaxBytesReader(w, r.Body, s.books.MaxUpload+2<<20)
	s.books.Upload(w, r, id.username, id.ip)
}

func (s *Server) handleBookList(w http.ResponseWriter, r *http.Request, id identity) {
	s.books.List(w, r, id.username, id.ip)
}

func (s *Server) handleBookGet(w http.ResponseWriter, r *http.Request, id identity) {
	bid := r.PathValue("id")
	s.books.GetMeta(w, r, bid, id.username, id.ip)
}

func (s *Server) handleBookContent(w http.ResponseWriter, r *http.Request, id identity) {
	bid := r.PathValue("id")
	s.books.Content(w, r, bid, id.username, id.ip)
}

func (s *Server) handleBookDelete(w http.ResponseWriter, r *http.Request, id identity) {
	bid := r.PathValue("id")
	s.books.Delete(w, r, bid, id.username, id.ip)
}

// handleChapters 返回章节目录（不含正文）。
func (s *Server) handleChapters(w http.ResponseWriter, r *http.Request, id identity) {
	s.books.Chapters(w, r, r.PathValue("id"), id.username, id.ip)
}

// handleChapterPlain 返回单章纯文本（ETag/304）。
func (s *Server) handleChapterPlain(w http.ResponseWriter, r *http.Request, id identity) {
	idx, err := strconv.Atoi(r.PathValue("index"))
	if err != nil || idx < 0 {
		writeErr(w, http.StatusBadRequest, "invalid chapter index")
		return
	}
	s.books.ChapterPlain(w, r, r.PathValue("id"), idx, id.username, id.ip)
}

// handleChapterAck 记录客户端本地索引回执。
func (s *Server) handleChapterAck(w http.ResponseWriter, r *http.Request, id identity) {
	idx, err := strconv.Atoi(r.PathValue("index"))
	if err != nil || idx < 0 {
		writeErr(w, http.StatusBadRequest, "invalid chapter index")
		return
	}
	s.books.Ack(w, r, r.PathValue("id"), idx, id.username, id.ip)
}

// handleBookPatch 只更新可索引元数据。
func (s *Server) handleBookPatch(w http.ResponseWriter, r *http.Request, id identity) {
	s.books.PatchMeta(w, r, r.PathValue("id"), id.username, id.ip)
}

// handleSpeak 流式朗读某一章。
func (s *Server) handleSpeak(w http.ResponseWriter, r *http.Request, id identity) {
	if s.tts == nil {
		writeErr(w, http.StatusServiceUnavailable, "tts not configured")
		return
	}
	idx, err := strconv.Atoi(r.PathValue("index"))
	if err != nil || idx < 0 {
		writeErr(w, http.StatusBadRequest, "invalid chapter index")
		return
	}
	s.tts.Speak(w, r, r.PathValue("id"), idx, id.username, id.ip)
}

// handleTTSStats 返回缓存规模、合成成功率与 Provider 健康状态。
func (s *Server) handleTTSStats(w http.ResponseWriter, r *http.Request, id identity) {
	if s.tts == nil {
		writeErr(w, http.StatusServiceUnavailable, "tts not configured")
		return
	}
	s.tts.Stats(w, r, id.username, id.ip)
}

// handleSearchMetadata 处理 GET /api/search/metadata?q=...&limit=20。
// 只查元数据索引；查询词不写日志明文（只记长度与 HMAC 指纹）。
func (s *Server) handleSearchMetadata(w http.ResponseWriter, r *http.Request, id identity) {
	if s.search == nil {
		writeErr(w, http.StatusServiceUnavailable, "search not configured")
		return
	}
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if len([]rune(q)) > 100 {
		writeErr(w, http.StatusBadRequest, "query too long")
		return
	}
	limit := search.ParseLimit(r, 20, 50)
	res, err := s.search.Search(r.Context(), q, limit)
	if err != nil {
		s.audit.Record(r.Context(), id.username, id.ip, "meta_search", "", "error", "search failed")
		writeErr(w, http.StatusServiceUnavailable, "search unavailable")
		return
	}
	s.audit.Record(r.Context(), id.username, id.ip, "meta_search", "", "ok",
		"q_len="+strconv.Itoa(res.QueryChars)+" hits="+strconv.Itoa(len(res.Hits))+" source="+res.Source)
	writeJSON(w, http.StatusOK, res)
}

// handleMetrics 汇总第三阶段指标：索引偏差、章节覆盖度、搜索延迟、网关成功率。
func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request, id identity) {
	ctx := r.Context()
	if s.db == nil {
		writeErr(w, http.StatusServiceUnavailable, "metrics unavailable")
		return
	}
	limit := search.ParseLimit(r, 200, 2000)

	var totalBooks, totalChapters, ackedChapters int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(SUM(chapter_count),0) FROM books
		WHERE deleted_at IS NULL`).Scan(&totalBooks, &totalChapters); err != nil {
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM (
			SELECT DISTINCT book_id, chapter_index FROM chapter_acks) `).Scan(&ackedChapters); err != nil {
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	coverage, err := s.books.Store.CoverageAll(ctx)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}

	idx := map[string]any{"backend": "disabled", "health": "disabled"}
	p50, p95, samples := 0.0, 0.0, 0
	if s.search != nil {
		idx = s.search.IndexStats(ctx)
		idx["books"] = totalBooks
		if docs, ok := idx["documents"].(int64); ok {
			idx["drift"] = docs - int64(totalBooks)
		}
		if a, b, n, lerr := s.search.Latency(ctx, limit); lerr == nil {
			p50, p95, samples = a, b, n
		}
	}

	gwOK, gwTotal := 0, 0
	if s.legado != nil {
		gwOK, gwTotal, _ = search.GatewaySuccess(ctx, s.db, limit)
	}
	rate := 0.0
	if gwTotal > 0 {
		rate = float64(gwOK) / float64(gwTotal)
	}

	s.audit.Record(ctx, id.username, id.ip, "metrics_view", "", "ok", "limit="+strconv.Itoa(limit))
	writeJSON(w, http.StatusOK, map[string]any{
		"generated_at": time.Now().UTC().Format(time.RFC3339),
		"books": map[string]any{
			"total":          totalBooks,
			"chapters":       totalChapters,
			"acked_chapters": ackedChapters,
		},
		"metadata_index": idx,
		"search": map[string]any{
			"window":  limit,
			"samples": samples,
			"p50_ms":  p50,
			"p95_ms":  p95,
		},
		"coverage": coverage,
		"legado": map[string]any{
			"enabled":      s.legado != nil,
			"window":       limit,
			"total":        gwTotal,
			"ok":           gwOK,
			"success_rate": rate,
		},
	})
}

func (s *Server) handleLegadoTokenCreate(w http.ResponseWriter, r *http.Request, id identity) {
	if s.legado == nil {
		writeErr(w, http.StatusServiceUnavailable, "gateway disabled")
		return
	}
	s.legado.CreateToken(w, r, id.username)
}

func (s *Server) handleLegadoTokenList(w http.ResponseWriter, r *http.Request, id identity) {
	if s.legado == nil {
		writeErr(w, http.StatusServiceUnavailable, "gateway disabled")
		return
	}
	s.legado.ListTokens(w, r, id.username)
}

func (s *Server) handleLegadoTokenRevoke(w http.ResponseWriter, r *http.Request, id identity) {
	if s.legado == nil {
		writeErr(w, http.StatusServiceUnavailable, "gateway disabled")
		return
	}
	s.legado.RevokeToken(w, r, id.username)
}

// ---- Legado 网关 ----

func (s *Server) handleLegadoLogin(w http.ResponseWriter, r *http.Request) {
	if s.legado == nil {
		writeErr(w, http.StatusServiceUnavailable, "gateway disabled")
		return
	}
	if !isJSONContentType(r.Header.Get("Content-Type")) {
		writeErr(w, http.StatusUnsupportedMediaType, "application/json required")
		return
	}
	ip := s.clientIP(r)
	if !s.limiter.Allow(ip) {
		s.audit.Record(r.Context(), "", ip, "legado_login_ratelimited", "", "ratelimited", "")
		writeErr(w, http.StatusTooManyRequests, "too many attempts, try later")
		return
	}
	s.legado.Login(w, r)
}

func (s *Server) handleLegadoLogout(w http.ResponseWriter, r *http.Request) {
	if s.legado == nil {
		writeErr(w, http.StatusServiceUnavailable, "gateway disabled")
		return
	}
	token := gatewaySessionToken(r)
	sess, _ := s.sessions.LookupSessionScoped(r.Context(), token, auth.ScopeLegado)
	username := ""
	if sess != nil {
		username = sess.Username
	}
	s.legado.Logout(w, r, username, token)
}

// gatewayAuth 是网关的双层认证：网关会话 + 书源令牌缺一不可。
// 令牌单独存在不能绕过会话；网关会话也不能用于 /api/*（scope 不同）。
func (s *Server) gatewayAuth(next func(w http.ResponseWriter, r *http.Request, id identity)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.legado == nil {
			writeErr(w, http.StatusServiceUnavailable, "gateway disabled")
			return
		}
		sessToken := gatewaySessionToken(r)
		if sessToken == "" {
			writeErr(w, http.StatusUnauthorized, "gateway session required")
			return
		}
		sess, err := s.sessions.LookupSessionScoped(r.Context(), sessToken, auth.ScopeLegado)
		if err != nil || sess == nil {
			writeErr(w, http.StatusUnauthorized, "gateway session required")
			return
		}
		token := strings.TrimSpace(r.Header.Get("X-Legado-Token"))
		if token == "" {
			s.audit.Record(r.Context(), sess.Username, s.clientIP(r), "legado_gateway", "", "denied", "missing book source token")
			writeErr(w, http.StatusForbidden, "book source token required")
			return
		}
		owner, ok, err := s.legado.Tokens.Verify(r.Context(), token)
		if err != nil || !ok || owner != sess.Username {
			s.audit.Record(r.Context(), sess.Username, s.clientIP(r), "legado_gateway", "", "denied", "invalid book source token")
			writeErr(w, http.StatusForbidden, "invalid book source token")
			return
		}
		next(w, r, identity{
			username:  sess.Username,
			ip:        s.clientIP(r),
			sessionID: shortHash(sess.TokenHash),
		})
	}
}

// gatewaySessionToken 从 cookie 或 X-Legado-Session / Bearer 头部取网关会话。
func gatewaySessionToken(r *http.Request) string {
	if c, err := r.Cookie(auth.GatewayCookieName); err == nil && c.Value != "" {
		return c.Value
	}
	if v := strings.TrimSpace(r.Header.Get("X-Legado-Session")); v != "" {
		return v
	}
	if a := r.Header.Get("Authorization"); strings.HasPrefix(a, "Bearer ") {
		return strings.TrimSpace(strings.TrimPrefix(a, "Bearer "))
	}
	return ""
}

func (s *Server) handleLegadoSearch(w http.ResponseWriter, r *http.Request, id identity) {
	s.legado.SearchBooks(w, r, id.username)
}

func (s *Server) handleLegadoBook(w http.ResponseWriter, r *http.Request, id identity) {
	s.legado.Book(w, r, r.PathValue("id"), id.username)
}

func (s *Server) handleLegadoToc(w http.ResponseWriter, r *http.Request, id identity) {
	s.legado.Toc(w, r, r.PathValue("id"), id.username)
}

func (s *Server) handleLegadoContent(w http.ResponseWriter, r *http.Request, id identity) {
	idx, err := strconv.Atoi(r.PathValue("index"))
	if err != nil || idx < 0 {
		writeErr(w, http.StatusBadRequest, "invalid chapter index")
		return
	}
	s.legado.Content(w, r, r.PathValue("id"), idx, id.username)
}

func (s *Server) handleAuditLegacy(w http.ResponseWriter, r *http.Request, id identity) {
	limit := 100
	if l := r.URL.Query().Get("limit"); l != "" && len(l) <= 4 {
		if n, err := strconv.Atoi(l); err == nil && n >= 1 && n <= 1000 {
			limit = n
		}
	}
	entries, err := s.audit.Recent(r.Context(), limit)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	if entries == nil {
		entries = []audit.Entry{}
	}
	s.audit.Record(r.Context(), id.username, id.ip, "audit_view", "", "ok", "limit")
	writeJSON(w, http.StatusOK, entries)
}

// ---- helpers ----

func isJSONContentType(ct string) bool {
	if ct == "" {
		return false
	}
	semi := len(ct)
	for i, c := range ct {
		if c == ';' {
			semi = i
			break
		}
	}
	return ct[:semi] == "application/json"
}

func shortHash(h string) string {
	if len(h) > 12 {
		return h[:12]
	}
	return h
}

// writeJSON 输出 JSON。
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeErr 输出模糊化错误信息（不泄漏内部路径、SQL、密钥）。
func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
