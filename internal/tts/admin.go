package tts

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"novelstack/internal/ttscache"
)

// acquireKeys 为一次朗读请求的全部片段增加进程内引用计数。
// 清理/淘汰会跳过被引用的键，避免朗读中途片段被删。
func (s *Service) acquireKeys(segs []seg) func() {
	s.refMu.Lock()
	if s.refs == nil {
		s.refs = make(map[string]int, len(segs))
	}
	for _, sg := range segs {
		s.refs[sg.key]++
	}
	s.refMu.Unlock()
	return func() {
		s.refMu.Lock()
		for _, sg := range segs {
			if s.refs[sg.key] <= 1 {
				delete(s.refs, sg.key)
			} else {
				s.refs[sg.key]--
			}
		}
		s.refMu.Unlock()
	}
}

// activeKeys 返回当前正在被流式朗读引用的片段键。
func (s *Service) activeKeys() map[string]struct{} {
	s.refMu.Lock()
	defer s.refMu.Unlock()
	out := make(map[string]struct{}, len(s.refs))
	for k, n := range s.refs {
		if n > 0 {
			out[k] = struct{}{}
		}
	}
	return out
}

// protectWindow 返回淘汰保护窗口（默认 5 分钟）。
func (s *Service) protectWindow() time.Duration {
	if s.ProtectWindow > 0 {
		return s.ProtectWindow
	}
	return 5 * time.Minute
}

// CacheStats 是缓存总量与按书占用（只含密文大小，不涉及音频内容）。
type CacheStats struct {
	Segments int             `json:"segments"`
	Bytes    int64           `json:"bytes"`
	ByBook   []CacheBookStat `json:"by_book"`
}

// CacheStats 汇总缓存占用。文件系统是权威来源，按书分组来自片段索引。
func (s *Service) CacheStats(ctx context.Context) (CacheStats, error) {
	out := CacheStats{ByBook: []CacheBookStat{}}
	segments, bytes, err := s.Cache.Stats()
	if err != nil {
		return out, err
	}
	out.Segments, out.Bytes = segments, bytes
	byBook, err := s.Repo.CacheByBook(ctx, 500)
	if err != nil {
		return out, err
	}
	out.ByBook = byBook
	return out, nil
}

// ClearResult 描述一次缓存清理。
type ClearResult struct {
	Scope        string `json:"scope"`
	BookID       string `json:"book_id,omitempty"`
	Removed      int    `json:"removed"`
	FreedBytes   int64  `json:"freed_bytes"`
	Protected    int    `json:"protected"`
	OrphanFiles  int    `json:"orphan_files_removed"`
	BytesBefore  int64  `json:"bytes_before"`
	BytesAfter   int64  `json:"bytes_after"`
	SegmentsLeft int    `json:"segments_left"`
}

// ClearCache 清理指定书籍或全部 TTS 缓存。
// 正在朗读引用的片段、以及最近保护窗口内访问过的片段不会被删除。
// 删除操作同时移除密文文件与片段索引行；无法解密的文件也可安全删除。
func (s *Service) ClearCache(ctx context.Context, bookID string) (ClearResult, error) {
	res := ClearResult{Scope: "all", BookID: bookID}
	if bookID != "" {
		res.Scope = "book"
	}
	if _, bytes, err := s.Cache.Stats(); err == nil {
		res.BytesBefore = bytes
	}
	active := s.activeKeys()

	// 先刷新访问队列，避免把刚刚播放的片段误判为冷数据
	_ = s.Repo.FlushAccess(ctx)

	keys, err := s.Repo.KeysForBook(ctx, bookID)
	if err != nil {
		return res, err
	}
	// 全量清理时补上数据库中缺失的孤儿文件
	if bookID == "" {
		known := make(map[string]struct{}, len(keys))
		for _, k := range keys {
			known[k] = struct{}{}
		}
		_ = s.Cache.WalkKeys(func(key string, _ int64, _ int) error {
			if _, ok := known[key]; !ok {
				keys = append(keys, key)
				res.OrphanFiles++
			}
			return nil
		})
	}

	deleted := make([]string, 0, len(keys))
	for _, key := range keys {
		if _, ok := active[key]; ok {
			res.Protected++
			continue
		}
		size := s.Cache.CipherSize(key)
		if err := s.Cache.Remove(key); err != nil {
			return res, fmt.Errorf("删除缓存片段失败: %w", err)
		}
		deleted = append(deleted, key)
		res.Removed++
		res.FreedBytes += size
	}
	if err := s.Repo.DeleteSegments(ctx, deleted); err != nil {
		return res, err
	}
	if bookID == "" {
		// 全量清理：残留行（文件已不存在）也一并清除，但保留受保护片段的行。
		if len(active) == 0 {
			if _, err := s.Repo.cleanupAllRows(ctx); err != nil {
				return res, err
			}
		} else {
			keep := make([]string, 0, len(active))
			for k := range active {
				keep = append(keep, k)
			}
			if err := s.Repo.deleteAllExcept(ctx, keep); err != nil {
				return res, err
			}
		}
	}
	if segments, bytes, err := s.Cache.Stats(); err == nil {
		res.SegmentsLeft, res.BytesAfter = segments, bytes
	}
	return res, nil
}

