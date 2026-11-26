package backup

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"novelstack/internal/audit"
	"novelstack/internal/db"
)

func schedulerFixture(t *testing.T, now time.Time) (*Scheduler, string, *audit.Logger) {
	t.Helper()
	dir := t.TempDir()
	database, err := db.Open(filepath.Join(dir, "app.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	a := audit.New(database)
	out := filepath.Join(dir, "backups")
	if err := os.MkdirAll(out, 0o700); err != nil {
		t.Fatal(err)
	}
	s := NewScheduler(SchedulerOptions{
		Enabled: true, Hour: 3, Minute: 0, RetentionDays: 7,
		DataDir: dir, OutDir: out,
		Passphrase: []byte("scheduler-test-passphrase"),
		Params:     KDFParams{Time: 1, Memory: 8 * 1024, Threads: 1},
		DB:         database, Audit: a,
		Now: func() time.Time { return now },
	})
	return s, out, a
}

func putFakePackage(t *testing.T, dir, name string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte("fake"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name+".sha256"), []byte("deadbeef  "+name+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func actions(t *testing.T, a *audit.Logger, action string) int {
	t.Helper()
	entries, err := a.Query(context.Background(), audit.Filter{Action: action, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	return len(entries)
}

func TestSchedulerDueOnceAndRetention(t *testing.T) {
	now := time.Date(2026, 3, 10, 3, 0, 0, 0, time.UTC)
	s, out, a := schedulerFixture(t, now)

	old := "novelstack-20260301-030000-aaaaaaaa.nsb"
	recent := "novelstack-20260309-030000-bbbbbbbb.nsb"
	manual := "novelstack-20260101-010101.nsb"
	for _, n := range []string{old, recent, manual} {
		putFakePackage(t, out, n)
	}

	// 同一天 02:59 不触发；03:00 触发；当天再次巡检不重复。
	if s.due(now.Add(-time.Minute)) {
		t.Fatal("03:00 之前不应触发")
	}
	s.tick(now)
	if s.due(now.Add(30 * time.Minute)) {
		t.Fatal("同一天不应重复触发")
	}
	if got := actions(t, a, "backup_scheduled"); got != 1 {
		t.Fatalf("backup_scheduled = %d, want 1", got)
	}

	entries, err := os.ReadDir(out)
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]bool{}
	for _, e := range entries {
		found[e.Name()] = true
		if strings.HasSuffix(e.Name(), ".nsb") && strings.HasPrefix(e.Name(), "novelstack-20260310-030000-") && !IsAutoBackupName(e.Name()) {
			t.Fatalf("新包名不合规: %s", e.Name())
		}
	}
	if found[old] || found[old+".sha256"] {
		t.Fatal("过期自动备份未被删除")
	}
	if !found[recent] || !found[manual] {
		t.Fatal("保留窗口内的包与手工备份不应被删除")
	}
	if got := actions(t, a, "backup_pruned"); got != 1 {
		t.Fatalf("backup_pruned = %d, want 1", got)
	}

	// 新包已写入 backup_runs 且路径带校验和前缀。
	var path string
	if err := s.opts.DB.QueryRow(`SELECT path FROM backup_runs ORDER BY id DESC LIMIT 1`).Scan(&path); err != nil {
		t.Fatal(err)
	}
	if !IsAutoBackupName(path) || !strings.Contains(path, "-030000-") {
		t.Fatalf("backup_runs.path = %s", path)
	}
}

func TestSchedulerFailureDoesNotDeleteAnything(t *testing.T) {
	now := time.Date(2026, 3, 10, 3, 0, 0, 0, time.UTC)
	s, out, a := schedulerFixture(t, now)
	keep := "novelstack-20260101-010101-deadbeef.nsb"
	putFakePackage(t, out, keep)

	// 数据目录不存在 → 备份失败；已有包必须原封不动。
	s.opts.DataDir = filepath.Join(t.TempDir(), "missing")
	if _, err := s.RunOnce(context.Background()); err == nil {
		t.Fatal("期望备份失败")
	}
	if _, err := os.Stat(filepath.Join(out, keep)); err != nil {
		t.Fatalf("失败后不应删除已有备份: %v", err)
	}
	entries, err := a.Query(context.Background(), audit.Filter{Action: "backup_failed"})
	if err != nil || len(entries) != 1 {
		t.Fatalf("backup_failed 审计 = %+v err=%v", entries, err)
	}
	if !strings.Contains(entries[0].Detail, "reason=") {
		t.Fatalf("失败审计缺少原因: %s", entries[0].Detail)
	}
	if got := actions(t, a, "backup_scheduled"); got != 0 {
		t.Fatal("失败时不应写成功审计")
	}
}

func TestIsAutoBackupName(t *testing.T) {
	good := []string{
		"novelstack-20260310-030000-0123abcd.nsb",
		"novelstack-20260310-030000-00000000.nsb",
	}
	bad := []string{
		"novelstack-20260310-030000.nsb",          // 手工备份
		"novelstack-20260310-030000-ABCDEFAB.nsb", // 大写
		"novelstack-2026-03-10-0123abcd.nsb",
		"novelstack-20260310-030000-0123abcd.nsb.tmp",
		"../novelstack-20260310-030000-0123abcd.nsb",
		"notes.txt",
	}
	for _, n := range good {
		if !IsAutoBackupName(n) {
			t.Fatalf("应识别为自动备份: %s", n)
		}
	}
	for _, n := range bad {
		if IsAutoBackupName(n) {
			t.Fatalf("不应识别为自动备份: %s", n)
		}
	}
}
