// Package db 负责 SQLite 连接与内嵌迁移。
package db

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	// 纯 Go SQLite 驱动（CGO_ENABLED=0 可用）。
	_ "modernc.org/sqlite"
)

// Migration 是一次向前迁移，version 单调递增。
type Migration struct {
	Version int
	SQL     string
}

// migrations 内嵌全部迁移 SQL，不依赖外部 migrate 工具。
var migrations = []Migration{
	{
		Version: 1,
		SQL: `
CREATE TABLE IF NOT EXISTS users (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    username      TEXT NOT NULL UNIQUE,
    password_hash TEXT NOT NULL,              -- Argon2id 编码串，不含明文
    created_at    TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);

CREATE TABLE IF NOT EXISTS sessions (
    token_hash  TEXT PRIMARY KEY,             -- SHA-256(token) 的 hex，不存原始 token
    user_id     INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_at  TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    expires_at  TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_sessions_user ON sessions(user_id);
CREATE INDEX IF NOT EXISTS idx_sessions_expiry ON sessions(expires_at);

CREATE TABLE IF NOT EXISTS books (
    id             TEXT PRIMARY KEY,          -- 随机 ID，同时是密文文件名，不用原始文件名
    title          TEXT NOT NULL,
    orig_filename  TEXT NOT NULL DEFAULT '',  -- 仅用于展示，绝不参与磁盘路径
    size_bytes     INTEGER NOT NULL,
    sha256         TEXT NOT NULL,             -- 明文 SHA-256，用于完整性校验
    wrapped_dek    BLOB NOT NULL,             -- KEK 包装后的 DEK（AES-256-GCM）
    dek_nonce      BLOB NOT NULL,             -- 包装 DEK 的 nonce
    kek_version    INTEGER NOT NULL DEFAULT 1,
    format_version INTEGER NOT NULL DEFAULT 1,
    chunk_size     INTEGER NOT NULL,
    storage_path   TEXT NOT NULL,             -- 密文文件相对 DATA_DIR 的路径
    created_at     TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    deleted_at     TEXT                       -- 软删除标记
);

CREATE TABLE IF NOT EXISTS audit_logs (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    ts         TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    username   TEXT NOT NULL DEFAULT '',
    ip         TEXT NOT NULL DEFAULT '',
    action     TEXT NOT NULL,
    book_id    TEXT NOT NULL DEFAULT '',
    result     TEXT NOT NULL,                 -- ok / denied / error / ratelimited
    detail     TEXT NOT NULL DEFAULT ''       -- 摘要信息，绝不包含正文、密码、密钥
);
CREATE INDEX IF NOT EXISTS idx_audit_ts ON audit_logs(id DESC);
`,
	},
	{
		Version: 2,
		SQL: `
-- 第二阶段：书籍归属（单用户阶段用于保持“不可仅凭 bookID 越权”的校验习惯）
ALTER TABLE books ADD COLUMN owner_id TEXT NOT NULL DEFAULT '';
UPDATE books SET owner_id = COALESCE((SELECT username FROM users ORDER BY id LIMIT 1), '')
 WHERE owner_id = '';
CREATE INDEX IF NOT EXISTS idx_books_owner ON books(owner_id);

-- 片段级音频缓存索引（权威数据在文件系统，此表用于统计与章节关联查询）
CREATE TABLE IF NOT EXISTS tts_segments (
    cache_key     TEXT PRIMARY KEY,              -- 片段指纹（含文本规范化哈希/voice/speed/引擎/格式/版本）
    book_id       TEXT NOT NULL DEFAULT '',
    chapter_index INTEGER NOT NULL DEFAULT -1,
    segment_index INTEGER NOT NULL DEFAULT -1,
    provider      TEXT NOT NULL,
    voice         TEXT NOT NULL DEFAULT '',
    speed         REAL NOT NULL DEFAULT 1.0,
    format        TEXT NOT NULL DEFAULT 'wav',
    plain_bytes   INTEGER NOT NULL,
    cipher_bytes  INTEGER NOT NULL,
    created_at    TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    last_hit_at   TEXT
);
CREATE INDEX IF NOT EXISTS idx_tts_segments_book ON tts_segments(book_id, chapter_index);

-- 合成日志：只记录耗时与片段键，绝不记录文本
CREATE TABLE IF NOT EXISTS tts_synth_logs (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    ts          TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    cache_key   TEXT NOT NULL,
    provider    TEXT NOT NULL,
    result      TEXT NOT NULL,                   -- ok / error / timeout / ratelimited
    duration_ms INTEGER NOT NULL,
    err_class   TEXT NOT NULL DEFAULT ''         -- 归一化错误类别，不含引擎原始响应
);
CREATE INDEX IF NOT EXISTS idx_tts_synth_ts ON tts_synth_logs(id DESC);
`,
	},
	{
		Version: 3,
		SQL: `
-- 第三阶段：元数据（只允许这些字段进入服务端检索索引，正文永不入库）
ALTER TABLE books ADD COLUMN author      TEXT NOT NULL DEFAULT '';
ALTER TABLE books ADD COLUMN tags        TEXT NOT NULL DEFAULT '';  -- 逗号分隔
ALTER TABLE books ADD COLUMN description TEXT NOT NULL DEFAULT '';
ALTER TABLE books ADD COLUMN chapter_count INTEGER NOT NULL DEFAULT 0;
ALTER TABLE books ADD COLUMN index_state TEXT NOT NULL DEFAULT 'pending'; -- pending/indexed/error/disabled

-- 章节目录：上传时由流式扫描产生，只含标题与字数，不含正文
CREATE TABLE IF NOT EXISTS book_chapters (
    book_id       TEXT NOT NULL,
    chapter_index INTEGER NOT NULL,
    title         TEXT NOT NULL,
    chars         INTEGER NOT NULL,
    start_rune    INTEGER NOT NULL,
    end_rune      INTEGER NOT NULL,
    PRIMARY KEY (book_id, chapter_index)
);

-- 客户端本地索引回执（用于覆盖度指标，非强制）
CREATE TABLE IF NOT EXISTS chapter_acks (
    book_id       TEXT NOT NULL,
    chapter_index INTEGER NOT NULL,
    client_id     TEXT NOT NULL DEFAULT '',
    etag          TEXT NOT NULL DEFAULT '',
    acked_at      TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    PRIMARY KEY (book_id, chapter_index, client_id)
);

-- 搜索观测：只记录长度与带密钥的哈希，绝不记录查询词明文
CREATE TABLE IF NOT EXISTS search_logs (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    ts          TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    username    TEXT NOT NULL DEFAULT '',
    q_len       INTEGER NOT NULL,
    q_hash      TEXT NOT NULL,
    hits        INTEGER NOT NULL DEFAULT 0,
    duration_ms INTEGER NOT NULL,
    ok          TEXT NOT NULL,
    source      TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_search_logs_id ON search_logs(id DESC);

-- Legado 书源令牌：与用户 session 分离，可单独撤销；只存哈希
CREATE TABLE IF NOT EXISTS legado_tokens (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    name         TEXT NOT NULL DEFAULT '',
    token_hash   TEXT NOT NULL UNIQUE,
    user_id      INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_at   TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    last_used_at TEXT,
    revoked_at   TEXT
);

-- 会话分级：api（管理接口）/ legado（仅书源网关）。网关会话不能用于管理接口。
ALTER TABLE sessions ADD COLUMN scope TEXT NOT NULL DEFAULT 'api';
CREATE INDEX IF NOT EXISTS idx_sessions_scope ON sessions(scope, expires_at);
`,
	},
	{
		Version: 4,
		SQL: `
-- 第四阶段：数据生命周期管理

-- 音频片段访问时间：淘汰策略依据（不记录文本，只记录键与时间）
ALTER TABLE tts_segments ADD COLUMN last_access TEXT;
UPDATE tts_segments SET last_access = COALESCE(last_hit_at, created_at)
 WHERE last_access IS NULL;
CREATE INDEX IF NOT EXISTS idx_tts_segments_access ON tts_segments(last_access);

-- 音频缓存 KEK 版本：轮换后按版本判断是否需要重包装
ALTER TABLE tts_segments ADD COLUMN kek_version INTEGER NOT NULL DEFAULT 1;

-- 每次朗读请求一行统计（只有计数与耗时，不含文本/音频）
CREATE TABLE IF NOT EXISTS tts_speak_stats (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    ts            TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    book_id       TEXT NOT NULL DEFAULT '',
    chapter_index INTEGER NOT NULL DEFAULT -1,
    segments      INTEGER NOT NULL DEFAULT 0,
    hits          INTEGER NOT NULL DEFAULT 0,
    synths        INTEGER NOT NULL DEFAULT 0,
    bytes         INTEGER NOT NULL DEFAULT 0,
    duration_ms   INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_tts_speak_stats_ts ON tts_speak_stats(id DESC);

-- 备份记录：由离线备份命令写入，供控制台展示「最近备份时间与校验和」
CREATE TABLE IF NOT EXISTS backup_runs (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    ts         TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    path       TEXT NOT NULL DEFAULT '',
    checksum   TEXT NOT NULL,
    size_bytes INTEGER NOT NULL,
    books      INTEGER NOT NULL DEFAULT 0,
    segments   INTEGER NOT NULL DEFAULT 0,
    result     TEXT NOT NULL DEFAULT 'ok',
    detail     TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_backup_runs_ts ON backup_runs(id DESC);

-- KEK 轮换记录（旧 KEK 的归档是人工步骤，不由代码执行）
CREATE TABLE IF NOT EXISTS kek_rotations (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    ts               TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    from_version     INTEGER NOT NULL,
    to_version       INTEGER NOT NULL,
    books_rotated    INTEGER NOT NULL DEFAULT 0,
    segments_rotated INTEGER NOT NULL DEFAULT 0,
    result           TEXT NOT NULL DEFAULT 'ok',
    detail           TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_kek_rotations_ts ON kek_rotations(id DESC);

-- 恢复演练记录（与 JSONL 演练日志并存）
CREATE TABLE IF NOT EXISTS drill_runs (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    ts         TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    package    TEXT NOT NULL DEFAULT '',
    checksum   TEXT NOT NULL DEFAULT '',
    book_id    TEXT NOT NULL DEFAULT '',
    result     TEXT NOT NULL,
    detail     TEXT NOT NULL DEFAULT '',
    duration_ms INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_drill_runs_ts ON drill_runs(id DESC);
`,
	},
	{
		Version: 5,
		SQL: `
-- 第五阶段：移动端 App（device token / 增量同步 / 阅读进度）

-- 同步游标：每次新增、删除、改元数据都推进；客户端只拉 sync_seq > 游标 的记录。
-- 不用时间戳做游标：同一毫秒内多次写入会丢变更，且依赖时钟一致性。
ALTER TABLE books ADD COLUMN sync_seq INTEGER NOT NULL DEFAULT 0;
UPDATE books SET sync_seq = rowid;
CREATE INDEX IF NOT EXISTS idx_books_sync ON books(owner_id, sync_seq);

-- 设备长期凭证：只存 token 的 SHA-256 与前缀，泄露库文件无法反推凭证。
CREATE TABLE IF NOT EXISTS device_tokens (
    id            TEXT PRIMARY KEY,              -- 随机 16 hex，管理接口与审计用
    owner_id      TEXT NOT NULL,
    name          TEXT NOT NULL DEFAULT '',
    token_hash    TEXT NOT NULL UNIQUE,          -- SHA-256(token) hex
    token_prefix  TEXT NOT NULL DEFAULT '',      -- 前 12 字符，仅用于展示/识别
    public_key_pem TEXT NOT NULL DEFAULT '',     -- 设备 RSA 公钥（SPKI PEM），用于包装 DEK
    created_at    TEXT NOT NULL,
    last_used_at  TEXT,
    revoked_at    TEXT,
    expires_at    TEXT                           -- 空表示长期有效，撤销仍然优先
);
CREATE INDEX IF NOT EXISTS idx_device_tokens_owner ON device_tokens(owner_id);

-- 一次性配对码：手机输入短码换 device token，避免在手机键盘上输长密码。
CREATE TABLE IF NOT EXISTS pairing_codes (
    id         TEXT PRIMARY KEY,
    owner_id   TEXT NOT NULL,
    code_hash  TEXT NOT NULL UNIQUE,
    created_at TEXT NOT NULL,
    expires_at TEXT NOT NULL,
    used_at    TEXT
);
CREATE INDEX IF NOT EXISTS idx_pairing_codes_owner ON pairing_codes(owner_id);

-- 阅读进度：最后写入胜；不含正文，按需求不加密。
CREATE TABLE IF NOT EXISTS reading_progress (
    owner_id      TEXT NOT NULL,
    book_id       TEXT NOT NULL,
    chapter_index INTEGER NOT NULL DEFAULT 0,
    char_offset   INTEGER NOT NULL DEFAULT 0,
    percent       REAL NOT NULL DEFAULT 0,
    device_id     TEXT NOT NULL DEFAULT '',
    updated_at    TEXT NOT NULL,
    PRIMARY KEY (owner_id, book_id)
);
CREATE INDEX IF NOT EXISTS idx_reading_progress_updated ON reading_progress(owner_id, updated_at DESC);
`,
	},
	{
		Version: 6,
		SQL: `
-- 第六阶段：健康信号持续时间。只存信号名与首次/最近观测时间，不含正文或用户数据。
-- 「索引偏差持续超过 1 小时」靠这张表跨重启保留首次观测时间。
CREATE TABLE IF NOT EXISTS health_signals (
    key        TEXT PRIMARY KEY,
    since      TEXT NOT NULL,
    last_seen  TEXT NOT NULL
);
`,
	},
	{
		Version: 7,
		SQL: `
-- 第八阶段：书籍收藏（星标/心标）。独立表而非 books 列，避免收藏状态污染
-- books 的 sync_seq 语义（收藏是用户偏好，不应触发整本书重新同步）。
-- 只存 book_id 与布尔标记，绝不涉及正文。
CREATE TABLE IF NOT EXISTS user_bookmarks (
    owner_id     TEXT NOT NULL,
    book_id      TEXT NOT NULL,
    is_favorite  INTEGER NOT NULL DEFAULT 0,
    updated_at   TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    PRIMARY KEY (owner_id, book_id)
);
CREATE INDEX IF NOT EXISTS idx_user_bookmarks_fav ON user_bookmarks(owner_id, is_favorite);
`,
	},
}

