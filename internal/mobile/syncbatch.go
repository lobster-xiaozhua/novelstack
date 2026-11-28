package mobile

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"novelstack/internal/books"
)

// ErrTooManyItems 表示单次批量同步条目超过上限。
var ErrTooManyItems = errors.New("mobile: too many progress items")

// Position 是阅读位置的单调比较键：章节优先，同章再比字符偏移。
//
// 之所以不用「最后写入胜」：多端同时阅读时，较晚但更靠前的写入（例如手机端点错
// 回翻一章）会把另一端的进度拽回去。按位置取最靠后的一页，最终一定收敛到
// 「读得最远」的那一端，符合多端同步的直觉。
type Position struct {
	ChapterIndex int
	CharOffset   int
}

// Ahead 判断 a 是否严格比 b 更靠后。位置相同返回 false（保留现有记录，避免无意义写入）。
func (a Position) Ahead(b Position) bool {
	if a.ChapterIndex != b.ChapterIndex {
		return a.ChapterIndex > b.ChapterIndex
	}
	return a.CharOffset > b.CharOffset
}

// MergeProgress 把客户端批量上报的进度合并进数据库，返回实际采纳的条目数。
//
// 规则：
//   - 同一本书在 items 内出现多次时先取最靠后的一条；
//   - 与库内记录比较，只有更靠后才覆盖；更靠前或相同则忽略；
//   - 未知 / 已删除的书静默跳过（客户端可能持有过期书架）；
//   - 整批在一个事务内完成，任一条目字段非法则整批拒绝（避免半写）。
func MergeProgress(ctx context.Context, db *sql.DB, ownerID string, items []Progress, deviceID string, max int) (int, error) {
	if len(items) == 0 {
		return 0, nil
	}
	if max > 0 && len(items) > max {
		return 0, ErrTooManyItems
	}

	best := make(map[string]Progress, len(items))
	order := make([]string, 0, len(items))
	for _, it := range items {
		if it.BookID == "" || it.ChapterIndex < 0 || it.CharOffset < 0 ||
			it.Percent < 0 || it.Percent > 1 {
			return 0, ErrProgressInvalid
		}
		if it.DeviceID == "" {
			it.DeviceID = deviceID
		}
		if len(it.DeviceID) > 64 {
			it.DeviceID = it.DeviceID[:64]
		}
		cur, ok := best[it.BookID]
		if !ok {
			best[it.BookID] = it
			order = append(order, it.BookID)
			continue
		}
		if (Position{it.ChapterIndex, it.CharOffset}).Ahead(Position{cur.ChapterIndex, cur.CharOffset}) {
			best[it.BookID] = it
		}
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()

	applied := 0
	ts := nowUTC().Format(time.RFC3339Nano)
	for _, bookID := range order {
		it := best[bookID]

		var exists int
		err := tx.QueryRowContext(ctx,
			`SELECT 1 FROM books WHERE id = ? AND owner_id = ? AND deleted_at IS NULL`,
			bookID, ownerID).Scan(&exists)
		if errors.Is(err, sql.ErrNoRows) {
			continue // 过期书架条目：跳过，不整批失败
		}
		if err != nil {
			return 0, err
		}

		var curChapter, curOffset int
		err = tx.QueryRowContext(ctx,
			`SELECT chapter_index, char_offset FROM reading_progress
			 WHERE owner_id = ? AND book_id = ?`, ownerID, bookID).
			Scan(&curChapter, &curOffset)
		switch {
		case err == nil:
			if !(Position{it.ChapterIndex, it.CharOffset}).Ahead(Position{curChapter, curOffset}) {
				continue // 库内更靠后：保留
			}
		case errors.Is(err, sql.ErrNoRows):
			// 首次上报，直接写入
		default:
			return 0, err
		}

		if _, err := tx.ExecContext(ctx, `INSERT INTO reading_progress
			(owner_id, book_id, chapter_index, char_offset, percent, device_id, updated_at)
			VALUES (?,?,?,?,?,?,?)
			ON CONFLICT(owner_id, book_id) DO UPDATE SET
				chapter_index = excluded.chapter_index,
				char_offset   = excluded.char_offset,
				percent       = excluded.percent,
				device_id     = excluded.device_id,
				updated_at    = excluded.updated_at`,
			ownerID, bookID, it.ChapterIndex, it.CharOffset, it.Percent, it.DeviceID, ts); err != nil {
			return applied, err
		}
		applied++
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return applied, nil
}

// ListProgress 返回某用户的全部阅读进度（按更新时间倒序）。
// since 非空时只返回 updated_at >= since 的记录；用于增量拉取。
func ListProgress(ctx context.Context, db *sql.DB, ownerID, since string, limit int) ([]Progress, error) {
	if limit < 1 || limit > 1000 {
		limit = 500
	}
	q := `SELECT p.book_id, b.title, p.chapter_index, p.char_offset, p.percent,
			p.device_id, p.updated_at
		FROM reading_progress p JOIN books b ON b.id = p.book_id
		WHERE p.owner_id = ? AND b.deleted_at IS NULL`
	args := []any{ownerID}
	if since != "" {
		q += ` AND p.updated_at >= ?`
		args = append(args, since)
	}
	q += ` ORDER BY p.updated_at DESC LIMIT ?`
	args = append(args, limit)

	rows, err := db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]Progress, 0, 16)
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

// ProgressCursor 生成下一次增量拉取的游标。用服务器 UTC 时间而不是自增序号：
// 进度表天然按时间覆盖，时间戳足以表达「已同步到哪一刻」，且不需要额外迁移。
func ProgressCursor() string { return nowUTC().Format(time.RFC3339Nano) }

// GetOwnedBook 是轻量存在性检查，供 Web 端接口在解密前确认归属。
func GetOwnedBook(ctx context.Context, db *sql.DB, ownerID, bookID string) (string, error) {
	var title string
	err := db.QueryRowContext(ctx,
		`SELECT title FROM books WHERE id = ? AND owner_id = ? AND deleted_at IS NULL`,
		bookID, ownerID).Scan(&title)
	if errors.Is(err, sql.ErrNoRows) {
		return "", books.ErrNotFound
	}
	return title, err
}

func nowUTC() time.Time { return time.Now().UTC() }
