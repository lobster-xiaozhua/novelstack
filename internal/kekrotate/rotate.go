// Package kekrotate 实现 KEK 轮换：用旧 KEK 解包 DEK，用新 KEK 重新包装。
//
// 轮换是幂等的：按 records 的 kek_version 判断，已轮换的记录跳过，中途失败可重跑。
// 代码从不删除旧 KEK 文件；旧 KEK 的归档是人工步骤（确认全部轮换并校验通过后）。
package kekrotate

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"

	"novelstack/internal/audit"
	"novelstack/internal/cryptobox"
	"novelstack/internal/ttscache"

	_ "modernc.org/sqlite"
)

// RotateOptions 是轮换参数。
type RotateOptions struct {
	DBPath        string
	CiphertextDir string
	TTSCacheDir   string
	OldKEK        []byte
	NewKEK        []byte
	FromVersion   int
	ToVersion     int
	// SkipAudio 为 true 时只轮换书籍 DEK（默认同时轮换音频缓存）。
	SkipAudio bool
	// OnBook 是测试用失败注入钩子：返回错误即中止（已提交的行保持轮换状态）。
	OnBook func(done int) error
	// Now 便于测试。
	Now func() time.Time
}

// RotateResult 汇总一次轮换。
type RotateResult struct {
	FromVersion     int      `json:"from_version"`
	ToVersion       int      `json:"to_version"`
	BooksTotal      int      `json:"books_total"`
	BooksRotated    int      `json:"books_rotated"`
	BooksSkipped    int      `json:"books_skipped"`
	SegmentsRotated int      `json:"segments_rotated"`
	SegmentsSkipped int      `json:"segments_skipped"`
	DurationMS      int64    `json:"duration_ms"`
	Warnings        []string `json:"warnings,omitempty"`
	Completed       bool     `json:"completed"`
}

// Rotate 执行书籍与音频缓存的 KEK 轮换。
// 服务可在轮换窗口内继续读：只要进程同时加载了旧/新 KEK（KEK_OLD_PATHS）。
func Rotate(ctx context.Context, opts RotateOptions) (*RotateResult, error) {
	started := time.Now()
	if opts.FromVersion <= 0 || opts.ToVersion <= 0 || opts.FromVersion == opts.ToVersion {
		return nil, errors.New("kekrotate: 版本号非法")
	}
	if len(opts.OldKEK) != cryptobox.KeySize || len(opts.NewKEK) != cryptobox.KeySize {
		return nil, errors.New("kekrotate: 需要 32 字节的新旧 KEK")
	}
	if opts.DBPath == "" {
		return nil, errors.New("kekrotate: 数据库路径不能为空")
	}
	res := &RotateResult{FromVersion: opts.FromVersion, ToVersion: opts.ToVersion}
	defer func() { res.DurationMS = time.Since(started).Milliseconds() }()

	db, err := sql.Open("sqlite", "file:"+opts.DBPath+"?_pragma=busy_timeout(10000)&_pragma=journal_mode(WAL)")
	if err != nil {
		return nil, fmt.Errorf("kekrotate: 打开数据库失败: %w", err)
	}
	defer db.Close()

	rows, err := db.QueryContext(ctx, `SELECT id, wrapped_dek, dek_nonce, kek_version FROM books ORDER BY created_at`)
	if err != nil {
		return nil, fmt.Errorf("kekrotate: 读取书籍失败: %w", err)
	}
	type bookRow struct {
		id      string
		wrapped []byte
		nonce   []byte
		ver     int
	}
	var books []bookRow
	for rows.Next() {
		var b bookRow
		if err := rows.Scan(&b.id, &b.wrapped, &b.nonce, &b.ver); err != nil {
			rows.Close()
			return nil, err
		}
		books = append(books, b)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	res.BooksTotal = len(books)

	for _, b := range books {
		switch {
		case b.ver == opts.ToVersion:
			res.BooksSkipped++
			continue
		case b.ver != opts.FromVersion:
			return res, fmt.Errorf("kekrotate: 书籍 %s 的 KEK 版本为 v%d，既不是 v%d 也不是 v%d；请先处理版本异常",
				b.id, b.ver, opts.FromVersion, opts.ToVersion)
		}
		dek, err := cryptobox.UnwrapDEK(opts.OldKEK, b.wrapped, b.nonce)
		if err != nil {
			return res, fmt.Errorf("kekrotate: 书籍 %s 用旧 KEK 解包失败（可能已被轮换或数据损坏）: %w", b.id, err)
		}
		wrapped, nonce, err := cryptobox.WrapDEK(opts.NewKEK, dek)
		if err != nil {
			return res, err
		}
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return res, err
		}
		r, err := tx.ExecContext(ctx, `UPDATE books SET wrapped_dek=?, dek_nonce=?, kek_version=?
			WHERE id=? AND kek_version=?`, wrapped, nonce, opts.ToVersion, b.id, opts.FromVersion)
		if err != nil {
			tx.Rollback()
			return res, err
		}
		n, _ := r.RowsAffected()
		if err := tx.Commit(); err != nil {
			return res, err
		}
		if n == 0 {
			// 并发轮换已处理：按幂等跳过
			res.BooksSkipped++
			continue
		}
		res.BooksRotated++
		if opts.OnBook != nil {
			if err := opts.OnBook(res.BooksRotated); err != nil {
				return res, err
			}
		}
	}

	if !opts.SkipAudio && opts.TTSCacheDir != "" {
		if st, err := os.Stat(opts.TTSCacheDir); err == nil && st.IsDir() {
			store, err := ttscache.NewWithKeyRing(opts.TTSCacheDir,
				map[int][]byte{opts.FromVersion: opts.OldKEK, opts.ToVersion: opts.NewKEK},
				opts.ToVersion, 1<<30)
			if err != nil {
				return res, err
			}
			rotated, skipped, err := store.RotateAll(opts.ToVersion)
			if err != nil {
				return res, fmt.Errorf("kekrotate: 音频缓存轮换中断（可重跑）: %w", err)
			}
			res.SegmentsRotated, res.SegmentsSkipped = rotated, skipped
			if _, err := db.ExecContext(ctx,
				`UPDATE tts_segments SET kek_version=? WHERE kek_version <> ?`,
				opts.ToVersion, opts.ToVersion); err != nil {
				res.Warnings = append(res.Warnings, "音频片段索引的 kek_version 校正失败")
			}
		}
	}

	res.Completed = true
	recordRotation(db, res, opts.FromVersion, opts.ToVersion)
	return res, nil
}

