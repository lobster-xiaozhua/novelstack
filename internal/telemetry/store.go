package telemetry

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"time"
)

// TimeLayout 是库内统一的时间格式：UTC + 毫秒 + 固定宽度。
// 固定宽度很关键——event_time 用 TEXT 存储，区间查询依赖字典序与时间序一致。
const TimeLayout = "2006-01-02T15:04:05.000Z"

// Store 是遥测数据的持久层。
type Store struct {
	db *sql.DB
}

func NewStore(db *sql.DB) *Store { return &Store{db: db} }

// InsertBatch 在一个事务里写入整批事件，返回实际写入条数。
// 用事务而不是逐条自动提交：一批 500 条时能少 500 次 fsync。
func (s *Store) InsertBatch(ctx context.Context, ownerID string, c Cleaned, received time.Time) (int, error) {
	if len(c.Events) == 0 {
		return 0, nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()

	stmt, err := tx.PrepareContext(ctx, `
INSERT INTO telemetry_events
    (device_id, owner_id, kind, name, severity, event_time, received_at,
     app_version, os_version, model, duration_ms, stack, context_json, occurrence)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		return 0, err
	}
	defer stmt.Close()

	n := 0
	for _, e := range c.Events {
		if _, err := stmt.ExecContext(ctx,
			c.DeviceID, ownerID, e.Kind, e.Name, e.Severity,
			e.EventTime.Format(TimeLayout), received.UTC().Format(TimeLayout),
			c.AppVersion, c.OS, c.Model, e.DurationMS, e.Stack, e.ContextJSON, e.Count,
		); err != nil {
			return n, err
		}
		n++
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return n, nil
}

// AggregateHour 重新计算某个整点小时的 P50/P95 并幂等写入聚合表。
// 重复执行同一小时是安全的（UPSERT），调度器重试不会产生重复行。
func (s *Store) AggregateHour(ctx context.Context, hour time.Time) (int, error) {
	start := hour.UTC().Truncate(time.Hour)
	end := start.Add(time.Hour)
	rows, err := s.db.QueryContext(ctx, `
SELECT kind, name, duration_ms
FROM telemetry_events
WHERE event_time >= ? AND event_time < ? AND duration_ms > 0
ORDER BY kind, name, duration_ms`, start.Format(TimeLayout), end.Format(TimeLayout))
	if err != nil {
		return 0, err
	}
	defer rows.Close()

	type key struct{ kind, name string }
	groups := map[key][]int64{}
	for rows.Next() {
		var k key
		var d int64
		if err := rows.Scan(&k.kind, &k.name, &d); err != nil {
			return 0, err
		}
		groups[k] = append(groups[k], d)
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	stmt, err := tx.PrepareContext(ctx, `
INSERT INTO telemetry_rollups (bucket, kind, name, count, p50_ms, p95_ms)
VALUES (?,?,?,?,?,?)
ON CONFLICT(bucket, kind, name) DO UPDATE SET
    count = excluded.count, p50_ms = excluded.p50_ms, p95_ms = excluded.p95_ms`)
	if err != nil {
		return 0, err
	}
	defer stmt.Close()

	bucket := start.Format(TimeLayout)
	n := 0
	for k, ds := range groups {
		sort.Slice(ds, func(i, j int) bool { return ds[i] < ds[j] })
		if _, err := stmt.ExecContext(ctx, bucket, k.kind, k.name, len(ds), percentile(ds, 0.50), percentile(ds, 0.95)); err != nil {
			return 0, err
		}
		n++
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return n, nil
}

// Cleanup 删除早于 before 的明细。聚合表永久保留（体积只有明细的千分之一量级）。
func (s *Store) Cleanup(ctx context.Context, before time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM telemetry_events WHERE event_time < ?`, before.UTC().Format(TimeLayout))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// ---- 控制台查询 ----

type Overview struct {
	SinceDays      int   `json:"since_days"`
	ActiveDevices  int64 `json:"active_devices"`
	Events         int64 `json:"events"`
	Crashes        int64 `json:"crashes"`
	APIP95MS       int64 `json:"api_p95_ms"`
	ColdStartP95MS int64 `json:"cold_start_p95_ms"`
	TTSP95MS       int64 `json:"tts_p95_ms"`
}

type HeatCell struct {
	Model string `json:"model"`
	Kind  string `json:"kind"`
	Count int64  `json:"count"`
}

type SlowChapter struct {
	Name  string `json:"name"`
	Count int64  `json:"count"`
	AvgMS int64  `json:"avg_ms"`
	MaxMS int64  `json:"max_ms"`
	P95MS int64  `json:"p95_ms"`
}

type CrashRow struct {
	EventTime  string          `json:"event_time"`
	DeviceID   string          `json:"device_id"`
	AppVersion string          `json:"app_version"`
	OS         string          `json:"os_version"`
	Model      string          `json:"model"`
	Name       string          `json:"name"`
	Stack      string          `json:"stack"`
	Context    json.RawMessage `json:"context"`
	Occurrence int64           `json:"occurrence"`
}

type RollupRow struct {
	Bucket string `json:"bucket"`
	Kind   string `json:"kind"`
	Name   string `json:"name"`
	Count  int64  `json:"count"`
	P50MS  int64  `json:"p50_ms"`
	P95MS  int64  `json:"p95_ms"`
}

// Overview 汇总 KPI。百分位在 Go 侧计算（SQLite 没有 percentile 函数）。
func (s *Store) Overview(ctx context.Context, since time.Time) (Overview, error) {
	o := Overview{SinceDays: int(time.Since(since).Hours() / 24)}
	ts := since.UTC().Format(TimeLayout)
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(DISTINCT device_id) FROM telemetry_events WHERE event_time >= ?`, ts).
		Scan(&o.ActiveDevices); err != nil {
		return o, err
	}
	if err := s.db.QueryRowContext(ctx,
		`SELECT COALESCE(SUM(occurrence),0) FROM telemetry_events WHERE event_time >= ?`, ts).
		Scan(&o.Events); err != nil {
		return o, err
	}
	if err := s.db.QueryRowContext(ctx,
		`SELECT COALESCE(SUM(occurrence),0) FROM telemetry_events WHERE event_time >= ? AND kind = ?`,
		ts, KindCrash).Scan(&o.Crashes); err != nil {
		return o, err
	}
	var err error
	if o.APIP95MS, err = s.p95(ctx, ts, KindAPI); err != nil {
		return o, err
	}
	if o.ColdStartP95MS, err = s.p95(ctx, ts, KindColdStart); err != nil {
		return o, err
	}
	if o.TTSP95MS, err = s.p95(ctx, ts, KindTTSFirstFrame); err != nil {
		return o, err
	}
	return o, nil
}

// p95 取某类事件在窗口内的 95 分位。上限 5 万行，避免异常量级把控制台拖垮。
func (s *Store) p95(ctx context.Context, sinceTS, kind string) (int64, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT duration_ms FROM telemetry_events
WHERE event_time >= ? AND kind = ? AND duration_ms > 0
ORDER BY duration_ms LIMIT 50000`, sinceTS, kind)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	var ds []int64
	for rows.Next() {
		var d int64
		if err := rows.Scan(&d); err != nil {
			return 0, err
		}
		ds = append(ds, d)
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	return percentile(ds, 0.95), nil
}

// Heatmap 返回「机型 × 事件类型」的计数，供控制台热力图使用。
func (s *Store) Heatmap(ctx context.Context, since time.Time) ([]HeatCell, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT model, kind, SUM(occurrence) AS c
FROM telemetry_events
WHERE event_time >= ? AND model <> ''
GROUP BY model, kind
ORDER BY c DESC
LIMIT 300`, since.UTC().Format(TimeLayout))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []HeatCell
	for rows.Next() {
		var c HeatCell
		if err := rows.Scan(&c.Model, &c.Kind, &c.Count); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// SlowChapters 返回最慢章节排行。name 是「书名·章节标题」，不含正文。
func (s *Store) SlowChapters(ctx context.Context, since time.Time, limit int) ([]SlowChapter, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT name, COUNT(*), AVG(duration_ms), MAX(duration_ms)
FROM telemetry_events
WHERE event_time >= ? AND kind = ?
GROUP BY name
ORDER BY MAX(duration_ms) DESC
LIMIT ?`, since.UTC().Format(TimeLayout), KindSlowChapter, limit)
	if err != nil {
		return nil, err
	}
	var out []SlowChapter
	for rows.Next() {
		var r SlowChapter
		var avg float64
		if err := rows.Scan(&r.Name, &r.Count, &avg, &r.MaxMS); err != nil {
			rows.Close()
			return nil, err
		}
		r.AvgMS = int64(avg)
		out = append(out, r)
	}
	err = rows.Err()
	rows.Close() // 必须先关闭再发新查询：连接池可能只有有限连接。
	if err != nil {
		return nil, err
	}
	// P95 需要逐章排序，行数很少（Top N），单独算一遍。
	for i := range out {
		p, err := s.chapterP95(ctx, since, out[i].Name)
		if err != nil {
			return nil, err
		}
		out[i].P95MS = p
	}
	return out, nil
}

func (s *Store) chapterP95(ctx context.Context, since time.Time, name string) (int64, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT duration_ms FROM telemetry_events
WHERE event_time >= ? AND kind = ? AND name = ?
ORDER BY duration_ms`, since.UTC().Format(TimeLayout), KindSlowChapter, name)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	var ds []int64
	for rows.Next() {
		var d int64
		if err := rows.Scan(&d); err != nil {
			return 0, err
		}
		ds = append(ds, d)
	}
	return percentile(ds, 0.95), rows.Err()
}

// Crashes 返回最近崩溃，带完整（已截断的）堆栈与上下文，供控制台展开。
func (s *Store) Crashes(ctx context.Context, since time.Time, limit int) ([]CrashRow, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT event_time, device_id, app_version, os_version, model, name, stack, context_json, occurrence
FROM telemetry_events
WHERE event_time >= ? AND kind IN (?, ?)
ORDER BY event_time DESC
LIMIT ?`, since.UTC().Format(TimeLayout), KindCrash, KindANR, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CrashRow
	for rows.Next() {
		var r CrashRow
		var ctx string
		if err := rows.Scan(&r.EventTime, &r.DeviceID, &r.AppVersion, &r.OS, &r.Model,
			&r.Name, &r.Stack, &ctx, &r.Occurrence); err != nil {
			return nil, err
		}
		if json.Valid([]byte(ctx)) {
			r.Context = json.RawMessage(ctx)
		} else {
			r.Context = json.RawMessage("{}")
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Series 返回按小时聚合的趋势，用于控制台折线/柱状图。
func (s *Store) Series(ctx context.Context, since time.Time, kind string) ([]RollupRow, error) {
	q := `SELECT bucket, kind, name, count, p50_ms, p95_ms FROM telemetry_rollups WHERE bucket >= ?`
	args := []any{since.UTC().Format(TimeLayout)}
	if kind != "" {
		q += ` AND kind = ?`
		args = append(args, kind)
	}
	q += ` ORDER BY bucket`
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RollupRow
	for rows.Next() {
		var r RollupRow
		if err := rows.Scan(&r.Bucket, &r.Kind, &r.Name, &r.Count, &r.P50MS, &r.P95MS); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// percentile 用「最近秩」法取分位：对 n 个已排序样本，idx = ceil(p*n)-1。
// 数据量小、无需插值时这比线性插值更符合「第 95 个百分位实际耗时」的直觉。
func percentile(sorted []int64, p float64) int64 {
	if len(sorted) == 0 {
		return 0
	}
	idx := int(float64(len(sorted))*p+0.999999) - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}

// FormatHour 把时间格式化为聚合桶（整点）。
func FormatHour(t time.Time) string { return t.UTC().Truncate(time.Hour).Format(TimeLayout) }

// EnsureStore 便于测试注入 nil 时报错更清晰。
func EnsureStore(s *Store) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("telemetry: store 未初始化")
	}
	return nil
}
