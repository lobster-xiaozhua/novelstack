package backup_test

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"novelstack/internal/app"
	"novelstack/internal/audit"
	"novelstack/internal/auth"
	"novelstack/internal/backup"
	"novelstack/internal/books"
	"novelstack/internal/config"
	"novelstack/internal/cryptobox"
	"novelstack/internal/db"
)

const drillPassword = "drill-password-123"

// makeDrillSource 构造一个含真实加密书籍的最小数据目录。
func makeDrillSource(t *testing.T) (dir, kekPath string, bookID string) {
	t.Helper()
	dir = t.TempDir()
	kek := make([]byte, cryptobox.KeySize)
	if _, err := rand.Read(kek); err != nil {
		t.Fatal(err)
	}
	kekPath = filepath.Join(dir, "kek")
	if err := os.WriteFile(kekPath, kek, 0o600); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(dir, "app.db")
	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	sessions := auth.NewStore(database)
	if _, err := sessions.EnsureAdmin(context.Background(), "admin", drillPassword); err != nil {
		t.Fatal(err)
	}
	svc := &books.Service{
		Store: books.NewStore(database), KEK: kek, KEKVersion: 1,
		CiphertextDir: filepath.Join(dir, "ciphertext"), ChunkSize: 1 << 20,
		MaxUpload: 20 << 20, Audit: audit.New(database),
	}
	in := filepath.Join(dir, "in")
	if err := os.MkdirAll(in, 0o700); err != nil {
		t.Fatal(err)
	}
	content := "第一章 起航\n海上没有风，船帆垂着。\n第二章 抵达\n港口在雾里。\n"
	if err := os.WriteFile(filepath.Join(in, "drill.txt"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	report, err := svc.ImportDirectory(context.Background(), books.ImportOptions{Dir: in, Username: "admin"})
	if err != nil || report.Imported != 1 {
		t.Fatalf("import: %+v err=%v", report, err)
	}
	bookID = report.Items[0].BookID
	database.Close()
	return dir, kekPath, bookID
}

// inProcessLauncher 用同一套 app 装配启动 httptest 实例（不占端口、不碰生产目录）。
func inProcessLauncher(t *testing.T) backup.Launcher {
	t.Helper()
	return backup.LauncherFunc(func(ctx context.Context, cfg backup.LaunchConfig) (string, func(), error) {
		c := &config.Config{
			Addr:               "127.0.0.1:0",
			DataDir:            cfg.DataDir,
			DBPath:             filepath.Join(cfg.DataDir, "app.db"),
			CiphertextDir:      filepath.Join(cfg.DataDir, "ciphertext"),
			TTSCacheDir:        filepath.Join(cfg.DataDir, "tts-cache"),
			ClientIndexDir:     filepath.Join(cfg.DataDir, "client-index"),
			BackupDir:          filepath.Join(cfg.DataDir, "backups"),
			KEKPath:            cfg.KEKPath,
			KEKVersion:         cfg.KEKVersion,
			CookieSecure:       false,
			MaxUploadBytes:     20 << 20,
			ChunkSize:          1 << 20,
			SessionTTL:         time.Hour,
			LoginFailPerMinute: 5,
			TTSProvider:        "off",
			TTSMaxSegmentRunes: 100,
			TTSDefaultSpeed:    1,
			TTSMaxAudioBytes:   16 << 20,
			TTSSegmentTimeout:  5 * time.Second,
			SearchTimeout:      5 * time.Second,
			CacheProtectWindow: 5 * time.Minute,
			LegadoSessionTTL:   time.Hour,
		}
		a, err := app.New(ctx, app.Options{Config: c})
		if err != nil {
			return "", nil, err
		}
		ts := httptest.NewServer(a.Handler())
		return ts.URL, func() { ts.Close(); a.Close() }, nil
	})
}

func TestRestoreDrillEndToEnd(t *testing.T) {
	dir, kekPath, bookID := makeDrillSource(t)
	res, err := backup.Create(context.Background(), backup.CreateOptions{
		DataDir: dir, OutDir: filepath.Join(dir, "backups"),
		Passphrase: []byte("a-very-long-backup-passphrase"),
		Params:     testKDF(), ChunkSize: 1 << 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(dir, "drill-log.jsonl")
	drill, err := backup.RunDrill(context.Background(), backup.DrillOptions{
		PackagePath: res.Path, Passphrase: []byte("a-very-long-backup-passphrase"),
		KEKPath: kekPath, KEKVersion: 1, Username: "admin", Password: drillPassword,
		BookID: bookID, LogPath: logPath, Launcher: inProcessLauncher(t),
		RecordDBPath: filepath.Join(dir, "app.db"), Timeout: 60 * time.Second,
	})
	if err != nil {
		t.Fatalf("drill: %v (%+v)", err, drill)
	}
	if drill.Result != "ok" || drill.ActualSHA256 == "" || drill.ActualSHA256 != drill.ExpectedSHA256 {
		t.Fatalf("drill result = %+v", drill)
	}
	raw, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	var line backup.DrillResult
	if err := json.Unmarshal([]byte(strings.TrimSpace(string(raw))), &line); err != nil {
		t.Fatalf("log line: %v (%s)", err, raw)
	}
	if line.Result != "ok" || line.Checksum == "" || line.BookID != bookID {
		t.Fatalf("log = %+v", line)
	}
	// drill_runs 有记录
	database, err := db.Open(filepath.Join(dir, "app.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	var n int
	if err := database.QueryRow(`SELECT COUNT(*) FROM drill_runs WHERE result='ok'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("drill_runs = %d", n)
	}
}

func TestRestoreDrillWrongPassphraseFails(t *testing.T) {
	dir, kekPath, _ := makeDrillSource(t)
	res, err := backup.Create(context.Background(), backup.CreateOptions{
		DataDir: dir, OutDir: filepath.Join(dir, "backups"),
		Passphrase: []byte("a-very-long-backup-passphrase"),
		Params:     testKDF(), ChunkSize: 1 << 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(dir, "drill-log.jsonl")
	drill, err := backup.RunDrill(context.Background(), backup.DrillOptions{
		PackagePath: res.Path, Passphrase: []byte("wrong-passphrase"),
		KEKPath: kekPath, KEKVersion: 1, Username: "admin", Password: drillPassword,
		LogPath: logPath, Launcher: inProcessLauncher(t), Timeout: 30 * time.Second,
	})
	if err == nil {
		t.Fatal("错误口令演练应当失败")
	}
	if drill.Result != "fail" || drill.Reason != "extract_failed" {
		t.Fatalf("drill = %+v", drill)
	}
	raw, err := os.ReadFile(logPath)
	if err != nil || !strings.Contains(string(raw), `"result":"fail"`) {
		t.Fatalf("演练失败未记录: %v %s", err, raw)
	}
}

func TestRestoreDrillEmptyLibrary(t *testing.T) {
	dir := t.TempDir()
	database, err := db.Open(filepath.Join(dir, "app.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO users(username,password_hash) VALUES('admin','x')`); err != nil {
		t.Fatal(err)
	}
	database.Close()
	kek := make([]byte, 32)
	if _, err := rand.Read(kek); err != nil {
		t.Fatal(err)
	}
	kekPath := filepath.Join(dir, "kek")
	if err := os.WriteFile(kekPath, kek, 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := backup.Create(context.Background(), backup.CreateOptions{
		DataDir: dir, OutDir: filepath.Join(dir, "backups"),
		Passphrase: []byte("a-very-long-backup-passphrase"), Params: testKDF(), ChunkSize: 1 << 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	drill, err := backup.RunDrill(context.Background(), backup.DrillOptions{
		PackagePath: res.Path, Passphrase: []byte("a-very-long-backup-passphrase"),
		KEKPath: kekPath, KEKVersion: 1, Username: "admin", Password: drillPassword,
		LogPath: filepath.Join(dir, "drill.jsonl"), Launcher: inProcessLauncher(t),
	})
	if err == nil {
		t.Fatal("空书库演练应明确失败")
	}
	if drill.Reason != "no_book_to_verify" {
		t.Fatalf("reason = %s", drill.Reason)
	}
}

func testKDF() backup.KDFParams { return backup.KDFParams{Time: 1, Memory: 8 * 1024, Threads: 1} }
