// Package search 实现「服务端元数据检索」这一层。
//
// 分层原则（第三阶段最关键的架构约束）：
//   - 服务端只索引元数据：书名、作者、标签、简介、章节标题；
//   - 正文永不进入服务端索引、日志或审计；
//   - 正文全文搜索在客户端本地完成（见 clientcore）。
//
// 因此本包只接受 Document（字段白名单固定），并在发送给 Meilisearch 之前
// 再次校验 JSON 字段名，任何多余字段都视为实现错误而拒绝。
package search

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"novelstack/internal/audit"
)

// Document 是唯一允许进入服务端检索索引的结构。
// 注意：新增字段必须同时加入 AllowedDocFields，并确认它不是正文。
type Document struct {
	ID            string   `json:"id"`
	Title         string   `json:"title"`
	Author        string   `json:"author"`
	Tags          []string `json:"tags"`
	Description   string   `json:"description"`
	ChapterTitles []string `json:"chapter_titles"`
	ChapterCount  int      `json:"chapter_count"`
	SizeBytes     int64    `json:"size_bytes"`
	CreatedAt     string   `json:"created_at"`
}

// AllowedDocFields 是索引文档允许出现的全部字段名。
// 正文类字段（content/text/body/chapter_body/plain）不在其中，且永远不会加入。
var AllowedDocFields = []string{
	"id", "title", "author", "tags", "description",
	"chapter_titles", "chapter_count", "size_bytes", "created_at",
}

var allowedDocFieldSet = func() map[string]struct{} {
	m := make(map[string]struct{}, len(AllowedDocFields))
	for _, f := range AllowedDocFields {
		m[f] = struct{}{}
	}
	return m
}()

// ErrForbiddenField 表示索引文档包含白名单之外的字段（视为实现错误，拒绝发送）。
var ErrForbiddenField = errors.New("search: document contains field outside allowlist")

// ValidateDocumentJSON 校验文档序列化后的字段名都在白名单内。
// 这是「正文绝不进索引」的硬性防线：即使有人给 Document 加了正文字段，
// 这里也会在请求发出前失败。
func ValidateDocumentJSON(raw []byte) error {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return fmt.Errorf("search: 文档不是合法 JSON: %w", err)
	}
	for k := range m {
		if _, ok := allowedDocFieldSet[k]; !ok {
			return fmt.Errorf("%w: %q", ErrForbiddenField, k)
		}
	}
	return nil
}

// Hit 是一条检索结果（highlight 只含元数据字段的高亮，不含正文）。
type Hit struct {
	BookID       string `json:"book_id"`
	Title        string `json:"title"`
	Author       string `json:"author"`
	MatchedField string `json:"matched_field"`
	Highlight    string `json:"highlight"`
}

// Backend 是可替换的索引后端（Meilisearch，或测试用的假后端）。
type Backend interface {
	Name() string
	// PublishedIndex 返回索引名（用于指标展示，不含密钥）
	PublishedIndex() string
	EnsureSchema(ctx context.Context) error
	Upsert(ctx context.Context, docs []Document) error
	Delete(ctx context.Context, id string) error
	Search(ctx context.Context, q string, limit int) ([]Hit, error)
	DocIDs(ctx context.Context) ([]string, error)
	DocCount(ctx context.Context) (int64, error)
	Health(ctx context.Context) error
}

// Service 负责取元数据、写索引、执行查询与记录观测指标。
type Service struct {
	db      *sql.DB
	backend Backend // nil 表示未配置 Meilisearch，直接走本地匹配
	audit   *audit.Logger
	timeout time.Duration
	// logKey 用于对查询词做 HMAC：库里只有不可逆指纹，没有查询明文。
	logKey []byte
	queue  chan op
	wg     sync.WaitGroup
	once   sync.Once
	closed sync.Once
	// lastErr 记录最近一次索引错误类别（供指标展示，不含正文）
	errMu   sync.Mutex
	lastErr string
}

type op struct {
	kind   string // upsert | delete
	bookID string
}

// NewService 构造检索服务；backend 为 nil 时启用本地降级匹配。
func NewService(db *sql.DB, backend Backend, a *audit.Logger, timeout time.Duration, logKey []byte) *Service {
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	return &Service{db: db, backend: backend, audit: a, timeout: timeout, logKey: logKey, queue: make(chan op, 256)}
}

// Start 启动索引 worker（幂等）。
func (s *Service) Start() {
	s.once.Do(func() {
		s.wg.Add(1)
		go s.worker()
	})
}

// Close 停止 worker（幂等）。
func (s *Service) Close() {
	s.closed.Do(func() {
		close(s.queue)
		s.wg.Wait()
	})
}