// PrunePolicy 是缓存淘汰策略；零值表示不限制（不会删除任何片段）。
type PrunePolicy struct {
	MaxBytes int64 `json:"max_bytes"`
	KeepDays int   `json:"keep_days"`
	// DryRun 只计算将被删除的片段，不实际删除。
	DryRun bool `json:"dry_run"`
}

// PruneResult 描述一次淘汰。
type PruneResult struct {
	MaxBytes     int64 `json:"max_bytes"`
	KeepDays     int   `json:"keep_days"`
	DryRun       bool  `json:"dry_run"`
	BytesBefore  int64 `json:"bytes_before"`
	BytesAfter   int64 `json:"bytes_after"`
	Removed      int   `json:"removed"`
	FreedBytes   int64 `json:"freed_bytes"`
	Protected    int   `json:"protected"`
	SegmentsLeft int   `json:"segments_left"`
	// RemovedDeleted 是因所属书籍被软删除而回收的片段数（不计入策略淘汰）。
	RemovedDeleted int `json:"removed_deleted"`
}

// PruneCache 按总大小上限与保留天数淘汰缓存。
// 保护规则：正在朗读引用的不删；最近 ProtectWindow 内访问过的不删。
func (s *Service) PruneCache(ctx context.Context, policy PrunePolicy) (PruneResult, error) {
	res := PruneResult{MaxBytes: policy.MaxBytes, KeepDays: policy.KeepDays, DryRun: policy.DryRun}
	if _, bytes, err := s.Cache.Stats(); err == nil {
		res.BytesBefore = bytes
	}
	// 软删除回收优先于容量/时间淘汰：书籍已被删除，它的音频没有任何保留价值，
	// 也不该占用磁盘等待 LRU 自然过期。即使 policy 为零值也要执行。
	removed, freed, err := s.purgeDeletedBookSegments(ctx, policy.DryRun)
	if err != nil {
		return res, err
	}
	res.RemovedDeleted = removed
	res.FreedBytes += freed
	if policy.MaxBytes <= 0 && policy.KeepDays <= 0 {
		if policy.DryRun {
			res.BytesAfter = res.BytesBefore - freed
		} else if _, bytes, err := s.Cache.Stats(); err == nil {
			res.BytesAfter = bytes
		}
		if seg, _, err := s.Cache.Stats(); err == nil {
			res.SegmentsLeft = seg
		}
		return res, nil
	}
	_ = s.Repo.FlushAccess(ctx)
	active := s.activeKeys()
	now := time.Now().UTC()
	protectCutoff := now.Add(-s.protectWindow())
	var keepCutoff time.Time
	if policy.KeepDays > 0 {
		keepCutoff = now.AddDate(0, 0, -policy.KeepDays)
	}

	cands, err := s.Repo.PruneCandidates(ctx)
	if err != nil {
		return res, err
	}
	total := res.BytesBefore
	deleted := make([]string, 0, len(cands))
	for _, c := range cands {
		needBySize := policy.MaxBytes > 0 && total > policy.MaxBytes
		needByAge := policy.KeepDays > 0 && olderThan(c.LastAccess, keepCutoff)
		if !needBySize && !needByAge {
			continue
		}
		if _, ok := active[c.Key]; ok {
			res.Protected++
			continue
		}
		if recent(c.LastAccess, protectCutoff) {
			res.Protected++
			continue
		}
		size := c.CipherSize
		if size <= 0 {
			size = s.Cache.CipherSize(c.Key)
		}
		if !policy.DryRun {
			if err := s.Cache.Remove(c.Key); err != nil {
				return res, err
			}
			deleted = append(deleted, c.Key)
		}
		res.Removed++
		res.FreedBytes += size
		total -= size
		if total < 0 {
			total = 0
		}
	}
	if !policy.DryRun {
		if err := s.Repo.DeleteSegments(ctx, deleted); err != nil {
			return res, err
		}
	}
	if policy.DryRun {
		res.BytesAfter = res.BytesBefore - res.FreedBytes
	} else if _, bytes, err := s.Cache.Stats(); err == nil {
		res.BytesAfter = bytes
	}
	if seg, _, err := s.Cache.Stats(); err == nil {
		res.SegmentsLeft = seg
	}
	return res, nil
}

