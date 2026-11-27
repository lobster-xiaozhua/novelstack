package telemetry

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"novelstack/internal/db"
)

// 第九阶段：聚合 SQL 与索引的验收用例。

func openTestStore(t *testing.T) (*Store, func()) {
	t.Helper()
	database, err := db.Open(filepath.Join(t.TempDir(), "telemetry-test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	return NewStore(database), func() { _ = database.Close() }
}

func seed(t *testing.T, s *Store, base time.Time, kind, name string, durations []int64) {
	t.Helper()
	c := Cleaned{DeviceID: "dev-1", AppVersion: "0.2.1", OS: "14", Model: "Pixel 8"}
	for _, d := range durations {
		c.Events = append(c.Events, CleanEvent{
			Kind: kind, Name: name, Severity: "info",
			EventTime: base, DurationMS: d, ContextJSON: "{}", Count: 1,
		})
	}
	if _, err := s.InsertBatch(context.Background(), "admin", c, time.Now()); err != nil {
		t.Fatalf("insert: %v", err)
	}
}

func TestAggregateHourProducesP50P95(t *testing.T) {
	s, closeFn := openTestStore(t)
	defer closeFn()
	hour := time.Date(2026, 11, 27, 10, 0, 0, 0, time.UTC)
	seed(t, s, hour.Add(5*time.Minute), KindSlowChapter, "夜航船·第一章", []int64{100, 200, 300, 400, 500})

	n, err := s.AggregateHour(context.Background(), hour)
	if err != nil {
		t.Fatalf("aggregate: %v", err)
	}
	if n != 1 {
		t.Fatalf("应生成 1 行聚合，实际 %d", n)
	}

	rows, err := s.Series(context.Background(), hour.Add(-time.Hour), KindSlowChapter)
	if err != nil {
		t.Fatalf("series: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("聚合行数 = %d", len(rows))
	}
	r := rows[0]
	if r.Count != 5 || r.P50MS != 300 || r.P95MS != 500 {
		t.Fatalf("聚合结果错误: count=%d p50=%d p95=%d（期望 5/300/500）", r.Count, r.P50MS, r.P95MS)
	}

	// 重跑必须幂等：同一小时再聚合一次，行数不增加、数值不变。
	if _, err := s.AggregateHour(context.Background(), hour); err != nil {
		t.Fatalf("re-aggregate: %v", err)
	}
	rows, _ = s.Series(context.Background(), hour.Add(-time.Hour), KindSlowChapter)
	if len(rows) != 1 || rows[0].Count != 5 {
		t.Fatalf("重跑产生了重复行: %+v", rows)
	}
}

func TestAggregateQueryUsesIndex(t *testing.T) {
	s, closeFn := openTestStore(t)
	defer closeFn()
	var detail string
	err := s.db.QueryRow(`
EXPLAIN QUERY PLAN
SELECT kind, name, duration_ms FROM telemetry_events
WHERE event_time >= ? AND event_time < ? AND duration_ms > 0
ORDER BY kind, name, duration_ms`, "2026-11-27T10:00:00.000Z", "2026-11-27T11:00:00.000Z").Scan(
		new(int), new(int), new(int), &detail)
	if err != nil {
		t.Fatalf("explain: %v", err)
	}
	t.Logf("query plan: %s", detail)
	if !strings.Contains(detail, "USING INDEX") {
		t.Fatalf("聚合查询未走索引: %s", detail)
	}
	if !strings.Contains(detail, "telemetry_events") {
		t.Fatalf("计划里没出现目标表: %s", detail)
	}
}

func TestCleanupDropsOldDetailsKeepsRollups(t *testing.T) {
	s, closeFn := openTestStore(t)
	defer closeFn()
	now := time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)
	oldHour := now.Add(-100 * 24 * time.Hour)
	seed(t, s, oldHour, KindAPI, "/api/books", []int64{120})
	if _, err := s.AggregateHour(context.Background(), oldHour); err != nil {
		t.Fatalf("aggregate: %v", err)
	}

	deleted, err := s.Cleanup(context.Background(), now.Add(-90*24*time.Hour))
	if err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	if deleted != 1 {
		t.Fatalf("应删除 1 条明细，实际 %d", deleted)
	}
	rows, err := s.Series(context.Background(), oldHour.Add(-time.Hour), KindAPI)
	if err != nil {
		t.Fatalf("series: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("聚合应保留，实际 %d 行", len(rows))
	}
}

func TestOverviewAggregatesDevicesAndCrashes(t *testing.T) {
	s, closeFn := openTestStore(t)
	defer closeFn()
	now := time.Now().UTC()
	seed(t, s, now.Add(-time.Hour), KindCrash, "java.lang.IllegalStateException", []int64{0})
	seed(t, s, now.Add(-30*time.Minute), KindAPI, "/api/discover", []int64{210, 320})

	o, err := s.Overview(context.Background(), now.Add(-24*time.Hour))
	if err != nil {
		t.Fatalf("overview: %v", err)
	}
	if o.ActiveDevices != 1 {
		t.Fatalf("活跃设备 = %d", o.ActiveDevices)
	}
	if o.Crashes != 1 {
		t.Fatalf("崩溃数 = %d", o.Crashes)
	}
	if o.APIP95MS < 300 {
		t.Fatalf("API P95 过低: %d", o.APIP95MS)
	}
}
