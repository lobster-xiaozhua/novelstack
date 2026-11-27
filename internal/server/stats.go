package server

import (
	"net/http"
)

// handleReadingStats 处理 GET /api/stats/reading。
//
// 返回今日/累计阅读时长（秒）、连续阅读天数与最近 90 天序列。
// 只含数字与日期，不含书名、正文或设备信息。
func (s *Server) handleReadingStats(w http.ResponseWriter, r *http.Request, id identity) {
	if s.stats == nil {
		writeErr(w, http.StatusServiceUnavailable, "stats not available")
		return
	}
	ov, err := s.stats.Overview(r.Context(), id.username)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, ov)
}
