// Package config 加载并校验运行时配置。
// 所有配置来自环境变量；缺失关键配置时启动即失败（fail-fast）。
package config

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
	// 内嵌时区库：容器里没有 /usr/share/zoneinfo 时 READING_TZ 仍可用。
	_ "time/tzdata"
)

// Config 是进程级只读配置。
type Config struct {
	// HTTP 监听地址（容器内），默认 :8080
	Addr string
	// 数据根目录：SQLite 与密文文件都在其下
	DataDir string
	// 密文文件目录，默认 DataDir/ciphertext
	CiphertextDir string
	// KEK 只读 secret 文件路径
	KEKPath string
	// 当前 KEK 版本（写入新记录时使用），默认 1
	KEKVersion int
	// 旧 KEK 路径映射（轮换窗口期只读加载），格式 "1=/path,2=/path"
	KEKOldPaths string
	// SQLite 文件路径
	DBPath string
	// Cookie 是否加 Secure 标记。生产默认 true；开发可显式设 COOKIE_SECURE=false
	CookieSecure bool
	// 上传大小上限（明文字节数）
	MaxUploadBytes int64
	// 加密块大小（明文分块，默认 1MiB）
	ChunkSize int
	// Session 有效期
	SessionTTL time.Duration
	// 登录限流：每 IP 每分钟允许的失败次数
	LoginFailPerMinute int

	// ---- 第二阶段：TTS ----

	// TTS Provider：off / mock / piper / openai（默认 off，未配置时朗读接口返回 503）
	TTSProvider string
	// 音频缓存目录（与书籍密文目录隔离），默认 DataDir/tts-cache
	TTSCacheDir string
	// 单片段最大字符数（汉字计），默认 100
	TTSMaxSegmentRunes int
	// 单片段合成超时
	TTSSegmentTimeout time.Duration
	// 默认 voice / speed（请求未指定时使用）
	TTSDefaultVoice string
	TTSDefaultSpeed float64
	// 单片段合成音频大小上限（防引擎异常输出撑爆内存）
	TTSMaxAudioBytes int64

	// 方式 A：本地进程桥接（piper）
	PiperBin      string
	PiperModel    string
	PiperModelDir string

	// 方式 B：OpenAI 兼容 HTTP 服务
	HTTPTTSBaseURL  string
	HTTPTTSModel    string
	HTTPTTSVoice    string
	HTTPTTSAPIKey   string
	HTTPTTSAPIKeyFn string

	// ---- 第三阶段：元数据搜索（Meilisearch）----

	// MEILI_URL 为空表示不启用 Meilisearch，元数据搜索自动降级为 SQLite 本地匹配
	MeiliURL string
	// MeiliMasterKey / MeiliMasterKeyFile：二选一，推荐只读 secret 文件
	MeiliMasterKey     string
	MeiliMasterKeyFile string
	// 索引名（默认 books）
	MeiliIndex string
	// 启动时执行一次全量同步（幂等兜底）
	SearchSyncOnStart bool
	// 单次搜索 / 索引请求超时
	SearchTimeout time.Duration

	// ---- 第三阶段：Legado 书源网关 ----

	LegadoEnabled bool
	// 网关会话有效期（Legado 长期使用，默认 30 天）
	LegadoSessionTTL time.Duration

	// ---- 第四阶段：数据生命周期 ----

	// 客户端本地索引快照目录（随备份一起打包；不存在则跳过）
	ClientIndexDir string
	// 备份输出目录（CLI 默认值）
	BackupDir string
	// 缓存淘汰保护窗口：该时间内访问过的片段不删除
	CacheProtectWindow time.Duration
	// 缓存总大小上限（0 表示不限制，仅手动淘汰时使用）
	CacheMaxBytes int64
	// 缓存保留天数（0 表示不按时间淘汰）
	CacheKeepDays int

	// ---- 第五阶段：移动端 ----

	// 单次预取最多章节数（防止一次拉整本）
	PrefetchMaxChapters int
	// 每设备每分钟允许的预取请求数（令牌桶）
	PrefetchPerMinute int
	// 单次预取响应的密文上限（字节），超出则截断并标记 truncated
	PrefetchMaxBytes int64
	// device token 默认有效期（0 表示长期有效，撤销由管理端控制）
	DeviceTokenTTL time.Duration

	// ---- 第六阶段：自动备份与运维告警 ----

	// AutoBackup 为 false 时不启动内置调度器（备份仍可用 CLI/控制台手动触发）
	AutoBackup bool
	// 每日触发时间（本地时区），默认 03:00
	AutoBackupHour   int
	AutoBackupMinute int
	// 自动备份保留天数（按包名里的日期计算），默认 7
	BackupRetainDays int
	// 自动备份口令文件（推荐只读 secret 文件）；为空时读 NOVELSTACK_BACKUP_PASSPHRASE
	BackupPassphraseFile string
	// KEK 离线备份确认标记；默认为 KEK 同目录下的 .kek-offline-backup-ok
	KekOfflineMarker string
	// 告警 webhook；为空表示只显示控制台横幅
	AlertWebhook  string
	AlertCooldown time.Duration

	// ---- 第八阶段：发现页与 App 版本分发 ----

	// 最新 App 版本号与更新说明（展示给客户端「检查更新」弹窗）
	AppVersion   string
	AppChangelog string
	// APK 镜像 URL 列表：由环境变量 APP_MIRRORS 注入（逗号分隔），禁止在代码中写死。
	AppMirrors []string
	// 本机 APK 文件路径：仅用于 /download/novelstack.apk 这一个固定下载口，
	// 不从请求参数取路径，避免任意文件读取。为空时该接口返回 404。
	AppAPKFile string
	// 最新 APK 的 SHA-256（hex）：客户端测速下载后必须校验该值才允许安装。
	AppAPKSHA256 string

	// ---- 第九阶段：正文净化与双音色朗读 ----

	// 正文明文净化规则（按顺序删除命中内容）。来源 REGEX_PURGE_RULES，
	// 支持 JSON 数组、换行分隔、";;" 分隔三种写法。
	PurgeRules []string
	// 对话音色；为空表示不启用双音色（全部用请求里的 voice）。
	TTSDialogueVoice string
	// 识别对话段落的正则（必须含一个捕获组，捕获组内容作为对话文本朗读）。
	TTSDialoguePattern string

	// ---- 第九阶段：阅读统计 ----

	// 统计「今天」使用的时区（IANA 名称），默认 Asia/Shanghai。
	// 用服务端时区而不是客户端时间，避免设备改时间刷出假的阅读天数。
	ReadingTZ string
	// 导出单次最多包含的书籍数（防止一次请求把整个库打进内存）。
	ExportMaxBooks int

	// ---- 第十阶段：局域网发现与动态根地址 ----

	// LANDiscovery 为 false 时完全关闭 UDP/mDNS 广播（默认开启）。
	LANDiscovery bool
	// LANBroadcastPort 是 UDP 广播监听/应答端口，默认 5359。
	// 5353 通常被系统 mDNS 服务占用，这里用独立端口避免冲突。
	LANBroadcastPort int
	// LANPairTTL 是 6 位短码有效期，默认 5 分钟。
	LANPairTTL time.Duration
	// LANPairPerMinute 是短码兑换的每 IP 每分钟尝试上限，默认 8。
	LANPairPerMinute int
	// LANAdvertiseHost 显式指定广播出去的主机（IP 或域名）；
	// 为空时由发现服务自动探测第一个非回环 IPv4。
	LANAdvertiseHost string
	// LANAdvertisePort 显式指定广播出去的 HTTP 端口；为空时用 Addr 里的端口。
	LANAdvertisePort int

	// ---- 终端防御阶段：IP 熔断 ----
	// BlocklistEnabled 为 false 时完全关闭 IP 熔断中间件（默认开启）。
	BlocklistEnabled bool
	// BlocklistWindow 是违规计数滑动窗口，默认 1 小时。
	BlocklistWindow time.Duration
	// BlocklistThreshold 是窗口内触发封禁的 401/404 次数，默认 10。
	BlocklistThreshold int
	// BlocklistBanTTL 是封禁时长，默认 24 小时。
	BlocklistBanTTL time.Duration
	// BlocklistMaxEntries 是黑名单条数上限（防被探测撑爆表），默认 5000。
	BlocklistMaxEntries int
}

