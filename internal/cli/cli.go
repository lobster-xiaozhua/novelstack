// Package cli 实现 novelstack 的子命令：
//
//	serve            启动 HTTP 服务（默认）
//	backup create    生成加密备份包
//	backup verify    校验并试解包
//	restore-drill    一键恢复演练（临时目录 + 临时端口）
//	kek rotate       KEK 轮换（书籍 DEK + 音频缓存）
//	kek verify       轮换后抽样解密校验
//	import           批量导入目录下的 .txt
//	cache prune      按大小/天数淘汰 TTS 缓存
//	cache clear      按书或全量清理 TTS 缓存
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"novelstack/internal/app"
	"novelstack/internal/audit"
	"novelstack/internal/backup"
	"novelstack/internal/books"
	"novelstack/internal/config"
	"novelstack/internal/cryptobox"
	"novelstack/internal/db"
	"novelstack/internal/kekrotate"
	"novelstack/internal/tts"
	"novelstack/internal/ttscache"
)

// Run 分派子命令并返回退出码。
func Run(args []string) int {
	if len(args) == 0 {
		return runServe(nil)
	}
	cmd := args[0]
	rest := args[1:]
	switch cmd {
	case "serve":
		return runServe(rest)
	case "backup":
		return runBackup(rest)
	case "restore-drill":
		return runRestoreDrill(rest)
	case "kek":
		return runKEK(rest)
	case "import":
		return runImport(rest)
	case "cache":
		return runCache(rest)
	case "help", "-h", "--help":
		usage()
		return 0
	default:
		fmt.Fprintf(os.Stderr, "未知子命令 %q\n\n", cmd)
		usage()
		return 2
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `novelstack 子命令：
  serve                         启动 HTTP 服务（默认）
  backup create --out-dir DIR   生成加密备份包（口令来自 NOVELSTACK_BACKUP_PASSPHRASE 或 --passphrase-file）
  backup verify --package FILE  校验校验和并试解包
  restore-drill --package FILE --kek FILE --password-file FILE
                                恢复演练：临时目录+临时端口，登录并比对明文 SHA-256
  kek rotate --old FILE --new FILE --from 1 --to 2
                                KEK 轮换（幂等，可重跑）
  kek verify --kek 1=FILE --kek 2=FILE --sample 3
                                轮换后抽样校验
  import --dir DIR              批量导入 .txt（按明文 SHA-256 幂等）
  cache prune --max-mb 512      按上限/天数淘汰缓存
  cache clear --book ID|--all   手工清理缓存（需 --yes）
`)
}

// ---- serve ----

func runServe(args []string) int {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	cfg, err := config.FromEnv()
	if err != nil {
		fmt.Fprintf(os.Stderr, "config: %v\n", err)
		return 1
	}
	initial := os.Getenv("ADMIN_INITIAL_PASSWORD")
	if err := cfg.ValidateExtra(initial); err != nil {
		fmt.Fprintf(os.Stderr, "config: %v\n", err)
		return 1
	}
	return serve(cfg, initial)
}

// ---- backup ----

func runBackup(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "用法: backup create|verify ...")
		return 2
	}
	switch args[0] {
	case "create":
		return runBackupCreate(args[1:])
	case "verify":
		return runBackupVerify(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "未知 backup 子命令 %q\n", args[0])
		return 2
	}
}

