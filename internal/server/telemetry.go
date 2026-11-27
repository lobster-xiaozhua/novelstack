package server

import (
	"compress/gzip"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"time"

	"novelstack/internal/telemetry"
)

// 第九阶段：全链路遥测的接收与控制台查询。
//
// 安全边界：
//   - 入口做白名单式清洗（internal/telemetry.Clean），正文/音频/凭证一律丢弃；
//   - 解压有上限，防 zip bomb；单批事件数有上限，防单请求打爆数据库；
//   - 控制台接口只返回已存字段，不触达书籍/音频任何原始数据。

const (
	maxTelemetryCompressed = 2 << 20 // 2MB 压缩体
	maxTelemetryPlain      = 8 << 20 // 8MB 解压后
)

// handleTelemetryBatch 处理 POST /api/telemetry/batch。
// 请求体：JSON（可 gzip 压缩，Content-Encoding: gzip）。
//
// 幂等性说明：客户端上传成功后会删除本地记录，因此这里不做去重；
// 极端情况下重复上报只会让同一条 Crash 的 occurrence 被多算一次，可接受。
func (s *Server) handleTelemetryBatch(w http.ResponseWriter, r *http.Request, id identity) {
	if s.telemetry == nil {
		writeErr(w, http.StatusServiceUnavailable, "telemetry disabled")
		return
	}
	var body io.Reader = http.MaxBytesReader(w, r.Body, maxTelemetryCompressed)
	gz := r.Header.Get("Content-Encoding") == "gzip" ||
		r.Header.Get("Content-Type") == "application/gzip"
	if gz {
		zr, err := gzip.NewReader(body)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "invalid gzip body")
			return
		}
		defer zr.Close()
		body = io.LimitReader(zr, maxTelemetryPlain)
	}

	dec := json.NewDecoder(body)
	dec.DisallowUnknownFields() // 客户端字段固定，未知字段直接拒绝，避免夹带
	var env telemetry.Envelope
	if err := dec.Decode(&env); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json body")
		return
	}
	if env.DeviceID == "" {
		writeErr(w, http.StatusBadRequest, "device_id required")
		return
	}
	if len(env.Events) == 0 {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "accepted": 0, "dropped": 0})
		return
	}

	cleaned := telemetry.Clean(env, time.Now())
	n, err := s.telemetry.InsertBatch(r.Context(), id.username, cleaned, time.Now())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	s.audit.Record(r.Context(), id.username, id.ip, "telemetry_batch", env.DeviceID, "ok",
		strconv.Itoa(n)+" events")
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "accepted": n, "dropped": cleaned.Dropped,
	})
}

// ---- 控制台查询 ----

func (s *Server) telemetrySince(r *http.Request) time.Time {
	days, _ := strconv.Atoi(r.URL.Query().Get("days"))
	if days <= 0 || days > 90 {
		days = 7
	}
	return time.Now().Add(-time.Duration(days) * 24 * time.Hour)
}

// handleAdminTelemetryOverview 处理 GET /api/admin/telemetry/overview。
func (s *Server) handleAdminTelemetryOverview(w http.ResponseWriter, r *http.Request) {
	o, err := s.telemetry.Overview(r.Context(), s.telemetrySince(r))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, o)
}

func (s *Server) handleAdminTelemetryHeatmap(w http.ResponseWriter, r *http.Request) {
	cells, err := s.telemetry.Heatmap(r.Context(), s.telemetrySince(r))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	if cells == nil {
		cells = []telemetry.HeatCell{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"cells": cells})
}

func (s *Server) handleAdminTelemetrySlowChapters(w http.ResponseWriter, r *http.Request) {
	rows, err := s.telemetry.SlowChapters(r.Context(), s.telemetrySince(r), 20)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	if rows == nil {
		rows = []telemetry.SlowChapter{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": rows})
}

func (s *Server) handleAdminTelemetryCrashes(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	rows, err := s.telemetry.Crashes(r.Context(), s.telemetrySince(r), limit)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	if rows == nil {
		rows = []telemetry.CrashRow{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": rows})
}

func (s *Server) handleAdminTelemetrySeries(w http.ResponseWriter, r *http.Request) {
	kind := r.URL.Query().Get("kind")
	rows, err := s.telemetry.Series(r.Context(), s.telemetrySince(r), kind)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	if rows == nil {
		rows = []telemetry.RollupRow{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": rows})
}
