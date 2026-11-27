// Package app 负责把配置、数据库、密钥、业务服务与 HTTP 路由装配起来。
// main / CLI / 恢复演练都复用同一套装配逻辑，避免行为漂移。
package app

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/hkdf"

	"novelstack/internal/audit"
	"novelstack/internal/auth"
	"novelstack/internal/backup"
	"novelstack/internal/books"
	"novelstack/internal/config"
	"novelstack/internal/cryptobox"
	"novelstack/internal/db"
	"novelstack/internal/devices"
	"novelstack/internal/legado"
	"novelstack/internal/ops"
	"novelstack/internal/search"
	"novelstack/internal/server"
	"novelstack/internal/telemetry"
	"novelstack/internal/tts"
	"novelstack/internal/ttscache"
)

// Options 是装配参数。
type Options struct {
	Config *config.Config
	// InitialPassword 仅在 users 表为空时使用；演练/重启时可为空。
	InitialPassword string
	// SearchBackend 可注入测试后端；nil 时按配置创建 Meilisearch 客户端。
	SearchBackend search.Backend
}

// App 持有运行期依赖。
type App struct {
	Cfg       *config.Config
	DB        *sql.DB
	Sessions  *auth.Store
	Books     *books.Service
	TTS       *tts.Service
	Search    *search.Service
	Legado    *legado.Service
	Audit     *audit.Logger
	Devices   *devices.Store
	Scheduler *backup.Scheduler
	Health    *ops.HealthStore
	Server    *server.Server
	// Telemetry 是遥测明细/聚合存储；TelemetrySched 负责小时聚合与 90 天清理。
	Telemetry      *telemetry.Store
	TelemetrySched *telemetry.Scheduler
}

