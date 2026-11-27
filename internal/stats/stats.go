// Package stats 汇总阅读时长：按天累计 + 连续阅读天数 + 近 90 天序列。
//
// 时间口径：一律使用服务端时区（默认 Asia/Shanghai）的日历日，
// 客户端只上报「本次阅读了多少秒」，不上报任何时间戳，避免时钟漂移污染统计。
package stats

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// MaxSecondsPerReport 是单次上报允许的最大阅读秒数（防客户端异常刷量）。
const MaxSecondsPerReport = 600

// Day 是一天的阅读量。
type Day struct {
	Day      string `json:"day"`
	Seconds  int64  `json:"seconds"`
	Chapters int64  `json:"chapters"`
}

// Overview 是统计中心的返回体。
type Overview struct {
	TodaySeconds int64  `json:"today_seconds"`
	TotalSeconds int64  `json:"total_seconds"`
	StreakDays   int    `json:"streak_days"`
	BestStreak   int    `json:"best_streak"`
	TotalDays    int    `json:"total_days"`
	LastReadAt   string `json:"last_read_at,omitempty"`
	// Days 是最近 WindowDays 天（含今天），缺失的日期补 0，便于前端直接画图。
	Days []Day `json:"days"`
}

// Service 读写 reading_daily。
type Service struct {
	DB *sql.DB
	// Loc 决定「今天」的边界；为 nil 时使用 UTC。
	Loc *time.Location
}

// WindowDays 是返回给前端的日历窗口长度。
const WindowDays = 90

// New 构造服务；loc 为 nil 时回退 UTC。
func New(db *sql.DB, loc *time.Location) *Service {
	if loc == nil {
		loc = time.UTC
	}
	return &Service{DB: db, Loc: loc}
}

// Today 返回服务端时区下的当前日期（YYYY-MM-DD）。
func (s *Service) Today() string {
	return time.Now().In(s.loc()).Format("2006-01-02")
}

func (s *Service) loc() *time.Location {
	if s.Loc == nil {
		return time.UTC
	}
	return s.Loc
}

// AddDaily 把本次阅读秒数累加到当天。seconds<=0 时直接返回（不写库）。
// chapters 表示本次跨越的章节数，通常为 0 或 1，仅用于展示。
func (s *Service) AddDaily(ctx context.Context, ownerID string, seconds, chapters int) error {
	if seconds <= 0 {
		return nil
	}
	if seconds > MaxSecondsPerReport {
		seconds = MaxSecondsPerReport
	}
	if chapters < 0 {
		chapters = 0
	}
	if chapters > 50 {
		chapters = 50
	}
	_, err := s.DB.ExecContext(ctx, `INSERT INTO reading_daily (owner_id, day, seconds, chapters, updated_at)
		VALUES (?,?,?,?,strftime('%Y-%m-%dT%H:%M:%fZ','now'))
		ON CONFLICT(owner_id, day) DO UPDATE SET
			seconds = seconds + excluded.seconds,
			chapters = chapters + excluded.chapters,
			updated_at = excluded.updated_at`,
		ownerID, s.Today(), seconds, chapters)
	return err
}

// Overview 汇总统计。days 只返回最近 WindowDays 天，但累计值与连续天数基于全量。
func (s *Service) Overview(ctx context.Context, ownerID string) (*Overview, error) {
	out := &Overview{Days: []Day{}}
	var total, totalDays int64
	if err := s.DB.QueryRowContext(ctx,
		`SELECT COALESCE(SUM(seconds),0), COUNT(*) FROM reading_daily WHERE owner_id = ? AND seconds > 0`,
		ownerID).Scan(&total, &totalDays); err != nil {
		return nil, err
	}
	out.TotalSeconds = total
	out.TotalDays = int(totalDays)

	// 全量日期（升序）用于计算连续天数；单用户数据量很小（最多几年）。
	rows, err := s.DB.QueryContext(ctx,
		`SELECT day FROM reading_daily WHERE owner_id = ? AND seconds > 0 ORDER BY day ASC`, ownerID)
	if err != nil {
		return nil, err
	}
	var allDays []string
	for rows.Next() {
		var d string
		if err := rows.Scan(&d); err != nil {
			rows.Close()
			return nil, err
		}
		allDays = append(allDays, d)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out.StreakDays, out.BestStreak = streaks(allDays, s.Today())

	// 最近 WindowDays 天：先取有数据的日子，再补齐空白天。
	start := time.Now().In(s.loc()).AddDate(0, 0, -(WindowDays - 1)).Format("2006-01-02")
	filled := make(map[string]Day, len(allDays))
	byDay, err := s.DB.QueryContext(ctx,
		`SELECT day, seconds, chapters FROM reading_daily
		 WHERE owner_id = ? AND day >= ? ORDER BY day ASC`, ownerID, start)
	if err != nil {
		return nil, err
	}
	defer byDay.Close()
	for byDay.Next() {
		var d Day
		if err := byDay.Scan(&d.Day, &d.Seconds, &d.Chapters); err != nil {
			return nil, err
		}
		filled[d.Day] = d
	}
	if err := byDay.Err(); err != nil {
		return nil, err
	}
	base := time.Now().In(s.loc())
	for i := WindowDays - 1; i >= 0; i-- {
		key := base.AddDate(0, 0, -i).Format("2006-01-02")
		if d, ok := filled[key]; ok {
			out.Days = append(out.Days, d)
		} else {
			out.Days = append(out.Days, Day{Day: key})
		}
		if key == s.Today() {
			out.TodaySeconds = filled[key].Seconds
		}
	}

	if err := s.DB.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(updated_at), '') FROM reading_progress WHERE owner_id = ?`,
		ownerID).Scan(&out.LastReadAt); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	return out, nil
}

// streaks 计算「当前连续天数」与「历史最长连续天数」。
// 当前连续：今天有记录则从今天起算，否则从昨天起算（今天还没读不算断签）。
func streaks(sortedDays []string, today string) (current, best int) {
	if len(sortedDays) == 0 {
		return 0, 0
	}
	set := make(map[string]bool, len(sortedDays))
	for _, d := range sortedDays {
		set[d] = true
	}
	loc := time.UTC
	t, err := time.ParseInLocation("2006-01-02", today, loc)
	if err != nil {
		return 0, 0
	}
	// 当前连续
	cursor := t
	if !set[today] {
		cursor = t.AddDate(0, 0, -1)
	}
	for set[cursor.Format("2006-01-02")] {
		current++
		cursor = cursor.AddDate(0, 0, -1)
	}
	// 历史最长：顺序扫描，日期相差 1 天则延续
	run := 1
	for i := 1; i < len(sortedDays); i++ {
		prev, err1 := time.ParseInLocation("2006-01-02", sortedDays[i-1], loc)
		cur, err2 := time.ParseInLocation("2006-01-02", sortedDays[i], loc)
		if err1 != nil || err2 != nil {
			continue
		}
		if cur.Sub(prev) == 24*time.Hour {
			run++
		} else {
			run = 1
		}
		if run > best {
			best = run
		}
	}
	if len(sortedDays) == 1 {
		best = 1
	}
	if best == 0 {
		best = 1
	}
	if current > best {
		best = current
	}
	return current, best
}
