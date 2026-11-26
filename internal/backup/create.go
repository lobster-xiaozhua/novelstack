package backup

import (
	"compress/gzip"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"novelstack/internal/audit"

	_ "modernc.org/sqlite"
)

// CreateOptions 是备份命令参数。
type CreateOptions struct {
	DataDir string
	OutDir  string
	// Name 为空时自动生成 novelstack-YYYYmmdd-HHMMSS.nsb
	Name       string
	Passphrase []byte
	// Params 为零值时使用 DefaultKDFParams。
	Params KDFParams
	// ChunkSize 为零值时使用 1MiB。
	ChunkSize int
	// OmitTTSCache 为 true 时不打包音频缓存（默认打包）。
	OmitTTSCache bool
	// Compression 为 0 时使用 gzip。
	Compression byte
	// Now 便于测试注入固定时间。
	Now func() time.Time
}

// CreateResult 描述一次备份的结果。
type CreateResult struct {
	Path       string    `json:"path"`
	Checksum   string    `json:"sha256"`
	SizeBytes  int64     `json:"size_bytes"`
	Manifest   *Manifest `json:"manifest"`
	Warnings   []string  `json:"warnings,omitempty"`
	DurationMS int64     `json:"duration_ms"`
}

// ErrMissingCiphertext 表示数据库有书籍记录但密文文件缺失（异常状态）。
var ErrMissingCiphertext = fmt.Errorf("backup: 存在只有元数据、没有密文的书籍")