func (s *Service) worker() {
	defer s.wg.Done()
	for o := range s.queue {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		switch o.kind {
		case "delete":
			s.applyDelete(ctx, o.bookID)
		default:
			s.applyUpsert(ctx, o.bookID)
		}
		cancel()
	}
}

// Enqueue 异步同步一本书（去重由 worker 串行执行天然保证）。
func (s *Service) Enqueue(bookID string) {
	if bookID == "" {
		return
	}
	select {
	case s.queue <- op{kind: "upsert", bookID: bookID}:
	default:
		// 队列满时丢弃：启动时的全量同步会兜底
		log.Printf("search: index queue full, dropped book=%s", bookID)
	}
}

// Remove 异步删除一本书的索引文档。
func (s *Service) Remove(bookID string) {
	if bookID == "" {
		return
	}
	select {
	case s.queue <- op{kind: "delete", bookID: bookID}:
	default:
		log.Printf("search: index queue full, dropped delete book=%s", bookID)
	}
}

func (s *Service) setLastErr(v string) {
	s.errMu.Lock()
	s.lastErr = v
	s.errMu.Unlock()
}

// LastError 返回最近一次索引错误的类别（不含任何正文）。
func (s *Service) LastError() string {
	s.errMu.Lock()
	defer s.errMu.Unlock()
	return s.lastErr
}

func (s *Service) applyUpsert(ctx context.Context, bookID string) {
	if s.backend == nil {
		return
	}
	doc, err := s.document(ctx, bookID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			s.applyDelete(ctx, bookID)
			return
		}
		s.setLastErr("source_error")
		log.Printf("search: load metadata for %s failed", bookID)
		return
	}
	if err := s.backend.Upsert(ctx, []Document{*doc}); err != nil {
		s.setLastErr(classify(err))
		_, _ = s.db.ExecContext(ctx, `UPDATE books SET index_state='error' WHERE id=?`, bookID)
		log.Printf("search: upsert %s failed: %v", bookID, err)
		return
	}
	s.setLastErr("")
	_, _ = s.db.ExecContext(ctx, `UPDATE books SET index_state='indexed' WHERE id=?`, bookID)
}

func (s *Service) applyDelete(ctx context.Context, bookID string) {
	if s.backend == nil {
		return
	}
	if err := s.backend.Delete(ctx, bookID); err != nil {
		s.setLastErr(classify(err))
		log.Printf("search: delete %s failed: %v", bookID, err)
	}
}

func classify(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(err, ErrForbiddenField):
		return "forbidden_field"
	default:
		return "backend_error"
	}
}

// document 从 SQLite 组装可索引元数据（只读标题类字段，不触碰密文与正文）。
func (s *Service) document(ctx context.Context, bookID string) (*Document, error) {
	var (
		d    Document
		tags string
	)
	err := s.db.QueryRowContext(ctx, `SELECT id, title, author, tags, description,
		chapter_count, size_bytes, created_at FROM books
		WHERE id = ? AND deleted_at IS NULL`, bookID).Scan(
		&d.ID, &d.Title, &d.Author, &tags, &d.Description,
		&d.ChapterCount, &d.SizeBytes, &d.CreatedAt)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(tags) != "" {
		for _, t := range strings.Split(tags, ",") {
			if t = strings.TrimSpace(t); t != "" {
				d.Tags = append(d.Tags, t)
			}
		}
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT title FROM book_chapters WHERE book_id = ? ORDER BY chapter_index LIMIT 500`, bookID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var t string
		if err := rows.Scan(&t); err != nil {
			return nil, err
		}
		d.ChapterTitles = append(d.ChapterTitles, t)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return &d, nil
}

// SyncAll 执行一次全量同步（幂等兜底）：重建全部文档并清理已删除书籍的残留。
func (s *Service) SyncAll(ctx context.Context) error {
	if s.backend == nil {
		return nil
	}
	if err := s.backend.EnsureSchema(ctx); err != nil {
		s.setLastErr("schema_error")
		return err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM books WHERE deleted_at IS NULL ORDER BY created_at`)
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	docs := make([]Document, 0, len(ids))
	for _, id := range ids {
		d, err := s.document(ctx, id)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				continue
			}
			return err
		}
		docs = append(docs, *d)
	}
	for start := 0; start < len(docs); start += 100 {
		end := start + 100
		if end > len(docs) {
			end = len(docs)
		}
		if err := s.backend.Upsert(ctx, docs[start:end]); err != nil {
			s.setLastErr(classify(err))
			return err
		}
	}
	// 清理残留文档（书籍被软删除或索引里有多余 id）
	live := make(map[string]struct{}, len(docs))
	for _, d := range docs {
		live[d.ID] = struct{}{}
	}
	if existing, err := s.backend.DocIDs(ctx); err == nil {
		for _, id := range existing {
			if _, ok := live[id]; !ok {
				_ = s.backend.Delete(ctx, id)
			}
		}
	}
	if len(docs) > 0 {
		if _, err := s.db.ExecContext(ctx, `UPDATE books SET index_state='indexed'
			WHERE deleted_at IS NULL AND id IN (`+placeholders(len(docs))+`)`, toAny(docs)...); err != nil {
			log.Printf("search: update index_state failed: %v", err)
		}
	}
	s.setLastErr("")
	log.Printf("search: full sync done, documents=%d", len(docs))
	return nil
}

