package mobile

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"novelstack/internal/books"
	"novelstack/internal/chapterscan"
)

// ChapterRef 是同步清单里的章节条目：只有定位信息与 ETag，不含正文。
type ChapterRef struct {
	Index int    `json:"index"`
	Title string `json:"title"`
	Chars int    `json:"chars"`
	ETag  string `json:"etag"`
}

// BookChange 是一本书的同步条目。
type BookChange struct {
	ID           string       `json:"id"`
	Title        string       `json:"title"`
	Author       string       `json:"author,omitempty"`
	Tags         []string     `json:"tags,omitempty"`
	Description  string       `json:"description,omitempty"`
	ChapterCount int          `json:"chapter_count"`
	SizeBytes    int64        `json:"size_bytes"`
	SHA256       string       `json:"sha256"`
	ChunkSize    int          `json:"chunk_size"`
	KEKVersion   int          `json:"kek_version"`
	CreatedAt    string       `json:"created_at"`
	SyncSeq      int64        `json:"sync_seq"`
	Chapters     []ChapterRef `json:"chapters"`
	// WrappedDEK 是用该设备 RSA 公钥包装的书籍 DEK（base64）。
	// 设备用 Keystore 私钥解包后才能解密章节包；服务端从不输出明文 DEK。
	// 无设备公钥（例如浏览器 session 调用）时省略。
	WrappedDEK string `json:"wrapped_dek,omitempty"`
}

// Manifest 是 GET /api/sync/manifest 的响应。
type Manifest struct {
	Cursor        int64        `json:"cursor"`
	Since         int64        `json:"since"`
	Full          bool         `json:"full"`
	ResetRequired bool         `json:"reset_required,omitempty"`
	GeneratedAt   string       `json:"generated_at"`
	Count         int          `json:"count"`
	Books         []BookChange `json:"books"`
	Deleted       []string     `json:"deleted"`
}

// WrapFunc 把某本书的 DEK 包装成设备可解的字符串。
// 返回空字符串表示跳过（无设备公钥）。
type WrapFunc func(bookID string) (string, error)

// BuildManifest 返回 since 之后的增量；since=0 表示全量。
//
// 游标是 books.sync_seq（单调递增整数），由上传/删除/元数据修改推进，
// 不依赖客户端时钟，也不会因为同一毫秒内多次写入而丢变更。
func BuildManifest(ctx context.Context, db *sql.DB, ownerID string, since int64, wrap WrapFunc) (*Manifest, error) {
	return BuildManifestWithVariant(ctx, db, ownerID, since, wrap, "none")
}