func runBackupCreate(args []string) int {
	fs := flag.NewFlagSet("backup create", flag.ContinueOnError)
	dataDir := fs.String("data-dir", defaultDataDir(), "数据目录")
	outDir := fs.String("out-dir", "", "输出目录（默认 <data-dir>/backups）")
	name := fs.String("name", "", "文件名（默认 novelstack-时间戳.nsb）")
	passFile := fs.String("passphrase-file", "", "备份口令文件（推荐）；否则读 NOVELSTACK_BACKUP_PASSPHRASE")
	noTTS := fs.Bool("no-tts-cache", false, "不打包 TTS 缓存")
	memMB := fs.Int("kdf-memory-mb", 64, "Argon2id 内存（MiB）")
	kdfTime := fs.Int("kdf-time", 3, "Argon2id 迭代次数")
	chunkKB := fs.Int("chunk-kb", 1024, "加密分块（KiB）")
	asJSON := fs.Bool("json", false, "输出 JSON")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	pass, err := readPassphrase(*passFile, "NOVELSTACK_BACKUP_PASSPHRASE")
	if err != nil {
		fmt.Fprintf(os.Stderr, "backup: %v\n", err)
		return 1
	}
	if len(pass) < 12 {
		fmt.Fprintln(os.Stderr, "backup: 备份口令至少 12 个字符")
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Hour)
	defer cancel()
	res, err := backup.Create(ctx, backup.CreateOptions{
		DataDir:      *dataDir,
		OutDir:       *outDir,
		Name:         *name,
		Passphrase:   pass,
		Params:       backup.KDFParams{Time: uint32(*kdfTime), Memory: uint32(*memMB) * 1024, Threads: 2},
		ChunkSize:    *chunkKB << 10,
		OmitTTSCache: *noTTS,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "backup 失败: %v\n", err)
		return 1
	}
	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(res)
	} else {
		fmt.Printf("backup=%s\n", res.Path)
		fmt.Printf("sha256=%s\n", res.Checksum)
		fmt.Printf("size_bytes=%d\n", res.SizeBytes)
		fmt.Printf("books=%d segments=%d duration_ms=%d\n", res.Manifest.Books, res.Manifest.Segments, res.DurationMS)
		for _, w := range res.Warnings {
			fmt.Fprintf(os.Stderr, "warning: %s\n", w)
		}
		fmt.Fprintf(os.Stderr, "提醒：主 KEK 不会进入备份包，请确认已离线保存（打印/硬件密钥）。\n")
	}
	return 0
}

func runBackupVerify(args []string) int {
	fs := flag.NewFlagSet("backup verify", flag.ContinueOnError)
	pkg := fs.String("package", "", "备份包路径")
	passFile := fs.String("passphrase-file", "", "备份口令文件")
	checksum := fs.String("checksum", "", "期望的 SHA-256（默认读 .sha256 伴生文件）")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	pass, err := readPassphrase(*passFile, "NOVELSTACK_BACKUP_PASSPHRASE")
	if err != nil {
		fmt.Fprintf(os.Stderr, "backup: %v\n", err)
		return 1
	}
	tmp, err := os.MkdirTemp("", "novelstack-verify-*")
	if err != nil {
		fmt.Fprintf(os.Stderr, "backup: %v\n", err)
		return 1
	}
	defer os.RemoveAll(tmp)
	ctx, cancel := context.WithTimeout(context.Background(), time.Hour)
	defer cancel()
	res, err := backup.Extract(ctx, backup.ExtractOptions{
		PackagePath: *pkg, Passphrase: pass, DestDir: tmp, ExpectedSHA256: *checksum,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "校验失败: %v\n", err)
		return 1
	}
	fmt.Printf("ok sha256=%s size_bytes=%d books=%d segments=%d files=%d\n",
		res.Checksum, res.SizeBytes, res.Manifest.Books, res.Manifest.Segments, res.Files)
	return 0
}

// ---- restore drill ----

