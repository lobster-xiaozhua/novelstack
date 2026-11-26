// Package dashboard 聚合只读运营指标。
//
// 安全边界：本包只读取元数据表、计数与审计摘要，绝不调用任何解密路径，
// 也不读取章节正文/查询词/密钥。返回结构使用固定字段，便于测试断言无明文泄漏。
package dashboard

import (
	"context"
	"database/sql"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"time"

	"novelstack/internal/audit"
	"novelstack/internal/books"
	"novelstack/internal/mobile"
	"novelstack/internal/ops"
	"novelstack/internal/search"
	"novelstack/internal/tts"
)

// Deps 是聚合所需依赖。
type Deps struct {
	DB     *sql.DB
	Books  *books.Service
	TTS    *tts.Service
	Search *search.Service
	Audit  *audit.Logger
	// LegadoEnabled 仅用于展示。
	LegadoEnabled bool
	// KekOfflineMarker 是用户手动创建的离线备份确认标记路径。
	KekOfflineMarker string
	// BackupDir 是自动备份输出目录，用于统计目录占用。
	BackupDir string
}

// Response 是 GET /api/admin/dashboard 的响应。
type Response struct {
	GeneratedAt string      `json:"generated_at"`
	System      System      `json:"system"`
	Books       BooksBlock  `json:"books"`
	KEK         KEKBlock    `json:"kek"`
	TTS         TTSBlock    `json:"tts"`
	Search      SearchBlock `json:"search"`
	Audit       AuditBlock  `json:"audit"`
	Backup      BackupBlock `json:"backup"`
	Cache       CacheBlock  `json:"cache"`
	Mobile      MobileBlock `json:"mobile"`
	Health      ops.Health  `json:"health"`
	// BackupsDirBytes 是 backups/ 目录内普通文件的总字节数（含子目录，只读元数据）。
	BackupsDirBytes int64 `json:"backups_dir_bytes"`
}

type System struct {
	LegadoEnabled bool   `json:"legado_enabled"`
	TTSProvider   string `json:"tts_provider"`
	SearchBackend string `json:"search_backend"`
}

type BooksBlock struct {
	Total       int   `json:"total"`
	Deleted     int   `json:"deleted"`
	CipherBytes int64 `json:"cipher_bytes"`
	CipherFiles int   `json:"cipher_files"`
	Chapters    int   `json:"chapters"`
	// Drift 表示密文文件数与书籍记录数的偏差（非 0 需要人工核对）。
	Drift int `json:"cipher_drift"`
}

type KEKVersionCount struct {
	Version int `json:"version"`
	Books   int `json:"books"`
}

type RotationRecord struct {
	TS              string `json:"ts"`
	FromVersion     int    `json:"from_version"`
	ToVersion       int    `json:"to_version"`
	BooksRotated    int    `json:"books_rotated"`
	SegmentsRotated int    `json:"segments_rotated"`
	Result          string `json:"result"`
}

type KEKBlock struct {
	CurrentVersion       int               `json:"current_version"`
	LoadedVersions       []int             `json:"loaded_versions"`
	Distribution         []KEKVersionCount `json:"distribution"`
	LastRotation         *RotationRecord   `json:"last_rotation,omitempty"`
	DecryptFailures24h   int               `json:"decrypt_failures_24h"`
	DecryptFailuresTotal int               `json:"decrypt_failures_total"`
}

type ProviderInfo struct {
	Kind   string `json:"kind"`
	Name   string `json:"name"`
	Health string `json:"health"`
	Error  string `json:"error,omitempty"`
}

type SynthBlock struct {
	Window      int     `json:"window"`
	Total       int     `json:"total"`
	OK          int     `json:"ok"`
	Failed      int     `json:"failed"`
	SuccessRate float64 `json:"success_rate"`
	AvgMS       float64 `json:"avg_ms"`
	P50MS       float64 `json:"p50_ms"`
	P95MS       float64 `json:"p95_ms"`
}

type SpeakBlock struct {
	Window   int     `json:"window"`
	Segments int64   `json:"segments"`
	Hits     int64   `json:"hits"`
	Synths   int64   `json:"synths"`
	HitRate  float64 `json:"hit_rate"`
	P50MS    float64 `json:"p50_ms"`
	P95MS    float64 `json:"p95_ms"`
}

type TTSCacheBlock struct {
	Segments int                 `json:"segments"`
	Bytes    int64               `json:"bytes"`
	HitRate  float64             `json:"hit_rate"`
	ByBook   []tts.CacheBookStat `json:"by_book"`
}

