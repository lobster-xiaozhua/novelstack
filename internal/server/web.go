package server

import (
	_ "embed"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"strings"

	"novelstack/internal/adminui"
)

//go:embed loginpage.html
var loginPageHTML string

// registerUserWeb 注册用户端 SPA 与静态资源。
//
// 设计要点：
//   - 用户端与管理端共用一份 index.html，靠前端路由前缀切换布局；
//   - 静态资源统一挂在 /assets/*（vite base 已改为 /）；
//   - 未登录访问任何用户页都 303 到 /login?next=...，登录后原路返回。
func (s *Server) registerUserWeb() {
	s.mux.HandleFunc("GET /{$}", s.handleUserIndex)
	s.mux.HandleFunc("GET /library", s.handleUserIndex)
	s.mux.HandleFunc("GET /discover", s.handleUserIndex)
	s.mux.HandleFunc("GET /history", s.handleUserIndex)
	s.mux.HandleFunc("GET /settings", s.handleUserIndex)
	s.mux.HandleFunc("GET /read/{id}", s.handleUserIndex)
	s.mux.HandleFunc("GET /login", s.handleUserLogin)
	s.mux.HandleFunc("GET /assets/{path...}", s.handlePublicAsset)
}

// handleUserIndex 返回用户端 SPA；未登录跳登录页并带回跳地址。
func (s *Server) handleUserIndex(w http.ResponseWriter, r *http.Request) {
	if !s.adminLoggedIn(r) {
		next := r.URL.Path
		if r.URL.RawQuery != "" {
			next += "?" + r.URL.RawQuery
		}
		http.Redirect(w, r, "/login?next="+url.QueryEscape(next), http.StatusSeeOther)
		return
	}
	s.serveAdminFile(w, r, "index.html")
}

// handleUserLogin 提供用户端登录页（与管理端共用同一模板，仅默认回跳不同）。
func (s *Server) handleUserLogin(w http.ResponseWriter, r *http.Request) {
	if s.adminLoggedIn(r) {
		http.Redirect(w, r, "/library", http.StatusSeeOther)
		return
	}
	s.serveLoginPage(w, "/library")
}

// serveLoginPage 输出统一登录页。defaultNext 既决定默认回跳地址，
// 也决定副标题语义（控制台 / 书架），避免同一模板在两个入口自相矛盾。
func (s *Server) serveLoginPage(w http.ResponseWriter, defaultNext string) {
	area := "私人阅读站"
	if strings.HasPrefix(defaultNext, "/admin") {
		area = "运营控制台"
	}
	page := strings.ReplaceAll(loginPageHTML, "__AREA__", area)
	page = strings.ReplaceAll(page, "__NEXT__", defaultNext)

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, page)
}

// handlePublicAsset 提供构建产物（JS/CSS）。资源不含数据，未登录也可加载，
// 这样登录页与离线 shell 都能正常启动。
func (s *Server) handlePublicAsset(w http.ResponseWriter, r *http.Request) {
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
	http.StripPrefix("/", http.FileServer(http.FS(sub))).ServeHTTP(w, r)
}
