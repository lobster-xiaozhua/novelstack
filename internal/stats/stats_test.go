package stats

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"novelstack/internal/db"
)

func newTestService(t *testing.T) *Service {
	t.Helper()
	database, err := db.Open(filepath.Join(t.TempDir(), "stats.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	return New(database, time.UTC)
}

func TestAddDailyAccumulates(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	if err := svc.AddDaily(ctx, "admin", 60, 1); err != nil {
		t.Fatal(err)
	}
	if err := svc.AddDaily(ctx, "admin", 120, 0); err != nil {
		t.Fatal(err)
	}
	ov, err := svc.Overview(ctx, "admin")
	if err != nil {
		t.Fatal(err)
	}
	if ov.TodaySeconds != 180 {
		t.Fatalf("today = %d", ov.TodaySeconds)
	}
	if ov.TotalSeconds != 180 {
		t.Fatalf("total = %d", ov.TotalSeconds)
	}
	if ov.StreakDays != 1 {
		t.Fatalf("streak = %d", ov.StreakDays)
	}
	if len(ov.Days) != WindowDays {
		t.Fatalf("days len = %d", len(ov.Days))
	}
}

func TestAddDailyIgnoresNonPositive(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	if err := svc.AddDaily(ctx, "admin", 0, 0); err != nil {
		t.Fatal(err)
	}
	if err := svc.AddDaily(ctx, "admin", -5, 0); err != nil {
		t.Fatal(err)
	}
	ov, _ := svc.Overview(ctx, "admin")
	if ov.TotalSeconds != 0 || ov.TodaySeconds != 0 {
		t.Fatalf("不应写入: %+v", ov)
	}
}

func TestAddDailyCapsSingleReport(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	if err := svc.AddDaily(ctx, "admin", 100000, 0); err != nil {
		t.Fatal(err)
	}
	ov, _ := svc.Overview(ctx, "admin")
	if ov.TodaySeconds != MaxSecondsPerReport {
		t.Fatalf("today = %d, want %d", ov.TodaySeconds, MaxSecondsPerReport)
	}
}

func TestStreakCountsConsecutiveDays(t *testing.T) {
	current, best := streaks([]string{"2026-11-25", "2026-11-26", "2026-11-27"}, "2026-11-27")
	if current != 3 || best != 3 {
		t.Fatalf("current=%d best=%d", current, best)
	}
}

func TestStreakAllowsMissingToday(t *testing.T) {
	current, _ := streaks([]string{"2026-11-25", "2026-11-26"}, "2026-11-27")
	if current != 2 {
		t.Fatalf("current = %d", current)
	}
}

func TestStreakBreaksOnGap(t *testing.T) {
	current, best := streaks([]string{"2026-11-20", "2026-11-21", "2026-11-22"}, "2026-11-27")
	if current != 0 {
		t.Fatalf("断签后 current 应为 0，得到 %d", current)
	}
	if best != 3 {
		t.Fatalf("best = %d", best)
	}
}

func TestStreakEmpty(t *testing.T) {
	if c, b := streaks(nil, "2026-11-27"); c != 0 || b != 0 {
		t.Fatalf("c=%d b=%d", c, b)
	}
}
