package server

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"novelstack/internal/auth"
	"novelstack/internal/books"
	"novelstack/internal/config"
	"novelstack/internal/devices"
	"novelstack/internal/mobile"
)

// 本文件实现第五阶段（移动端）的全部 HTTP 端点：
//
//	POST   /api/device/token                    用户名密码或配对码换 device token
//	POST   /api/admin/pairing                   生成一次性配对码（仅 session）
//	GET    /api/admin/devices                   设备列表（仅 session）
//	DELETE /api/admin/devices/{id}              撤销设备（仅 session）
//	GET    /api/sync/manifest?since=<cursor>    增量同步清单
//	PUT    /api/books/{id}/progress             上报阅读进度（最后写入胜）
//	GET    /api/books/{id}/progress             读取阅读进度
//	POST   /api/books/{id}/chapters/prefetch    批量预取章节密文（限流）
//	GET    /api/books/{id}/chapters/{i}/audio   该章音频段元信息（设备包装 DEK）
//	POST   /api/books/{id}/chapters/{i}/audio/prepare  合成缺失音频段（加密落盘）
//	GET    /api/tts/segments/{key}              NSAC 加密音频文件原文
//
// 所有这些端点返回的都是密文或元数据；没有任何一个返回正文明文。

func prefetchPerMinute(c *config.Config) int {
	if c == nil || c.PrefetchPerMinute <= 0 {
		return 40
	}
	return c.PrefetchPerMinute
}

func prefetchMaxChapters(c *config.Config) int {
	if c == nil || c.PrefetchMaxChapters <= 0 {
		return 8
	}
	return c.PrefetchMaxChapters
}

func prefetchMaxBytes(c *config.Config) int64 {
	if c == nil || c.PrefetchMaxBytes <= 0 {
		return 8 << 20
	}
	return c.PrefetchMaxBytes
}

func deviceTTL(c *config.Config) time.Duration {
	if c == nil || c.DeviceTokenTTL <= 0 {
		return 180 * 24 * time.Hour
	}
	return c.DeviceTokenTTL
}

// deviceTokenRequest 是换取 device token 的请求体。
type deviceTokenRequest struct {
	Username    string `json:"username"`
	Password    string `json:"password"`
	PairingCode string `json:"pairing_code"`
	DeviceName  string `json:"device_name"`
	// PublicKey 是设备 RSA 公钥（SPKI PEM）。移动端必须提供；
	// 不提供则只能同步元数据，无法解密章节。
	PublicKey string `json:"public_key"`
}

// handleDeviceToken 用用户名密码或一次性配对码换取长期 device token。
// 返回的 token 只在此响应出现一次；服务端只存 SHA-256。
func (s *Server) handleDeviceToken(w http.ResponseWriter, r *http.Request) {
	if s.devices == nil {
		writeErr(w, http.StatusServiceUnavailable, "device tokens not configured")
		return
	}
	if !isJSONContentType(r.Header.Get("Content-Type")) {
		writeErr(w, http.StatusUnsupportedMediaType, "application/json required")
		return
	}
	ip := s.clientIP(r)
	if !s.limiter.Allow(ip) {
		s.audit.Record(r.Context(), "", ip, "device_token_denied", "", "ratelimited", "too many attempts")
		writeErr(w, http.StatusTooManyRequests, "too many attempts")
		return
	}
	var req deviceTokenRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json body")
		return
	}

	var owner string
	var ok bool
	switch {
	case strings.TrimSpace(req.PairingCode) != "":
		o, err := s.devices.RedeemPairing(r.Context(), req.PairingCode)
		if err != nil {
			s.audit.Record(r.Context(), "", ip, "device_token_denied", "", "denied", "invalid pairing code")
			writeErr(w, http.StatusUnauthorized, "invalid or expired pairing code")
			return
		}
		owner, ok = o, true
	case strings.TrimSpace(req.Username) != "":
		u := strings.TrimSpace(req.Username)
		hash, err := s.sessions.PasswordHash(r.Context(), u)
		if err != nil {
			// 用户不存在与密码错误返回同一状态，避免枚举。
			s.audit.Record(r.Context(), u, ip, "device_token_denied", "", "denied", "bad credentials")
			writeErr(w, http.StatusUnauthorized, "invalid credentials")
			return
		}
		match, err := auth.VerifyPassword(req.Password, hash)
		if err != nil || !match {
			s.audit.Record(r.Context(), u, ip, "device_token_denied", "", "denied", "bad credentials")
			writeErr(w, http.StatusUnauthorized, "invalid credentials")
			return
		}
		owner, ok = u, true
	default:
		writeErr(w, http.StatusBadRequest, "username/password or pairing_code required")
		return
	}
	if !ok || owner == "" {
		writeErr(w, http.StatusUnauthorized, "invalid credentials")
		return
	}

	token, dev, err := s.devices.Create(r.Context(), owner, req.DeviceName, req.PublicKey, deviceTTL(s.cfg))
	if err != nil {
		if errors.Is(err, devices.ErrBadPubKey) {
			writeErr(w, http.StatusBadRequest, "invalid public_key (RSA 2048+ SPKI PEM required)")
			return
		}
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	s.audit.Record(r.Context(), owner, ip, "device_token_created", "", "ok",
		fmt.Sprintf("device=%s name=%s key=%t", dev.ID, dev.Name, dev.HasKey))

	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{
		"token":       token,
		"token_type":  "Bearer",
		"device":      dev,
		"server_time": time.Now().UTC().Format(time.RFC3339Nano),
		"expires_at":  dev.ExpiresAt,
	})
}

