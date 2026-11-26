package kekrotate_test

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"novelstack/internal/audit"
	"novelstack/internal/books"
	"novelstack/internal/cryptobox"
	"novelstack/internal/db"
	"novelstack/internal/kekrotate"
	"novelstack/internal/ttscache"
)

type rotFixture struct {
	dir       string
	dbPath    string
	cipherDir string
	ttsDir    string
	oldKEK    []byte
	newKEK    []byte
	bookIDs   []string
}

func newRotFixture(t *testing.T, n int) *rotFixture {
	t.Helper()
	dir := t.TempDir()
	f := &rotFixture{
		dir: dir, dbPath: filepath.Join(dir, "app.db"),
		cipherDir: filepath.Join(dir, "ciphertext"), ttsDir: filepath.Join(dir, "tts-cache"),
		oldKEK: make([]byte, 32), newKEK: make([]byte, 32),
	}
	if _, err := rand.Read(f.oldKEK); err != nil {
		t.Fatal(err)
	}
	if _, err := rand.Read(f.newKEK); err != nil {
		t.Fatal(err)
	}
	database, err := db.Open(f.dbPath)
	if err != nil {
		t.Fatal(err)
	}
	svc := &books.Service{
		Store: books.NewStore(database), KEK: f.oldKEK, KEKVersion: 1,
		CiphertextDir: f.cipherDir, ChunkSize: 1 << 20, MaxUpload: 20 << 20,
		Audit: audit.New(database),
	}
	in := filepath.Join(dir, "in")
	if err := os.MkdirAll(in, 0o700); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < n; i++ {
		name := filepath.Join(in, "book-"+string(rune('a'+i))+".txt")
		content := "第一章 开始\n这是第 " + string(rune('A'+i)) + " 本书。\n第二章 结束\n完。\n"
		if err := os.WriteFile(name, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	rep, err := svc.ImportDirectory(context.Background(), books.ImportOptions{Dir: in, Username: "admin"})
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range rep.Items {
		if it.Status != "ok" {
			t.Fatalf("import %s: %s", it.File, it.Reason)
		}
		f.bookIDs = append(f.bookIDs, it.BookID)
	}
	// 音频缓存（用旧主 KEK 加密）
	store, err := ttscache.New(f.ttsDir, f.oldKEK, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"aaaa1111", "bbbb2222"} {
		if err := store.Put(k, []byte("RIFF-"+k)); err != nil {
			t.Fatal(err)
		}
	}
	repo := newRepo(database)
	for _, k := range []string{"aaaa1111", "bbbb2222"} {
		if err := repo.upsert(k, f.bookIDs[0]); err != nil {
			t.Fatal(err)
		}
	}
	database.Close()
	return f
}

// 极简直接 SQL 访问，避免依赖 tts.Repo 的内部字段。
type repo struct{ db *sql.DB }

func newRepo(db *sql.DB) *repo { return &repo{db: db} }

func (r *repo) upsert(key, bookID string) error {
	_, err := r.db.Exec(`INSERT INTO tts_segments(cache_key, book_id, provider, plain_bytes, cipher_bytes, last_access, kek_version)
		VALUES(?,?,'mock',10,120,'2026-01-01T00:00:00Z',1)`, key, bookID)
	return err
}

func (f *rotFixture) rotate(t *testing.T, onBook func(int) error) (*kekrotate.RotateResult, error) {
	t.Helper()
	return kekrotate.Rotate(context.Background(), kekrotate.RotateOptions{
		DBPath: f.dbPath, CiphertextDir: f.cipherDir, TTSCacheDir: f.ttsDir,
		OldKEK: f.oldKEK, NewKEK: f.newKEK, FromVersion: 1, ToVersion: 2, OnBook: onBook,
	})
}

func TestRotateBooksAndAudio(t *testing.T) {
	f := newRotFixture(t, 2)
	res, err := f.rotate(t, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.BooksRotated != 2 || res.SegmentsRotated != 2 || !res.Completed {
		t.Fatalf("result = %+v", res)
	}
	for _, id := range f.bookIDs {
		got, err := kekrotate.DecryptWhole(context.Background(), f.dbPath, f.cipherDir, id, f.newKEK)
		if err != nil {
			t.Fatalf("新 KEK 解密 %s 失败: %v", id, err)
		}
		if len(got) != 64 {
			t.Fatalf("sha len = %d", len(got))
		}
		if _, err := kekrotate.DecryptWhole(context.Background(), f.dbPath, f.cipherDir, id, f.oldKEK); !errors.Is(err, cryptobox.ErrTampered) {
			t.Fatalf("旧 KEK 仍可解包 %s: %v", id, err)
		}
	}
	database, err := db.Open(f.dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	var ver int
	if err := database.QueryRow(`SELECT kek_version FROM books LIMIT 1`).Scan(&ver); err != nil {
		t.Fatal(err)
	}
	if ver != 2 {
		t.Fatalf("kek_version = %d", ver)
	}
	if err := database.QueryRow(`SELECT COUNT(*) FROM kek_rotations WHERE result='ok'`).Scan(&ver); err != nil {
		t.Fatal(err)
	}
	var n int
	_ = database.QueryRow(`SELECT COUNT(*) FROM audit_logs WHERE action='kek_rotated'`).Scan(&n)
	if n != 1 {
		t.Fatalf("kek_rotated audit = %d", n)
	}

	// 音频：新 KEK 可读，旧 KEK 不可读
	newStore, err := ttscache.NewWithKeyRing(f.ttsDir, map[int][]byte{2: f.newKEK}, 2, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := newStore.Get("aaaa1111"); err != nil {
		t.Fatalf("新 KEK 读取音频失败: %v", err)
	}
	oldStore, err := ttscache.NewWithKeyRing(f.ttsDir, map[int][]byte{1: f.oldKEK}, 1, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := oldStore.Get("aaaa1111"); !errors.Is(err, ttscache.ErrTampered) {
		t.Fatalf("旧 KEK 仍可读音频: %v", err)
	}
}

func TestRotateIdempotent(t *testing.T) {
	f := newRotFixture(t, 2)
	if _, err := f.rotate(t, nil); err != nil {
		t.Fatal(err)
	}
	res, err := f.rotate(t, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.BooksRotated != 0 || res.BooksSkipped != 2 {
		t.Fatalf("second run = %+v", res)
	}
}

func TestRotateResumesAfterInterruption(t *testing.T) {
	f := newRotFixture(t, 2)
	stop := errors.New("simulated crash")
	res, err := f.rotate(t, func(done int) error {
		if done == 1 {
			return stop
		}
		return nil
	})
	if !errors.Is(err, stop) {
		t.Fatalf("err = %v", err)
	}
	if res.BooksRotated != 1 || res.Completed {
		t.Fatalf("partial = %+v", res)
	}
	res2, err := f.rotate(t, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res2.BooksRotated != 1 || res2.BooksSkipped != 1 {
		t.Fatalf("resume = %+v", res2)
	}
	for _, id := range f.bookIDs {
		if _, err := kekrotate.DecryptWhole(context.Background(), f.dbPath, f.cipherDir, id, f.newKEK); err != nil {
			t.Fatalf("解密失败 %s: %v", id, err)
		}
	}
}

func TestVerifyAfterRotation(t *testing.T) {
	f := newRotFixture(t, 2)
	if _, err := f.rotate(t, nil); err != nil {
		t.Fatal(err)
	}
	res, err := kekrotate.Verify(context.Background(), kekrotate.VerifyOptions{
		DBPath: f.dbPath, CiphertextDir: f.cipherDir, TTSCacheDir: f.ttsDir,
		KEKs: map[int][]byte{1: f.oldKEK, 2: f.newKEK}, Sample: 2,
	})
	if err != nil {
		t.Fatalf("verify: %v (%+v)", err, res)
	}
	if len(res.Checked) != 2 || res.AudioChecked != 2 {
		t.Fatalf("checked = %+v", res)
	}
	// 缺少当前版本 KEK 时抽样应失败（书籍仍是 v2）
	_, err = kekrotate.Verify(context.Background(), kekrotate.VerifyOptions{
		DBPath: f.dbPath, CiphertextDir: f.cipherDir, KEKs: map[int][]byte{1: f.oldKEK}, Sample: 2,
	})
	if err == nil {
		t.Fatal("缺少 v2 KEK 时不应通过")
	}
}