// New 构建完整应用；任何关键步骤失败都返回 error（由调用方决定退出）。
func New(ctx context.Context, opts Options) (*App, error) {
	cfg := opts.Config
	if cfg == nil {
		return nil, fmt.Errorf("app: 配置不能为空")
	}
	database, err := db.Open(cfg.DBPath)
	if err != nil {
		return nil, fmt.Errorf("db: %w", err)
	}
	cleanup := func(e error) (*App, error) {
		database.Close()
		return nil, e
	}

	sessions := auth.NewStore(database)
	created, err := sessions.EnsureAdmin(ctx, "admin", opts.InitialPassword)
	if err != nil {
		return cleanup(fmt.Errorf("init admin: %w", err))
	}
	if created {
		log.Printf("admin user %q created (set ADMIN_INITIAL_PASSWORD only on first start)", "admin")
	}

	currentKEK, kekVersion, oldKEKs, err := LoadKeyRing(cfg)
	if err != nil {
		return cleanup(fmt.Errorf("kek: %w", err))
	}

	a := audit.New(database)
	bookSvc := &books.Service{
		Store:         books.NewStore(database),
		KEK:           currentKEK,
		KEKVersion:    kekVersion,
		OldKEKs:       oldKEKs,
		CiphertextDir: cfg.CiphertextDir,
		ChunkSize:     cfg.ChunkSize,
		MaxUpload:     cfg.MaxUploadBytes,
		Audit:         a,
	}

	// ---- TTS ----
	allKEKs := make(map[int][]byte, len(oldKEKs)+1)
	for v, k := range oldKEKs {
		allKEKs[v] = k
	}
	allKEKs[kekVersion] = currentKEK
	audioCache, err := ttscache.NewWithKeyRing(cfg.TTSCacheDir, allKEKs, kekVersion, cfg.TTSMaxAudioBytes)
	if err != nil {
		return cleanup(fmt.Errorf("tts cache: %w", err))
	}
	provider, err := tts.NewProvider(tts.ProviderConfig{
		Kind:           cfg.TTSProvider,
		DefaultVoice:   cfg.TTSDefaultVoice,
		Timeout:        cfg.TTSSegmentTimeout,
		MaxAudioBytes:  cfg.TTSMaxAudioBytes,
		PiperBin:       cfg.PiperBin,
		PiperModel:     cfg.PiperModel,
		PiperModelDir:  cfg.PiperModelDir,
		HTTPBaseURL:    cfg.HTTPTTSBaseURL,
		HTTPModel:      cfg.HTTPTTSModel,
		HTTPVoice:      cfg.HTTPTTSVoice,
		HTTPAPIKey:     cfg.HTTPTTSAPIKey,
		HTTPAPIKeyFile: cfg.HTTPTTSAPIKeyFn,
	})
	if err != nil {
		return cleanup(fmt.Errorf("tts provider: %w", err))
	}
	if provider == nil {
		log.Printf("tts disabled (TTS_PROVIDER=off); /api/books/{id}/chapters/{i}/speak will return 503")
	} else {
		log.Printf("tts provider=%s cache_dir=%s max_segment_chars=%d",
			provider.Name(), cfg.TTSCacheDir, cfg.TTSMaxSegmentRunes)
	}
	ttsSvc := &tts.Service{
		Books:                 bookSvc,
		Cache:                 audioCache,
		Repo:                  tts.NewRepo(database),
		Provider:              provider,
		ProviderKind:          cfg.TTSProvider,
		Audit:                 a,
		MaxSegmentRunes:       cfg.TTSMaxSegmentRunes,
		DefaultVoice:          cfg.TTSDefaultVoice,
		DefaultSpeed:          cfg.TTSDefaultSpeed,
		SegmentTimeout:        cfg.TTSSegmentTimeout,
		MaxSegmentsPerRequest: 1000,
		ProtectWindow:         cfg.CacheProtectWindow,
	}

	// ---- 元数据检索（只索引元数据）----
	backend := opts.SearchBackend
	if backend == nil && cfg.MeiliURL != "" {
		key, err := cfg.LoadMeiliKey()
		if err != nil {
			return cleanup(fmt.Errorf("meilisearch key: %w", err))
		}
		backend = search.NewMeili(cfg.MeiliURL, key, cfg.MeiliIndex, cfg.SearchTimeout)
		log.Printf("search: meilisearch backend %s index=%s", cfg.MeiliURL, cfg.MeiliIndex)
	} else if backend == nil {
		log.Printf("search: MEILI_URL 未设置，元数据检索降级为 SQLite 本地匹配")
	}
	searchSvc := search.NewService(database, backend, a, cfg.SearchTimeout, deriveSearchLogKey(currentKEK))
	searchSvc.Start()
	bookSvc.Index = searchSvc

	if backend != nil && cfg.SearchSyncOnStart {
		go func() {
			sctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			if err := searchSvc.SyncAll(sctx); err != nil {
				log.Printf("search: 启动全量同步失败（将在下次元数据变更时重试）: %v", err)
			}
		}()
	}

	// ---- Legado 书源网关 ----
	var legadoSvc *legado.Service
	if cfg.LegadoEnabled {
		legadoSvc = &legado.Service{
			Sessions:     sessions,
			Books:        bookSvc,
			Search:       searchSvc,
			Audit:        a,
			Tokens:       legado.NewTokenStore(database),
			SessionTTL:   cfg.LegadoSessionTTL,
			CookieSecure: cfg.CookieSecure,
		}
		log.Printf("legado gateway enabled: session_ttl=%s", cfg.LegadoSessionTTL)
	}

	devSvc := devices.NewStore(database)

	// ---- 第六阶段：内置每日备份调度器（复用 backup.Create）----
	sched := newAutoBackupScheduler(cfg, database, a)
	health := ops.NewHealthStore(ops.HealthOptions{
		Webhook: cfg.AlertWebhook, Cooldown: cfg.AlertCooldown, Audit: a,
	})

	// ---- 第九阶段：遥测存储与小时聚合调度 ----
	telStore := telemetry.NewStore(database)
	telSched := telemetry.NewScheduler(telemetry.SchedulerOptions{
		Store:     telStore,
		Retention: 90 * 24 * time.Hour,
	})

	srv := server.New(server.Deps{
		Config: cfg, Sessions: sessions, Books: bookSvc, TTS: ttsSvc,
		Search: searchSvc, Legado: legadoSvc, Audit: a, DB: database,
		Devices: devSvc, Health: health, Scheduler: sched, Telemetry: telStore,
	})
	return &App{
		Cfg: cfg, DB: database, Sessions: sessions, Books: bookSvc, TTS: ttsSvc,
		Search: searchSvc, Legado: legadoSvc, Audit: a, Devices: devSvc,
		Scheduler: sched, Health: health, Server: srv,
		Telemetry: telStore, TelemetrySched: telSched,
	}, nil
}

