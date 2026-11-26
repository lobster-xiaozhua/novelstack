package books

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"novelstack/internal/chapterscan"
)

// ChapterRow 是章节目录的一行：只有标题与字数，不含正文。
type ChapterRow struct {
	Index int    `json:"index"`
	Title string `json:"title"`
	Chars int    `json:"chars"`
	// WordCount 与 Chars 同义（中文按字计），保留两个名字便于客户端对接。
	WordCount int    `json:"word_count"`
	ETag      string `json:"etag"`
}

// 单个客户端 ID 的格式约束（回执用，不含任何敏感信息）。
var clientIDPattern = regexp.MustCompile(`^[A-Za-z0-9_.\-]{1,64}$`)

// SaveChapters 覆盖写入某本书的章节目录（先删后插，单事务）。
func (s *Store) SaveChapters(ctx context.Context, bookID string, chapters []chapterscan.Chapter) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // 提交后为 no-op
	if _, err := tx.ExecContext(ctx, `DELETE FROM book_chapters WHERE book_id = ?`, bookID); err != nil {
		return err
	}
	stmt, err := tx.PrepareContext(ctx, `INSERT INTO book_chapters
		(book_id, chapter_index, title, chars, start_rune, end_rune) VALUES (?,?,?,?,?,?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, c := range chapters {
		if _, err := stmt.ExecContext(ctx, bookID, c.Index, c.Title, c.Chars, c.Start, c.End); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE books SET chapter_count = ? WHERE id = ?`, len(chapters), bookID); err != nil {
		return err
	}
	return tx.Commit()
}

// ListChapters 读取章节目录（不含正文）。
func (s *Store) ListChapters(ctx context.Context, bookID string) ([]chapterscan.Chapter, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT chapter_index, title, chars, start_rune, end_rune
		FROM book_chapters WHERE book_id = ? ORDER BY chapter_index`, bookID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []chapterscan.Chapter
	for rows.Next() {
		var c chapterscan.Chapter
		if err := rows.Scan(&c.Index, &c.Title, &c.Chars, &c.Start, &c.End); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// ChapterTitles 返回章节标题切片（供检索索引使用，只有标题）。
func (s *Store) ChapterTitles(ctx context.Context, bookID string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT title FROM book_chapters WHERE book_id = ? ORDER BY chapter_index`, bookID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var t string
		if err := rows.Scan(&t); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// SetIndexState 记录该书的检索索引状态（pending/indexed/error/disabled）。
func (s *Store) SetIndexState(ctx context.Context, bookID, state string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE books SET index_state = ? WHERE id = ?`, state, bookID)
	return err
}

// UpdateMetadata 更新可索引元数据；ownerID 非空时校验归属。返回是否命中。
func (s *Store) UpdateMetadata(ctx context.Context, bookID, ownerID string, m MetadataPatch) (bool, error) {
	sets := make([]string, 0, 4)
	args := make([]any, 0, 6)
	if m.Title != nil {
		sets = append(sets, "title = ?")
		args = append(args, *m.Title)
	}
	if m.Author != nil {
		sets = append(sets, "author = ?")
		args = append(args, *m.Author)
	}
	if m.Tags != nil {
		sets = append(sets, "tags = ?")
		args = append(args, strings.Join(*m.Tags, ","))
	}
	if m.Description != nil {
		sets = append(sets, "description = ?")
		args = append(args, *m.Description)
	}
	if len(sets) == 0 {
		return false, nil
	}
	sets = append(sets, "index_state = 'pending'",
		"sync_seq = (SELECT COALESCE(MAX(sync_seq),0)+1 FROM books)")
	args = append(args, bookID)
	q := `UPDATE books SET ` + strings.Join(sets, ", ") +
		` WHERE id = ? AND deleted_at IS NULL AND (? = '' OR owner_id = ?)`
	args = append(args, ownerID, ownerID)
	res, err := s.db.ExecContext(ctx, q, args...)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// MetadataPatch 是 PATCH 请求的元数据补丁（nil 表示不修改）。
type MetadataPatch struct {
	Title       *string   `json:"title,omitempty"`
	Author      *string   `json:"author,omitempty"`
	Tags        *[]string `json:"tags,omitempty"`
	Description *string   `json:"description,omitempty"`
}

// AckChapter 记录客户端已把某章写入本地索引（幂等）。
func (s *Store) AckChapter(ctx context.Context, bookID string, chapterIndex int, clientID, etag string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO chapter_acks (book_id, chapter_index, client_id, etag)
		VALUES (?,?,?,?)
		ON CONFLICT(book_id, chapter_index, client_id) DO UPDATE SET
			etag = excluded.etag,
			acked_at = strftime('%Y-%m-%dT%H:%M:%fZ','now')`,
		bookID, chapterIndex, clientID, etag)
	return err
}

