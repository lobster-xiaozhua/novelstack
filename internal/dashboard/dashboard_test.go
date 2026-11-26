package dashboard

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"novelstack/internal/audit"
	"novelstack/internal/books"
	"novelstack/internal/db"
	"novelstack/internal/search"
	"novelstack/internal/tts"
	"novelstack/internal/ttscache"
)

func newDeps(t *testing.T) Deps {
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
	if _, err := database.Exec(`INSERT INTO books(id,title,author,size_bytes,sha256,wrapped_dek,dek_nonce,kek_version,chunk_size,storage_path,owner_id,chapter_count)
		VALUES('book1','测试书','作者',100,'abc',x'00',x'00',1,1048576,'ciphertext/book1','admin',2)`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO audit_logs(username,ip,action,result,detail) VALUES
		('admin','127.0.0.1','book_download','ok','size=100'),
		('admin','127.0.0.1','chapter_plain_tamper','denied','integrity check failed'),
		('admin','127.0.0.1','meta_search','ok','q_len=3 hits=1 source=local')`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO search_logs(q_len,q_hash,hits,duration_ms,ok,source)
		VALUES(3,'deadbeef',1,2.5,'ok','local')`); err != nil {
		t.Fatal(err)
	}
	cache, err := ttscache.New(filepath.Join(dir, "tts-cache"), kek, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	repo := tts.NewRepo(database)
	if err := repo.UpsertSegment(context.Background(), tts.SegmentRecord{
		CacheKey: "abcdef123456", BookID: "book1", Provider: "mock", CipherBytes: 64, KEKVersion: 1,
	}); err != nil {
		t.Fatal(err)
	}
	// 备份目录：模拟一个包 + 伴生文件 + 子目录里的记录，用于统计总字节。
	backupDir := filepath.Join(dir, "backups")
	if err := os.MkdirAll(filepath.Join(backupDir, "nested"), 0o700); err != nil {
		t.Fatal(err)
	}
	for name, size := range map[string]int{"a.nsb": 100, "a.nsb.sha256": 64, "nested/log.jsonl": 20} {
		if err := os.WriteFile(filepath.Join(backupDir, name), make([]byte, size), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	a := audit.New(database)
	return Deps{
		DB:        database,
		BackupDir: backupDir,
		Books: &books.Service{
			Store: books.NewStore(database), KEK: kek, KEKVersion: 1,
			CiphertextDir: filepath.Join(dir, "ciphertext"),
		},
		TTS: &tts.Service{
			Cache: cache, Repo: repo, ProviderKind: "mock",
			ProtectWindow: 5 * time.Minute,
		},
		Search: search.NewService(database, nil, a, time.Second, []byte("0123456789abcdef0123456789abcdef")),
		Audit:  a,
	}
}

func TestBuildAndNoSensitiveFields(t *testing.T) {
	deps := newDeps(t)
	resp, err := Build(context.Background(), deps)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Books.Total != 1 || resp.Books.Chapters != 2 {
		t.Fatalf("books = %+v", resp.Books)
	}
	if resp.KEK.DecryptFailuresTotal != 1 {
		t.Fatalf("decrypt failures = %d", resp.KEK.DecryptFailuresTotal)
	}
	if len(resp.KEK.Distribution) != 1 || resp.KEK.Distribution[0].Version != 1 {
		t.Fatalf("distribution = %+v", resp.KEK.Distribution)
	}
	if len(resp.Audit.Recent) == 0 {
		t.Fatal("audit recent empty")
	}
	if resp.Search.Latency.Samples != 1 {
		t.Fatalf("latency = %+v", resp.Search.Latency)
	}
	if resp.BackupsDirBytes != 184 {
		t.Fatalf("backups_dir_bytes = %d, want 184", resp.BackupsDirBytes)
	}

	raw, err := json.Marshal(resp)
	if err != nil {
		t.Fatal(err)
	}
	lower := strings.ToLower(string(raw))
	if !strings.Contains(lower, `"backups_dir_bytes"`) {
		t.Fatal("dashboard 缺少 backups_dir_bytes 字段")
	}
	for _, bad := range []string{
		`"content"`, `"plaintext"`, `"body"`, `"password"`, `"token"`,
		`"query"`, `"dek"`, `"secret"`, `"q_hash"`, `"query_text"`,
	} {
		if strings.Contains(lower, bad) {
			t.Fatalf("dashboard 输出包含敏感字段 %s", bad)
		}
	}
	// 查询摘要只能出现长度与命中数，不能出现查询词原文
	if !strings.Contains(lower, "q_len=3") {
		t.Fatal("审计摘要缺失")
	}
}

func TestBuildEmptyDatabase(t *testing.T) {
	dir := t.TempDir()
	database, err := db.Open(filepath.Join(dir, "app.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	resp, err := Build(context.Background(), Deps{DB: database, Audit: audit.New(database)})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Books.Total != 0 || resp.KEK.CurrentVersion != 0 {
		t.Fatalf("resp = %+v", resp)
	}
}