// recordRotation 写入 kek_rotations 与审计（失败不影响轮换结果）。
func recordRotation(db *sql.DB, res *RotateResult, from, to int) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, _ = db.ExecContext(ctx, `INSERT INTO kek_rotations
		(from_version, to_version, books_rotated, segments_rotated, result, detail)
		VALUES (?,?,?,?, 'ok', ?)`,
		from, to, res.BooksRotated, res.SegmentsRotated,
		fmt.Sprintf("books_skipped=%d segments_skipped=%d", res.BooksSkipped, res.SegmentsSkipped))
	audit.New(db).Record(ctx, "system", "cli", "kek_rotated", "", "ok",
		fmt.Sprintf("from=v%d to=v%d books=%d segments=%d", from, to, res.BooksRotated, res.SegmentsRotated))
}

// VerifyOptions 是轮换后抽样校验参数。
type VerifyOptions struct {
	DBPath        string
	CiphertextDir string
	TTSCacheDir   string
	// KEKs 是版本 → KEK 的映射，必须同时包含旧版本与当前版本。
	KEKs   map[int][]byte
	Sample int
}

// BookCheck 是单本书的校验结果。
type BookCheck struct {
	BookID string `json:"book_id"`
	KEKVer int    `json:"kek_version"`
	Size   int64  `json:"size_bytes"`
	SHA256 string `json:"sha256"`
	OK     bool   `json:"ok"`
	Error  string `json:"error,omitempty"`
}

// VerifyResult 汇总抽样校验。
type VerifyResult struct {
	Checked      []BookCheck `json:"checked"`
	AudioChecked int         `json:"audio_checked"`
	Failures     []string    `json:"failures,omitempty"`
}