// Coverage 返回某本书「已回执章节数 / 总章节数」。
func (s *Store) Coverage(ctx context.Context, bookID string) (acked, total int, err error) {
	if err = s.db.QueryRowContext(ctx,
		`SELECT COUNT(DISTINCT chapter_index) FROM chapter_acks WHERE book_id = ?`, bookID).Scan(&acked); err != nil {
		return 0, 0, err
	}
	if err = s.db.QueryRowContext(ctx,
		`SELECT chapter_count FROM books WHERE id = ?`, bookID).Scan(&total); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, 0, ErrNotFound
		}
		return 0, 0, err
	}
	return acked, total, nil
}

// CoverageRow 是覆盖度指标的一行。
type CoverageRow struct {
	BookID   string  `json:"book_id"`
	Title    string  `json:"title"`
	Indexed  int     `json:"indexed_chapters"`
	Total    int     `json:"total_chapters"`
	Coverage float64 `json:"coverage"`
}

// CoverageAll 返回全部未删除书籍的覆盖度。
func (s *Store) CoverageAll(ctx context.Context) ([]CoverageRow, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT b.id, b.title, b.chapter_count,
		COALESCE((SELECT COUNT(DISTINCT a.chapter_index) FROM chapter_acks a WHERE a.book_id = b.id), 0)
		FROM books b WHERE b.deleted_at IS NULL ORDER BY b.created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []CoverageRow{}
	for rows.Next() {
		var r CoverageRow
		if err := rows.Scan(&r.BookID, &r.Title, &r.Total, &r.Indexed); err != nil {
			return nil, err
		}
		if r.Total > 0 {
			r.Coverage = float64(r.Indexed) / float64(r.Total)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ChapterETag 由「书籍密文身份 + 章节偏移」导出，章节内容变化时必然变化。
// 书籍重新上传会得到新的 bookID 与新的 SHA-256，因此同一 bookID 下 ETag 稳定。
func ChapterETag(b *Book, c chapterscan.Chapter) string {
	h := sha256.New()
	fmt.Fprintf(h, "novelstack-chapter-v1|%s|%s|%d|%d|%d|%d",
		b.ID, b.SHA256, b.ChunkSize, c.Index, c.Start, c.End)
	return `"` + hex.EncodeToString(h.Sum(nil))[:32] + `"`
}

// matchETag 实现 If-None-Match 的强/弱比较（含 *）。
func matchETag(header, etag string) bool {
	if header == "" {
		return false
	}
	norm := func(s string) string {
		s = strings.TrimSpace(s)
		return strings.TrimPrefix(s, "W/")
	}
	target := norm(etag)
	for _, part := range strings.Split(header, ",") {
		part = strings.TrimSpace(part)
		if part == "*" {
			return true
		}
		if norm(part) == target {
			return true
		}
	}
	return false
}

// isJSONContentType 判断请求体是否为 application/json（忽略 charset 参数）。
func isJSONContentType(ct string) bool {
	if ct == "" {
		return false
	}
	base := ct
	if i := strings.IndexByte(ct, ';'); i >= 0 {
		base = ct[:i]
	}
	return strings.EqualFold(strings.TrimSpace(base), "application/json")
}

// Chapters 处理 GET /api/books/{id}/chapters：返回章节目录（不返回正文）。
func (svc *Service) Chapters(w http.ResponseWriter, r *http.Request, bookID, username, ip string) {
	ctx := r.Context()
	meta, err := svc.Store.GetOwned(ctx, bookID, username, false)
	if err != nil {
		st, msg := errStatus(err)
		svc.Audit.Record(ctx, username, ip, "book_chapters", bookID, "denied", "not found")
		writeErr(w, st, msg)
		return
	}
	rows, err := svc.Store.ListChapters(ctx, bookID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	// 目录缺失（例如旧数据）时解密一次补齐——只为标题，不缓存正文。
	if len(rows) == 0 {
		_, plain, lerr := svc.LoadPlaintextFor(ctx, bookID, username)
		if lerr != nil {
			svc.auditLoadError(ctx, bookID, username, ip, "book_chapters", lerr)
			st, msg := errStatus(lerr)
			writeErr(w, st, msg)
			return
		}
		rows = chapterscan.Split(string(plain))
		_ = svc.Store.SaveChapters(ctx, bookID, rows)
	}
	out := make([]ChapterRow, 0, len(rows))
	for _, c := range rows {
		out = append(out, ChapterRow{
			Index: c.Index, Title: c.Title, Chars: c.Chars, WordCount: c.Chars,
			ETag: ChapterETag(meta, c),
		})
	}
	svc.Audit.Record(ctx, username, ip, "book_chapters", bookID, "ok", fmt.Sprintf("chapters=%d", len(out)))
	writeJSON(w, http.StatusOK, map[string]any{
		"book_id":  bookID,
		"title":    meta.Title,
		"count":    len(out),
		"chapters": out,
	})
}

// ChapterPlain 处理 GET /api/books/{id}/chapters/{index}/plain：
// 解密并返回单章纯文本，支持 If-None-Match/ETag（客户端已缓存且未变更时 304）。
func (svc *Service) ChapterPlain(w http.ResponseWriter, r *http.Request, bookID string, index int, username, ip string) {
	ctx := r.Context()
	meta, plain, err := svc.LoadPlaintextFor(ctx, bookID, username)
	if err != nil {
		svc.auditLoadError(ctx, bookID, username, ip, "chapter_plain", err)
		st, msg := errStatus(err)
		writeErr(w, st, msg)
		return
	}
	chapters := chapterscan.Split(string(plain))
	if index < 0 || index >= len(chapters) {
		writeErr(w, http.StatusNotFound, "chapter not found")
		return
	}
	ch := chapters[index]
	etag := ChapterETag(meta, ch)
	w.Header().Set("ETag", etag)
	w.Header().Set("Cache-Control", "private, max-age=0, must-revalidate")
	if matchETag(r.Header.Get("If-None-Match"), etag) {
		svc.Audit.Record(ctx, username, ip, "chapter_plain_304", bookID, "ok", fmt.Sprintf("chapter=%d", index))
		w.WriteHeader(http.StatusNotModified)
		return
	}
	runes := []rune(string(plain))
	text := string(runes[ch.Start:ch.End])

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Chapter-Index", strconv.Itoa(ch.Index))
	w.Header().Set("X-Chapter-Chars", strconv.Itoa(ch.Chars))
	w.Header().Set("Content-Length", strconv.Itoa(len(text)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(text))
	svc.Audit.Record(ctx, username, ip, "chapter_plain", bookID, "ok",
		fmt.Sprintf("chapter=%d bytes=%d", index, len(text)))
}

// Ack 处理 POST /api/books/{id}/chapters/{index}/ack：客户端本地索引回执。
func (svc *Service) Ack(w http.ResponseWriter, r *http.Request, bookID string, index int, username, ip string) {
	ctx := r.Context()
	var req struct {
		ClientID string `json:"client_id"`
		ETag     string `json:"etag"`
	}
	if r.Body != nil {
		// 回执体很小；非法 JSON 视为空回执（不强制）
		_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10)).Decode(&req)
	}
	if req.ClientID == "" {
		req.ClientID = "default"
	}
	if !clientIDPattern.MatchString(req.ClientID) {
		writeErr(w, http.StatusBadRequest, "invalid client_id")
		return
	}
	if len(req.ETag) > 64 {
		req.ETag = ""
	}
	if _, err := svc.Store.GetOwned(ctx, bookID, username, false); err != nil {
		st, msg := errStatus(err)
		writeErr(w, st, msg)
		return
	}
	acked, total, err := svc.Store.Coverage(ctx, bookID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	if index < 0 || (total > 0 && index >= total) {
		writeErr(w, http.StatusNotFound, "chapter not found")
		return
	}
	if err := svc.Store.AckChapter(ctx, bookID, index, req.ClientID, req.ETag); err != nil {
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	svc.Audit.Record(ctx, username, ip, "chapter_ack", bookID, "ok",
		fmt.Sprintf("chapter=%d client=%s prev=%d/%d", index, req.ClientID, acked, total))
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":            true,
		"book_id":       bookID,
		"chapter_index": index,
		"acked":         true,
	})
}

// PatchMeta 处理 PATCH /api/books/{id}：只允许更新可索引元数据。
func (svc *Service) PatchMeta(w http.ResponseWriter, r *http.Request, bookID, username, ip string) {
	ctx := r.Context()
	if !isJSONContentType(r.Header.Get("Content-Type")) {
		writeErr(w, http.StatusUnsupportedMediaType, "application/json required")
		return
	}
	var patch MetadataPatch
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&patch); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json body")
		return
	}
	if patch.Title != nil {
		t := strings.TrimSpace(*patch.Title)
		if t == "" || len([]rune(t)) > 200 {
			writeErr(w, http.StatusBadRequest, "invalid title")
			return
		}
		patch.Title = &t
	}
	if patch.Author != nil {
		a := strings.TrimSpace(*patch.Author)
		if len([]rune(a)) > 100 {
			writeErr(w, http.StatusBadRequest, "author too long")
			return
		}
		patch.Author = &a
	}
	if patch.Tags != nil {
		joined, err := NormalizeTags(*patch.Tags)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "invalid tags")
			return
		}
		tags := SplitTags(joined)
		if tags == nil {
			tags = []string{}
		}
		patch.Tags = &tags
	}
	if patch.Description != nil {
		d := strings.TrimSpace(*patch.Description)
		if len([]rune(d)) > 2000 {
			writeErr(w, http.StatusBadRequest, "description too long")
			return
		}
		patch.Description = &d
	}
	ok, err := svc.Store.UpdateMetadata(ctx, bookID, username, patch)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	if !ok {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	svc.EnqueueIndex(bookID)
	svc.Audit.Record(ctx, username, ip, "book_meta_update", bookID, "ok", "metadata changed")
	meta, err := svc.Store.GetOwned(ctx, bookID, username, false)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, meta)
}
