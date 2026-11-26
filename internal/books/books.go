// Package books 实现书籍加密上传 / 元数据 / 解密下载 / 软删除。
package books

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"novelstack/internal/audit"
	"novelstack/internal/chapterscan"
	"novelstack/internal/cryptobox"
)

// Store 封装 books 表操作。
type Store struct {
	db *sql.DB
}

func NewStore(db *sql.DB) *Store { return &Store{db: db} }

// Book 是书籍元数据（不含任何内容）。
// Author / Tags / Description / ChapterCount 是第三阶段允许进入检索索引的字段，
// 正文永远不会出现在这里。
type Book struct {
	ID           string   `json:"id"`
	Title        string   `json:"title"`
	Author       string   `json:"author,omitempty"`
	Tags         []string `json:"tags,omitempty"`
	Description  string   `json:"description,omitempty"`
	OrigName     string   `json:"orig_filename"`
	SizeBytes    int64    `json:"size_bytes"`
	SHA256       string   `json:"sha256"`
	KEKVer       int      `json:"kek_version"`
	ChunkSize    int      `json:"chunk_size"`
	ChapterCount int      `json:"chapter_count"`
	IndexState   string   `json:"index_state"`
	CreatedAt    string   `json:"created_at"`
	DeletedAt    *string  `json:"deleted_at,omitempty"`
	// OwnerID 是上传者用户名；单用户阶段仍用于“不可仅凭 bookID 越权”的校验。
	OwnerID string `json:"-"`
}

// Book 查询列（get / list 共用，顺序与 scanBook 一致）。
const bookColumns = `id, title, author, tags, description, orig_filename, size_bytes, sha256,
	kek_version, chunk_size, chapter_count, index_state, created_at, deleted_at, owner_id`

// scanBook 按 bookColumns 顺序扫描一行。
func scanBook(sc interface{ Scan(...any) error }) (*Book, error) {
	var b Book
	var tags string
	if err := sc.Scan(&b.ID, &b.Title, &b.Author, &tags, &b.Description, &b.OrigName,
		&b.SizeBytes, &b.SHA256, &b.KEKVer, &b.ChunkSize, &b.ChapterCount,
		&b.IndexState, &b.CreatedAt, &b.DeletedAt, &b.OwnerID); err != nil {
		return nil, err
	}
	b.Tags = SplitTags(tags)
	return &b, nil
}

// SplitTags 把逗号分隔的标签串转成数组（去空、去重、保序）。
func SplitTags(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	seen := make(map[string]bool, 8)
	out := make([]string, 0, 8)
	for _, t := range strings.Split(s, ",") {
		t = strings.TrimSpace(t)
		if t == "" || seen[t] {
			continue
		}
		seen[t] = true
		out = append(out, t)
	}
	return out
}

// NormalizeTags 校验并规范化标签：单个标签 ≤24 字符、最多 10 个、不含逗号。
func NormalizeTags(tags []string) (string, error) {
	out := make([]string, 0, len(tags))
	seen := make(map[string]bool, len(tags))
	for _, t := range tags {
		t = strings.TrimSpace(strings.ReplaceAll(t, ",", " "))
		t = strings.Join(strings.Fields(t), " ")
		if t == "" || seen[t] {
			continue
		}
		if len([]rune(t)) > 24 {
			return "", errors.New("tag too long")
		}
		seen[t] = true
		out = append(out, t)
		if len(out) > 10 {
			return "", errors.New("too many tags")
		}
	}
	return strings.Join(out, ","), nil
}

// ErrNotFound 表示书籍不存在或已删除。
var ErrNotFound = errors.New("book not found")

const maxTitleBytes = 512

// Get 返回指定书籍元数据；includeDeleted 控制软删除书籍是否可见。
func (s *Store) Get(ctx context.Context, id string, includeDeleted bool) (*Book, error) {
	return s.get(ctx, id, "", includeDeleted)
}

