package tts

import (
	"context"
	"crypto/rand"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	"novelstack/internal/db"
	"novelstack/internal/ttscache"
)

type cacheFixture struct {
	svc   *Service
	db    *sql.DB
	cache *ttscache.Store
	dir   string
}

func newCacheFixture(t *testing.T) *cacheFixture {
	t.Helper()
	dir := t.TempDir()
	database, err := db.Open(filepath.Join(dir, "app.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	kek := make([]byte, 32)
	if _, err := rand.Read(kek); err != nil {
		t.Fatal(err)
	}
	cache, err := ttscache.New(filepath.Join(dir, "tts-cache"), kek, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	return &cacheFixture{
		svc: &Service{Cache: cache, Repo: NewRepo(database), ProtectWindow: 5 * time.Minute},
		db:  database, cache: cache, dir: dir,
	}
}

// addSegment 造一个指定密文大小的片段，并设置最后访问时间。
func (f *cacheFixture) addSegment(t *testing.T, key, bookID string, plainBytes int, accessed time.Time) {
	t.Helper()
	payload := make([]byte, plainBytes)
	if _, err := rand.Read(payload); err != nil {
		t.Fatal(err)
	}
	if err := f.cache.Put(key, payload); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.Repo.UpsertSegment(context.Background(), SegmentRecord{
		CacheKey: key, BookID: bookID, Provider: "mock", PlainBytes: int64(plainBytes),
		CipherBytes: f.cache.CipherSize(key), KEKVersion: 1,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec(`UPDATE tts_segments SET last_access = ? WHERE cache_key = ?`,
		accessed.UTC().Format(time.RFC3339Nano), key); err != nil {
		t.Fatal(err)
	}
}

func (f *cacheFixture) exists(t *testing.T, key string) bool {
	t.Helper()
	p, err := f.cache.PathFor(key)
	if err != nil {
		t.Fatal(err)
	}
	_, err = os.Stat(p)
	return err == nil
}

func TestPruneRemovesLeastRecentlyUsed(t *testing.T) {
	f := newCacheFixture(t)
	now := time.Now()
	f.addSegment(t, "aaaa0001", "bookA", 1000, now.Add(-10*24*time.Hour))
	f.addSegment(t, "aaaa0002", "bookA", 2000, now.Add(-8*24*time.Hour))
	f.addSegment(t, "bbbb0003", "bookB", 3000, now.Add(-1*time.Hour))
	f.addSegment(t, "bbbb0004", "bookB", 4000, now)

	_, total, err := f.cache.Stats()
	if err != nil {
		t.Fatal(err)
	}
	// 上限低于总量约 2.5KB：应淘汰最久未访问的两片
	res, err := f.svc.PruneCache(context.Background(), PrunePolicy{MaxBytes: total - 2500})
	if err != nil {
		t.Fatal(err)
	}
	if res.Removed != 2 || res.FreedBytes == 0 {
		t.Fatalf("prune = %+v", res)
	}
	if f.exists(t, "aaaa0001") || f.exists(t, "aaaa0002") {
		t.Fatal("最久未访问的片段未删除")
	}
	if !f.exists(t, "bbbb0003") || !f.exists(t, "bbbb0004") {
		t.Fatal("最近访问的片段被误删")
	}
	if res.BytesAfter > res.MaxBytes {
		t.Fatalf("after = %d > max = %d", res.BytesAfter, res.MaxBytes)
	}
	var n int
	if err := f.db.QueryRow(`SELECT COUNT(*) FROM tts_segments`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("rows = %d", n)
	}
}

func TestPruneProtectsActiveAndRecent(t *testing.T) {
	f := newCacheFixture(t)
	now := time.Now()
	f.addSegment(t, "cccc0001", "bookA", 1000, now.Add(-10*24*time.Hour))
	f.addSegment(t, "cccc0002", "bookA", 1000, now.Add(-10*24*time.Hour))
	f.addSegment(t, "cccc0003", "bookA", 1000, now.Add(-1*time.Minute)) // 保护窗口内
	// 模拟正在朗读：引用 cccc0001
	release := f.svc.acquireKeys([]seg{{key: "cccc0001"}})
	defer release()

	res, err := f.svc.PruneCache(context.Background(), PrunePolicy{MaxBytes: 1})
	if err != nil {
		t.Fatal(err)
	}
	if res.Protected < 2 {
		t.Fatalf("protected = %d, want >=2 (%+v)", res.Protected, res)
	}
	if !f.exists(t, "cccc0001") {
		t.Fatal("正在朗读的片段被删除")
	}
	if !f.exists(t, "cccc0003") {
		t.Fatal("保护窗口内的片段被删除")
	}
	if f.exists(t, "cccc0002") {
		t.Fatal("冷片段未被删除")
	}
}

func TestPruneKeepDays(t *testing.T) {
	f := newCacheFixture(t)
	now := time.Now()
	f.addSegment(t, "dddd0001", "bookA", 1000, now.Add(-10*24*time.Hour))
	f.addSegment(t, "dddd0002", "bookA", 1000, now.Add(-48*time.Hour))
	f.addSegment(t, "dddd0003", "bookA", 1000, now.Add(-1*time.Minute))
	res, err := f.svc.PruneCache(context.Background(), PrunePolicy{KeepDays: 1})
	if err != nil {
		t.Fatal(err)
	}
	if res.Removed != 2 {
		t.Fatalf("removed = %d (%+v)", res.Removed, res)
	}
	if !f.exists(t, "dddd0003") {
		t.Fatal("最近片段被误删")
	}
}

func TestClearByBookAndAll(t *testing.T) {
	f := newCacheFixture(t)
	now := time.Now()
	f.addSegment(t, "eeee0001", "bookA", 500, now)
	f.addSegment(t, "eeee0002", "bookA", 500, now)
	f.addSegment(t, "eeee0003", "bookB", 500, now)

	res, err := f.svc.ClearCache(context.Background(), "bookA")
	if err != nil {
		t.Fatal(err)
	}
	if res.Removed != 2 || res.Scope != "book" {
		t.Fatalf("clear A = %+v", res)
	}
	if f.exists(t, "eeee0001") || f.exists(t, "eeee0002") {
		t.Fatal("bookA 片段未删除")
	}
	if !f.exists(t, "eeee0003") {
		t.Fatal("bookB 片段被误删")
	}

	// 全量清理，但保护正在朗读的 bookB
	release := f.svc.acquireKeys([]seg{{key: "eeee0003"}})
	res, err = f.svc.ClearCache(context.Background(), "")
	release()
	if err != nil {
		t.Fatal(err)
	}
	if res.Removed != 0 || res.Protected != 1 {
		t.Fatalf("clear all = %+v", res)
	}
	if !f.exists(t, "eeee0003") {
		t.Fatal("被引用片段被删除")
	}
}

func TestPruneZeroPolicyDoesNothing(t *testing.T) {
	f := newCacheFixture(t)
	f.addSegment(t, "ffff0001", "bookA", 500, time.Now().Add(-time.Hour))
	res, err := f.svc.PruneCache(context.Background(), PrunePolicy{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Removed != 0 {
		t.Fatalf("zero policy removed = %d", res.Removed)
	}
	if !f.exists(t, "ffff0001") {
		t.Fatal("零策略不应删除")
	}
}

func TestClearExpiredUnknownKeyStillOk(t *testing.T) {
	f := newCacheFixture(t)
	// 只有索引行、没有文件：清理应成功并清掉行
	if err := f.svc.Repo.UpsertSegment(context.Background(), SegmentRecord{
		CacheKey: "99990000", BookID: "ghost", Provider: "mock", CipherBytes: 10,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.ClearCache(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := f.db.QueryRow(`SELECT COUNT(*) FROM tts_segments`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("rows = %d", n)
	}
}