func runRestoreDrill(args []string) int {
	fs := flag.NewFlagSet("restore-drill", flag.ContinueOnError)
	pkg := fs.String("package", "", "备份包路径")
	kekPath := fs.String("kek", "", "离线 KEK 文件（人工保存的那一份）")
	passFile := fs.String("passphrase-file", "", "备份口令文件")
	passEnv := fs.String("password-env", "NOVELSTACK_DRILL_PASSWORD", "备份用户口令所在环境变量名")
	username := fs.String("username", "admin", "登录用户名")
	bookID := fs.String("book", "", "指定验证的 book_id（默认第一本）")
	logPath := fs.String("log", "", "演练记录 JSONL（默认 <data-dir>/backups/drill-log.jsonl）")
	dataDir := fs.String("data-dir", defaultDataDir(), "生产数据目录（仅用于定位日志/记录库，不会被修改）")
	workDir := fs.String("work-dir", "", "临时工作目录（默认系统临时目录）")
	checksum := fs.String("checksum", "", "期望的包 SHA-256")
	kekVersion := fs.Int("kek-version", 1, "当前 KEK 版本")
	kekOld := fs.String("kek-old", "", "旧 KEK 映射，如 1=/path/kek.v1")
	recordDB := fs.String("record-db", "", "演练记录写入的数据库（默认 <data-dir>/app.db）")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	pass, err := readPassphrase(*passFile, "NOVELSTACK_BACKUP_PASSPHRASE")
	if err != nil {
		fmt.Fprintf(os.Stderr, "drill: %v\n", err)
		return 1
	}
	userPass := os.Getenv(*passEnv)
	if userPass == "" {
		fmt.Fprintf(os.Stderr, "drill: 需要环境变量 %s（登录口令）\n", *passEnv)
		return 1
	}
	if *logPath == "" {
		*logPath = filepath.Join(*dataDir, "backups", "drill-log.jsonl")
	}
	if *recordDB == "" {
		*recordDB = filepath.Join(*dataDir, "app.db")
		if _, err := os.Stat(*recordDB); err != nil {
			*recordDB = ""
		}
	}
	old, err := app.ParseKEKOldPaths(*kekOld)
	if err != nil {
		fmt.Fprintf(os.Stderr, "drill: %v\n", err)
		return 1
	}
	res, err := backup.RunDrill(context.Background(), backup.DrillOptions{
		PackagePath: *pkg, Passphrase: pass, KEKPath: *kekPath, KEKVersion: *kekVersion,
		OldKEKs: old, Username: *username, Password: userPass, BookID: *bookID,
		LogPath: *logPath, WorkDir: *workDir, ExpectedSHA256: *checksum, RecordDBPath: *recordDB,
	})
	if res != nil {
		raw, _ := json.MarshalIndent(res, "", "  ")
		fmt.Println(string(raw))
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "演练失败: %v\n", err)
		fmt.Fprintf(os.Stderr, "记录已追加: %s\n", *logPath)
		return 1
	}
	fmt.Fprintf(os.Stderr, "演练成功：明文 SHA-256 与备份清单一致；记录已追加 %s\n", *logPath)
	return 0
}

// ---- KEK ----

func runKEK(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "用法: kek rotate|verify ...")
		return 2
	}
	switch args[0] {
	case "rotate":
		return runKEKRotate(args[1:])
	case "verify":
		return runKEKVerify(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "未知 kek 子命令 %q\n", args[0])
		return 2
	}
}