// Create 生成一致性加密备份包。
//
// 关键约束：
//   - SQLite 使用 VACUUM INTO 生成一致快照，绝不直接拷贝 WAL 数据库；
//   - 快照中清除 sessions 与 legado 令牌（恢复后需重新登录/重新生成令牌）；
//   - 加密密钥只来自备份口令，与主 KEK 无关；主 KEK 永不进入备份包。
func Create(ctx context.Context, opts CreateOptions) (*CreateResult, error) {
	started := time.Now()
	if opts.DataDir == "" {
		return nil, fmt.Errorf("backup: DATA_DIR 不能为空")
	}
	if len(opts.Passphrase) == 0 {
		return nil, fmt.Errorf("backup: 必须提供备份口令")
	}
	if opts.Params == (KDFParams{}) {
		opts.Params = DefaultKDFParams
	}
	if opts.ChunkSize == 0 {
		opts.ChunkSize = 1 << 20
	}
	if opts.Compression == 0 {
		opts.Compression = CompressionGzip
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.OutDir == "" {
		opts.OutDir = filepath.Join(opts.DataDir, "backups")
	}
	dbPath := filepath.Join(opts.DataDir, "app.db")
	if st, err := os.Stat(dbPath); err != nil || st.IsDir() {
		return nil, fmt.Errorf("backup: 数据库不存在或不可读: %s", dbPath)
	}

	man, books, err := inspectData(ctx, dbPath, opts)
	if err != nil {
		return nil, err
	}

	if err := os.MkdirAll(opts.OutDir, 0o700); err != nil {
		return nil, fmt.Errorf("backup: 创建输出目录失败: %w", err)
	}
	name := opts.Name
	if name == "" {
		name = "novelstack-" + opts.Now().UTC().Format("20060102-150405") + ".nsb"
	}
	finalPath := filepath.Join(opts.OutDir, name)
	if _, err := os.Stat(finalPath); err == nil {
		return nil, fmt.Errorf("backup: 目标已存在，拒绝覆盖: %s", finalPath)
	}

	tmpDir, err := os.MkdirTemp("", "novelstack-backup-*")
	if err != nil {
		return nil, fmt.Errorf("backup: 创建临时目录失败: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	snapshot := filepath.Join(tmpDir, "app.db")
	if err := vacuumInto(ctx, dbPath, snapshot); err != nil {
		return nil, err
	}
	if err := scrubSnapshot(snapshot); err != nil {
		return nil, err
	}
	dbSum, dbSize, err := hashFile(snapshot)
	if err != nil {
		return nil, err
	}
	man.DBBytes = dbSize
	man.DBSHA256 = dbSum

	files, stats, err := collectFiles(opts, books)
	if err != nil {
		return nil, err
	}
	man.CiphertextBytes = stats.cipherBytes
	man.TTSCacheBytes = stats.ttsBytes
	man.ClientIndexFiles = stats.clientFiles
	man.OrphanFiles = stats.orphanFiles

	files = append([]archiveFile{{Name: "data/app.db", Path: snapshot, Size: dbSize}}, files...)
	rawManifest, err := writeManifestJSON(man)
	if err != nil {
		return nil, err
	}

	tmpOut := filepath.Join(opts.OutDir, "."+name+".partial")
	_ = os.Remove(tmpOut)
	f, err := os.OpenFile(tmpOut, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, fmt.Errorf("backup: 创建备份文件失败: %w", err)
	}
	hasher := sha256.New()
	multi := io.MultiWriter(f, hasher)
	ew, _, err := NewWriter(multi, opts.Passphrase, opts.Params, opts.Compression, opts.ChunkSize)
	if err != nil {
		f.Close()
		os.Remove(tmpOut)
		return nil, err
	}
	var sink io.Writer = ew
	var gz *gzip.Writer
	if opts.Compression == CompressionGzip {
		gz = gzip.NewWriter(ew)
		sink = gz
	}
	if err := writeTar(sink, rawManifest, files); err != nil {
		f.Close()
		os.Remove(tmpOut)
		return nil, err
	}
	if gz != nil {
		if err := gz.Close(); err != nil {
			f.Close()
			os.Remove(tmpOut)
			return nil, err
		}
	}
	if err := ew.Close(); err != nil {
		f.Close()
		os.Remove(tmpOut)
		return nil, err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(tmpOut)
		return nil, err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmpOut)
		return nil, err
	}
	if err := os.Rename(tmpOut, finalPath); err != nil {
		os.Remove(tmpOut)
		return nil, fmt.Errorf("backup: 落盘失败: %w", err)
	}
	st, err := os.Stat(finalPath)
	if err != nil {
		return nil, err
	}
	sum := hex.EncodeToString(hasher.Sum(nil))
	sidecar := finalPath + ".sha256"
	if err := os.WriteFile(sidecar, []byte(sum+"  "+filepath.Base(finalPath)+"\n"), 0o600); err != nil {
		return nil, fmt.Errorf("backup: 写入校验和文件失败: %w", err)
	}

	res := &CreateResult{
		Path:       finalPath,
		Checksum:   sum,
		SizeBytes:  st.Size(),
		Manifest:   man,
		DurationMS: time.Since(started).Milliseconds(),
	}
	res.Warnings = append(res.Warnings, recordBackupRun(dbPath, res)...)
	return res, nil
}

// inspectData 读取元数据并校验「每本书都有密文」这一不变量。
func inspectData(ctx context.Context, dbPath string, opts CreateOptions) (*Manifest, map[string]struct{}, error) {
	src, err := sql.Open("sqlite", "file:"+dbPath+"?mode=ro&_pragma=busy_timeout(10000)")
	if err != nil {
		return nil, nil, fmt.Errorf("backup: 打开数据库失败: %w", err)
	}
	defer src.Close()
	if err := src.PingContext(ctx); err != nil {
		return nil, nil, fmt.Errorf("backup: 数据库不可用: %w", err)
	}

	man := &Manifest{
		FormatVersion: FormatVersion,
		CreatedAt:     opts.Now().UTC().Format(time.RFC3339),
		Tool:          "novelstack backup v1",
		DataLayout:    "data/app.db + data/ciphertext + data/tts-cache + data/client-index",
		KEKVersions:   map[string]int{},
		BookList:      []BookEntry{},
		Notes: []string{
			"备份包不包含主 KEK；KEK 必须离线人工备份（打印或硬件密钥）",
			"快照已清除 sessions 与 legado_tokens，恢复后需重新登录并重新生成书源令牌",
		},
	}
	if err := src.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(SUM(chapter_count),0)
		FROM books WHERE deleted_at IS NULL`).Scan(&man.Books, &man.Chapters); err != nil {
		return nil, nil, fmt.Errorf("backup: 统计书籍失败: %w", err)
	}
	if err := src.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(SUM(cipher_bytes),0) FROM tts_segments`).
		Scan(&man.Segments, &man.TTSCacheBytes); err != nil {
		// 旧库可能没有该表：不阻断备份
		man.Segments = 0
	}

	rows, err := src.QueryContext(ctx, `SELECT id, title, sha256, size_bytes, kek_version, chapter_count
		FROM books WHERE deleted_at IS NULL ORDER BY created_at`)
	if err != nil {
		return nil, nil, fmt.Errorf("backup: 读取书籍清单失败: %w", err)
	}
	defer rows.Close()
	books := make(map[string]struct{}, man.Books)
	var missing []string
	for rows.Next() {
		var b BookEntry
		if err := rows.Scan(&b.ID, &b.Title, &b.SHA256, &b.SizeBytes, &b.KEKVersion, &b.Chapters); err != nil {
			return nil, nil, err
		}
		if b.SHA256 == "" {
			return nil, nil, fmt.Errorf("backup: 书籍 %s 缺少明文校验和，数据异常", b.ID)
		}
		books[b.ID] = struct{}{}
		man.BookList = append(man.BookList, b)
		man.KEKVersions[fmt.Sprintf("v%d", b.KEKVersion)]++
		if st, err := os.Stat(filepath.Join(opts.DataDir, "ciphertext", b.ID)); err != nil || !st.Mode().IsRegular() {
			missing = append(missing, b.ID)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	if len(missing) > 0 {
		sample := missing
		if len(sample) > 5 {
			sample = sample[:5]
		}
		return nil, nil, fmt.Errorf("%w: %d 本（示例: %s）", ErrMissingCiphertext, len(missing), strings.Join(sample, ", "))
	}
	return man, books, nil
}

// vacuumInto 用 SQLite 官方一致性快照机制，而不是复制 WAL 文件。
func vacuumInto(ctx context.Context, dbPath, dest string) error {
	src, err := sql.Open("sqlite", "file:"+dbPath+"?mode=ro&_pragma=busy_timeout(10000)")
	if err != nil {
		return fmt.Errorf("backup: 打开数据库失败: %w", err)
	}
	defer src.Close()
	stmt := `VACUUM INTO '` + strings.ReplaceAll(dest, "'", "''") + `'`
	if _, err := src.ExecContext(ctx, stmt); err != nil {
		return fmt.Errorf("backup: VACUUM INTO 失败: %w", err)
	}
	return nil
}

// scrubSnapshot 清除会话与书源令牌，并压缩快照（避免已删除数据残留）。
func scrubSnapshot(path string) error {
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(5000)")
	if err != nil {
		return fmt.Errorf("backup: 打开快照失败: %w", err)
	}
	defer db.Close()
	for _, stmt := range []string{
		`PRAGMA journal_mode=DELETE`,
		`DELETE FROM sessions`,
		`DELETE FROM legado_tokens`,
		`VACUUM`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			// 旧库可能没有 legado_tokens，不阻断
			if strings.Contains(stmt, "legado_tokens") {
				continue
			}
			return fmt.Errorf("backup: 清理快照失败: %w", err)
		}
	}
	var check string
	if err := db.QueryRow(`PRAGMA integrity_check`).Scan(&check); err != nil {
		return fmt.Errorf("backup: 快照完整性检查失败: %w", err)
	}
	if check != "ok" {
		return fmt.Errorf("backup: 快照完整性检查未通过: %s", check)
	}
	return nil
}

type fileStats struct {
	cipherBytes int64
	ttsBytes    int64
	clientFiles int
	orphanFiles int
}

// collectFiles 收集密文书籍、音频缓存与（可选的）客户端索引快照。
func collectFiles(opts CreateOptions, books map[string]struct{}) ([]archiveFile, fileStats, error) {
	var (
		files []archiveFile
		stats fileStats
	)
	cipherDir := filepath.Join(opts.DataDir, "ciphertext")
	if st, err := os.Stat(cipherDir); err == nil && st.IsDir() {
		err := filepath.WalkDir(cipherDir, func(p string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !d.Type().IsRegular() {
				return nil
			}
			info, err := d.Info()
			if err != nil {
				return err
			}
			base := d.Name()
			if _, ok := books[base]; !ok {
				stats.orphanFiles++
			}
			rel, err := filepath.Rel(opts.DataDir, p)
			if err != nil {
				return err
			}
			files = append(files, archiveFile{Name: "data/" + filepath.ToSlash(rel), Path: p, Size: info.Size()})
			stats.cipherBytes += info.Size()
			return nil
		})
		if err != nil {
			return nil, stats, fmt.Errorf("backup: 遍历密文目录失败: %w", err)
		}
	}
	if !opts.OmitTTSCache {
		ttsDir := filepath.Join(opts.DataDir, "tts-cache")
		if st, err := os.Stat(ttsDir); err == nil && st.IsDir() {
			err := filepath.WalkDir(ttsDir, func(p string, d os.DirEntry, err error) error {
				if err != nil {
					return err
				}
				if d.IsDir() || !strings.HasSuffix(d.Name(), ".bin") || strings.Contains(d.Name(), ".tmp") {
					return nil
				}
				info, err := d.Info()
				if err != nil {
					return err
				}
				rel, err := filepath.Rel(opts.DataDir, p)
				if err != nil {
					return err
				}
				files = append(files, archiveFile{Name: "data/" + filepath.ToSlash(rel), Path: p, Size: info.Size()})
				stats.ttsBytes += info.Size()
				return nil
			})
			if err != nil {
				return nil, stats, fmt.Errorf("backup: 遍历音频缓存失败: %w", err)
			}
		}
	}
	clientDir := filepath.Join(opts.DataDir, "client-index")
	if st, err := os.Stat(clientDir); err == nil && st.IsDir() {
		err := filepath.WalkDir(clientDir, func(p string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !d.Type().IsRegular() {
				return nil
			}
			info, err := d.Info()
			if err != nil {
				return err
			}
			rel, err := filepath.Rel(opts.DataDir, p)
			if err != nil {
				return err
			}
			files = append(files, archiveFile{Name: "data/" + filepath.ToSlash(rel), Path: p, Size: info.Size()})
			stats.clientFiles++
			return nil
		})
		if err != nil {
			return nil, stats, fmt.Errorf("backup: 遍历客户端索引失败: %w", err)
		}
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Name < files[j].Name })
	return files, stats, nil
}

// hashFile 返回文件 SHA-256 与大小。
func hashFile(path string) (string, int64, error) {
	f, err := os.Open(path) // #nosec G304
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

// recordBackupRun 把备份结果写入运行库，供控制台展示。
// 失败不阻断备份本身（备份包已生成），只返回警告。
func recordBackupRun(dbPath string, res *CreateResult) []string {
	db, err := sql.Open("sqlite", "file:"+dbPath+"?_pragma=busy_timeout(5000)")
	if err != nil {
		return []string{"备份已生成，但写入备份记录失败"}
	}
	defer db.Close()
	_, err = db.Exec(`INSERT INTO backup_runs(path, checksum, size_bytes, books, segments, result, detail)
		VALUES (?,?,?,?,?, 'ok', ?)`, filepath.Base(res.Path), res.Checksum, res.SizeBytes,
		res.Manifest.Books, res.Manifest.Segments, fmt.Sprintf("duration_ms=%d", res.DurationMS))
	if err != nil {
		return []string{"备份已生成，但写入备份记录失败"}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	audit.New(db).Record(ctx, "system", "cli", "backup_created", "", "ok",
		fmt.Sprintf("sha256=%s size=%d books=%d", res.Checksum[:16], res.SizeBytes, res.Manifest.Books))
	return nil
}