// handlePairingCreate 生成一次性配对码（手机输入短码即可配对）。
func (s *Server) handlePairingCreate(w http.ResponseWriter, r *http.Request, id identity) {
	if s.devices == nil {
		writeErr(w, http.StatusServiceUnavailable, "device tokens not configured")
		return
	}
	code, err := s.devices.CreatePairing(r.Context(), id.username, 10*time.Minute)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	s.audit.Record(r.Context(), id.username, id.ip, "pairing_created", "", "ok",
		fmt.Sprintf("expires_in=%ds", code.ExpiresIn))
	writeJSON(w, http.StatusOK, code)
}

// handleDeviceList 返回设备列表（含已撤销）。
func (s *Server) handleDeviceList(w http.ResponseWriter, r *http.Request, id identity) {
	if s.devices == nil {
		writeErr(w, http.StatusServiceUnavailable, "device tokens not configured")
		return
	}
	list, err := s.devices.List(r.Context(), id.username)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	active, revoked, _ := s.devices.Count(r.Context(), id.username)
	writeJSON(w, http.StatusOK, map[string]any{
		"devices": list, "active": active, "revoked": revoked,
	})
}

// handleDeviceRevoke 撤销设备；撤销后该 token 立即 401。
func (s *Server) handleDeviceRevoke(w http.ResponseWriter, r *http.Request, id identity) {
	if s.devices == nil {
		writeErr(w, http.StatusServiceUnavailable, "device tokens not configured")
		return
	}
	devID := r.PathValue("id")
	ok, err := s.devices.Revoke(r.Context(), id.username, devID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	if !ok {
		writeErr(w, http.StatusNotFound, "device not found")
		return
	}
	s.audit.Record(r.Context(), id.username, id.ip, "device_revoked", "", "ok", "device="+devID)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "device_id": devID})
}

