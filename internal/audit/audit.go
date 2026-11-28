// Package audit 提供审计日志写入与查询。审计不记录任何正文、密码或密钥材料。
package audit

import (
	"context"
	"database/sql"
	"strings"
	"time"
)

// Logger 写入 SQLite audit_logs 表。
type Logger struct {
	db *sql.DB
}

func New(db *sql.DB) *Logger { return &Logger{db: db} }

// Entry 是一条审计记录。
type Entry struct {
	ID       int64  `json:"id"`
	TS       string `json:"ts"`
	Username string `json:"username"`
	IP       string `json:"ip"`
	Action   string `json:"action"`
	BookID   string `json:"book_id,omitempty"`
	Result   string `json:"result"`
	Detail   string `json:"detail,omitempty"`
}

// Record 异步友好地写入一条审计记录；写入失败只打日志不中断业务。
// detail 必须是摘要（文件名、原因码等），调用方不得放入敏感内容。
func (l *Logger) Record(ctx context.Context, username, ip, action, bookID, result, detail string) {
	if ctx == nil || ctx.Err() != nil {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
	}
	_, err := l.db.ExecContext(ctx,
		`INSERT INTO audit_logs(username, ip, action, book_id, result, detail) VALUES (?,?,?,?,?,?)`,
		username, ip, action, bookID, result, detail)
	if err != nil {
		// 审计失败不应泄露内部信息到 HTTP 响应；这里静默（标准库 log 走 stderr）。
		// 带上错误原因：排障时只知道「失败了」等于没有信息。
		println("audit write failed:", err.Error())
	}
}

// Filter 是审计查询条件（全部为可选）。
type Filter struct {
	// Action 支持精确动作名，或以 '_' 结尾做前缀匹配（如 "legado_"）。
	Action string
	Result string
	// BeforeID 用于向前翻页（id < BeforeID）。
	BeforeID int64
	Limit    int
}

// Query 按条件返回审计记录（id 倒序）。
func (l *Logger) Query(ctx context.Context, f Filter) ([]Entry, error) {
	if f.Limit <= 0 || f.Limit > 1000 {
		f.Limit = 100
	}
	where := make([]string, 0, 3)
	args := make([]any, 0, 4)
	if f.Action != "" {
		if strings.HasSuffix(f.Action, "_") {
			where = append(where, `action LIKE ? ESCAPE '\'`)
			args = append(args, escapeLike(f.Action)+"%")
		} else {
			where = append(where, `action = ?`)
			args = append(args, f.Action)
		}
	}
	if f.Result != "" {
		where = append(where, `result = ?`)
		args = append(args, f.Result)
	}
	if f.BeforeID > 0 {
		where = append(where, `id < ?`)
		args = append(args, f.BeforeID)
	}
	q := `SELECT id, ts, username, ip, action, book_id, result, detail FROM audit_logs`
	if len(where) > 0 {
		q += ` WHERE ` + strings.Join(where, " AND ")
	}
	q += ` ORDER BY id DESC LIMIT ?`
	args = append(args, f.Limit)

	rows, err := l.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Entry
	for rows.Next() {
		var e Entry
		if err := rows.Scan(&e.ID, &e.TS, &e.Username, &e.IP, &e.Action, &e.BookID, &e.Result, &e.Detail); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// Recent 返回最近 limit 条审计记录（按 id 倒序）。
func (l *Logger) Recent(ctx context.Context, limit int) ([]Entry, error) {
	return l.Query(ctx, Filter{Limit: limit})
}

// RecentFiltered 是 Recent 的带过滤版本（控制台审计页使用）。
func (l *Logger) RecentFiltered(ctx context.Context, action, result string, beforeID int64, limit int) ([]Entry, error) {
	return l.Query(ctx, Filter{Action: action, Result: result, BeforeID: beforeID, Limit: limit})
}

// CountsByAction 统计最近 limit 条记录按动作分组（供控制台概览）。
func (l *Logger) CountsByAction(ctx context.Context, limit int) (map[string]int, error) {
	if limit <= 0 || limit > 5000 {
		limit = 1000
	}
	rows, err := l.db.QueryContext(ctx, `SELECT action, COUNT(*) FROM (
		SELECT action FROM audit_logs ORDER BY id DESC LIMIT ?) GROUP BY action`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var action string
		var n int
		if err := rows.Scan(&action, &n); err != nil {
			return nil, err
		}
		out[action] = n
	}
	return out, rows.Err()
}

func escapeLike(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return r.Replace(s)
}