// voiceNamePattern 与 tts 包内的校验保持一致（配置阶段先挡一次，避免朗读时才 400）。
var voiceNamePattern = regexp.MustCompile(`^[A-Za-z0-9_.\-]{1,64}$`)

// DefaultDialoguePattern 匹配中文/英文引号包裹的对话。
// 使用捕获组：只有组内文本按对话音色朗读，引号本身不朗读。
const DefaultDialoguePattern = `[“"「『]([^”"」』\n]{1,400})[”"」』]`

// FromEnv 从环境变量构建配置。任何关键错误都返回 error，由 main 打印后退出。
func FromEnv() (*Config, error) {
	c := &Config{
		Addr:               envStr("ADDR", ":8080"),
		DataDir:            envStr("DATA_DIR", "/data"),
		KEKPath:            envStr("KEK_PATH", "/run/secrets/kek"),
		KEKVersion:         envInt("KEK_VERSION", 1),
		KEKOldPaths:        envStr("KEK_OLD_PATHS", ""),
		CookieSecure:       envBool("COOKIE_SECURE", true),
		MaxUploadBytes:     int64(envInt("MAX_UPLOAD_MB", 20)) << 20,
		ChunkSize:          envInt("CHUNK_SIZE", 1<<20), // 1MiB
		SessionTTL:         time.Duration(envInt("SESSION_TTL_HOURS", 24)) * time.Hour,
		LoginFailPerMinute: envInt("LOGIN_FAIL_PER_MINUTE", 5),

		TTSProvider:        strings.ToLower(envStr("TTS_PROVIDER", "off")),
		TTSMaxSegmentRunes: envInt("TTS_MAX_SEGMENT_CHARS", 100),
		TTSSegmentTimeout:  time.Duration(envInt("TTS_SEGMENT_TIMEOUT_MS", 20000)) * time.Millisecond,
		TTSDefaultVoice:    envStr("TTS_VOICE", ""),
		TTSDefaultSpeed:    envFloat("TTS_SPEED", 1.0),
		TTSMaxAudioBytes:   int64(envInt("TTS_MAX_AUDIO_MB", 16)) << 20,

		PiperBin:      envStr("PIPER_BIN", "piper"),
		PiperModel:    envStr("PIPER_MODEL", ""),
		PiperModelDir: envStr("PIPER_MODEL_DIR", ""),

		HTTPTTSBaseURL:  strings.TrimRight(envStr("TTS_HTTP_BASE_URL", ""), "/"),
		HTTPTTSModel:    envStr("TTS_HTTP_MODEL", "tts-1"),
		HTTPTTSVoice:    envStr("TTS_HTTP_VOICE", "alloy"),
		HTTPTTSAPIKey:   envStr("TTS_HTTP_API_KEY", ""),
		HTTPTTSAPIKeyFn: envStr("TTS_HTTP_API_KEY_FILE", ""),

		MeiliURL:           strings.TrimRight(envStr("MEILI_URL", ""), "/"),
		MeiliMasterKey:     envStr("MEILI_MASTER_KEY", ""),
		MeiliMasterKeyFile: envStr("MEILI_MASTER_KEY_FILE", ""),
		MeiliIndex:         envStr("MEILI_INDEX", "books"),
		SearchSyncOnStart:  envBool("SEARCH_SYNC_ON_START", true),
		SearchTimeout:      time.Duration(envInt("SEARCH_TIMEOUT_MS", 5000)) * time.Millisecond,

		LegadoEnabled:    envBool("LEGADO_ENABLED", true),
		LegadoSessionTTL: time.Duration(envInt("LEGADO_SESSION_TTL_HOURS", 720)) * time.Hour,

		CacheProtectWindow: time.Duration(envInt("CACHE_PROTECT_MINUTES", 5)) * time.Minute,
		CacheMaxBytes:      int64(envInt("CACHE_MAX_MB", 0)) << 20,
		CacheKeepDays:      envInt("CACHE_KEEP_DAYS", 0),

		PrefetchMaxChapters: envInt("PREFETCH_MAX_CHAPTERS", 8),
		PrefetchPerMinute:   envInt("PREFETCH_PER_MINUTE", 40),
		PrefetchMaxBytes:    int64(envInt("PREFETCH_MAX_MB", 8)) << 20,
		DeviceTokenTTL:      time.Duration(envInt("DEVICE_TOKEN_TTL_DAYS", 180)) * 24 * time.Hour,

		AutoBackup:           envBool("AUTO_BACKUP", true),
		AutoBackupHour:       envInt("AUTO_BACKUP_HOUR", 3),
		AutoBackupMinute:     envInt("AUTO_BACKUP_MINUTE", 0),
		BackupRetainDays:     envInt("BACKUP_RETAIN_DAYS", 7),
		BackupPassphraseFile: envStr("BACKUP_PASSPHRASE_FILE", ""),
		AlertWebhook:         envStr("BACKUP_ALERT_WEBHOOK", ""),
		AlertCooldown:        time.Duration(envInt("ALERT_COOLDOWN_MINUTES", 60)) * time.Minute,

		AppVersion:   envStr("APP_VERSION", "0.1.0"),
		AppChangelog: envStr("APP_CHANGELOG", ""),
		AppMirrors:   splitList(envStr("APP_MIRRORS", "")),
		AppAPKFile:   envStr("APP_APK_FILE", ""),
		AppAPKSHA256: strings.ToLower(strings.TrimSpace(envStr("APP_APK_SHA256", ""))),

		PurgeRules:         parseRules(envStr("REGEX_PURGE_RULES", "")),
		TTSDialogueVoice:   strings.TrimSpace(envStr("TTS_DIALOGUE_VOICE", "")),
		TTSDialoguePattern: envStr("TTS_DIALOGUE_PATTERN", DefaultDialoguePattern),

		ReadingTZ:      envStr("READING_TZ", "Asia/Shanghai"),
		ExportMaxBooks: envInt("EXPORT_MAX_BOOKS", 200),

		LANDiscovery:     envBool("LAN_DISCOVERY", true),
		LANBroadcastPort: envInt("LAN_BROADCAST_PORT", 5359),
		LANPairTTL:       time.Duration(envInt("LAN_PAIR_TTL_MINUTES", 5)) * time.Minute,
		LANPairPerMinute: envInt("LAN_PAIR_PER_MINUTE", 8),
		LANAdvertiseHost: strings.TrimSpace(envStr("LAN_ADVERTISE_HOST", "")),
		LANAdvertisePort: envInt("LAN_ADVERTISE_PORT", 0),

		BlocklistEnabled:    envBool("IP_BLOCKLIST_ENABLED", true),
		BlocklistWindow:     time.Duration(envInt("IP_BLOCKLIST_WINDOW_MINUTES", 60)) * time.Minute,
		BlocklistThreshold:  envInt("IP_BLOCKLIST_THRESHOLD", 10),
		BlocklistBanTTL:     time.Duration(envInt("IP_BLOCKLIST_BAN_HOURS", 24)) * time.Hour,
		BlocklistMaxEntries: envInt("IP_BLOCKLIST_MAX_ENTRIES", 5000),
	}
	if c.ChunkSize < 64<<10 || c.ChunkSize > 16<<20 {
		return nil, fmt.Errorf("CHUNK_SIZE 必须在 64KiB..16MiB 之间")
	}
	switch c.TTSProvider {
	case "off", "mock", "piper", "openai":
	default:
		return nil, fmt.Errorf("TTS_PROVIDER 只能是 off/mock/piper/openai，收到 %q", c.TTSProvider)
	}
	if c.PrefetchMaxChapters < 1 || c.PrefetchMaxChapters > 50 {
		return nil, fmt.Errorf("PREFETCH_MAX_CHAPTERS 必须在 1..50 之间")
	}
	if c.PrefetchPerMinute < 1 || c.PrefetchPerMinute > 600 {
		return nil, fmt.Errorf("PREFETCH_PER_MINUTE 必须在 1..600 之间")
	}
	if c.PrefetchMaxBytes < 1<<20 || c.PrefetchMaxBytes > 64<<20 {
		return nil, fmt.Errorf("PREFETCH_MAX_MB 必须在 1..64 之间")
	}
	if c.TTSMaxSegmentRunes < 20 || c.TTSMaxSegmentRunes > 500 {
		return nil, fmt.Errorf("TTS_MAX_SEGMENT_CHARS 必须在 20..500 之间")
	}
	if c.TTSDefaultSpeed < 0.5 || c.TTSDefaultSpeed > 2.0 {
		return nil, fmt.Errorf("TTS_SPEED 必须在 0.5..2.0 之间")
	}
	if c.MeiliIndex == "" || strings.ContainsAny(c.MeiliIndex, "/ ?#") {
		return nil, fmt.Errorf("MEILI_INDEX 必须是非空且不含 / ? # 的索引名")
	}
	if c.LegadoSessionTTL < time.Hour {
		return nil, fmt.Errorf("LEGADO_SESSION_TTL_HOURS 至少为 1 小时")
	}
	if c.SearchTimeout < time.Second || c.SearchTimeout > time.Minute {
		return nil, fmt.Errorf("SEARCH_TIMEOUT_MS 必须在 1000..60000 之间")
	}
	if c.KEKVersion < 1 || c.KEKVersion > 255 {
		return nil, fmt.Errorf("KEK_VERSION 必须在 1..255 之间")
	}
	if c.CacheProtectWindow < time.Minute || c.CacheProtectWindow > 24*time.Hour {
		return nil, fmt.Errorf("CACHE_PROTECT_MINUTES 必须在 1..1440 之间")
	}
	if c.CacheMaxBytes < 0 {
		return nil, fmt.Errorf("CACHE_MAX_MB 不能为负数")
	}
	if c.CacheKeepDays < 0 {
		return nil, fmt.Errorf("CACHE_KEEP_DAYS 不能为负数")
	}
	if c.AutoBackupHour < 0 || c.AutoBackupHour > 23 || c.AutoBackupMinute < 0 || c.AutoBackupMinute > 59 {
		return nil, fmt.Errorf("AUTO_BACKUP_HOUR/MINUTE 超出范围（0..23 / 0..59）")
	}
	if c.BackupRetainDays < 1 || c.BackupRetainDays > 365 {
		return nil, fmt.Errorf("BACKUP_RETAIN_DAYS 必须在 1..365 之间")
	}
	if c.AlertCooldown < time.Minute || c.AlertCooldown > 24*time.Hour {
		return nil, fmt.Errorf("ALERT_COOLDOWN_MINUTES 必须在 1..1440 之间")
	}
	if _, err := time.LoadLocation(c.ReadingTZ); err != nil {
		return nil, fmt.Errorf("READING_TZ 不是合法的 IANA 时区 %q: %w", c.ReadingTZ, err)
	}
	if c.ExportMaxBooks < 1 || c.ExportMaxBooks > 5000 {
		return nil, fmt.Errorf("EXPORT_MAX_BOOKS 必须在 1..5000 之间")
	}
	if c.TTSDialogueVoice != "" && !voiceNamePattern.MatchString(c.TTSDialogueVoice) {
		return nil, fmt.Errorf("TTS_DIALOGUE_VOICE 只能包含字母、数字、下划线、点和连字符（≤64）")
	}
	if c.LANBroadcastPort < 1024 || c.LANBroadcastPort > 65535 {
		return nil, fmt.Errorf("LAN_BROADCAST_PORT 必须在 1024..65535 之间")
	}
	if c.LANPairTTL < time.Minute || c.LANPairTTL > 30*time.Minute {
		return nil, fmt.Errorf("LAN_PAIR_TTL_MINUTES 必须在 1..30 之间")
	}
	if c.LANPairPerMinute < 1 || c.LANPairPerMinute > 60 {
		return nil, fmt.Errorf("LAN_PAIR_PER_MINUTE 必须在 1..60 之间")
	}
	if c.LANAdvertisePort < 0 || c.LANAdvertisePort > 65535 {
		return nil, fmt.Errorf("LAN_ADVERTISE_PORT 必须在 0..65535 之间")
	}
	if c.BlocklistWindow < time.Minute || c.BlocklistWindow > 24*time.Hour {
		return nil, fmt.Errorf("IP_BLOCKLIST_WINDOW_MINUTES 必须在 1..1440 之间")
	}
	if c.BlocklistThreshold < 2 || c.BlocklistThreshold > 1000 {
		return nil, fmt.Errorf("IP_BLOCKLIST_THRESHOLD 必须在 2..1000 之间")
	}
	if c.BlocklistBanTTL < time.Minute || c.BlocklistBanTTL > 30*24*time.Hour {
		return nil, fmt.Errorf("IP_BLOCKLIST_BAN_HOURS 必须在 1..720 之间")
	}
	if c.BlocklistMaxEntries < 100 || c.BlocklistMaxEntries > 100000 {
		return nil, fmt.Errorf("IP_BLOCKLIST_MAX_ENTRIES 必须在 100..100000 之间")
	}
	if c.AlertWebhook != "" {
		u, err := url.Parse(c.AlertWebhook)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return nil, fmt.Errorf("BACKUP_ALERT_WEBHOOK 必须是合法的 http(s) URL")
		}
	}
	c.CiphertextDir = filepath.Join(c.DataDir, "ciphertext")
	c.TTSCacheDir = filepath.Join(c.DataDir, "tts-cache")
	c.DBPath = filepath.Join(c.DataDir, "app.db")
	c.ClientIndexDir = filepath.Join(c.DataDir, "client-index")
	c.BackupDir = envStr("BACKUP_DIR", filepath.Join(c.DataDir, "backups"))
	c.KekOfflineMarker = envStr("KEK_OFFLINE_MARKER",
		filepath.Join(filepath.Dir(c.KEKPath), ".kek-offline-backup-ok"))
	return c, nil
}

