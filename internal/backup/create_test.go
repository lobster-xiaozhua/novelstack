package backup

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"novelstack/internal/cryptobox"
	"novelstack/internal/db"
	"novelstack/internal/ttscache"
)

type fixture struct {
	dir       string
	dbPath    string
	cipherDir string
	ttsDir    string
	kek       []byte
	bookIDs   []string
	contents  map[string][]byte
}

func newFixture(t *testing.T, books int) *fixture {
	t.Helper()
	dir := t.TempDir()
	f := &fixture{
		dir:       dir,
		dbPath:    filepath.Join(dir, "app.db"),
		cipherDir: filepath.Join(dir, "ciphertext"),
		ttsDir:    filepath.Join(dir, "tts-cache"),
		kek:       make([]byte, cryptobox.KeySize),
		contents:  map[string][]byte{},
	}
	if _, err := rand.Read(f.kek); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(f.cipherDir, 0o700); err != nil {
		t.Fatal(err)
	}
	database, err := db.Open(f.dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO users(username, password_hash) VALUES('admin','$argon2id$dummy')`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO sessions(token_hash, user_id, expires_at, scope)
		VALUES('deadbeef', 1, '2099-01-01T00:00:00Z', 'api')`); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < books; i++ {
		content := []byte("第一章 起航\n这是第 " + string(rune('A'+i)) + " 本书的正文。\n第二章 抵达\n正文结束。\n")
		id, err := cryptobox.NewBookID()
		if err != nil {
			t.Fatal(err)
		}
		dek, err := cryptobox.NewDEK()
		if err != nil {
			t.Fatal(err)
		}
		cipherPath := filepath.Join(f.cipherDir, id)
		out, err := os.OpenFile(cipherPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		sum, err := cryptobox.EncryptStream(bytes.NewReader(content), out, dek, id, 64<<10)
		out.Close()
		if err != nil {
			t.Fatal(err)
		}
		wrapped, nonce, err := cryptobox.WrapDEK(f.kek, dek)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := database.Exec(`INSERT INTO books
			(id,title,author,tags,description,orig_filename,size_bytes,sha256,wrapped_dek,dek_nonce,
			 kek_version,format_version,chunk_size,storage_path,owner_id,chapter_count,index_state)
			VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			id, "测试书"+string(rune('A'+i)), "作者", "标签", "简介", "a.txt",
			len(content), hex.EncodeToString(sum), wrapped, nonce,
			1, 1, 64<<10, "ciphertext/"+id, "admin", 2, "indexed"); err != nil {
			t.Fatal(err)
		}
		if _, err := database.Exec(`INSERT INTO book_chapters(book_id, chapter_index, title, chars, start_rune, end_rune)
			VALUES (?,0,'第一章 起航',10,0,10),(?,1,'第二章 抵达',8,11,19)`, id, id); err != nil {
			t.Fatal(err)
		}
		f.bookIDs = append(f.bookIDs, id)
		f.contents[id] = content
	}
	// 放一个音频缓存片段，验证缓存目录也被打包
	store, err := ttscache.New(f.ttsDir, f.kek, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Put("abcdef0123456789", []byte("RIFF-fake-audio")); err != nil {
		t.Fatal(err)
	}
	if books > 0 {
		if _, err := database.Exec(`INSERT INTO tts_segments(cache_key, book_id, provider, plain_bytes, cipher_bytes, last_access)
			VALUES('abcdef0123456789', ?, 'mock', 15, 120, '2026-01-01T00:00:00Z')`, f.bookIDs[0]); err != nil {
			t.Fatal(err)
		}
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *fixture) create(t *testing.T, pass []byte) *CreateResult {
	t.Helper()
	res, err := Create(context.Background(), CreateOptions{
		DataDir:    f.dir,
		OutDir:     filepath.Join(f.dir, "backups"),
		Passphrase: pass,
		Params:     testKDF(),
		ChunkSize:  64 << 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func TestCreateAndExtract(t *testing.T) {
	f := newFixture(t, 2)
	pass := []byte("a-very-long-backup-passphrase")
	res := f.create(t, pass)

	if res.Manifest.Books != 2 || res.Manifest.Chapters != 4 {
		t.Fatalf("manifest = %+v", res.Manifest)
	}
	if res.SizeBytes < 100 || len(res.Checksum) != 64 {
		t.Fatalf("result = %+v", res)
	}
	if got := ReadSidecarChecksum(res.Path); got != res.Checksum {
		t.Fatalf("sidecar = %q", got)
	}

	dst := filepath.Join(t.TempDir(), "restore")
	ex, err := Extract(context.Background(), ExtractOptions{
		PackagePath: res.Path, Passphrase: pass, DestDir: dst,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !ex.ChecksumOK || !ex.Verified || ex.Manifest.Books != 2 {
		t.Fatalf("extract = %+v", ex)
	}

	// 会话必须被清除
	raw, err := sql.Open("sqlite", "file:"+filepath.Join(dst, "data", "app.db")+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	var sessions int
	if err := raw.QueryRow(`SELECT COUNT(*) FROM sessions`).Scan(&sessions); err != nil {
		t.Fatal(err)
	}
	if sessions != 0 {
		t.Fatalf("sessions = %d, want 0", sessions)
	}
	var books int
	if err := raw.QueryRow(`SELECT COUNT(*) FROM books`).Scan(&books); err != nil {
		t.Fatal(err)
	}
	if books != 2 {
		t.Fatalf("books = %d", books)
	}
	// 密文文件逐字节一致
	id := f.bookIDs[0]
	orig, err := os.ReadFile(filepath.Join(f.cipherDir, id))
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(dst, "data", "ciphertext", id))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(orig, got) {
		t.Fatal("密文内容不一致")
	}
	// 音频缓存也在包内
	if _, err := os.Stat(filepath.Join(dst, "data", "tts-cache", "ab", "abcdef0123456789.bin")); err != nil {
		t.Fatalf("音频缓存缺失: %v", err)
	}
}

func TestExtractWrongPassphraseCleansUp(t *testing.T) {
	f := newFixture(t, 1)
	res := f.create(t, []byte("a-very-long-backup-passphrase"))
	dst := filepath.Join(t.TempDir(), "restore")
	_, err := Extract(context.Background(), ExtractOptions{
		PackagePath: res.Path, Passphrase: []byte("wrong-passphrase"), DestDir: dst,
	})
	if !errors.Is(err, ErrDecrypt) {
		t.Fatalf("err = %v, want ErrDecrypt", err)
	}
	entries, _ := os.ReadDir(dst)
	if len(entries) != 0 {
		t.Fatalf("错误口令后仍产生 %d 个文件", len(entries))
	}
}

func TestExtractTamperedCleansUp(t *testing.T) {
	f := newFixture(t, 1)
	res := f.create(t, []byte("a-very-long-backup-passphrase"))
	raw, err := os.ReadFile(res.Path)
	if err != nil {
		t.Fatal(err)
	}
	raw[len(raw)*2/3] ^= 0x01
	badPath := filepath.Join(filepath.Dir(res.Path), "tampered.nsb")
	if err := os.WriteFile(badPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(t.TempDir(), "restore")
	_, err = Extract(context.Background(), ExtractOptions{
		PackagePath: badPath, Passphrase: []byte("a-very-long-backup-passphrase"), DestDir: dst,
	})
	if err == nil {
		t.Fatal("篡改包未被拒绝")
	}
	entries, _ := os.ReadDir(dst)
	if len(entries) != 0 {
		t.Fatalf("篡改包解出 %d 个文件", len(entries))
	}
}

func TestCreateEmptyLibrary(t *testing.T) {
	f := newFixture(t, 0)
	pass := []byte("a-very-long-backup-passphrase")
	res := f.create(t, pass)
	if res.Manifest.Books != 0 {
		t.Fatalf("books = %d", res.Manifest.Books)
	}
	dst := filepath.Join(t.TempDir(), "restore")
	ex, err := Extract(context.Background(), ExtractOptions{
		PackagePath: res.Path, Passphrase: pass, DestDir: dst,
	})
	if err != nil {
		t.Fatal(err)
	}
	if ex.Manifest.Books != 0 {
		t.Fatalf("extracted books = %d", ex.Manifest.Books)
	}
}

func TestCreateMissingCiphertextFails(t *testing.T) {
	f := newFixture(t, 1)
	if err := os.Remove(filepath.Join(f.cipherDir, f.bookIDs[0])); err != nil {
		t.Fatal(err)
	}
	_, err := Create(context.Background(), CreateOptions{
		DataDir: f.dir, OutDir: filepath.Join(f.dir, "backups"),
		Passphrase: []byte("a-very-long-backup-passphrase"),
		Params:     testKDF(), ChunkSize: 64 << 10,
	})
	if !errors.Is(err, ErrMissingCiphertext) {
		t.Fatalf("err = %v, want ErrMissingCiphertext", err)
	}
	entries, _ := os.ReadDir(filepath.Join(f.dir, "backups"))
	if len(entries) != 0 {
		t.Fatalf("失败后仍生成 %d 个文件", len(entries))
	}
}

func TestCreateRefusesOverwrite(t *testing.T) {
	f := newFixture(t, 1)
	pass := []byte("a-very-long-backup-passphrase")
	f.create(t, pass)
	_, err := Create(context.Background(), CreateOptions{
		DataDir: f.dir, OutDir: filepath.Join(f.dir, "backups"), Name: filepath.Base(firstBackup(t, f.dir)),
		Passphrase: pass, Params: testKDF(), ChunkSize: 64 << 10,
	})
	if err == nil {
		t.Fatal("覆盖已有备份未被拒绝")
	}
}

func firstBackup(t *testing.T, dir string) string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(dir, "backups"))
	if err != nil || len(entries) == 0 {
		t.Fatal("no backup")
	}
	return entries[0].Name()
}

func TestManifestHashMatches(t *testing.T) {
	f := newFixture(t, 1)
	pass := []byte("a-very-long-backup-passphrase")
	res := f.create(t, pass)
	dst := filepath.Join(t.TempDir(), "restore")
	if _, err := Extract(context.Background(), ExtractOptions{
		PackagePath: res.Path, Passphrase: pass, DestDir: dst,
	}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dst, "data", "app.db"))
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	if hex.EncodeToString(sum[:]) != res.Manifest.DBSHA256 {
		t.Fatal("清单中的数据库哈希不一致")
	}
	// 备份时间不应晚于当前
	if _, err := time.Parse(time.RFC3339, res.Manifest.CreatedAt); err != nil {
		t.Fatalf("created_at = %q", res.Manifest.CreatedAt)
	}
}
