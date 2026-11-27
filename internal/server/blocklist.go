package server

import (
	"net/http"
	"net/url"
	"strings"

	"novelstack/internal/blocklist"
)

// handleBlocklistList 返回 IP 黑名单（含已解封/过期条目，便于追溯）。
func (s *Server) handleBlocklistList(w http.ResponseWriter, r *http.Request, id identity) {
	if s.blocklist == nil {
		writeErr(w, http.StatusServiceUnavailable, "blocklist disabled")
		return
	}
	limit := 100
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" && len(raw) <= 4 {
		if n, ok := parsePositive(raw); ok && n <= 500 {
			limit = n
		}
	}
	entries, err := s.blocklist.List(r.Context(), limit)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	if entries == nil {
		entries = []blocklist.Entry{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"entries": entries,
		"enabled": s.blocklist.Enabled(),
	})
}

// handleBlocklistUnblock 手动解封指定 IP，写审计日志。
func (s *Server) handleBlocklistUnblock(w http.ResponseWriter, r *http.Request, id identity) {
	if s.blocklist == nil {
		writeErr(w, http.StatusServiceUnavailable, "blocklist disabled")
		return
	}
	ip, err := url.PathUnescape(r.PathValue("ip"))
	if err != nil || strings.TrimSpace(ip) == "" {
		writeErr(w, http.StatusBadRequest, "invalid ip")
		return
	}
	ip = strings.TrimSpace(ip)
	if err := s.blocklist.Unblock(r.Context(), ip); err != nil {
		s.audit.Record(r.Context(), id.username, id.ip, "ip_unblocked", "", "error", "db error")
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	s.audit.Record(r.Context(), id.username, id.ip, "ip_unblocked", "", "ok", "ip="+ip)
	writeJSON(w, http.StatusOK, map[string]any{"status": "unblocked", "ip": ip})
}

func parsePositive(s string) (int, bool) {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, false
		}
		n = n*10 + int(c-'0')
	}
	return n, n > 0
}