// LoadBackupPassphrase 读取自动备份口令：优先只读 secret 文件，其次环境变量。
// 复用第四阶段的口令来源，不引入新密钥体系；返回值只在内存中使用，绝不写日志。
func (c *Config) LoadBackupPassphrase() ([]byte, error) {
	if c.BackupPassphraseFile != "" {
		raw, err := os.ReadFile(c.BackupPassphraseFile) // #nosec G304 -- 路径来自运维配置
		if err != nil {
			return nil, fmt.Errorf("读取 BACKUP_PASSPHRASE_FILE 失败: %w", err)
		}
		pass := []byte(strings.TrimRight(string(raw), "\r\n"))
		if len(pass) < 12 {
			return nil, fmt.Errorf("备份口令长度不足 12")
		}
		return pass, nil
	}
	if v := os.Getenv("NOVELSTACK_BACKUP_PASSPHRASE"); v != "" {
		return []byte(v), nil
	}
	return nil, fmt.Errorf("缺少备份口令：设置 BACKUP_PASSPHRASE_FILE 或 NOVELSTACK_BACKUP_PASSPHRASE")
}

// ValidateExtra 校验依赖运行期才能确认的事项（目录、初始管理员密码、TTS 依赖）。
// initialAdminPassword 为空表示 users 表非空、无需初始化。
func (c *Config) ValidateExtra(initialAdminPassword string) error {
	if initialAdminPassword != "" && len(initialAdminPassword) < 8 {
		return fmt.Errorf("ADMIN_INITIAL_PASSWORD 至少需要 8 个字符")
	}
	if strings.HasPrefix(initialAdminPassword, "$argon2") {
		return fmt.Errorf("ADMIN_INITIAL_PASSWORD 必须是明文口令，而不是哈希")
	}
	switch c.TTSProvider {
	case "piper":
		if c.PiperModel == "" && c.PiperModelDir == "" {
			return fmt.Errorf("TTS_PROVIDER=piper 需要设置 PIPER_MODEL 或 PIPER_MODEL_DIR")
		}
		if _, err := os.Stat(c.PiperBin); err != nil {
			return fmt.Errorf("PIPER_BIN 不可用（%s）：%w", c.PiperBin, err)
		}
	case "openai":
		if c.HTTPTTSBaseURL == "" {
			return fmt.Errorf("TTS_PROVIDER=openai 需要设置 TTS_HTTP_BASE_URL")
		}
	}
	if c.MeiliURL != "" && c.MeiliMasterKey == "" && c.MeiliMasterKeyFile == "" {
		return fmt.Errorf("MEILI_URL 已设置但缺少 MEILI_MASTER_KEY 或 MEILI_MASTER_KEY_FILE")
	}
	return nil
}

