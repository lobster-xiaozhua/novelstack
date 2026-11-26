package server

import (
	"context"
	"encoding/json"
	"io"
	"io/fs"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"novelstack/internal/adminui"
	"novelstack/internal/audit"
	"novelstack/internal/auth"
	"novelstack/internal/dashboard"
	"novelstack/internal/devices"
	"novelstack/internal/tts"
)

// registerAdmin 注册控制台页面与只读运营接口。
func (s *Server) registerAdmin() {
	// 页面与静态资源
	s.mux.HandleFunc("GET /admin", s.handleAdminIndex)
	s.mux.HandleFunc("GET /admin/", s.handleAdminIndex)
	s.mux.HandleFunc("GET /admin/login", s.handleAdminLogin)
	s.mux.HandleFunc("GET /admin/assets/{path...}", s.handleAdminAsset)
	// SPA 路由：/admin/overview 等由前端路由接管（更具体的 assets/login 优先匹配）。
	s.mux.HandleFunc("GET /admin/{page}", s.handleAdminIndex)

	// 只读聚合指标（一个请求返回全部）。控制台一律 session-only：
	// device token 调用会得到 403，避免手机端凭证获得运营面权限。
	s.mux.HandleFunc("GET /api/admin/dashboard", s.requireSession(s.handleDashboard))
	// 手动触发一次自动备份（仍需浏览器会话；device token 403）
	s.mux.HandleFunc("POST /api/admin/backup/run", s.requireSession(s.handleBackupRun))
	// 缓存治理是允许写操作的入口
	s.mux.HandleFunc("POST /api/admin/cache/clear", s.requireSession(s.handleCacheClear))
	s.mux.HandleFunc("POST /api/admin/cache/prune", s.requireSession(s.handleCachePrune))
}

// adminLoggedIn 只校验管理会话（scope=api）。
func (s *Server) adminLoggedIn(r *http.Request) bool {
	c, err := r.Cookie(auth.SessionCookieName)
	if err != nil || c.Value == "" {
		return false
	}
	sess, err := s.sessions.LookupSession(r.Context(), c.Value)
	return err == nil && sess != nil
}

// handleAdminIndex 返回 SPA 入口；未登录跳转登录页。
func (s *Server) handleAdminIndex(w http.ResponseWriter, r *http.Request) {
	// device token 访问控制台明确 403（前端据此提示「请用浏览器登录」），
	// 而不是 303 到登录页——后者会让客户端误以为重新登录即可。
	if tok := bearerToken(r); strings.HasPrefix(tok, devices.TokenPrefix) {
		writeErr(w, http.StatusForbidden, "device token cannot access admin console")
		return
	}
	if !s.adminLoggedIn(r) {
		http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
		return
	}
	s.serveAdminFile(w, r, "index.html")
}

// handleAdminLogin 提供不依赖前端构建的极简登录页。
func (s *Server) handleAdminLogin(w http.ResponseWriter, r *http.Request) {
	if s.adminLoggedIn(r) {
		http.Redirect(w, r, "/admin", http.StatusSeeOther)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, adminLoginHTML)
}

// handleAdminAsset 提供构建产物（JS/CSS/字体）。资源本身不含数据，允许未登录加载。
func (s *Server) handleAdminAsset(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("path")
	if name == "" {
		http.NotFound(w, r)
		return
	}
	sub, err := adminui.FS()
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if _, err := fs.Stat(sub, "assets/"+name); err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", "public, max-age=3600")
	http.StripPrefix("/admin/", http.FileServer(http.FS(sub))).ServeHTTP(w, r)
}

// serveAdminFile 从内嵌产物读取单个文件。
func (s *Server) serveAdminFile(w http.ResponseWriter, r *http.Request, name string) {
	sub, err := adminui.FS()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "console not built")
		return
	}
	raw, err := fs.ReadFile(sub, name)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "console not built")
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(raw)
}

// handleDashboard 聚合只读指标。响应结构固定，不含正文、查询词或密钥。
func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request, id identity) {
	marker, backupDir := "", ""
	if s.cfg != nil {
		marker = s.cfg.KekOfflineMarker
		backupDir = s.cfg.BackupDir
	}
	resp, err := dashboard.Build(r.Context(), dashboard.Deps{
		DB:               s.db,
		Books:            s.books,
		TTS:              s.tts,
		Search:           s.search,
		Audit:            s.audit,
		LegadoEnabled:    s.legado != nil,
		KekOfflineMarker: marker,
		BackupDir:        backupDir,
	})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	// webhook 异步投递；判定本身已在 dashboard.Build 完成，横幅不等待网络。
	if s.health != nil && s.health.Enabled() {
		go s.health.Notify(context.Background(), resp.Health)
	}
	// 不记录 dashboard_view：控制台每 15s 轮询一次，写审计会把真正的运维动作淹没
	// （登录/上传/备份/轮换等已各自留痕）。
	writeJSON(w, http.StatusOK, resp)
}