type TTSBlock struct {
	Provider ProviderInfo  `json:"provider"`
	Synth    SynthBlock    `json:"synth"`
	Speak    SpeakBlock    `json:"speak"`
	Cache    TTSCacheBlock `json:"cache"`
}

type LatencyBlock struct {
	Window  int     `json:"window"`
	Samples int     `json:"samples"`
	P50MS   float64 `json:"p50_ms"`
	P95MS   float64 `json:"p95_ms"`
}

type SearchBlock struct {
	Backend   string              `json:"backend"`
	Index     string              `json:"index"`
	Health    string              `json:"health"`
	Documents int64               `json:"documents"`
	Drift     int64               `json:"drift"`
	Latency   LatencyBlock        `json:"latency"`
	Coverage  []books.CoverageRow `json:"coverage"`
}

type AuditBlock struct {
	Recent         []audit.Entry  `json:"recent"`
	CountsByAction map[string]int `json:"counts_by_action"`
}

type BackupRun struct {
	TS       string `json:"ts"`
	Path     string `json:"path"`
	Checksum string `json:"sha256"`
	Size     int64  `json:"size_bytes"`
	Books    int    `json:"books"`
	Segments int    `json:"segments"`
	Result   string `json:"result"`
}

type DrillRun struct {
	TS         string `json:"ts"`
	Package    string `json:"package"`
	Checksum   string `json:"sha256"`
	BookID     string `json:"book_id"`
	Result     string `json:"result"`
	Detail     string `json:"detail,omitempty"`
	DurationMS int64  `json:"duration_ms"`
}

type BackupBlock struct {
	Last      *BackupRun `json:"last,omitempty"`
	TotalRuns int        `json:"total_runs"`
	LastDrill *DrillRun  `json:"last_drill,omitempty"`
}

// MobileBlock 汇总第五阶段移动端状态：设备数、进度同步、最近一次清单拉取。
type MobileBlock struct {
	DevicesActive  int               `json:"devices_active"`
	DevicesRevoked int               `json:"devices_revoked"`
	ProgressBooks  int               `json:"progress_books"`
	LastProgressAt string            `json:"last_progress_at,omitempty"`
	RecentProgress []mobile.Progress `json:"recent_progress"`
	LastManifestAt string            `json:"last_manifest_at,omitempty"`
	Manifests24h   int               `json:"manifests_24h"`
}

type CacheBlock struct {
	Segments        int                 `json:"segments"`
	Bytes           int64               `json:"bytes"`
	ProtectedWindow int                 `json:"protect_window_s"`
	ByBook          []tts.CacheBookStat `json:"by_book"`
}