// Verify 按 KEK 版本分别抽样解密书籍，并抽样解密音频缓存。
// 任何失败都返回 error（调用方据此返回非零退出码）。
func Verify(ctx context.Context, opts VerifyOptions) (*VerifyResult, error) {
	if opts.DBPath == "" {
		return nil, errors.New("kekrotate: 数据库路径不能为空")
	}
	if opts.Sample <= 0 {
		opts.Sample = 3
	}
	res := &VerifyResult{Checked: []BookCheck{}}
	db, err := sql.Open("sqlite", "file:"+opts.DBPath+"?mode=ro&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	defer db.Close()

	if len(opts.KEKs) == 0 {
		return nil, errors.New("kekrotate: 未提供任何 KEK")
	}
	versions := make([]int, 0, len(opts.KEKs))
	for v, k := range opts.KEKs {
		if len(k) != cryptobox.KeySize {
			return nil, fmt.Errorf("kekrotate: v%d 的 KEK 长度不是 32 字节", v)
		}
		versions = append(versions, v)
	}
	sort.Ints(versions)

	// 数据库里存在、但本次未提供 KEK 的版本必须显式报告，
	// 否则“抽样 0 本”会被误判为通过。
	missingRows, err := db.QueryContext(ctx, `SELECT DISTINCT kek_version FROM books`)
	if err != nil {
		return nil, err
	}
	for missingRows.Next() {
		var v int
		if err := missingRows.Scan(&v); err != nil {
			missingRows.Close()
			return nil, err
		}
		if _, ok := opts.KEKs[v]; !ok {
			res.Failures = append(res.Failures, fmt.Sprintf("缺少 v%d 的 KEK（数据库中仍有该版本记录）", v))
		}
	}
	missingRows.Close()
	if err := missingRows.Err(); err != nil {
		return nil, err
	}

	for _, ver := range versions {
		rows, err := db.QueryContext(ctx, `SELECT id, sha256, size_bytes, chunk_size FROM books
			WHERE kek_version = ? ORDER BY RANDOM() LIMIT ?`, ver, opts.Sample)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var id, sha string
			var size int64
			var chunk int
			if err := rows.Scan(&id, &sha, &size, &chunk); err != nil {
				rows.Close()
				return nil, err
			}
			c := checkBook(ctx, db, opts, ver, id, sha, size, chunk)
			res.Checked = append(res.Checked, c)
			if !c.OK {
				res.Failures = append(res.Failures, fmt.Sprintf("%s(v%d): %s", id, ver, c.Error))
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}

	if opts.TTSCacheDir != "" {
		if st, err := os.Stat(opts.TTSCacheDir); err == nil && st.IsDir() {
			store, err := ttscache.NewWithKeyRing(opts.TTSCacheDir, opts.KEKs, versions[len(versions)-1], 1<<30)
			if err != nil {
				return nil, err
			}
			n := 0
			err = store.WalkKeys(func(key string, _ int64, _ int) error {
				if n >= opts.Sample {
					return nil
				}
				if _, err := store.Get(key); err != nil {
					res.Failures = append(res.Failures, "音频片段: 解密失败")
					return nil
				}
				n++
				return nil
			})
			if err != nil {
				return nil, err
			}
			res.AudioChecked = n
		}
	}
	if len(res.Failures) > 0 {
		return res, fmt.Errorf("kekrotate: 抽样校验失败 %d 项", len(res.Failures))
	}
	return res, nil
}

func checkBook(ctx context.Context, db *sql.DB, opts VerifyOptions, ver int, id, wantSHA string, wantSize int64, chunkSize int) BookCheck {
	c := BookCheck{BookID: id, KEKVer: ver, Size: wantSize, SHA256: wantSHA}
	fail := func(err error) BookCheck {
		c.OK = false
		c.Error = err.Error()
		return c
	}
	var wrapped, nonce []byte
	err := db.QueryRowContext(ctx,
		`SELECT wrapped_dek, dek_nonce FROM books WHERE id=?`, id).Scan(&wrapped, &nonce)
	if err != nil {
		return fail(err)
	}
	kek, ok := opts.KEKs[ver]
	if !ok {
		return fail(fmt.Errorf("缺少 v%d 的 KEK", ver))
	}
	dek, err := cryptobox.UnwrapDEK(kek, wrapped, nonce)
	if err != nil {
		return fail(err)
	}
	f, err := os.Open(filepath.Join(opts.CiphertextDir, id))
	if err != nil {
		return fail(err)
	}
	defer f.Close()
	h := sha256.New()
	sum, size, err := cryptobox.DecryptStream(f, h, dek, id, chunkSize, wantSize+1)
	if err != nil {
		return fail(err)
	}
	if size != wantSize {
		return fail(fmt.Errorf("长度不一致"))
	}
	got := hex.EncodeToString(sum)
	if got != wantSHA {
		return fail(fmt.Errorf("SHA-256 不一致"))
	}
	c.OK = true
	return c
}

// ErrKeyMismatch 表示旧 KEK 无法解包已轮换记录（供测试断言）。
var ErrKeyMismatch = cryptobox.ErrTampered

// DecryptWhole 解密整本书并返回 SHA-256（供测试与人工诊断）。
func DecryptWhole(ctx context.Context, dbPath, cipherDir, id string, kek []byte) (string, error) {
	db, err := sql.Open("sqlite", "file:"+dbPath+"?mode=ro&_pragma=busy_timeout(5000)")
	if err != nil {
		return "", err
	}
	defer db.Close()
	var wrapped, nonce []byte
	var ver int
	var size int64
	var chunk int
	if err := db.QueryRowContext(ctx,
		`SELECT wrapped_dek, dek_nonce, kek_version, size_bytes, chunk_size FROM books WHERE id=?`, id).
		Scan(&wrapped, &nonce, &ver, &size, &chunk); err != nil {
		return "", err
	}
	_ = ver
	dek, err := cryptobox.UnwrapDEK(kek, wrapped, nonce)
	if err != nil {
		return "", err
	}
	f, err := os.Open(filepath.Join(cipherDir, id))
	if err != nil {
		return "", err
	}
	defer f.Close()
	sum, _, err := cryptobox.DecryptStream(f, io.Discard, dek, id, chunk, size+1)
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(sum), nil
}