// newAutoBackupScheduler 装配每日备份调度器。自动备份已开启但口令缺失/不可用时，
// 只在 stdout 输出一条不含口令的 WARNING 并禁用调度器；手动备份路径不受影响。
func newAutoBackupScheduler(cfg *config.Config, database *sql.DB, a *audit.Logger) *backup.Scheduler {
	if !cfg.AutoBackup {
		return nil
	}
	pass, err := cfg.LoadBackupPassphrase()
	if err != nil {
		// 不打印 err：它可能含口令文件路径；更不打印口令本身。
		warnAutoBackupPassphrase()
		return nil
	}
	sched := backup.NewScheduler(backup.SchedulerOptions{
		Enabled: true, Hour: cfg.AutoBackupHour, Minute: cfg.AutoBackupMinute,
		RetentionDays: cfg.BackupRetainDays, DataDir: cfg.DataDir,
		OutDir: cfg.BackupDir, Passphrase: pass,
		ChunkSize: cfg.ChunkSize, DB: database, Audit: a,
	})
	log.Printf("auto backup enabled: %02d:%02d retain=%dd out=%s",
		cfg.AutoBackupHour, cfg.AutoBackupMinute, cfg.BackupRetainDays, cfg.BackupDir)
	return sched
}

// warnAutoBackupPassphrase 是启动时唯一的缺口令警告；文本固定，不含任何口令内容。
func warnAutoBackupPassphrase() {
	fmt.Fprintln(os.Stdout,
		"WARNING: AUTO_BACKUP=true 但没有可用的备份口令，自动备份已禁用（手动备份不受影响）。"+
			"请设置 BACKUP_PASSPHRASE_FILE 或 NOVELSTACK_BACKUP_PASSPHRASE，详见 README §15.2。")
}

// StartBackground 启动后台 worker（每日备份调度器 + 遥测聚合）；只在 serve 命令中调用。
func (a *App) StartBackground() {
	if a.Scheduler != nil {
		a.Scheduler.Start()
	}
	if a.TelemetrySched != nil {
		a.TelemetrySched.Start()
	}
}

// Handler 返回 HTTP 根 handler。
func (a *App) Handler() http.Handler { return a.Server.Handler() }

// Close 停止后台 worker 并关闭数据库。
func (a *App) Close() {
	if a.Scheduler != nil {
		a.Scheduler.Close()
	}
	if a.TelemetrySched != nil {
		a.TelemetrySched.Close()
	}
	if a.Search != nil {
		a.Search.Close()
	}
	if a.DB != nil {
		_ = a.DB.Close()
	}
}

// LoadKeyRing 读取当前 KEK 与旧版本 KEK。
// KEK_OLD_PATHS 形如 "1=/run/secrets/kek.v1,2=/run/secrets/kek.v2"（只读，轮换窗口期使用）。
func LoadKeyRing(cfg *config.Config) (current []byte, version int, old map[int][]byte, err error) {
	version = cfg.KEKVersion
	if version <= 0 {
		version = 1
	}
	current, err = cryptobox.LoadKEK(cfg.KEKPath)
	if err != nil {
		return nil, 0, nil, err
	}
	paths, err := ParseKEKOldPaths(cfg.KEKOldPaths)
	if err != nil {
		return nil, 0, nil, err
	}
	old = make(map[int][]byte, len(paths))
	for v, p := range paths {
		if v == version {
			return nil, 0, nil, fmt.Errorf("KEK_OLD_PATHS 不应包含当前版本 v%d", v)
		}
		k, err := cryptobox.LoadKEK(p)
		if err != nil {
			return nil, 0, nil, fmt.Errorf("加载旧 KEK v%d 失败: %w", v, err)
		}
		old[v] = k
	}
	return current, version, old, nil
}

// ParseKEKOldPaths 解析 "版本=路径" 列表。
func ParseKEKOldPaths(s string) (map[int]string, error) {
	out := map[int]string{}
	s = strings.TrimSpace(s)
	if s == "" {
		return out, nil
	}
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		i := strings.IndexByte(part, '=')
		if i <= 0 {
			return nil, fmt.Errorf("KEK_OLD_PATHS 条目格式错误: %q", part)
		}
		v, err := strconv.Atoi(part[:i])
		if err != nil || v <= 0 {
			return nil, fmt.Errorf("KEK_OLD_PATHS 版本号非法: %q", part[:i])
		}
		out[v] = part[i+1:]
	}
	return out, nil
}

// deriveSearchLogKey 从 KEK 派生搜索日志指纹密钥：
// 库里只存 HMAC，无法用字典反推查询词。
func deriveSearchLogKey(kek []byte) []byte {
	out := make([]byte, 32)
	if _, err := io.ReadFull(hkdf.New(sha256.New, kek, nil, []byte("novelstack-search-log-hmac-v1")), out); err != nil {
		return nil
	}
	return out
}
