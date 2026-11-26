package books

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// ImportOptions 控制批量导入行为。
type ImportOptions struct {
	// Dir 是待扫描目录（必须是已存在的本地目录）。
	Dir string
	// Glob 为文件匹配模式，默认 *.txt（不区分大小写）。
	Glob string
	// Recursive 为 true 时递归子目录。
	Recursive bool
	// Username 记为书籍归属与审计主体（单用户场景通常为 admin）。
	Username string
	// 以下元数据会应用到本次导入的所有书籍（留空则不设置）。
	Author      string
	Tags        string
	Description string
	// DryRun 只做扫描与查重，不写任何密文。
	DryRun bool
}

// ImportItem 是单个文件的导入结果（只含文件元信息，不含正文）。
type ImportItem struct {
	File     string `json:"file"`
	Title    string `json:"title,omitempty"`
	Status   string `json:"status"` // ok / skipped / failed
	BookID   string `json:"book_id,omitempty"`
	SHA256   string `json:"sha256,omitempty"`
	Reason   string `json:"reason,omitempty"`
	SizeByte int64  `json:"size_bytes,omitempty"`
}

// ImportReport 汇总一次批量导入。
type ImportReport struct {
	StartedAt string       `json:"started_at"`
	Dir       string       `json:"dir"`
	Scanned   int          `json:"scanned"`
	Imported  int          `json:"imported"`
	Skipped   int          `json:"skipped"`
	Failed    int          `json:"failed"`
	DryRun    bool         `json:"dry_run"`
	Items     []ImportItem `json:"items"`
}

// ImportDirectory 扫描目录并按现有加密上传流程逐本导入。
// 幂等：以明文 SHA-256 去重，已存在（含软删除）的书籍跳过。
// 章节扫描复用 chapterscan 的流式实现，不重复解析。
func (svc *Service) ImportDirectory(ctx context.Context, opts ImportOptions) (*ImportReport, error) {
	if opts.Dir == "" {
		return nil, errors.New("导入目录不能为空")
	}
	st, err := os.Stat(opts.Dir)
	if err != nil {
		return nil, fmt.Errorf("导入目录不可用: %w", err)
	}
	if !st.IsDir() {
		return nil, errors.New("导入路径不是目录")
	}
	if opts.Username == "" {
		opts.Username = "admin"
	}
	glob := opts.Glob
	if glob == "" {
		glob = "*.txt"
	}
	files, err := collectImportFiles(opts.Dir, glob, opts.Recursive)
	if err != nil {
		return nil, err
	}
	report := &ImportReport{
		StartedAt: time.Now().UTC().Format(time.RFC3339),
		Dir:       opts.Dir,
		DryRun:    opts.DryRun,
		Items:     make([]ImportItem, 0, len(files)),
	}
	seen := make(map[string]struct{}, len(files))
	for _, path := range files {
		report.Scanned++
		item := svc.importOne(ctx, path, opts, seen)
		switch item.Status {
		case "ok":
			report.Imported++
		case "skipped":
			report.Skipped++
		default:
			report.Failed++
		}
		report.Items = append(report.Items, item)
	}
	return report, nil
}

