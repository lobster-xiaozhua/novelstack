package books_test

import (
	"context"
	"crypto/rand"
	"os"
	"path/filepath"
	"testing"

	"novelstack/internal/audit"
	"novelstack/internal/books"
	"novelstack/internal/db"
)

func newBulkService(t *testing.T) (*books.Service, string) {
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
	svc := &books.Service{
		Store: books.NewStore(database), KEK: kek, KEKVersion: 1,
		CiphertextDir: filepath.Join(dir, "ciphertext"), ChunkSize: 64 << 10,
		MaxUpload: 1 << 20, Audit: audit.New(database), MaxChapters: 500,
	}
	return svc, dir
}

func TestBulkImportIdempotent(t *testing.T) {
	svc, dir := newBulkService(t)
	in := filepath.Join(dir, "in")
	if err := os.MkdirAll(in, 0o700); err != nil {
		t.Fatal(err)
	}
	bodyA := "第一章 甲\n内容甲。\n第二章 乙\n结束。\n"
	bodyB := "第一章 丙\n内容丙。\n"
	files := map[string]string{
		"a.txt": bodyA,
		"b.txt": bodyB,
		"c.txt": bodyA, // 与 a 内容相同
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(in, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	rep, err := svc.ImportDirectory(context.Background(), books.ImportOptions{Dir: in, Username: "admin"})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Scanned != 3 || rep.Imported != 2 || rep.Skipped != 1 || rep.Failed != 0 {
		t.Fatalf("first report = %+v", rep)
	}
	var dup books.ImportItem
	for _, it := range rep.Items {
		if it.Status == "skipped" {
			dup = it
		}
	}
	if dup.Reason != "duplicate_in_batch" {
		t.Fatalf("duplicate reason = %q", dup.Reason)
	}

	// 幂等：第二次全部跳过（含已存在与批内重复）
	rep2, err := svc.ImportDirectory(context.Background(), books.ImportOptions{Dir: in, Username: "admin"})
	if err != nil {
		t.Fatal(err)
	}
	if rep2.Imported != 0 || rep2.Skipped != 3 || rep2.Failed != 0 {
		t.Fatalf("second report = %+v", rep2)
	}

	list, err := svc.Store.List(context.Background(), "admin")
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("books = %d", len(list))
	}
	for _, b := range list {
		if b.ChapterCount < 1 {
			t.Fatalf("book %s chapter_count = %d", b.ID, b.ChapterCount)
		}
		if _, err := os.Stat(filepath.Join(dir, "ciphertext", b.ID)); err != nil {
			t.Fatalf("密文缺失 %s: %v", b.ID, err)
		}
	}
}

func TestBulkImportDryRun(t *testing.T) {
	svc, dir := newBulkService(t)
	in := filepath.Join(dir, "in")
	if err := os.MkdirAll(in, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(in, "x.txt"), []byte("第一章 试\n内容。\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	rep, err := svc.ImportDirectory(context.Background(), books.ImportOptions{Dir: in, Username: "admin", DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Imported != 1 || !rep.DryRun {
		t.Fatalf("dry run report = %+v", rep)
	}
	list, _ := svc.Store.List(context.Background(), "admin")
	if len(list) != 0 {
		t.Fatalf("dry run 写入了 %d 本书", len(list))
	}
}

func TestBulkImportRejectsBadDir(t *testing.T) {
	svc, _ := newBulkService(t)
	if _, err := svc.ImportDirectory(context.Background(), books.ImportOptions{Dir: "/nonexistent-novelstack"}); err == nil {
		t.Fatal("不存在的目录应失败")
	}
	if _, err := svc.ImportDirectory(context.Background(), books.ImportOptions{}); err == nil {
		t.Fatal("空目录参数应失败")
	}
}