// GetOwned 在 Get 基础上校验归属：ownerID 非空时要求 books.owner_id 匹配。
// 单用户阶段用于保持“不得仅凭 bookID 越权”的校验习惯；多用户阶段可直接复用。
func (s *Store) GetOwned(ctx context.Context, id, ownerID string, includeDeleted bool) (*Book, error) {
	if ownerID == "" {
		return nil, ErrNotFound
	}
	return s.get(ctx, id, ownerID, includeDeleted)
}

func (s *Store) get(ctx context.Context, id, ownerID string, includeDeleted bool) (*Book, error) {
	q := `SELECT ` + bookColumns + ` FROM books WHERE id = ?`
	args := []any{id}
	if !includeDeleted {
		q += ` AND deleted_at IS NULL`
	}
	if ownerID != "" {
		q += ` AND owner_id = ?`
		args = append(args, ownerID)
	}
	b, err := scanBook(s.db.QueryRowContext(ctx, q, args...))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return b, nil
}

// List 返回未删除书籍元数据（按归属过滤，ownerID 为空时不过滤）。
func (s *Store) List(ctx context.Context, ownerID string) ([]Book, error) {
	q := `SELECT ` + bookColumns + ` FROM books WHERE deleted_at IS NULL`
	args := []any{}
	if ownerID != "" {
		q += ` AND owner_id = ?`
		args = append(args, ownerID)
	}
	q += ` ORDER BY created_at DESC`
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Book
	for rows.Next() {
		b, err := scanBook(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *b)
	}
	return out, rows.Err()
}