// handleBackupRun 立即执行一次与定时任务完全相同的备份（session-only）。
func (s *Server) handleBackupRun(w http.ResponseWriter, r *http.Request, id identity) {
	if s.scheduler == nil {
		writeErr(w, http.StatusServiceUnavailable, "auto backup is not configured")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Minute)
	defer cancel()
	res, err := s.scheduler.RunOnce(ctx)
	if err != nil {
		// 细节只写审计 backup_failed，不回显到 HTTP 响应。
		writeErr(w, http.StatusInternalServerError, "backup failed; see audit backup_failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "path": filepath.Base(res.Path), "sha256": res.Checksum,
		"size_bytes": res.SizeBytes, "duration_ms": res.DurationMS,
	})
}

// cacheClearRequest 是缓存清理请求体。
type cacheClearRequest struct {
	BookID  string `json:"book_id"`
	All     bool   `json:"all"`
	Confirm bool   `json:"confirm"`
}

// handleCacheClear 按书或全量清理 TTS 缓存；必须显式 confirm，且写审计。
func (s *Server) handleCacheClear(w http.ResponseWriter, r *http.Request, id identity) {
	if s.tts == nil {
		writeErr(w, http.StatusServiceUnavailable, "tts not configured")
		return
	}
	var req cacheClearRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json body")
		return
	}
	if !req.Confirm {
		writeErr(w, http.StatusBadRequest, "confirm=true required")
		return
	}
	if !req.All && strings.TrimSpace(req.BookID) == "" {
		writeErr(w, http.StatusBadRequest, "book_id or all=true required")
		return
	}
	if req.BookID != "" {
		if _, err := s.books.Store.GetOwned(r.Context(), req.BookID, id.username, true); err != nil {
			writeErr(w, http.StatusNotFound, "not found")
			return
		}
	}
	scopeID := ""
	if !req.All {
		scopeID = req.BookID
	}
	res, err := s.tts.ClearCache(r.Context(), scopeID)
	if err != nil {
		s.audit.Record(r.Context(), id.username, id.ip, "cache_cleared", scopeID, "error", "clear failed")
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	s.audit.Record(r.Context(), id.username, id.ip, "cache_cleared", scopeID, "ok",
		"removed="+strconv.Itoa(res.Removed)+" freed="+strconv.FormatInt(res.FreedBytes, 10))
	writeJSON(w, http.StatusOK, res)
}

// cachePruneRequest 是缓存淘汰请求体。
type cachePruneRequest struct {
	MaxBytes int64 `json:"max_bytes"`
	KeepDays int   `json:"keep_days"`
	Confirm  bool  `json:"confirm"`
	DryRun   bool  `json:"dry_run"`
}

// handleCachePrune 按上限/保留天数淘汰缓存；最近访问与正在朗读的片段受保护。
func (s *Server) handleCachePrune(w http.ResponseWriter, r *http.Request, id identity) {
	if s.tts == nil {
		writeErr(w, http.StatusServiceUnavailable, "tts not configured")
		return
	}
	var req cachePruneRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json body")
		return
	}
	if !req.Confirm && !req.DryRun {
		writeErr(w, http.StatusBadRequest, "confirm=true required")
		return
	}
	if req.MaxBytes == 0 {
		req.MaxBytes = s.cfg.CacheMaxBytes
	}
	if req.KeepDays == 0 {
		req.KeepDays = s.cfg.CacheKeepDays
	}
	if req.MaxBytes <= 0 && req.KeepDays <= 0 {
		writeErr(w, http.StatusBadRequest, "max_bytes or keep_days required")
		return
	}
	res, err := s.tts.PruneCache(r.Context(), tts.PrunePolicy{
		MaxBytes: req.MaxBytes, KeepDays: req.KeepDays, DryRun: req.DryRun,
	})
	if err != nil {
		s.audit.Record(r.Context(), id.username, id.ip, "cache_pruned", "", "error", "prune failed")
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	result := "ok"
	if req.DryRun {
		result = "dry_run"
	}
	s.audit.Record(r.Context(), id.username, id.ip, "cache_pruned", "", result,
		"removed="+strconv.Itoa(res.Removed)+" freed="+strconv.FormatInt(res.FreedBytes, 10))
	writeJSON(w, http.StatusOK, res)
}