// handleSyncManifest 返回 since 之后的增量（书籍元数据 + 章节 ETag + 设备包装的 DEK）。
func (s *Server) handleSyncManifest(w http.ResponseWriter, r *http.Request, id identity) {
	if s.db == nil {
		writeErr(w, http.StatusServiceUnavailable, "database not ready")
		return
	}
	var since int64
	if v := r.URL.Query().Get("since"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n < 0 {
			writeErr(w, http.StatusBadRequest, "invalid since cursor")
			return
		}
		since = n
	}

	var wrap mobile.WrapFunc
	if id.deviceKey != nil {
		pub := id.deviceKey
		wrap = func(bookID string) (string, error) {
			return s.books.WrapDEKFor(r.Context(), bookID, id.username, func(dek []byte) (string, error) {
				return devices.WrapDEK(pub, dek)
			})
		}
	}
	m, err := mobile.BuildManifest(r.Context(), s.db, id.username, since, wrap)
	if err != nil {
		if errors.Is(err, books.ErrKeyVersion) {
			writeErr(w, http.StatusServiceUnavailable, "kek version not available")
			return
		}
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	s.audit.Record(r.Context(), id.username, id.ip, "sync_manifest", "", "ok",
		fmt.Sprintf("since=%d books=%d deleted=%d full=%t", since, m.Count, len(m.Deleted), m.Full))
	writeJSON(w, http.StatusOK, m)
}

// handleProgressPut 写入阅读进度（最后写入胜）。
func (s *Server) handleProgressPut(w http.ResponseWriter, r *http.Request, id identity) {
	if !isJSONContentType(r.Header.Get("Content-Type")) {
		writeErr(w, http.StatusUnsupportedMediaType, "application/json required")
		return
	}
	var req struct {
		ChapterIndex int     `json:"chapter_index"`
		CharOffset   int     `json:"char_offset"`
		Percent      float64 `json:"percent"`
		DeviceID     string  `json:"device_id"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json body")
		return
	}
	deviceID := id.deviceID
	if deviceID == "" {
		deviceID = req.DeviceID
	}
	if len(deviceID) > 64 {
		deviceID = deviceID[:64]
	}
	p, err := mobile.PutProgress(r.Context(), s.db, id.username, r.PathValue("id"), mobile.Progress{
		ChapterIndex: req.ChapterIndex,
		CharOffset:   req.CharOffset,
		Percent:      req.Percent,
	}, deviceID)
	if err != nil {
		switch {
		case errors.Is(err, mobile.ErrProgressInvalid):
			writeErr(w, http.StatusBadRequest, "invalid progress fields")
		case errors.Is(err, books.ErrNotFound):
			writeErr(w, http.StatusNotFound, "book not found")
		default:
			writeErr(w, http.StatusInternalServerError, "internal error")
		}
		return
	}
	s.audit.Record(r.Context(), id.username, id.ip, "progress_updated", p.BookID, "ok",
		fmt.Sprintf("chapter=%d offset=%d percent=%.3f", p.ChapterIndex, p.CharOffset, p.Percent))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "progress": p})
}

// handleProgressGet 读取进度；无记录时 404（客户端据此从头开始）。
func (s *Server) handleProgressGet(w http.ResponseWriter, r *http.Request, id identity) {
	p, err := mobile.GetProgress(r.Context(), s.db, id.username, r.PathValue("id"))
	if err != nil {
		if errors.Is(err, books.ErrNotFound) {
			writeErr(w, http.StatusNotFound, "no progress")
			return
		}
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, p)
}

// handlePrefetch 批量返回章节密文包（NSCH）。单次章节数与响应字节都有上限。
func (s *Server) handlePrefetch(w http.ResponseWriter, r *http.Request, id identity) {
	if !isJSONContentType(r.Header.Get("Content-Type")) {
		writeErr(w, http.StatusUnsupportedMediaType, "application/json required")
		return
	}
	key := "s:" + id.username
	if id.deviceID != "" {
		key = "d:" + id.deviceID
	}
	if ok, wait := s.prefetch.Allow(key); !ok {
		w.Header().Set("Retry-After", strconv.Itoa(int(wait.Seconds())+1))
		s.audit.Record(r.Context(), id.username, id.ip, "chapter_prefetch", r.PathValue("id"), "ratelimited", "")
		writeErr(w, http.StatusTooManyRequests, "prefetch rate limit exceeded")
		return
	}
	var req struct {
		Indexes []int `json:"indexes"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json body")
		return
	}
	maxCh := prefetchMaxChapters(s.cfg)
	if len(req.Indexes) == 0 {
		writeErr(w, http.StatusBadRequest, "indexes required")
		return
	}
	if len(req.Indexes) > maxCh {
		writeErr(w, http.StatusBadRequest,
			fmt.Sprintf("too many chapters in one request (max %d)", maxCh))
		return
	}
	bookID := r.PathValue("id")
	sealed, err := s.books.SealChapters(r.Context(), bookID, id.username, req.Indexes, maxCh)
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
		Bytes      int    `json:"bytes"`
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
			Index: sc.Index, Title: sc.Title, ETag: sc.ETag, Bytes: len(sc.Package),
			Ciphertext: base64.StdEncoding.EncodeToString(sc.Package),
		})
	}

	wrapped := ""
	if id.deviceKey != nil {
		wrapped, err = s.books.WrapDEKFor(r.Context(), bookID, id.username, func(dek []byte) (string, error) {
			return devices.WrapDEK(id.deviceKey, dek)
		})
		if err != nil {
			if errors.Is(err, books.ErrKeyVersion) {
				writeErr(w, http.StatusServiceUnavailable, "kek version not available")
				return
			}
			writeErr(w, http.StatusInternalServerError, "internal error")
			return
		}
	}
	s.audit.Record(r.Context(), id.username, id.ip, "chapter_prefetch", bookID, "ok",
		fmt.Sprintf("chapters=%d bytes=%d truncated=%t", len(out), total, truncated))
	writeJSON(w, http.StatusOK, map[string]any{
		"book_id":     bookID,
		"count":       len(out),
		"bytes":       total,
		"truncated":   truncated,
		"wrapped_dek": wrapped,
		"chapters":    out,
	})
}