func runKEKRotate(args []string) int {
	fs := flag.NewFlagSet("kek rotate", flag.ContinueOnError)
	oldPath := fs.String("old", "", "旧 KEK 文件（只读）")
	newPath := fs.String("new", "", "新 KEK 文件")
	from := fs.Int("from", 1, "旧版本号")
	to := fs.Int("to", 2, "新版本号")
	dataDir := fs.String("data-dir", defaultDataDir(), "数据目录")
	skipAudio := fs.Bool("skip-audio", false, "只轮换书籍 DEK")
	asJSON := fs.Bool("json", false, "输出 JSON")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	oldKEK, err := readKEK(*oldPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "kek: %v\n", err)
		return 1
	}
	newKEK, err := readKEK(*newPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "kek: %v\n", err)
		return 1
	}
	if string(oldKEK) == string(newKEK) {
		fmt.Fprintln(os.Stderr, "kek: 新旧 KEK 相同，拒绝轮换")
		return 1
	}
	cfg := &config.Config{DataDir: *dataDir}
	cfg.DBPath = filepath.Join(cfg.DataDir, "app.db")
	cfg.CiphertextDir = filepath.Join(cfg.DataDir, "ciphertext")
	cfg.TTSCacheDir = filepath.Join(cfg.DataDir, "tts-cache")
	ctx, cancel := context.WithTimeout(context.Background(), time.Hour)
	defer cancel()
	res, err := kekrotate.Rotate(ctx, kekrotate.RotateOptions{
		DBPath: cfg.DBPath, CiphertextDir: cfg.CiphertextDir, TTSCacheDir: cfg.TTSCacheDir,
		OldKEK: oldKEK, NewKEK: newKEK, FromVersion: *from, ToVersion: *to, SkipAudio: *skipAudio,
	})
	if res != nil {
		if *asJSON {
			raw, _ := json.MarshalIndent(res, "", "  ")
			fmt.Println(string(raw))
		} else {
			fmt.Printf("rotated from=v%d to=v%d books=%d skipped=%d segments=%d completed=%v\n",
				res.FromVersion, res.ToVersion, res.BooksRotated, res.BooksSkipped, res.SegmentsRotated, res.Completed)
		}
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "轮换中断（可安全重跑）: %v\n", err)
		return 1
	}
	fmt.Fprintln(os.Stderr, "提示：先运行 kek verify 抽样校验，确认后再人工归档旧 KEK（代码不会删除）。")
	return 0
}

type versionKEKs map[int]string

func (v versionKEKs) String() string { return fmt.Sprint(map[int]string(v)) }
func (v versionKEKs) Set(s string) error {
	i := strings.IndexByte(s, '=')
	if i <= 0 {
		return errors.New("格式应为 版本=路径")
	}
	n, err := strconv.Atoi(s[:i])
	if err != nil || n <= 0 {
		return errors.New("版本号非法")
	}
	v[n] = s[i+1:]
	return nil
}

func runKEKVerify(args []string) int {
	fs := flag.NewFlagSet("kek verify", flag.ContinueOnError)
	dataDir := fs.String("data-dir", defaultDataDir(), "数据目录")
	sample := fs.Int("sample", 3, "每个版本抽样书籍数")
	keks := versionKEKs{}
	fs.Var(keks, "kek", "版本=路径（可重复）")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if len(keks) == 0 {
		fmt.Fprintln(os.Stderr, "kek: 至少提供一个 --kek 版本=路径")
		return 1
	}
	loaded := map[int][]byte{}
	for v, p := range keks {
		k, err := readKEK(p)
		if err != nil {
			fmt.Fprintf(os.Stderr, "kek: v%d: %v\n", v, err)
			return 1
		}
		loaded[v] = k
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Hour)
	defer cancel()
	res, err := kekrotate.Verify(ctx, kekrotate.VerifyOptions{
		DBPath: filepath.Join(*dataDir, "app.db"), CiphertextDir: filepath.Join(*dataDir, "ciphertext"),
		TTSCacheDir: filepath.Join(*dataDir, "tts-cache"), KEKs: loaded, Sample: *sample,
	})
	if res != nil {
		raw, _ := json.MarshalIndent(res, "", "  ")
		fmt.Println(string(raw))
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "校验失败: %v\n", err)
		return 1
	}
	return 0
}

// ---- import ----

