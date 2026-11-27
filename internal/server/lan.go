package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"novelstack/internal/devices"
	"novelstack/internal/lan"
)

// 本文件实现第十阶段的局域网入口：
//
//	POST /api/admin/lan/pairing   控制台生成 6 位短码（仅 session），含可扫码的 endpoint
//	POST /api/lan/pair            App 用 6 位短码 + 设备公钥换取 device token（无需 session）
//	GET  /api/lan/info            返回本机局域网发现信息（无需鉴权，只含地址不含数据）
//
// 短码是低熵凭证，兑换必须限流 + 绑定来源 IP + 一次性；真正的长期凭证仍由 devices 签发。

// handleLANPairingCreate 为当前管理员生成绑定其来源 IP 的 6 位短码。
func (s *Server) handleLANPairingCreate(w http.ResponseWriter, r *http.Request, id identity) {
	if s.lanPairs == nil {
		writeErr(w, http.StatusServiceUnavailable, "lan pairing not configured")
		return
	}
	endpoint := ""
	if s.discovery != nil {
		endpoint = s.discovery.Endpoint()
	}
	code, err := s.lanPairs.Create(r.Context(), id.username, id.ip, endpoint)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	s.audit.Record(r.Context(), id.username, id.ip, "lan_pairing_created", "", "ok",
		fmt.Sprintf("expires_in=%ds", code.ExpiresIn))
	writeJSON(w, http.StatusOK, code)
}

// handleLANPair 用 6 位短码换取 device token。
// 请求体与 /api/device/token 一致（device_name + public_key + pairing_code），
// 但 pairing_code 是本阶段生成的 6 位数字码。
func (s *Server) handleLANPair(w http.ResponseWriter, r *http.Request) {
	if s.lanPairs == nil || s.devices == nil {
		writeErr(w, http.StatusServiceUnavailable, "lan pairing not configured")
		return
	}
	if !isJSONContentType(r.Header.Get("Content-Type")) {
		writeErr(w, http.StatusUnsupportedMediaType, "application/json required")
		return
	}
	ip := s.clientIP(r)
	var req struct {
		PairingCode string `json:"pairing_code"`
		DeviceName  string `json:"device_name"`
		PublicKey   string `json:"public_key"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json body")
		return
	}
	owner, err := s.lanPairs.Redeem(r.Context(), strings.TrimSpace(req.PairingCode), ip)
	switch {
	case errors.Is(err, lan.ErrRateLimited):
		s.audit.Record(r.Context(), "", ip, "lan_pair_denied", "", "ratelimited", "")
		writeErr(w, http.StatusTooManyRequests, "too many attempts")
		return
	case err != nil:
		s.audit.Record(r.Context(), "", ip, "lan_pair_denied", "", "denied", "bad code")
		writeErr(w, http.StatusUnauthorized, "invalid or expired pairing code")
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
	s.audit.Record(r.Context(), owner, ip, "lan_pair_success", "", "ok",
		fmt.Sprintf("device=%s name=%s", dev.ID, dev.Name))
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{
		"token":       token,
		"token_type":  "Bearer",
		"device":      dev,
		"server_time": time.Now().UTC().Format(time.RFC3339Nano),
		"expires_at":  dev.ExpiresAt,
	})
}

// handleLANInfo 返回局域网发现信息。只含地址/端口/服务名，不含任何数据或凭证。
func (s *Server) handleLANInfo(w http.ResponseWriter, r *http.Request) {
	if s.discovery == nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"enabled": false,
			"service": "_novelstack._tcp",
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"enabled":  true,
		"service":  "_novelstack._tcp",
		"name":     s.discovery.Name(),
		"host":     s.discovery.Host(),
		"port":     s.discovery.Port(),
		"endpoint": s.discovery.Endpoint(),
		"udp_port": s.discovery.UDPPort(),
	})
}