// handleAudioManifest 返回该章音频段元信息（每段附设备包装的音频 DEK）。
func (s *Server) handleAudioManifest(w http.ResponseWriter, r *http.Request, id identity) {
	if s.tts == nil {
		writeErr(w, http.StatusServiceUnavailable, "tts not configured")
		return
	}
	idx, err := strconv.Atoi(r.PathValue("index"))
	if err != nil || idx < 0 {
		writeErr(w, http.StatusBadRequest, "invalid chapter index")
		return
	}
	var wrap func([]byte) (string, error)
	if id.deviceKey != nil {
		wrap = func(dek []byte) (string, error) { return devices.WrapDEK(id.deviceKey, dek) }
	}
	m, err := s.tts.AudioSegments(r.Context(), s.db, r.PathValue("id"), idx, id.username, wrap)
	if err != nil {
		if errors.Is(err, books.ErrNotFound) {
			writeErr(w, http.StatusNotFound, "book not found")
			return
		}
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, m)
}

// handleAudioPrepare 合成该章缺失的音频段（加密落盘），供离线播放。
func (s *Server) handleAudioPrepare(w http.ResponseWriter, r *http.Request, id identity) {
	if s.tts == nil {
		writeErr(w, http.StatusServiceUnavailable, "tts not configured")
		return
	}
	idx, err := strconv.Atoi(r.PathValue("index"))
	if err != nil || idx < 0 {
		writeErr(w, http.StatusBadRequest, "invalid chapter index")
		return
	}
	key := "s:" + id.username
	if id.deviceID != "" {
		key = "d:" + id.deviceID
	}
	if ok, wait := s.audioGen.Allow(key); !ok {
		w.Header().Set("Retry-After", strconv.Itoa(int(wait.Seconds())+1))
		writeErr(w, http.StatusTooManyRequests, "audio prepare rate limit exceeded")
		return
	}
	voice := strings.TrimSpace(r.URL.Query().Get("voice"))
	if voice == "" {
		voice = s.cfg.TTSDefaultVoice
	}
	speed := s.cfg.TTSDefaultSpeed
	if v := r.URL.Query().Get("speed"); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil && f >= 0.5 && f <= 2.0 {
			speed = f
		}
	}
	n, err := s.tts.PrepareAudio(r.Context(), r.PathValue("id"), idx, id.username, voice, speed)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "audio prepare failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "segments": n})
}

// handleAudioSegment 返回 NSAC 加密文件原文。
func (s *Server) handleAudioSegment(w http.ResponseWriter, r *http.Request, id identity) {
	if s.tts == nil {
		writeErr(w, http.StatusServiceUnavailable, "tts not configured")
		return
	}
	key := r.PathValue("key")
	if len(key) == 0 || len(key) > 128 {
		writeErr(w, http.StatusBadRequest, "invalid segment key")
		return
	}
	owned, err := s.tts.SegmentOwned(r.Context(), s.db, key, id.username)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	if !owned {
		writeErr(w, http.StatusNotFound, "segment not found")
		return
	}
	s.tts.ServeSegment(w, r, key, id.username)
}