func runImport(args []string) int {
	fs := flag.NewFlagSet("import", flag.ContinueOnError)
	dir := fs.String("dir", "", "待导入目录")
	dataDir := fs.String("data-dir", defaultDataDir(), "数据目录")
	username := fs.String("user", "admin", "书籍归属用户名")
	author := fs.String("author", "", "统一作者（可选）")
	tags := fs.String("tags", "", "统一标签，逗号分隔（可选）")
	desc := fs.String("description", "", "统一简介（可选）")
	recursive := fs.Bool("recursive", false, "递归子目录")
	dryRun := fs.Bool("dry-run", false, "只扫描与查重")
	asJSON := fs.Bool("json", false, "输出 JSON")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *dir == "" {
		fmt.Fprintln(os.Stderr, "import: 需要 --dir")
		return 1
	}
	cfg, err := loadConfigForDataDir(*dataDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "import: %v\n", err)
		return 1
	}
	database, err := db.Open(cfg.DBPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "import: %v\n", err)
		return 1
	}
	defer database.Close()
	kek, ver, old, err := app.LoadKeyRing(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "import: %v\n", err)
		return 1
	}
	svc := &books.Service{
		Store: books.NewStore(database), KEK: kek, KEKVersion: ver, OldKEKs: old,
		CiphertextDir: cfg.CiphertextDir, ChunkSize: cfg.ChunkSize,
		MaxUpload: cfg.MaxUploadBytes, Audit: audit.New(database),
	}
	report, err := svc.ImportDirectory(context.Background(), books.ImportOptions{
		Dir: *dir, Username: *username, Author: *author, Tags: *tags,
		Description: *desc, Recursive: *recursive, DryRun: *dryRun,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "import: %v\n", err)
		return 1
	}
	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(report)
	} else {
		for _, it := range report.Items {
			fmt.Printf("%-8s %s", it.Status, it.File)
			if it.BookID != "" {
				fmt.Printf(" -> %s", it.BookID)
			}
			if it.Reason != "" {
				fmt.Printf(" (%s)", it.Reason)
			}
			fmt.Println()
		}
		fmt.Printf("scanned=%d imported=%d skipped=%d failed=%d dry_run=%v\n",
			report.Scanned, report.Imported, report.Skipped, report.Failed, report.DryRun)
	}
	if report.Failed > 0 {
		return 1
	}
	return 0
}

// ---- cache ----

func runCache(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "用法: cache prune|clear ...")
		return 2
	}
	switch args[0] {
	case "prune":
		return runCachePrune(args[1:])
	case "clear":
		return runCacheClear(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "未知 cache 子命令 %q\n", args[0])
		return 2
	}
}

func runCachePrune(args []string) int {
	fs := flag.NewFlagSet("cache prune", flag.ContinueOnError)
	dataDir := fs.String("data-dir", defaultDataDir(), "数据目录")
	maxMB := fs.Int64("max-mb", 0, "缓存总大小上限（MB，0 表示不按大小）")
	keepDays := fs.Int("keep-days", 0, "保留最近 N 天（0 表示不按时间）")
	dryRun := fs.Bool("dry-run", false, "只计算，不删除")
	asJSON := fs.Bool("json", false, "输出 JSON")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	svc, cleanup, err := openCacheService(*dataDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "cache: %v\n", err)
		return 1
	}
	defer cleanup()
	res, err := svc.PruneCache(context.Background(), tts.PrunePolicy{
		MaxBytes: *maxMB << 20, KeepDays: *keepDays, DryRun: *dryRun,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "cache: %v\n", err)
		return 1
	}
	if *asJSON {
		raw, _ := json.MarshalIndent(res, "", "  ")
		fmt.Println(string(raw))
	} else {
		fmt.Printf("removed=%d freed=%d bytes_before=%d bytes_after=%d segments_left=%d dry_run=%v\n",
			res.Removed, res.FreedBytes, res.BytesBefore, res.BytesAfter, res.SegmentsLeft, res.DryRun)
	}
	return 0
}

