// Package ops 只做最小运维加固：健康判定 + 可选 webhook。
//
// 安全边界：只读元数据/审计计数，不解密、不读正文、不读查询词。
// webhook 的摘要由固定模板 + 计数生成，绝不携带正文、查询词、token、KEK/DEK 或审计 detail。
package ops

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"sync"
	"time"

	"novelstack/internal/audit"
)

const (
	alertBackupFailed = "backup_failed"
	alertBackupStale  = "backup_stale"
	alertTamper       = "integrity_tamper"
	alertIndexDrift   = "index_drift"
	alertKEKMarker    = "kek_offline_backup"

	signalIndexDrift = "index_drift"
)

// Alert 是一条可展示、可推送的告警（type 为稳定英文标识，summary 为固定模板）。
type Alert struct {
	Type    string `json:"type"`
	Summary string `json:"summary"`
}

// Health 是控制台健康横幅的数据源；字段全部为固定类型，不含自由文本。
type Health struct {
	Level            string  `json:"level"` // ok / warn / bad
	Alerts           []Alert `json:"alerts"`
	CheckedAt        string  `json:"checked_at"`
	LastBackupAt     string  `json:"last_backup_at,omitempty"`
	BackupOK         bool    `json:"backup_ok"`
	Integrity24h     int     `json:"integrity_24h"`
	IndexDrift       int64   `json:"index_drift"`
	IndexDriftSince  string  `json:"index_drift_since,omitempty"`
	KekMarkerPresent bool    `json:"kek_marker_present"`
}

// Input 由 dashboard 聚合层填充（它已经算好书籍数与索引文档数）。
type Input struct {
	DB              *sql.DB
	BooksTotal      int
	SearchDocuments int64
	KekMarkerPath   string
	Now             time.Time
}