// recent 解析 SQLite 时间串并判断是否晚于 cutoff。
func recent(ts string, cutoff time.Time) bool {
	t, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		// SQLite 格式为 2026-11-26T04:00:00.000Z，RFC3339Nano 可解析小数秒
		t, err = time.Parse(time.RFC3339Nano, ts)
		if err != nil {
			return false
		}
	}
	return t.After(cutoff)
}

// olderThan 判断时间是否早于 cutoff（解析失败按“未超期”处理，保守不删）。
func olderThan(ts string, cutoff time.Time) bool {
	if ts == "" {
		return true
	}
	t, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		t, err = time.Parse(time.RFC3339Nano, ts)
		if err != nil {
			return false
		}
	}
	return t.Before(cutoff)
}

// cleanupAllRows 删除全部片段索引行（文件已删除或本来就不存在）。
func (r *Repo) cleanupAllRows(ctx context.Context) (int64, error) {
	res, err := r.db.ExecContext(ctx, `DELETE FROM tts_segments`)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// deleteAllExcept 删除除 keep 之外的片段索引行（保护正在朗读的片段）。
func (r *Repo) deleteAllExcept(ctx context.Context, keep []string) error {
	if len(keep) == 0 {
		_, err := r.cleanupAllRows(ctx)
		return err
	}
	q := `DELETE FROM tts_segments WHERE cache_key NOT IN (` + placeholders(len(keep)) + `)`
	_, err := r.db.ExecContext(ctx, q, toAnyStrings(keep)...)
	return err
}

// VerifyCacheSample 抽样验证缓存文件可解密（轮换后校验用）。
func (s *Service) VerifyCacheSample(ctx context.Context, sample int) (checked int, err error) {
	if sample <= 0 {
		sample = 5
	}
	var keys []string
	_ = s.Cache.WalkKeys(func(key string, _ int64, _ int) error {
		if len(keys) < sample {
			keys = append(keys, key)
		}
		return nil
	})
	for _, k := range keys {
		if _, err := s.Cache.Get(k); err != nil {
			if errors.Is(err, ttscache.ErrNotFound) {
				continue
			}
			return checked, fmt.Errorf("缓存片段校验失败: %w", err)
		}
		checked++
	}
	return checked, nil
}

// ProtectWindowSeconds 返回淘汰保护窗口秒数（仅用于展示）。
func (s *Service) ProtectWindowSeconds() int {
	return int(s.protectWindow().Seconds())
}

// CacheDir 返回缓存目录（仅用于展示与诊断）。
func (s *Service) CacheDir() string {
	if s.Cache == nil {
		return ""
	}
	return s.Cache.Dir()
}

// dirExists 判断目录是否存在（供指标展示，不读取内容）。
func dirExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

// purgeDeletedBookSegments 回收「所属书籍已软删除」的全部音频片段。
//
// 这是第九阶段软删除语义的配套动作：书籍被删后其音频不再有播放入口，
// 即使 LRU 还没轮到它，也应该立即释放磁盘。
// 正在流式输出的片段（本进程引用计数 > 0）会被跳过，下次再清理。
func (s *Service) purgeDeletedBookSegments(ctx context.Context, dryRun bool) (removed int, freed int64, err error) {
	rows, err := s.Repo.db.QueryContext(ctx, `
		SELECT t.cache_key
		FROM tts_segments t
		JOIN books b ON b.id = t.book_id
		WHERE b.deleted_at IS NOT NULL`)
	if err != nil {
		return 0, 0, err
	}
	var keys []string
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			rows.Close()
			return 0, 0, err
		}
		keys = append(keys, k)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, 0, err
	}
	if len(keys) == 0 {
		return 0, 0, nil
	}
	active := s.activeKeys()
	deleted := make([]string, 0, len(keys))
	for _, k := range keys {
		if _, busy := active[k]; busy {
			continue
		}
		size := s.Cache.CipherSize(k)
		if !dryRun {
			if err := s.Cache.Remove(k); err != nil {
				continue
			}
			deleted = append(deleted, k)
		}
		removed++
		freed += size
	}
	if !dryRun && len(deleted) > 0 {
		if err := s.Repo.DeleteSegments(ctx, deleted); err != nil {
			return removed, freed, err
		}
	}
	return removed, freed, nil
}