// BuildManifestWithVariant 与 BuildManifest 相同，但把净化规则指纹混进章节 ETag：
// 运维改了 REGEX_PURGE_RULES 后，客户端必须重新拉取章节，否则会继续用旧缓存。
func BuildManifestWithVariant(
	ctx context.Context, db *sql.DB, ownerID string, since int64, wrap WrapFunc, etagVariant string,
) (*Manifest, error) {
	if since < 0 {
		since = 0
	}
	var maxSeq int64
	if err := db.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(sync_seq), 0) FROM books WHERE owner_id = ?`, ownerID).Scan(&maxSeq); err != nil {
		return nil, err
	}
	m := &Manifest{
		Cursor:      maxSeq,
		Since:       since,
		Full:        since == 0,
		GeneratedAt: time.Now().UTC().Format(time.RFC3339Nano),
		Books:       []BookChange{},
		Deleted:     []string{},
	}
	if since > maxSeq {
		// 客户端游标领先于服务端（例如恢复了旧备份）：要求客户端清库重同步，
		// 否则会永久错过中间变更。
		m.ResetRequired = true
		return m, nil
	}

	rows, err := db.QueryContext(ctx, `SELECT id, title, author, tags, description,
		chapter_count, size_bytes, sha256, kek_version, chunk_size, created_at, sync_seq, deleted_at
		FROM books WHERE owner_id = ? AND sync_seq > ? ORDER BY sync_seq`, ownerID, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	type raw struct {
		b       BookChange
		deleted sql.NullString
	}
	var list []raw
	for rows.Next() {
		var r raw
		var tags string
		if err := rows.Scan(&r.b.ID, &r.b.Title, &r.b.Author, &tags, &r.b.Description,
			&r.b.ChapterCount, &r.b.SizeBytes, &r.b.SHA256, &r.b.KEKVersion, &r.b.ChunkSize,
			&r.b.CreatedAt, &r.b.SyncSeq, &r.deleted); err != nil {
			return nil, err
		}
		r.b.Tags = books.SplitTags(tags)
		list = append(list, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	for _, r := range list {
		if r.deleted.Valid {
			if !m.Full {
				m.Deleted = append(m.Deleted, r.b.ID)
			}
			continue
		}
		if err := fillChapters(ctx, db, &r.b, etagVariant); err != nil {
			return nil, err
		}
		if wrap != nil {
			w, werr := wrap(r.b.ID)
			if werr != nil {
				return nil, werr
			}
			r.b.WrappedDEK = w
		}
		m.Books = append(m.Books, r.b)
	}
	m.Count = len(m.Books)
	return m, nil
}

// fillChapters 追加章节目录与 ETag。
// 每本书一次查询（N+1）：私人书库规模下可接受，换来实现简单与索引命中。
func fillChapters(ctx context.Context, db *sql.DB, b *BookChange, etagVariant string) error {
	rows, err := db.QueryContext(ctx, `SELECT chapter_index, title, chars, start_rune, end_rune
		FROM book_chapters WHERE book_id = ? ORDER BY chapter_index`, b.ID)
	if err != nil {
		return err
	}
	defer rows.Close()
	b.Chapters = []ChapterRef{}
	meta := &books.Book{ID: b.ID, SHA256: b.SHA256, ChunkSize: b.ChunkSize}
	for rows.Next() {
		var c chapterscan.Chapter
		if err := rows.Scan(&c.Index, &c.Title, &c.Chars, &c.Start, &c.End); err != nil {
			return err
		}
		b.Chapters = append(b.Chapters, ChapterRef{
			Index: c.Index, Title: c.Title, Chars: c.Chars,
			ETag: books.ChapterETag(meta, c, etagVariant),
		})
	}
	return rows.Err()
}

// ErrProgressInvalid 表示进度字段越界。
var ErrProgressInvalid = errors.New("mobile: invalid progress")

// Progress 是阅读进度（不涉及正文，按需求不加密）。
type Progress struct {
	BookID       string  `json:"book_id"`
	Title        string  `json:"title,omitempty"`
	ChapterIndex int     `json:"chapter_index"`
	CharOffset   int     `json:"char_offset"`
	Percent      float64 `json:"percent"`
	DeviceID     string  `json:"device_id,omitempty"`
	UpdatedAt    string  `json:"updated_at"`
}

// PutProgress 写入进度（最后写入胜）。
// 单用户场景下用 (owner_id, book_id) 作主键；不同设备的写入互相覆盖，
// 与「最后写入胜」的约定一致，不做版本比较。
func PutProgress(ctx context.Context, db *sql.DB, ownerID, bookID string, p Progress, deviceID string) (*Progress, error) {
	if p.ChapterIndex < 0 || p.CharOffset < 0 || p.Percent < 0 || p.Percent > 1 {
		return nil, ErrProgressInvalid
	}
	var exists int
	if err := db.QueryRowContext(ctx,
		`SELECT 1 FROM books WHERE id = ? AND owner_id = ? AND deleted_at IS NULL`,
		bookID, ownerID).Scan(&exists); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, books.ErrNotFound
		}
		return nil, err
	}
	ts := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := db.ExecContext(ctx, `INSERT INTO reading_progress
		(owner_id, book_id, chapter_index, char_offset, percent, device_id, updated_at)
		VALUES (?,?,?,?,?,?,?)
		ON CONFLICT(owner_id, book_id) DO UPDATE SET
			chapter_index = excluded.chapter_index,
			char_offset   = excluded.char_offset,
			percent       = excluded.percent,
			device_id     = excluded.device_id,
			updated_at    = excluded.updated_at`,
		ownerID, bookID, p.ChapterIndex, p.CharOffset, p.Percent, deviceID, ts)
	if err != nil {
		return nil, err
	}
	p.BookID = bookID
	p.DeviceID = deviceID
	p.UpdatedAt = ts
	return &p, nil
}

// GetProgress 读取单本书进度；不存在时返回 ErrNotFound。
func GetProgress(ctx context.Context, db *sql.DB, ownerID, bookID string) (*Progress, error) {
	p := &Progress{BookID: bookID}
	err := db.QueryRowContext(ctx, `SELECT p.chapter_index, p.char_offset, p.percent,
		p.device_id, p.updated_at, b.title
		FROM reading_progress p JOIN books b ON b.id = p.book_id
		WHERE p.owner_id = ? AND p.book_id = ?`, ownerID, bookID).
		Scan(&p.ChapterIndex, &p.CharOffset, &p.Percent, &p.DeviceID, &p.UpdatedAt, &p.Title)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, books.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return p, nil
}

// RecentProgress 返回最近更新的进度（dashboard 用）。
func RecentProgress(ctx context.Context, db *sql.DB, ownerID string, limit int) ([]Progress, error) {
	if limit <= 0 || limit > 50 {
		limit = 10
	}
	rows, err := db.QueryContext(ctx, `SELECT p.book_id, b.title, p.chapter_index,
		p.char_offset, p.percent, p.device_id, p.updated_at
		FROM reading_progress p JOIN books b ON b.id = p.book_id
		WHERE p.owner_id = ? ORDER BY p.updated_at DESC LIMIT ?`, ownerID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Progress{}
	for rows.Next() {
		var p Progress
		if err := rows.Scan(&p.BookID, &p.Title, &p.ChapterIndex, &p.CharOffset,
			&p.Percent, &p.DeviceID, &p.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