// handleAuditFiltered 在原有接口上支持 action/result/before_id 过滤。
func (s *Server) handleAuditFiltered(w http.ResponseWriter, r *http.Request, id identity) {
	q := r.URL.Query()
	limit := 100
	if l := q.Get("limit"); l != "" && len(l) <= 4 {
		if n, err := strconv.Atoi(l); err == nil && n >= 1 && n <= 1000 {
			limit = n
		}
	}
	var before int64
	if b := q.Get("before_id"); b != "" {
		if n, err := strconv.ParseInt(b, 10, 64); err == nil && n > 0 {
			before = n
		}
	}
	action := strings.TrimSpace(q.Get("action"))
	if len(action) > 64 {
		action = action[:64]
	}
	entries, err := s.audit.RecentFiltered(r.Context(), action, strings.TrimSpace(q.Get("result")), before, limit)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	if entries == nil {
		entries = []audit.Entry{}
	}
	s.audit.Record(r.Context(), id.username, id.ip, "audit_view", "", "ok", "limit="+strconv.Itoa(limit))
	writeJSON(w, http.StatusOK, entries)
}

const adminLoginHTML = `<!doctype html>
<html lang="zh-CN">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="robots" content="noindex">
<title>novelstack · 登录</title>
<style>
  :root { color-scheme: dark; }
  * { box-sizing: border-box; }
  body { margin: 0; min-height: 100vh; display: grid; place-items: center;
         background: #0b0d10; color: #e6e8eb;
         font: 15px/1.5 ui-sans-serif, -apple-system, "Segoe UI", "Noto Sans SC", sans-serif; }
  .card { width: min(380px, calc(100vw - 32px)); padding: 28px; border-radius: 14px;
          background: #14171c; border: 1px solid #232a33; box-shadow: 0 20px 60px rgba(0,0,0,.45); }
  h1 { margin: 0 0 4px; font-size: 18px; letter-spacing: .2px; }
  p.sub { margin: 0 0 22px; color: #8b949e; font-size: 13px; }
  label { display: block; margin: 14px 0 6px; font-size: 12px; color: #a9b1ba; letter-spacing: .04em; text-transform: uppercase; }
  input { width: 100%; padding: 10px 12px; border-radius: 8px; border: 1px solid #2c343e;
          background: #0f1216; color: #e6e8eb; font-size: 14px; outline: none; }
  input:focus { border-color: #4c8dff; box-shadow: 0 0 0 3px rgba(76,141,255,.15); }
  button { width: 100%; margin-top: 20px; padding: 11px; border: 0; border-radius: 8px;
           background: #4c8dff; color: #06101f; font-weight: 650; font-size: 14px; cursor: pointer; }
  button:disabled { opacity: .6; cursor: progress; }
  .err { margin-top: 14px; padding: 9px 11px; border-radius: 8px; font-size: 13px;
         background: rgba(255,87,87,.1); border: 1px solid rgba(255,87,87,.35); color: #ff9d9d; display: none; }
  .foot { margin-top: 18px; font-size: 12px; color: #6e7681; }
</style>
</head>
<body>
<form class="card" id="f" autocomplete="on">
  <h1>novelstack</h1>
  <p class="sub">私人阅读站 · 运营控制台</p>
  <label for="u">用户名</label>
  <input id="u" name="username" autocomplete="username" required autofocus>
  <label for="p">口令</label>
  <input id="p" name="password" type="password" autocomplete="current-password" required>
  <button id="b" type="submit">登录</button>
  <div class="err" id="e"></div>
  <div class="foot">会话 Cookie 为 HttpOnly + SameSite=Strict，24 小时过期。</div>
</form>
<script>
document.getElementById('f').addEventListener('submit', async (ev) => {
  ev.preventDefault();
  const b = document.getElementById('b'), e = document.getElementById('e');
  b.disabled = true; e.style.display = 'none';
  try {
    const res = await fetch('/api/login', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        username: document.getElementById('u').value,
        password: document.getElementById('p').value
      })
    });
    if (res.ok) { location.replace('/admin'); return; }
    e.textContent = res.status === 429 ? '尝试过于频繁，请稍后再试。' : '用户名或口令错误。';
    e.style.display = 'block';
  } catch (_) {
    e.textContent = '网络错误，请重试。'; e.style.display = 'block';
  } finally { b.disabled = false; }
});
</script>
</body>
</html>`