// Evaluate 判定健康状态。「索引偏差持续超过 1 小时」的首次观测时间落在 health_signals，
// 因此容器重启不会把计时器清零。
func Evaluate(ctx context.Context, in Input) Health {
	now := in.Now
	if now.IsZero() {
		now = time.Now()
	}
	out := Health{
		Level:     "ok",
		Alerts:    []Alert{},
		CheckedAt: now.UTC().Format(time.RFC3339),
	}
	if in.DB == nil {
		return out
	}

	// 1) 最近一次成功备份 / 最近一次备份失败
	var lastOK string
	_ = in.DB.QueryRowContext(ctx, `SELECT ts FROM backup_runs WHERE result='ok' ORDER BY id DESC LIMIT 1`).Scan(&lastOK)
	var lastFail string
	_ = in.DB.QueryRowContext(ctx, `SELECT ts FROM audit_logs WHERE action='backup_failed' ORDER BY id DESC LIMIT 1`).Scan(&lastFail)
	out.LastBackupAt = lastOK
	okTime, okErr := parseTS(lastOK)
	out.BackupOK = okErr == nil
	if failTime, err := parseTS(lastFail); err == nil && (okErr != nil || failTime.After(okTime)) {
		out.Alerts = append(out.Alerts, Alert{alertBackupFailed,
			"最近一次自动备份失败：请查看审计 backup_failed，并确认云盘可写与备份口令有效。"})
	}
	if okErr == nil {
		if now.Sub(okTime) > 48*time.Hour {
			out.Alerts = append(out.Alerts, Alert{alertBackupStale,
				"已超过 48 小时没有成功备份记录：请手动触发一次备份排查。"})
		}
	} else {
		out.Alerts = append(out.Alerts, Alert{alertBackupStale,
			"尚无成功备份记录：请在控制台手动触发一次备份。"})
	}

	// 2) 最近 24 小时的解密失败/篡改事件（只数固定 action，不读 detail）
	cutoff := now.UTC().Add(-24 * time.Hour).Format("2006-01-02T15:04:05.000Z")
	_ = in.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM audit_logs
		WHERE action IN ('decrypt_failed','book_download_tamper','tts_cache_tamper') AND ts >= ?`, cutoff).
		Scan(&out.Integrity24h)
	if out.Integrity24h > 0 {
		out.Alerts = append(out.Alerts, Alert{alertTamper,
			fmt.Sprintf("最近 24 小时检测到 %d 次解密失败或完整性篡改事件（见审计）。", out.Integrity24h)})
	}

	// 3) 索引偏差：|索引文档数 - 书籍数| > 0 且持续超过 1 小时才升级为告警。
	// 正偏差表示索引多出文档，负偏差表示索引缺失文档；计时器只看绝对值，方向只影响摘要。
	out.IndexDrift = in.SearchDocuments - int64(in.BooksTotal)
	nowStr := now.UTC().Format(time.RFC3339)
	if out.IndexDrift != 0 {
		var since string
		err := in.DB.QueryRowContext(ctx, `SELECT since FROM health_signals WHERE key=?`, signalIndexDrift).Scan(&since)
		if err != nil {
			since = nowStr
			_, _ = in.DB.ExecContext(ctx, `INSERT INTO health_signals(key,since,last_seen) VALUES(?,?,?)
				ON CONFLICT(key) DO UPDATE SET last_seen=excluded.last_seen`, signalIndexDrift, since, nowStr)
		} else {
			_, _ = in.DB.ExecContext(ctx, `UPDATE health_signals SET last_seen=? WHERE key=?`, nowStr, signalIndexDrift)
		}
		out.IndexDriftSince = since
		if t, err := parseTS(since); err == nil && now.Sub(t) > time.Hour {
			direction, count := "多", out.IndexDrift
			if count < 0 {
				direction, count = "少", -count
			}
			out.Alerts = append(out.Alerts, Alert{alertIndexDrift,
				fmt.Sprintf("元数据索引文档数比书籍数%s %d，已持续超过 1 小时（可能需要重建索引）。", direction, count)})
		}
	} else if _, err := in.DB.ExecContext(ctx, `DELETE FROM health_signals WHERE key=?`, signalIndexDrift); err != nil {
		// 清理失败不影响判定
		_ = err
	}

	// 4) KEK 离线备份确认标记（黄色，不阻塞启动）
	if in.KekMarkerPath != "" {
		if st, err := os.Stat(in.KekMarkerPath); err == nil && st.Mode().IsRegular() {
			out.KekMarkerPresent = true
		}
	}
	if !out.KekMarkerPresent {
		out.Alerts = append(out.Alerts, Alert{alertKEKMarker,
			"尚未确认 KEK 已离线备份：创建确认标记文件（见 README §15.4）后此提醒消失。"})
	}

	for _, a := range out.Alerts {
		if a.Type != alertKEKMarker {
			out.Level = "bad"
			break
		}
		out.Level = "warn"
	}
	return out
}

// HealthOptions 是 webhook 通知器依赖。
type HealthOptions struct {
	Webhook  string
	Cooldown time.Duration
	Client   *http.Client
	Audit    *audit.Logger
	Now      func() time.Time
}

// HealthStore 负责告警去重与 webhook 投递；Evaluate 本身不在这里，避免重复实现判定。
type HealthStore struct {
	opts HealthOptions
	mu   sync.Mutex
	sent map[string]time.Time
}

// NewHealthStore 创建告警通知器。webhook 为空时只显示控制台横幅。
func NewHealthStore(opts HealthOptions) *HealthStore {
	if opts.Cooldown <= 0 {
		opts.Cooldown = time.Hour
	}
	if opts.Client == nil {
		opts.Client = &http.Client{Timeout: 2 * time.Second}
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	return &HealthStore{opts: opts, sent: map[string]time.Time{}}
}

func (h *HealthStore) Enabled() bool { return h != nil && h.opts.Webhook != "" }

// Notify 对每个新告警发一条 JSON，最多尝试 3 次（首次 + 2 次重试），失败写审计后放弃。
// 同一类型在冷却期内只发一次；告警消失后从去重表移除，下次出现可立即再发。
func (h *HealthStore) Notify(ctx context.Context, health Health) {
	if !h.Enabled() {
		return
	}
	now := h.opts.Now()
	current := make(map[string]bool, len(health.Alerts))
	for _, a := range health.Alerts {
		current[a.Type] = true
	}
	h.mu.Lock()
	for t := range h.sent {
		if !current[t] {
			delete(h.sent, t)
		}
	}
	h.mu.Unlock()

	for _, a := range health.Alerts {
		h.mu.Lock()
		last, seen := h.sent[a.Type]
		if seen && now.Sub(last) < h.opts.Cooldown {
			h.mu.Unlock()
			continue
		}
		h.sent[a.Type] = now
		h.mu.Unlock()
		h.deliver(ctx, health, a)
	}
}

// webhookBody 是 POST 的完整请求体：只有时间、级别、类型与固定摘要。
type webhookBody struct {
	Time    string `json:"time"`
	Level   string `json:"level"`
	Type    string `json:"type"`
	Summary string `json:"summary"`
}

func (h *HealthStore) deliver(ctx context.Context, health Health, a Alert) {
	body, _ := json.Marshal(webhookBody{
		Time: health.CheckedAt, Level: health.Level, Type: a.Type, Summary: a.Summary,
	})
	reason := "network_error"
	attempts := 0
	for i := 0; i < 3; i++ {
		attempts = i + 1
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.opts.Webhook, bytes.NewReader(body))
		if err != nil {
			reason = "bad_webhook_url"
			break
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := h.opts.Client.Do(req)
		if err == nil {
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
			resp.Body.Close()
			if resp.StatusCode >= 200 && resp.StatusCode < 300 {
				if h.opts.Audit != nil {
					h.opts.Audit.Record(ctx, "system", "webhook", "health_webhook", "", "ok",
						fmt.Sprintf("type=%s attempts=%d", a.Type, attempts))
				}
				return
			}
			reason = "http_error"
		} else {
			// 不记 err.Error()：Go 的 http 错误会带上完整 URL（webhook 里可能含 bot token）。
			reason = "network_error"
		}
		if i < 2 {
			select {
			case <-ctx.Done():
				i = 2
			case <-time.After(time.Duration(i+1) * 200 * time.Millisecond):
			}
		}
	}
	if h.opts.Audit != nil {
		// 不记 URL（可能带 bot token），不记响应体。
		h.opts.Audit.Record(ctx, "system", "webhook", "health_webhook", "", "error",
			fmt.Sprintf("type=%s attempts=%d reason=%s", a.Type, attempts, reason))
	}
}

func parseTS(s string) (time.Time, error) {
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05.000Z", "2006-01-02T15:04:05Z"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("ops: 无法解析时间 %q", s)
}
