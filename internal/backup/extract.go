package backup

import (
	"compress/gzip"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// ExtractOptions 是解包参数。
type ExtractOptions struct {
	PackagePath string
	Passphrase  []byte
	// DestDir 必须是空目录或不存在（演练时使用临时目录）。
	DestDir string
	// ExpectedSHA256 非空时先校验整包校验和；为空则尝试读取 .sha256 伴生文件。
	ExpectedSHA256 string
}

// ExtractResult 描述解包与校验结果。
type ExtractResult struct {
	Manifest   *Manifest `json:"manifest"`
	Checksum   string    `json:"sha256"`
	SizeBytes  int64     `json:"size_bytes"`
	Files      int       `json:"files"`
	ChecksumOK bool      `json:"checksum_ok"`
	Verified   bool      `json:"verified"`
	ExtractDir string    `json:"-"`
}

// Extract 校验、解密、解包并验证备份内容；任何一步失败都不会留下部分恢复文件。
func Extract(ctx context.Context, opts ExtractOptions) (out *ExtractResult, err error) {
	if opts.PackagePath == "" || opts.DestDir == "" {
		return nil, errors.New("backup: 包路径与目标目录不能为空")
	}
	if len(opts.Passphrase) == 0 {
		return nil, errors.New("backup: 必须提供备份口令")
	}
	// 失败时清理已解包内容，保证“不产生部分恢复”。
	cleanupOnError := func() {
		if err == nil {
			return
		}
		entries, rerr := os.ReadDir(opts.DestDir)
		if rerr != nil {
			return
		}
		for _, e := range entries {
			_ = os.RemoveAll(filepath.Join(opts.DestDir, e.Name()))
		}
	}
	defer cleanupOnError()
	expected := strings.TrimSpace(opts.ExpectedSHA256)
	if expected == "" {
		expected = ReadSidecarChecksum(opts.PackagePath)
	}
	sum, size, err := hashFile(opts.PackagePath)
	if err != nil {
		return nil, fmt.Errorf("backup: 读取备份包失败: %w", err)
	}
	if expected != "" && !strings.EqualFold(expected, sum) {
		return nil, fmt.Errorf("backup: 备份包校验和不匹配（期望 %s，实际 %s）", short(expected), short(sum))
	}

	if err := os.MkdirAll(opts.DestDir, 0o700); err != nil {
		return nil, fmt.Errorf("backup: 创建解包目录失败: %w", err)
	}
	if entries, err := os.ReadDir(opts.DestDir); err == nil && len(entries) > 0 {
		return nil, errors.New("backup: 解包目录必须为空")
	}

	f, err := os.Open(opts.PackagePath) // #nosec G304
	if err != nil {
		return nil, err
	}
	defer f.Close()
	reader, header, err := NewReader(f, opts.Passphrase)
	if err != nil {
		return nil, err
	}
	var src io.Reader = reader
	var gz *gzip.Reader
	if header.Compression == CompressionGzip {
		gz, err = gzip.NewReader(reader)
		if err != nil {
			return nil, fmt.Errorf("%w: gzip 头解析失败", ErrDecrypt)
		}
		src = gz
	}
	files, _, err := extractTar(src, opts.DestDir, defaultExtractLimits)
	if gz != nil {
		gz.Close()
	}
	if err != nil {
		if errors.Is(err, ErrDecrypt) {
			return nil, ErrDecrypt
		}
		return nil, err
	}

	man, err := ReadManifest(opts.DestDir)
	if err != nil {
		return nil, err
	}
	if err := verifyExtracted(ctx, opts.DestDir, man); err != nil {
		return nil, err
	}
	return &ExtractResult{
		Manifest:   man,
		Checksum:   sum,
		SizeBytes:  size,
		Files:      files,
		ChecksumOK: true,
		Verified:   true,
		ExtractDir: opts.DestDir,
	}, nil
}

// verifyExtracted 交叉校验清单与解包内容，确保恢复后可直接运行。
func verifyExtracted(ctx context.Context, dir string, man *Manifest) error {
	dbPath := filepath.Join(dir, "data", "app.db")
	dbSum, dbSize, err := hashFile(dbPath)
	if err != nil {
		return fmt.Errorf("backup: 备份包缺少数据库快照: %w", err)
	}
	if man.DBSHA256 != "" && !strings.EqualFold(man.DBSHA256, dbSum) {
		return fmt.Errorf("backup: 数据库快照校验和不匹配")
	}
	_ = dbSize
	db, err := sql.Open("sqlite", "file:"+dbPath+"?mode=ro&_pragma=busy_timeout(5000)")
	if err != nil {
		return err
	}
	defer db.Close()
	var check string
	if err := db.QueryRowContext(ctx, `PRAGMA integrity_check`).Scan(&check); err != nil {
		return fmt.Errorf("backup: 数据库完整性检查失败: %w", err)
	}
	if check != "ok" {
		return fmt.Errorf("backup: 数据库完整性检查未通过: %s", check)
	}
	var books int
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM books WHERE deleted_at IS NULL`).Scan(&books); err != nil {
		return fmt.Errorf("backup: 统计书籍失败: %w", err)
	}
	if books != man.Books {
		return fmt.Errorf("backup: 清单书籍数 %d 与数据库 %d 不一致", man.Books, books)
	}
	for _, b := range man.BookList {
		p := filepath.Join(dir, "data", "ciphertext", b.ID)
		st, err := os.Stat(p)
		if err != nil || !st.Mode().IsRegular() || st.Size() == 0 {
			return fmt.Errorf("%w: 书籍 %s 的密文缺失", ErrMissingCiphertext, b.ID)
		}
	}
	return nil
}

// ReadSidecarChecksum 读取 <package>.sha256，返回第一段十六进制串；不存在返回空。
func ReadSidecarChecksum(pkg string) string {
	raw, err := os.ReadFile(pkg + ".sha256") // #nosec G304
	if err != nil {
		return ""
	}
	fields := strings.Fields(string(raw))
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}

func short(s string) string {
	if len(s) > 16 {
		return s[:16]
	}
	return s
}
