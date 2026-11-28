package mobile

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"novelstack/internal/db"
)

func newDB(t *testing.T) *sql.DB {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatalf("db: %v", err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

func insertBook(t *testing.T, d *sql.DB, id, title string, seq int) {
	t.Helper()
	_, err := d.Exec(`INSERT INTO books
		(id, title, author, tags, description, orig_filename, size_bytes, sha256,
		 wrapped_dek, dek_nonce, kek_version, format_version, chunk_size, storage_path,
		 owner_id, sync_seq, chapter_count)
		VALUES (?,?,'作者','a,b','简介','x.txt',123,'abc',x'01',x'02',1,1,1048576,'ciphertext/x','admin',?,2)`,
		id, title, seq)
	if err != nil {
		t.Fatalf("insert book: %v", err)
	}
	for i := 0; i < 2; i++ {
		if _, err := d.Exec(`INSERT INTO book_chapters
			(book_id, chapter_index, title, chars, start_rune, end_rune) VALUES (?,?,?,?,?,?)`,
			id, i, "第"+string(rune('一'+i))+"章", 10, i*10, i*10+10); err != nil {
			t.Fatalf("insert chapter: %v", err)
		}
	}
}

func TestManifestIncremental(t *testing.T) {
	d := newDB(t)
	ctx := context.Background()
	insertBook(t, d, "b1", "书一", 1)
	insertBook(t, d, "b2", "书二", 2)

	// 全量
	m, err := BuildManifest(ctx, d, "admin", 0, nil)
	if err != nil {
		t.Fatalf("manifest: %v", err)
	}
	if !m.Full || m.Cursor != 2 || m.Count != 2 || len(m.Deleted) != 0 {
		t.Fatalf("full manifest = %+v", m)
	}
	if len(m.Books[0].Chapters) != 2 || m.Books[0].Chapters[0].ETag == "" {
		t.Fatalf("chapters missing: %+v", m.Books[0].Chapters)
	}
	if m.Books[0].WrappedDEK != "" {
		t.Fatal("no wrap func should omit wrapped_dek")
	}

	// 无变更
	m, err = BuildManifest(ctx, d, "admin", 2, nil)
	if err != nil {
		t.Fatal(err)
	}
	if m.Full || m.Count != 0 || len(m.Deleted) != 0 || m.Cursor != 2 {
		t.Fatalf("empty incremental = %+v", m)
	}

	// 一本书改元数据 → sync_seq=3
	if _, err := d.Exec(`UPDATE books SET title='书一改', sync_seq=3 WHERE id='b1'`); err != nil {
		t.Fatal(err)
	}
	m, _ = BuildManifest(ctx, d, "admin", 2, nil)
	if m.Count != 1 || m.Books[0].ID != "b1" || m.Books[0].Title != "书一改" {
		t.Fatalf("incremental after update = %+v", m)
	}

	// 软删 → deleted 列表
	if _, err := d.Exec(`UPDATE books SET deleted_at='2026-01-01T00:00:00Z', sync_seq=4 WHERE id='b2'`); err != nil {
		t.Fatal(err)
	}
	m, _ = BuildManifest(ctx, d, "admin", 3, nil)
	if len(m.Deleted) != 1 || m.Deleted[0] != "b2" || m.Count != 0 || m.Cursor != 4 {
		t.Fatalf("deleted manifest = %+v", m)
	}
	// 全量不再返回已删除的书
	m, _ = BuildManifest(ctx, d, "admin", 0, nil)
	if m.Count != 1 || m.Books[0].ID != "b1" {
		t.Fatalf("full after delete = %+v", m)
	}
	if m.Books[0].WrappedDEK != "" {
		t.Fatal("wrap func nil should not add keys")
	}

	// 客户端游标超前 → 要求重置
	m, _ = BuildManifest(ctx, d, "admin", 99, nil)
	if !m.ResetRequired {
		t.Fatalf("cursor ahead should set reset_required: %+v", m)
	}
}

func TestManifestWrapsDEK(t *testing.T) {
	d := newDB(t)
	insertBook(t, d, "b1", "书一", 1)
	called := ""
	m, err := BuildManifest(context.Background(), d, "admin", 0, func(bookID string) (string, error) {
		called = bookID
		return "wrapped-" + bookID, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if called != "b1" || m.Books[0].WrappedDEK != "wrapped-b1" {
		t.Fatalf("wrap not applied: called=%q book=%+v", called, m.Books[0])
	}
}

func TestProgressKeepsFurthestPosition(t *testing.T) {
	d := newDB(t)
	ctx := context.Background()
	insertBook(t, d, "b1", "书一", 1)

	if _, err := PutProgress(ctx, d, "admin", "b1", Progress{ChapterIndex: 1, CharOffset: 20, Percent: 0.1}, "dev-a"); err != nil {
		t.Fatalf("put: %v", err)
	}
	time.Sleep(2 * time.Millisecond)
	if _, err := PutProgress(ctx, d, "admin", "b1", Progress{ChapterIndex: 2, CharOffset: 30, Percent: 0.6}, "dev-b"); err != nil {
		t.Fatalf("put2: %v", err)
	}
	got, err := GetProgress(ctx, d, "admin", "b1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.ChapterIndex != 2 || got.CharOffset != 30 || got.DeviceID != "dev-b" || got.Title != "书一" {
		t.Fatalf("further position should win: %+v", got)
	}

	// 更晚但更靠前的写入不得覆盖（多端同步回翻场景）。
	time.Sleep(2 * time.Millisecond)
	back, err := PutProgress(ctx, d, "admin", "b1", Progress{ChapterIndex: 0, CharOffset: 1, Percent: 0.02}, "dev-c")
	if err != nil {
		t.Fatalf("put back: %v", err)
	}
	if back.ChapterIndex != 2 || back.CharOffset != 30 {
		t.Fatalf("earlier position overwrote: %+v", back)
	}
	again, _ := GetProgress(ctx, d, "admin", "b1")
	if again.ChapterIndex != 2 || again.DeviceID != "dev-b" {
		t.Fatalf("store changed by earlier position: %+v", again)
	}

	// 越界字段
	for _, p := range []Progress{
		{ChapterIndex: -1}, {CharOffset: -1}, {Percent: 1.5}, {Percent: -0.1},
	} {
		if _, err := PutProgress(ctx, d, "admin", "b1", p, ""); err != ErrProgressInvalid {
			t.Fatalf("progress %+v err = %v", p, err)
		}
	}
	if _, err := PutProgress(ctx, d, "admin", "nope", Progress{}, ""); err == nil {
		t.Fatal("unknown book should fail")
	}
	if _, err := GetProgress(ctx, d, "admin", "b1-missing"); err == nil {
		t.Fatal("missing progress should fail")
	}

	recent, err := RecentProgress(ctx, d, "admin", 5)
	if err != nil || len(recent) != 1 || recent[0].BookID != "b1" {
		t.Fatalf("recent = %+v err=%v", recent, err)
	}
}

func TestLimiterTokenBucket(t *testing.T) {
	l := NewLimiter(1, 2) // 1/min, burst 2
	now := time.Now()
	l.now = func() time.Time { return now }

	if ok, _ := l.Allow("d1"); !ok {
		t.Fatal("first should pass")
	}
	if ok, _ := l.Allow("d1"); !ok {
		t.Fatal("second should pass")
	}
	if ok, wait := l.Allow("d1"); ok || wait <= 0 {
		t.Fatalf("third should be limited, wait=%v", wait)
	}
	// 其他 key 不受影响
	if ok, _ := l.Allow("d2"); !ok {
		t.Fatal("other key should pass")
	}
	now = now.Add(61 * time.Second)
	if ok, _ := l.Allow("d1"); !ok {
		t.Fatal("should refill after a minute")
	}
}