// SoftDelete 软删除书籍，返回是否命中。ownerID 非空时同时校验归属。
func (s *Store) SoftDelete(ctx context.Context, id, ownerID string) (bool, error) {
	res, err := s.db.ExecContext(ctx,
		`UPDATE books SET deleted_at = strftime('%Y-%m-%dT%H:%M:%fZ','now'),
			sync_seq = (SELECT COALESCE(MAX(sync_seq),0)+1 FROM books)
		 WHERE id = ? AND deleted_at IS NULL AND (? = '' OR owner_id = ?)`, id, ownerID, ownerID)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// Insert 写入新书元数据。
func (s *Store) Insert(ctx context.Context, b *Book, wrappedDEK, dekNonce []byte) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO books
		(id, title, author, tags, description, orig_filename, size_bytes, sha256,
		 wrapped_dek, dek_nonce, kek_version, format_version, chunk_size, storage_path, owner_id, sync_seq)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,(SELECT COALESCE(MAX(sync_seq),0)+1 FROM books))`,
		b.ID, b.Title, b.Author, strings.Join(b.Tags, ","), b.Description, b.OrigName, b.SizeBytes, b.SHA256,
		wrappedDEK, dekNonce, b.KEKVer, cryptobox.FormatVersion, b.ChunkSize,
		filepath.Join("ciphertext", b.ID), b.OwnerID)
	return err
}

// LoadDEK 读取并解包指定书籍的 DEK（仅内存短暂存在）。
func (s *Store) LoadDEK(ctx context.Context, kek []byte, id string) ([]byte, error) {
	wrapped, nonce, _, err := s.LoadWrappedDEK(ctx, id)
	if err != nil {
		return nil, err
	}
	return cryptobox.UnwrapDEK(kek, wrapped, nonce)
}

// LoadWrappedDEK 返回包装后的 DEK、nonce 与其 KEK 版本。
// 轮换与多版本读取都基于这里的 kek_version 选择正确 KEK。
func (s *Store) LoadWrappedDEK(ctx context.Context, id string) (wrapped, nonce []byte, version int, err error) {
	err = s.db.QueryRowContext(ctx,
		`SELECT wrapped_dek, dek_nonce, kek_version FROM books WHERE id = ? AND deleted_at IS NULL`, id).
		Scan(&wrapped, &nonce, &version)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, 0, ErrNotFound
	}
	if err != nil {
		return nil, nil, 0, err
	}
	return wrapped, nonce, version, nil
}

// FindBySHA256 按明文 SHA-256 查重（含软删除，用于批量导入幂等）。
// 返回 (bookID, deleted, found)。
func (s *Store) FindBySHA256(ctx context.Context, sha string) (string, bool, bool, error) {
	var id string
	var deleted sql.NullString
	err := s.db.QueryRowContext(ctx,
		`SELECT id, deleted_at FROM books WHERE sha256 = ? ORDER BY created_at LIMIT 1`, sha).
		Scan(&id, &deleted)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, false, nil
	}
	if err != nil {
		return "", false, false, err
	}
	return id, deleted.Valid, true, nil
}

// Service 承载书籍相关 HTTP handler。
type Service struct {
	Store         *Store
	KEK           []byte
	CiphertextDir string
	ChunkSize     int
	MaxUpload     int64
	Audit         *audit.Logger
	// MaxChapters 限制单本书记住的章节标题数（防止异常文件耗尽内存）
	MaxChapters int
	// Index 是元数据索引的入队钩子（可为 nil，表示不启用服务端检索）
	Index IndexHook
	// KEKVersion 是当前写入使用的 KEK 版本（默认 1）。
	KEKVersion int
	// OldKEKs 保存旧版本 KEK（只读），用于轮换窗口期读取尚未重包装的记录。
	// 键为版本号；不得包含当前版本（当前版本以 KEK 字段为准）。
	OldKEKs map[int][]byte
}

// currentKEKVersion 返回写入使用的版本（缺省为 1）。
func (svc *Service) currentKEKVersion() int {
	if svc.KEKVersion > 0 {
		return svc.KEKVersion
	}
	return 1
}

// kekFor 按版本取 KEK；版本不存在时返回 ErrKeyVersion。
func (svc *Service) kekFor(version int) ([]byte, error) {
	if version <= 0 {
		version = 1
	}
	if version == svc.currentKEKVersion() && len(svc.KEK) == cryptobox.KeySize {
		return svc.KEK, nil
	}
	if k, ok := svc.OldKEKs[version]; ok && len(k) == cryptobox.KeySize {
		return k, nil
	}
	return nil, ErrKeyVersion
}

// ErrKeyVersion 表示本进程没有该版本的 KEK（轮换窗口期配置缺失）。
var ErrKeyVersion = errors.New("kek version not loaded")

// IndexHook 由检索服务实现：书籍元数据变化时异步同步索引。
type IndexHook interface {
	Enqueue(bookID string)
	Remove(bookID string)
}

// EnqueueIndex 在配置了索引钩子时投递一次元数据同步。
func (svc *Service) EnqueueIndex(bookID string) {
	if svc.Index != nil {
		svc.Index.Enqueue(bookID)
	}
}

// errorsForHTTP 把领域错误映射为 (status, 对外消息)。
func errStatus(err error) (int, string) {
	switch {
	case errors.Is(err, ErrNotFound):
		return http.StatusNotFound, "not found"
	case errors.Is(err, cryptobox.ErrTampered):
		return http.StatusConflict, "content integrity check failed"
	case errors.Is(err, cryptobox.ErrBadFormat):
		return http.StatusUnprocessableEntity, "bad stored format"
	case errors.Is(err, ErrKeyVersion):
		return http.StatusServiceUnavailable, "key version not loaded"
	default:
		return http.StatusInternalServerError, "internal error"
	}
}

// Upload 处理 POST /api/books：multipart/form-data，字段 title 与 file（仅 .txt）。
// 流式边读边加密，明文不落盘；密文直接写入最终随机文件名。
func (svc *Service) Upload(w http.ResponseWriter, r *http.Request, username, ip string) {
	ctx := r.Context()
	mr, err := r.MultipartReader()
	if err != nil {
		auditFail(svc.Audit, ctx, username, ip, "book_upload", "multipart 解析失败")
		writeErr(w, http.StatusBadRequest, "invalid multipart body")
		return
	}

	var title, author, tags, description, origName string
	uploaded := false // 是否已流式处理完 file part
	for {
		p, perr := mr.NextPart()
		if perr != nil {
			if errors.Is(perr, io.EOF) {
				break
			}
			// 超过 MaxBytesReader 上限时在这里表现为读取错误。
			if isTooLarge(perr) {
				auditFail(svc.Audit, ctx, username, ip, "book_upload", "上传超过大小上限")
				writeErr(w, http.StatusRequestEntityTooLarge, "upload too large")
				return
			}
			auditFail(svc.Audit, ctx, username, ip, "book_upload", "multipart 读取失败")
			writeErr(w, http.StatusBadRequest, "invalid multipart body")
			return
		}
		switch p.FormName() {
		case "title":
			tb, err := io.ReadAll(io.LimitReader(p, maxTitleBytes+1))
			if err != nil {
				writeErr(w, http.StatusBadRequest, "invalid title")
				return
			}
			if len(tb) > maxTitleBytes {
				writeErr(w, http.StatusBadRequest, "title too long")
				return
			}
			title = strings.TrimSpace(string(tb))
		case "author":
			author = strings.TrimSpace(string(readField(p, 256)))
		case "tags":
			tags = strings.Join(SplitTags(string(readField(p, 512))), ",")
		case "description":
			description = strings.TrimSpace(string(readField(p, 4096)))
		case "file":
			// multipart 流只能顺序消费：file part 的数据必须在遍历到它的当下
			// 流式读完并加密，不能等循环结束再读。因此要求 title 字段位于
			// file 字段之前（curl -F 与常见客户端均满足）。
			origName = filepath.Base(p.FileName()) // 仅用于展示，绝不参与磁盘路径
			if title == "" {
				writeErr(w, http.StatusBadRequest, "title field must precede file field")
				return
			}
			meta := uploadMeta{Title: title, Author: author, Tags: tags, Description: description, OrigName: origName}
			if uerr := svc.encryptPart(w, r, p, username, ip, meta); uerr != nil {
				return // encryptPart 已写出响应并写审计
			}
			uploaded = true
		default:
			// 未知字段：丢弃内容，继续。
			_, _ = io.Copy(io.Discard, io.LimitReader(p, 1<<20))
		}
		p.Close()
	}

	if !uploaded {
		writeErr(w, http.StatusBadRequest, "title and file are required")
		return
	}
}

// uploadMeta 是上传时随文件一起提交的元数据（全部为可索引字段，不含正文）。
type uploadMeta struct {
	Title       string
	Author      string
	Tags        string
	Description string
	OrigName    string
}

// readField 读取一个普通表单字段（有长度上限，超限截断）。
func readField(p io.Reader, max int64) []byte {
	b, err := io.ReadAll(io.LimitReader(p, max+1))
	if err != nil || int64(len(b)) > max {
		return nil
	}
	return b
}

// 上传/导入错误分类：调用方据此映射 HTTP 状态或导入报告，不向客户端泄漏内部细节。
var (
	errUnsupportedType = errors.New("unsupported file type")
	errUploadTooLarge  = errors.New("upload too large")
)

// encryptPart 流式加密一个 file part 并入库；出错时写响应与审计，返回非 nil。
func (svc *Service) encryptPart(w http.ResponseWriter, r *http.Request, p io.Reader, username, ip string, meta uploadMeta) error {
	ctx := r.Context()
	if !strings.EqualFold(filepath.Ext(meta.OrigName), ".txt") {
		auditFail(svc.Audit, ctx, username, ip, "book_upload", "不支持的文件类型")
		writeErr(w, http.StatusUnsupportedMediaType, "only .txt accepted")
		return errUnsupportedType
	}
	book, err := svc.storeEncrypted(ctx, p, meta, username, ip, "book_upload")
	if err != nil {
		switch {
		case errors.Is(err, errUploadTooLarge):
			writeErr(w, http.StatusRequestEntityTooLarge, "upload too large")
		default:
			writeErr(w, http.StatusInternalServerError, "internal error")
		}
		return err
	}
	writeJSON(w, http.StatusCreated, book)
	return nil
}

// storeEncrypted 是上传与批量导入共用的唯一「明文 → 密文 → 元数据」路径。
// 流式加密，明文不落盘；失败时清理半成品密文，并将原因写入审计（action 区分来源）。
func (svc *Service) storeEncrypted(ctx context.Context, p io.Reader, meta uploadMeta, username, ip, action string) (*Book, error) {
	title, origName := meta.Title, meta.OrigName
	id, err := cryptobox.NewBookID()
	if err != nil {
		auditFail(svc.Audit, ctx, username, ip, action, "生成书籍 ID 失败")
		return nil, err
	}
	dek, err := cryptobox.NewDEK()
	if err != nil {
		auditFail(svc.Audit, ctx, username, ip, action, "生成 DEK 失败")
		return nil, err
	}
	if err := os.MkdirAll(svc.CiphertextDir, 0o700); err != nil {
		auditFail(svc.Audit, ctx, username, ip, action, "创建密文目录失败")
		return nil, err
	}

	path := filepath.Join(svc.CiphertextDir, id) // 随机 ID，不用原始文件名
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		auditFail(svc.Audit, ctx, username, ip, action, "创建密文文件失败")
		return nil, err
	}
	success := false
	defer func() {
		if !success {
			f.Close()
			os.Remove(path) // 失败时清理半成品密文
		}
	}()

	// 边加密边扫描章节标题：明文只经过管道，不额外驻留
	scanner := chapterscan.New(svc.MaxChapters)
	sum, size, encErr := encryptCounting(io.TeeReader(p, scanner), f, dek, id, svc.ChunkSize)
	closeErr := f.Close()
	if encErr != nil || closeErr != nil {
		if isTooLarge(encErr) || isTooLarge(closeErr) {
			auditFail(svc.Audit, ctx, username, ip, action, "上传超过大小上限")
			return nil, errUploadTooLarge
		}
		auditFail(svc.Audit, ctx, username, ip, action, "加密写入失败")
		return nil, fmt.Errorf("encrypt: %w", encErr)
	}
	if size > svc.MaxUpload {
		auditFail(svc.Audit, ctx, username, ip, action, "文件超过大小上限")
		return nil, errUploadTooLarge
	}

	chapters := scanner.Close()
	book := &Book{
		ID:           id,
		Title:        title,
		Author:       meta.Author,
		Tags:         SplitTags(meta.Tags),
		Description:  meta.Description,
		OrigName:     origName,
		SizeBytes:    size,
		SHA256:       hex.EncodeToString(sum),
		KEKVer:       svc.currentKEKVersion(),
		ChunkSize:    svc.ChunkSize,
		ChapterCount: len(chapters),
		IndexState:   "pending",
		OwnerID:      username,
	}
	kek, err := svc.kekFor(book.KEKVer)
	if err != nil {
		auditFail(svc.Audit, ctx, username, ip, action, "当前 KEK 未加载")
		return nil, err
	}
	wrapped, nonce, err := cryptobox.WrapDEK(kek, dek)
	if err != nil {
		auditFail(svc.Audit, ctx, username, ip, action, "包装 DEK 失败")
		return nil, err
	}
	if err := svc.Store.Insert(ctx, book, wrapped, nonce); err != nil {
		auditFail(svc.Audit, ctx, username, ip, action, "写入元数据失败")
		return nil, err
	}
	// 目录只存标题与字数；失败不影响书籍可用性（可按需重建）
	if err := svc.Store.SaveChapters(ctx, id, chapters); err != nil {
		svc.Audit.Record(ctx, username, ip, "book_chapters_save_error", id, "error", "chapter index failed")
	}
	success = true
	svc.EnqueueIndex(id)
	svc.Audit.Record(ctx, username, ip, action, id, "ok",
		fmt.Sprintf("size=%d chapters=%d title=%q", size, len(chapters), title))
	return book, nil
}

// encryptCounting 包装 EncryptStream 并统计明文字节数。
func encryptCounting(r io.Reader, w io.Writer, dek []byte, bookID string, chunkSize int) (sum []byte, size int64, err error) {
	cr := &countingReader{r: r}
	sum, err = cryptobox.EncryptStream(cr, w, dek, bookID, chunkSize)
	return sum, cr.n, err
}

type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

// isTooLarge 判断错误是否来自 http.MaxBytesReader 的超限。
func isTooLarge(err error) bool {
	if err == nil {
		return false
	}
	var mbe *http.MaxBytesError
	if errors.As(err, &mbe) {
		return true
	}
	return strings.Contains(err.Error(), "request body too large")
}

// List 处理 GET /api/books。
func (svc *Service) List(w http.ResponseWriter, r *http.Request, username, ip string) {
	list, err := svc.Store.List(r.Context(), username)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	if list == nil {
		list = []Book{}
	}
	svc.Audit.Record(r.Context(), username, ip, "book_list", "", "ok", fmt.Sprintf("count=%d", len(list)))
	writeJSON(w, http.StatusOK, list)
}

// GetMeta 处理 GET /api/books/{id}。
func (svc *Service) GetMeta(w http.ResponseWriter, r *http.Request, id, username, ip string) {
	b, err := svc.Store.GetOwned(r.Context(), id, username, false)
	if err != nil {
		st, msg := errStatus(err)
		svc.Audit.Record(r.Context(), username, ip, "book_get", id, "denied", "not found")
		writeErr(w, st, msg)
		return
	}
	svc.Audit.Record(r.Context(), username, ip, "book_get", id, "ok", "")
	writeJSON(w, http.StatusOK, b)
}

// Content 处理 GET /api/books/{id}/content：解密后返回明文。
// 明文只在内存与响应流中出现，不写临时文件；校验失败返回 409/422 并写审计。
func (svc *Service) Content(w http.ResponseWriter, r *http.Request, id, username, ip string) {
	ctx := r.Context()
	meta, body, err := svc.LoadPlaintextFor(ctx, id, username)
	if err != nil {
		svc.auditLoadError(ctx, id, username, ip, "book_download", err)
		st, msg := errStatus(err)
		writeErr(w, st, msg)
		return
	}

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Length", fmt.Sprintf("%d", len(body)))
	w.Header().Set("Content-Disposition", fmt.Sprintf("inline; filename=%q", sanitizeHeaderName(meta.OrigName, id)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
	svc.Audit.Record(ctx, username, ip, "book_download", id, "ok", fmt.Sprintf("size=%d", len(body)))
}

// LoadPlaintext 解密整本书并做完整性校验（GCM tag + 长度 + SHA-256）。
// 返回的明文仅驻留内存；调用方不得落盘或写日志。
// 朗读等上层功能复用此方法，保证“从密文到明文”只有一条路径。
func (svc *Service) LoadPlaintext(ctx context.Context, id string) (*Book, []byte, error) {
	return svc.loadPlaintext(ctx, id, "")
}

// LoadPlaintextFor 在 LoadPlaintext 基础上校验书籍归属。
func (svc *Service) LoadPlaintextFor(ctx context.Context, id, ownerID string) (*Book, []byte, error) {
	return svc.loadPlaintext(ctx, id, ownerID)
}

func (svc *Service) loadPlaintext(ctx context.Context, id, ownerID string) (*Book, []byte, error) {
	var meta *Book
	var err error
	if ownerID == "" {
		meta, err = svc.Store.Get(ctx, id, false)
	} else {
		meta, err = svc.Store.GetOwned(ctx, id, ownerID, false)
	}
	if err != nil {
		return nil, nil, err
	}
	dek, err := svc.unwrapDEK(ctx, id)
	if err != nil {
		if errors.Is(err, cryptobox.ErrTampered) || errors.Is(err, ErrKeyVersion) {
			return nil, nil, err
		}
		return nil, nil, fmt.Errorf("%w: dek", ErrNotFound)
	}
	f, err := os.Open(filepath.Join(svc.CiphertextDir, id))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil, ErrNotFound
		}
		return nil, nil, err
	}
	defer f.Close()

	var buf strings.Builder
	if meta.SizeBytes > 0 {
		buf.Grow(int(meta.SizeBytes))
	}
	sum, size, err := cryptobox.DecryptStream(f, &buf, dek, id, meta.ChunkSize, meta.SizeBytes)
	if err != nil {
		return nil, nil, err
	}
	if size != meta.SizeBytes || hex.EncodeToString(sum) != meta.SHA256 {
		return nil, nil, cryptobox.ErrTampered
	}
	return meta, []byte(buf.String()), nil
}

// unwrapDEK 读取记录并用其 kek_version 对应的 KEK 解包 DEK。
// 轮换窗口期内旧记录仍可用旧 KEK 读取；缺少对应版本时返回 ErrKeyVersion。
func (svc *Service) unwrapDEK(ctx context.Context, id string) ([]byte, error) {
	wrapped, nonce, version, err := svc.Store.LoadWrappedDEK(ctx, id)
	if err != nil {
		return nil, err
	}
	kek, err := svc.kekFor(version)
	if err != nil {
		return nil, err
	}
	return cryptobox.UnwrapDEK(kek, wrapped, nonce)
}

// auditLoadError 为读取失败写审计（区分篡改与一般错误）。
func (svc *Service) auditLoadError(ctx context.Context, id, username, ip, action string, err error) {
	switch {
	case errors.Is(err, cryptobox.ErrTampered):
		svc.Audit.Record(ctx, username, ip, action+"_tamper", id, "denied", "integrity check failed")
	case errors.Is(err, ErrNotFound):
		svc.Audit.Record(ctx, username, ip, action, id, "denied", "not found")
	default:
		svc.Audit.Record(ctx, username, ip, action, id, "error", "read failed")
	}
}

// sanitizeHeaderName 生成安全的下载文件名（无路径成分、无引号）。
func sanitizeHeaderName(orig, id string) string {
	n := filepath.Base(orig)
	n = strings.Map(func(r rune) rune {
		if r == '"' || r == '\\' || r < 0x20 || r > 0x7e {
			return -1
		}
		return r
	}, n)
	if n == "" || n == "." || n == "/" {
		return id + ".txt"
	}
	return n
}

// Delete 处理 DELETE /api/books/{id}：软删除。
func (svc *Service) Delete(w http.ResponseWriter, r *http.Request, id, username, ip string) {
	ok, err := svc.Store.SoftDelete(r.Context(), id, username)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	if !ok {
		svc.Audit.Record(r.Context(), username, ip, "book_delete", id, "denied", "not found")
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	if svc.Index != nil {
		svc.Index.Remove(id)
	}
	svc.Audit.Record(r.Context(), username, ip, "book_delete", id, "ok", "")
	w.WriteHeader(http.StatusNoContent)
}

// writerFunc 把 func([]byte) (int, error) 风格的写入适配为 io.Writer。
type writerFunc func(p []byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }

// writeJSON 输出 JSON 响应。
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

// writeErr 输出模糊化错误信息（不泄漏内部路径、SQL、密钥）。
func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// auditFail 记录一次失败操作。
func auditFail(l *audit.Logger, ctx context.Context, username, ip, action, detail string) {
	l.Record(ctx, username, ip, action, "", "denied", detail)
}