// Open 打开（必要时创建）SQLite 数据库，启用 WAL 与 busy_timeout，并执行迁移。
func Open(path string) (*sql.DB, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("创建数据目录失败: %w", err)
	}
	// modernc.org/sqlite 纯 Go 驱动：通过 DSN pragma 启用 WAL、busy_timeout、外键。
	dsn := "file:" + path +
		"?_pragma=journal_mode(WAL)" +
		"&_pragma=busy_timeout(5000)" +
		"&_pragma=foreign_keys(1)" +
		"&_pragma=synchronous(NORMAL)"
	d, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("打开数据库失败: %w", err)
	}
	// 单写者场景：SQLite 串行化写入更稳妥，也避免 SQLITE_BUSY。
	d.SetMaxOpenConns(1)
	d.SetMaxIdleConns(1)
	if err := d.Ping(); err != nil {
		d.Close()
		return nil, fmt.Errorf("数据库不可用: %w", err)
	}
	if err := migrate(d); err != nil {
		d.Close()
		return nil, err
	}
	return d, nil
}

// migrate 按 version 顺序执行未应用的迁移。每个迁移独立事务。
func migrate(d *sql.DB) error {
	if _, err := d.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (
		version    INTEGER PRIMARY KEY,
		applied_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
	)`); err != nil {
		return fmt.Errorf("创建 schema_migrations 失败: %w", err)
	}
	var current int
	if err := d.QueryRow(`SELECT COALESCE(MAX(version),0) FROM schema_migrations`).Scan(&current); err != nil {
		return fmt.Errorf("读取迁移版本失败: %w", err)
	}
	for _, m := range migrations {
		if m.Version <= current {
			continue
		}
		tx, err := d.Begin()
		if err != nil {
			return fmt.Errorf("开启迁移事务失败: %w", err)
		}
		if _, err := tx.Exec(m.SQL); err != nil {
			tx.Rollback()
			return fmt.Errorf("迁移 %d 执行失败: %w", m.Version, err)
		}
		if _, err := tx.Exec(`INSERT INTO schema_migrations(version) VALUES (?)`, m.Version); err != nil {
			tx.Rollback()
			return fmt.Errorf("记录迁移版本 %d 失败: %w", m.Version, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("提交迁移 %d 失败: %w", m.Version, err)
		}
	}
	return nil
}