// Build 聚合全部指标。任一子项失败只降级该块，不整体失败（控制台仍可用）。
func Build(ctx context.Context, d Deps) (*Response, error) {
	resp := &Response{
		GeneratedAt: time.Now().UTC().Format(time.RFC3339),
		System: System{
			LegadoEnabled: d.LegadoEnabled,
		},
		Books:  BooksBlock{},
		KEK:    KEKBlock{Distribution: []KEKVersionCount{}, LoadedVersions: []int{}},
		TTS:    TTSBlock{Cache: TTSCacheBlock{ByBook: []tts.CacheBookStat{}}},
		Search: SearchBlock{Coverage: []books.CoverageRow{}},
		Audit:  AuditBlock{Recent: []audit.Entry{}, CountsByAction: map[string]int{}},
		Cache:  CacheBlock{ByBook: []tts.CacheBookStat{}},
		Mobile: MobileBlock{RecentProgress: []mobile.Progress{}},
	}
	if d.DB == nil {
		return resp, nil
	}

	_ = d.DB.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(SUM(chapter_count),0)
		FROM books WHERE deleted_at IS NULL`).Scan(&resp.Books.Total, &resp.Books.Chapters)
	_ = d.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM books WHERE deleted_at IS NOT NULL`).Scan(&resp.Books.Deleted)

	if d.Books != nil {
		resp.KEK.CurrentVersion = currentKEKVersion(d.Books)
		resp.KEK.LoadedVersions = loadedKEKVersions(d.Books)
		resp.Books.CipherBytes, resp.Books.CipherFiles = dirUsage(d.Books.CiphertextDir)
		resp.Books.Drift = resp.Books.CipherFiles - resp.Books.Total
		if cov, err := d.Books.Store.CoverageAll(ctx); err == nil {
			resp.Search.Coverage = cov
		}
	}

	// KEK 版本分布
	rows, err := d.DB.QueryContext(ctx, `SELECT kek_version, COUNT(*) FROM books
		WHERE deleted_at IS NULL GROUP BY kek_version ORDER BY kek_version`)
	if err == nil {
		for rows.Next() {
			var v KEKVersionCount
			if err := rows.Scan(&v.Version, &v.Books); err == nil {
				resp.KEK.Distribution = append(resp.KEK.Distribution, v)
			}
		}
		rows.Close()
	}
	// 第五阶段：移动端设备与进度（都是元数据，不含正文）。
	_ = d.DB.QueryRowContext(ctx, `SELECT
		COALESCE(SUM(CASE WHEN revoked_at IS NULL THEN 1 ELSE 0 END), 0),
		COALESCE(SUM(CASE WHEN revoked_at IS NOT NULL THEN 1 ELSE 0 END), 0)
		FROM device_tokens`).Scan(&resp.Mobile.DevicesActive, &resp.Mobile.DevicesRevoked)
	_ = d.DB.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(MAX(updated_at),'')
		FROM reading_progress`).Scan(&resp.Mobile.ProgressBooks, &resp.Mobile.LastProgressAt)
	if recent, rerr := mobile.RecentProgress(ctx, d.DB, ownerOfFirstUser(ctx, d.DB), 5); rerr == nil {
		resp.Mobile.RecentProgress = recent
	}
	_ = d.DB.QueryRowContext(ctx, `SELECT COALESCE(MAX(ts),'') FROM audit_logs WHERE action = 'sync_manifest'`).
		Scan(&resp.Mobile.LastManifestAt)
	_ = d.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM audit_logs
		WHERE action = 'sync_manifest' AND ts >= strftime('%Y-%m-%dT%H:%M:%fZ','now','-1 day')`).
		Scan(&resp.Mobile.Manifests24h)

	_ = d.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM audit_logs
		WHERE (action LIKE '%\_tamper' ESCAPE '\' OR (action LIKE '%\_tamper\_%' ESCAPE '\'))`).
		Scan(&resp.KEK.DecryptFailuresTotal)
	_ = d.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM audit_logs
		WHERE (action LIKE '%\_tamper' ESCAPE '\' OR (action LIKE '%\_tamper\_%' ESCAPE '\'))
		  AND ts >= strftime('%Y-%m-%dT%H:%M:%fZ','now','-1 day')`).
		Scan(&resp.KEK.DecryptFailures24h)

	// 最近一次轮换
	var rot RotationRecord
	err = d.DB.QueryRowContext(ctx, `SELECT ts, from_version, to_version, books_rotated, segments_rotated, result
		FROM kek_rotations ORDER BY id DESC LIMIT 1`).
		Scan(&rot.TS, &rot.FromVersion, &rot.ToVersion, &rot.BooksRotated, &rot.SegmentsRotated, &rot.Result)
	if err == nil {
		resp.KEK.LastRotation = &rot
	}

	// TTS
	if d.TTS != nil {
		resp.System.TTSProvider = d.TTS.ProviderKind
		stats, err := d.TTS.CacheStats(ctx)
		if err == nil {
			resp.TTS.Cache.Segments = stats.Segments
			resp.TTS.Cache.Bytes = stats.Bytes
			resp.TTS.Cache.ByBook = stats.ByBook
			resp.Cache.Segments = stats.Segments
			resp.Cache.Bytes = stats.Bytes
			resp.Cache.ByBook = stats.ByBook
		}
		resp.TTS.Provider = ttsProviderInfo(ctx, d.TTS)
		if total, ok, avg, err := d.TTS.Repo.SynthSummary(ctx, 100); err == nil {
			p50, p95, _ := d.TTS.Repo.SynthLatency(ctx, 100)
			resp.TTS.Synth = SynthBlock{Window: 100, Total: total, OK: ok, Failed: total - ok,
				AvgMS: avg, P50MS: p50, P95MS: p95}
			if total > 0 {
				resp.TTS.Synth.SuccessRate = float64(ok) / float64(total)
			}
		}
		if segs, hits, synths, p50, p95, err := d.TTS.Repo.SpeakTotals(ctx, 200); err == nil {
			resp.TTS.Speak = SpeakBlock{Window: 200, Segments: segs, Hits: hits, Synths: synths, P50MS: p50, P95MS: p95}
			if segs > 0 {
				resp.TTS.Speak.HitRate = float64(hits) / float64(segs)
				resp.TTS.Cache.HitRate = resp.TTS.Speak.HitRate
			}
		}
		if d.TTS.Cache != nil {
			resp.Cache.ProtectedWindow = int(d.TTS.ProtectWindowSeconds())
		}
	}

	// 搜索
	if d.Search != nil {
		idx := d.Search.IndexStats(ctx)
		resp.Search.Backend, _ = idx["backend"].(string)
		resp.Search.Index, _ = idx["index"].(string)
		resp.Search.Health, _ = idx["health"].(string)
		if n, ok := idx["documents"].(int64); ok {
			resp.Search.Documents = n
			resp.Search.Drift = n - int64(resp.Books.Total)
		}
		p50, p95, samples, err := d.Search.Latency(ctx, 200)
		if err == nil {
			resp.Search.Latency = LatencyBlock{Window: 200, Samples: samples, P50MS: p50, P95MS: p95}
		}
	}
	resp.System.SearchBackend = resp.Search.Backend

	// 审计
	if d.Audit != nil {
		if entries, err := d.Audit.Recent(ctx, 20); err == nil {
			resp.Audit.Recent = entries
		}
		if counts, err := d.Audit.CountsByAction(ctx, 1000); err == nil {
			resp.Audit.CountsByAction = counts
		}
	}

	// 备份与演练
	var run BackupRun
	err = d.DB.QueryRowContext(ctx, `SELECT ts, path, checksum, size_bytes, books, segments, result
		FROM backup_runs ORDER BY id DESC LIMIT 1`).
		Scan(&run.TS, &run.Path, &run.Checksum, &run.Size, &run.Books, &run.Segments, &run.Result)
	if err == nil {
		resp.Backup.Last = &run
	}
	_ = d.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM backup_runs`).Scan(&resp.Backup.TotalRuns)
	var drill DrillRun
	err = d.DB.QueryRowContext(ctx, `SELECT ts, package, checksum, book_id, result, detail, duration_ms
		FROM drill_runs ORDER BY id DESC LIMIT 1`).
		Scan(&drill.TS, &drill.Package, &drill.Checksum, &drill.BookID, &drill.Result, &drill.Detail, &drill.DurationMS)
	if err == nil {
		resp.Backup.LastDrill = &drill
	}
	// 备份目录占用：只统计文件大小，不打开任何备份包内容。
	resp.BackupsDirBytes = dirBytesRecursive(d.BackupDir)

	// 健康横幅：固定摘要，不含正文/查询词/令牌；索引偏差的首次观测时间落库，
	// 因此「持续 1 小时」跨进程重启有效。
	resp.Health = ops.Evaluate(ctx, ops.Input{
		DB:              d.DB,
		BooksTotal:      resp.Books.Total,
		SearchDocuments: resp.Search.Documents,
		KekMarkerPath:   d.KekOfflineMarker,
		Now:             time.Now(),
	})
	return resp, nil
}