func placeholders(n int) string {
	if n <= 0 {
		return "NULL"
	}
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

func toAny(docs []Document) []any {
	out := make([]any, len(docs))
	for i := range docs {
		out[i] = docs[i].ID
	}
	return out
}

// Result 是一次查询的完整响应（交给 handler 序列化）。
type Result struct {
	QueryChars int    `json:"query_chars"`
	Limit      int    `json:"limit"`
	Source     string `json:"source"`
	TookMS     int64  `json:"took_ms"`
	Hits       []Hit  `json:"hits"`
	Degraded   bool   `json:"degraded,omitempty"`
}

// Search 执行元数据检索：优先 Meilisearch，不可用时降级为本地匹配。
// 查询词不落库：只记录长度与带密钥的 HMAC 指纹。
func (s *Service) Search(ctx context.Context, q string, limit int) (*Result, error) {
	if limit <= 0 || limit > 50 {
		limit = 20
	}
	start := time.Now()
	res := &Result{QueryChars: len([]rune(q)), Limit: limit, Hits: []Hit{}}

	if s.backend != nil {
		sctx, cancel := context.WithTimeout(ctx, s.timeout)
		hits, err := s.backend.Search(sctx, q, limit)
		cancel()
		if err == nil {
			res.Source = s.backend.Name()
			res.Hits = hits
			res.TookMS = time.Since(start).Milliseconds()
			s.logQuery(ctx, q, len(hits), time.Since(start), "ok", res.Source)
			return res, nil
		}
		// 后端故障 → 降级（仍然只查元数据，不触碰正文）
		s.setLastErr(classify(err))
		res.Degraded = true
		log.Printf("search: backend failed (%s), degraded to local metadata match", classify(err))
	}

	hits, err := s.localSearch(ctx, q, limit)
	if err != nil {
		s.logQuery(ctx, q, 0, time.Since(start), "error", "local")
		return nil, err
	}
	res.Source = "local"
	res.Hits = hits
	res.TookMS = time.Since(start).Milliseconds()
	s.logQuery(ctx, q, len(hits), time.Since(start), "ok", res.Source)
	return res, nil
}

// localSearch 是降级路径：对元数据列做 LIKE 匹配（仍然不触碰正文）。
func (s *Service) localSearch(ctx context.Context, q string, limit int) ([]Hit, error) {
	q = strings.TrimSpace(q)
	if q == "" {
		return []Hit{}, nil
	}
	like := "%" + escapeLike(q) + "%"
	rows, err := s.db.QueryContext(ctx, `SELECT id, title, author, tags, description,
		COALESCE((SELECT COUNT(*) FROM book_chapters c WHERE c.book_id = b.id AND c.title LIKE ?), 0)
		FROM books b
		WHERE b.deleted_at IS NULL AND (
			b.title LIKE ? OR b.author LIKE ? OR b.tags LIKE ? OR b.description LIKE ?
			OR EXISTS (SELECT 1 FROM book_chapters c WHERE c.book_id = b.id AND c.title LIKE ?))
		ORDER BY (CASE WHEN b.title LIKE ? THEN 0 ELSE 1 END), b.created_at DESC
		LIMIT ?`, like, like, like, like, like, like, like, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Hit{}
	for rows.Next() {
		var (
			id, title, author, tags, desc string
			chapterHits                   int
		)
		if err := rows.Scan(&id, &title, &author, &tags, &desc, &chapterHits); err != nil {
			return nil, err
		}
		field, text := "title", title
		switch {
		case strings.Contains(title, q):
			field, text = "title", title
		case strings.Contains(author, q):
			field, text = "author", author
		case strings.Contains(tags, q):
			field, text = "tags", tags
		case strings.Contains(desc, q):
			field, text = "description", desc
		case chapterHits > 0:
			field, text = "chapter_titles", "目录命中"
		}
		out = append(out, Hit{
			BookID:       id,
			Title:        title,
			Author:       author,
			MatchedField: field,
			Highlight:    markText(text, q),
		})
	}
	return out, rows.Err()
}

// escapeLike 转义 LIKE 通配符，避免用户输入被当作模式。
func escapeLike(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return r.Replace(s)
}

// markText 在降级路径里手工加高亮（Meilisearch 路径由 _formatted 提供）。
func markText(text, q string) string {
	if q == "" {
		return text
	}
	idx := strings.Index(text, q)
	if idx < 0 {
		return text
	}
	return text[:idx] + "<mark>" + text[idx:idx+len(q)] + "</mark>" + text[idx+len(q):]
}

// queryHash 用带密钥的 HMAC 生成查询指纹：库里无法反推查询词。
func (s *Service) queryHash(q string) string {
	mac := hmac.New(sha256.New, s.logKey)
	mac.Write([]byte("novelstack-search-log-v1\n"))
	mac.Write([]byte(q))
	return hex.EncodeToString(mac.Sum(nil))[:16]
}

func (s *Service) logQuery(ctx context.Context, q string, hits int, d time.Duration, ok, source string) {
	if s.logKey == nil {
		return
	}
	// 微秒级精度写入浮点毫秒：短查询也能在 P50/P95 上区分
	ms := float64(d.Microseconds()) / 1000.0
	_, err := s.db.ExecContext(ctx, `INSERT INTO search_logs(q_len, q_hash, hits, duration_ms, ok, source)
		VALUES (?,?,?,?,?,?)`, len([]rune(q)), s.queryHash(q), hits, ms, ok, source)
	if err != nil {
		log.Printf("search: log query failed")
	}
}

// Latency 返回最近 limit 次查询的 P50/P95 与样本数。
func (s *Service) Latency(ctx context.Context, limit int) (p50, p95 float64, samples int, err error) {
	if limit <= 0 || limit > 2000 {
		limit = 200
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT duration_ms FROM search_logs WHERE ok='ok' ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return 0, 0, 0, err
	}
	defer rows.Close()
	var ds []float64
	for rows.Next() {
		var d float64
		if err := rows.Scan(&d); err != nil {
			return 0, 0, 0, err
		}
		ds = append(ds, d)
	}
	if err := rows.Err(); err != nil {
		return 0, 0, 0, err
	}
	if len(ds) == 0 {
		return 0, 0, 0, nil
	}
	sort.Slice(ds, func(i, j int) bool { return ds[i] < ds[j] })
	return percentile(ds, 0.50), percentile(ds, 0.95), len(ds), nil
}

func percentile(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	idx := int(float64(len(sorted)-1) * p)
	return sorted[idx]
}

// IndexStats 汇总索引规模与健康状态（只读，供数据控制台）。
func (s *Service) IndexStats(ctx context.Context) map[string]any {
	out := map[string]any{
		"backend":    "local",
		"index":      "",
		"health":     "degraded",
		"documents":  int64(0),
		"last_error": s.LastError(),
	}
	if s.backend == nil {
		// 降级模式下「索引」就是 books 表本身：文档数按可索引书籍计
		if n, err := s.indexableBooks(ctx); err == nil {
			out["documents"] = n
		}
	}
	if s.backend != nil {
		out["backend"] = s.backend.Name()
		out["index"] = s.backend.PublishedIndex()
		hctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		if err := s.backend.Health(hctx); err != nil {
			out["health"] = "unavailable"
		} else {
			out["health"] = "ok"
			if n, err := s.backend.DocCount(hctx); err == nil {
				out["documents"] = n
			}
		}
	}
	return out
}

// indexableBooks 统计可被元数据检索覆盖的书籍数。
func (s *Service) indexableBooks(ctx context.Context) (int64, error) {
	var n int64
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM books WHERE deleted_at IS NULL`).Scan(&n)
	return n, err
}

// ---- 指标：Legado 网关成功率（只读，来自审计表）----

// GatewaySuccess 返回最近 limit 条网关请求的成功数/总数。
func GatewaySuccess(ctx context.Context, db *sql.DB, limit int) (ok, total int, err error) {
	if limit <= 0 || limit > 2000 {
		limit = 200
	}
	err = db.QueryRowContext(ctx, `SELECT COUNT(*),
		COALESCE(SUM(CASE WHEN result='ok' THEN 1 ELSE 0 END),0)
		FROM (SELECT result FROM audit_logs WHERE action LIKE 'legado\_%' ESCAPE '\'
		      ORDER BY id DESC LIMIT ?)`, limit).Scan(&total, &ok)
	return ok, total, err
}

// WriteJSON 与其它包保持一致的响应格式。
func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

// WriteErr 输出模糊化错误。
func WriteErr(w http.ResponseWriter, status int, msg string) {
	WriteJSON(w, status, map[string]string{"error": msg})
}

// ParseLimit 解析 ?limit=，非法值回落默认。
func ParseLimit(r *http.Request, def, max int) int {
	v := r.URL.Query().Get("limit")
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 1 || n > max {
		return def
	}
	return n
}