func runCacheClear(args []string) int {
	fs := flag.NewFlagSet("cache clear", flag.ContinueOnError)
	dataDir := fs.String("data-dir", defaultDataDir(), "数据目录")
	bookID := fs.String("book", "", "只清理该书（默认全量）")
	all := fs.Bool("all", false, "清理全部缓存")
	yes := fs.Bool("yes", false, "确认执行")
	asJSON := fs.Bool("json", false, "输出 JSON")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if !*yes {
		fmt.Fprintln(os.Stderr, "cache: 需要 --yes 确认")
		return 1
	}
	if !*all && *bookID == "" {
		fmt.Fprintln(os.Stderr, "cache: 需要 --book ID 或 --all")
		return 1
	}
	svc, cleanup, err := openCacheService(*dataDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "cache: %v\n", err)
		return 1
	}
	defer cleanup()
	res, err := svc.ClearCache(context.Background(), *bookID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "cache: %v\n", err)
		return 1
	}
	if *asJSON {
		raw, _ := json.MarshalIndent(res, "", "  ")
		fmt.Println(string(raw))
	} else {
		fmt.Printf("scope=%s removed=%d freed=%d protected=%d segments_left=%d\n",
			res.Scope, res.Removed, res.FreedBytes, res.Protected, res.SegmentsLeft)
	}
	return 0
}

// openCacheService 构造只含缓存治理能力的 TTS 服务（不加载模型）。
func openCacheService(dataDir string) (*tts.Service, func(), error) {
	cfg, err := loadConfigForDataDir(dataDir)
	if err != nil {
		return nil, nil, err
	}
	database, err := db.Open(cfg.DBPath)
	if err != nil {
		return nil, nil, err
	}
	cleanup := func() { database.Close() }
	kek, ver, old, err := app.LoadKeyRing(cfg)
	if err != nil {
		cleanup()
		return nil, nil, err
	}
	keys := map[int][]byte{ver: kek}
	for v, k := range old {
		keys[v] = k
	}
	cache, err := ttscache.NewWithKeyRing(cfg.TTSCacheDir, keys, ver, cfg.TTSMaxAudioBytes)
	if err != nil {
		cleanup()
		return nil, nil, err
	}
	return &tts.Service{
		Cache: cache, Repo: tts.NewRepo(database), Audit: audit.New(database),
		ProtectWindow: cfg.CacheProtectWindow,
	}, cleanup, nil
}

// ---- helpers ----

func defaultDataDir() string {
	if v := strings.TrimSpace(os.Getenv("DATA_DIR")); v != "" {
		return v
	}
	return "/data"
}

// loadConfigForDataDir 复用环境变量配置，但允许 CLI 覆盖数据目录。
func loadConfigForDataDir(dataDir string) (*config.Config, error) {
	cfg, err := config.FromEnv()
	if err != nil {
		return nil, err
	}
	cfg.DataDir = dataDir
	cfg.CiphertextDir = filepath.Join(dataDir, "ciphertext")
	cfg.TTSCacheDir = filepath.Join(dataDir, "tts-cache")
	cfg.DBPath = filepath.Join(dataDir, "app.db")
	cfg.ClientIndexDir = filepath.Join(dataDir, "client-index")
	cfg.BackupDir = filepath.Join(dataDir, "backups")
	return cfg, nil
}

// readPassphrase 从文件或环境变量读取口令；绝不从命令行参数读取（避免 ps/历史泄漏）。
func readPassphrase(file, env string) ([]byte, error) {
	if file != "" {
		raw, err := os.ReadFile(file) // #nosec G304 -- 路径来自运维命令
		if err != nil {
			return nil, fmt.Errorf("读取口令文件失败: %w", err)
		}
		return []byte(strings.TrimRight(string(raw), "\r\n")), nil
	}
	if v := os.Getenv(env); v != "" {
		return []byte(v), nil
	}
	return nil, fmt.Errorf("缺少口令：使用 --passphrase-file 或环境变量 %s", env)
}

func readKEK(path string) ([]byte, error) {
	if path == "" {
		return nil, errors.New("缺少 KEK 文件路径")
	}
	return cryptobox.LoadKEK(path)
}
