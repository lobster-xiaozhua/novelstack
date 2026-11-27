package books

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// 第八阶段：收藏（星标/心标）。
//
// 设计：收藏状态单独存 user_bookmarks，不写进 books 表。原因：
//   - 收藏是「用户偏好」，不是书籍元数据，混进 books 会让 sync_seq 语义变脏；
//   - 取消收藏应当是「有记录且 is_favorite=0」，而不是删行，便于客户端增量同步语义稳定。
//
// 该文件只处理 book_id 与布尔标记，不触及任何正文字段。

// SetFavorite 设置/取消收藏。书籍必须存在且未软删除，否则返回 ErrNotFound。
// 返回设置后的状态，便于调用方回显。
func (s *Store) SetFavorite(ctx context.Context, ownerID, bookID string, fav bool) (bool, error) {
	var exists int
	err := s.db.QueryRowContext(ctx,
		`SELECT 1 FROM books WHERE id = ? AND deleted_at IS NULL AND (? = '' OR owner_id = ?)`,
		bookID, ownerID, ownerID).Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		return false, ErrNotFound
	}
	if err != nil {
		return false, err
	}
	ts := time.Now().UTC().Format(time.RFC3339Nano)
	v := 0
	if fav {
		v = 1
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO user_bookmarks
		(owner_id, book_id, is_favorite, updated_at)
		VALUES (?,?,?,?)
		ON CONFLICT(owner_id, book_id) DO UPDATE SET
			is_favorite = excluded.is_favorite,
			updated_at  = excluded.updated_at`,
		ownerID, bookID, v, ts)
	if err != nil {
		return false, err
	}
	return fav, nil
}

// IsFavorite 返回某本书的收藏状态；无记录视为未收藏。
func (s *Store) IsFavorite(ctx context.Context, ownerID, bookID string) (bool, error) {
	var v int
	err := s.db.QueryRowContext(ctx,
		`SELECT is_favorite FROM user_bookmarks WHERE owner_id = ? AND book_id = ?`,
		ownerID, bookID).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return v != 0, nil
}

// FavoriteSet 返回该用户全部「已收藏」的 book_id 集合，供列表接口批量填充 is_favorite，
// 避免 N+1 查询。
func (s *Store) FavoriteSet(ctx context.Context, ownerID string) (map[string]bool, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT book_id FROM user_bookmarks WHERE owner_id = ? AND is_favorite = 1`, ownerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}