// importOne 处理单个文件：先哈希查重，再走统一加密入库路径。
func (svc *Service) importOne(ctx context.Context, path string, opts ImportOptions, seen map[string]struct{}) ImportItem {
	base := filepath.Base(path)
	item := ImportItem{File: path}

	st, err := os.Stat(path)
	if err != nil {
		item.Status, item.Reason = "failed", "stat_failed"
		svc.auditImport(ctx, opts.Username, "", "failed", "stat_failed")
		return item
	}
	item.SizeByte = st.Size()
	if st.Size() > svc.MaxUpload {
		item.Status, item.Reason = "failed", "too_large"
		svc.auditImport(ctx, opts.Username, "", "failed", "too_large")
		return item
	}

	// 第一遍：流式计算明文 SHA-256（不驻留内容）。
	f, err := os.Open(path)
	if err != nil {
		item.Status, item.Reason = "failed", "open_failed"
		svc.auditImport(ctx, opts.Username, "", "failed", "open_failed")
		return item
	}
	h := sha256.New()
	_, copyErr := io.Copy(h, io.LimitReader(f, svc.MaxUpload+1))
	f.Close()
	if copyErr != nil {
		item.Status, item.Reason = "failed", "read_failed"
		svc.auditImport(ctx, opts.Username, "", "failed", "read_failed")
		return item
	}
	sum := hex.EncodeToString(h.Sum(nil))
	item.SHA256 = sum

	if _, dup := seen[sum]; dup {
		item.Status, item.Reason = "skipped", "duplicate_in_batch"
		svc.auditImport(ctx, opts.Username, "", "skipped", "duplicate_in_batch")
		return item
	}

	existingID, deleted, found, err := svc.Store.FindBySHA256(ctx, sum)
	if err != nil {
		item.Status, item.Reason = "failed", "dedupe_failed"
		svc.auditImport(ctx, opts.Username, "", "failed", "dedupe_failed")
		return item
	}
	if found {
		seen[sum] = struct{}{}
		item.Status = "skipped"
		item.BookID = existingID
		item.Reason = "duplicate"
		if deleted {
			item.Reason = "duplicate_deleted"
		}
		svc.auditImport(ctx, opts.Username, existingID, "skipped", item.Reason)
		return item
	}

	title := strings.TrimSuffix(base, filepath.Ext(base))
	title = strings.TrimSpace(title)
	if title == "" {
		title = base
	}
	item.Title = title
	if opts.DryRun {
		item.Status = "ok"
		item.Reason = "dry_run"
		return item
	}

	f, err = os.Open(path)
	if err != nil {
		item.Status, item.Reason = "failed", "open_failed"
		svc.auditImport(ctx, opts.Username, "", "failed", "open_failed")
		return item
	}
	defer f.Close()

	book, err := svc.storeEncrypted(ctx, f, uploadMeta{
		Title:       title,
		Author:      opts.Author,
		Tags:        opts.Tags,
		Description: opts.Description,
		OrigName:    base,
	}, opts.Username, "cli", "bulk_import")
	if err != nil {
		item.Status = "failed"
		item.Reason = importReason(err)
		// storeEncrypted 已写审计；这里只补一条可归因到文件的结果
		svc.auditImport(ctx, opts.Username, "", "failed", item.Reason)
		return item
	}
	item.Status = "ok"
	item.BookID = book.ID
	item.SHA256 = book.SHA256
	seen[sum] = struct{}{}
	svc.auditImport(ctx, opts.Username, book.ID, "ok", "imported")
	return item
}

func (svc *Service) auditImport(ctx context.Context, username, bookID, result, reason string) {
	svc.Audit.Record(ctx, username, "cli", "bulk_import", bookID, result, "reason="+reason)
}

func importReason(err error) string {
	switch {
	case errors.Is(err, errUploadTooLarge):
		return "too_large"
	case errors.Is(err, errUnsupportedType):
		return "unsupported_type"
	case errors.Is(err, ErrKeyVersion):
		return "kek_version_missing"
	default:
		return "store_failed"
	}
}

// collectImportFiles 返回排序后的待导入文件（扩展名不区分大小写）。
func collectImportFiles(dir, glob string, recursive bool) ([]string, error) {
	var out []string
	if !recursive {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return nil, err
		}
		for _, e := range entries {
			if e.IsDir() || !e.Type().IsRegular() {
				continue
			}
			if matchTxt(e.Name(), glob) {
				out = append(out, filepath.Join(dir, e.Name()))
			}
		}
	} else {
		err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				return nil
			}
			if d.Type().IsRegular() && matchTxt(d.Name(), glob) {
				out = append(out, path)
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	sort.Strings(out)
	return out, nil
}

// matchTxt 只接受 .txt（默认模式）或与自定义 glob 匹配的文件。
func matchTxt(name, glob string) bool {
	if glob == "" || glob == "*.txt" {
		return strings.EqualFold(filepath.Ext(name), ".txt")
	}
	ok, err := filepath.Match(glob, name)
	return err == nil && ok
}