// dirUsage 统计目录内普通文件数与总字节数（只读元数据，不打开内容）。
func dirUsage(dir string) (bytes int64, files int) {
	if dir == "" {
		return 0, 0
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, 0
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		files++
		bytes += info.Size()
	}
	return bytes, files
}

// dirBytesRecursive 递归统计目录下普通文件的总字节数；忽略符号链接与不可读项。
// 只读元数据，不打开内容，也不会因目录缺失导致整个 dashboard 失败。
func dirBytesRecursive(dir string) int64 {
	if dir == "" {
		return 0
	}
	var total int64
	_ = filepath.WalkDir(dir, func(_ string, entry fs.DirEntry, err error) error {
		if err != nil || entry == nil || entry.IsDir() {
			return nil
		}
		if info, ierr := entry.Info(); ierr == nil && info.Mode().IsRegular() {
			total += info.Size()
		}
		return nil
	})
	return total
}

// ownerOfFirstUser 返回唯一用户的用户名（单用户部署）。
// 查询失败时返回空串，RecentProgress 会得到空列表而不是整体失败。
func ownerOfFirstUser(ctx context.Context, db *sql.DB) string {
	var u string
	_ = db.QueryRowContext(ctx, `SELECT username FROM users ORDER BY id LIMIT 1`).Scan(&u)
	return u
}

func currentKEKVersion(s *books.Service) int {
	if s.KEKVersion > 0 {
		return s.KEKVersion
	}
	return 1
}

func loadedKEKVersions(s *books.Service) []int {
	set := map[int]struct{}{currentKEKVersion(s): {}}
	for v := range s.OldKEKs {
		set[v] = struct{}{}
	}
	out := make([]int, 0, len(set))
	for v := range set {
		out = append(out, v)
	}
	sort.Ints(out)
	return out
}

func ttsProviderInfo(ctx context.Context, s *tts.Service) ProviderInfo {
	out := ProviderInfo{Kind: s.ProviderKind, Health: "disabled"}
	if s.Provider == nil {
		return out
	}
	out.Name = s.Provider.Name()
	hctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if err := s.Provider.Health(hctx); err != nil {
		out.Health = "unavailable"
		out.Error = "probe failed"
	} else {
		out.Health = "ok"
	}
	return out
}