// LoadMeiliKey 读取 Meilisearch 的 API key：优先 secret 文件，其次环境变量。
// 返回值只驻留内存，不写日志。
func (c *Config) LoadMeiliKey() (string, error) {
	if c.MeiliMasterKeyFile != "" {
		b, err := os.ReadFile(c.MeiliMasterKeyFile)
		if err != nil {
			return "", fmt.Errorf("读取 MEILI_MASTER_KEY_FILE 失败: %w", err)
		}
		key := strings.TrimSpace(string(b))
		if key == "" || len(key) < 16 {
			return "", fmt.Errorf("MEILI_MASTER_KEY_FILE 内容无效（长度不足）")
		}
		return key, nil
	}
	return c.MeiliMasterKey, nil
}

func envStr(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && strings.TrimSpace(v) != "" {
		return v
	}
	return def
}

// splitList 把逗号分隔的列表拆成去空、去重（保序）的切片。
func splitList(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	seen := map[string]bool{}
	out := []string{}
	for _, part := range strings.Split(s, ",") {
		v := strings.TrimSpace(part)
		if v == "" || seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	return out
}

// parseRules 解析净化规则。三种写法都支持，避免正则里的逗号被误当分隔符：
//   - JSON 数组：["请记住本站.*","https?://\\S+"]
//   - 换行分隔（推荐，可读性最好）
//   - ";;" 分隔（便于单行环境变量）
func parseRules(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	var parts []string
	if strings.HasPrefix(raw, "[") {
		var arr []string
		if err := json.Unmarshal([]byte(raw), &arr); err == nil {
			parts = arr
		}
	}
	if parts == nil {
		sep := "\n"
		if !strings.Contains(raw, "\n") && strings.Contains(raw, ";;") {
			sep = ";;"
		}
		parts = strings.Split(raw, sep)
	}
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if v := strings.TrimSpace(p); v != "" {
			out = append(out, v)
		}
	}
	return out
}

func envBool(key string, def bool) bool {
	v, ok := os.LookupEnv(key)
	if !ok || strings.TrimSpace(v) == "" {
		return def
	}
	b, err := strconv.ParseBool(strings.TrimSpace(v))
	if err != nil {
		return def
	}
	return b
}

func envInt(key string, def int) int {
	v, ok := os.LookupEnv(key)
	if !ok || strings.TrimSpace(v) == "" {
		return def
	}
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil {
		return def
	}
	return n
}

func envFloat(key string, def float64) float64 {
	v, ok := os.LookupEnv(key)
	if !ok || strings.TrimSpace(v) == "" {
		return def
	}
	f, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
	if err != nil {
		return def
	}
	return f
}
