// Package clientcore 是「客户端本地全文搜索」层的可执行参考实现。
//
// 分层模型（第三阶段）：服务端只索引元数据，正文全文搜索必须在设备本地完成。
// 本包演示了完整数据流：
//
//	GET /chapters/{i}/plain （ETag 协商）→ 本地解密/落库 → FTS5 建索引 → BM25 检索 → 高亮片段
//
// 落盘形态：整库密文快照（AES-256-GCM，密钥来自 KeyProvider）。
// 这与移动端 SQLCipher 的整体库加密语义一致：磁盘上没有任何明文索引或正文；
// 索引常驻内存，退出时加密回写。KeyProvider 在 Android/iOS 上由
// Keystore / Keychain 包裹的随机密钥实现（见 README「路线 B 数据层映射」）。
package clientcore

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"

	_ "modernc.org/sqlite"
)

// 快照格式常量。
const (
	snapshotMagic   = "NSIDX1"
	snapshotVersion = 1
)

// KeyProvider 提供设备数据密钥（32 字节）。
// 生产实现：Android Keystore / iOS Keychain 包裹的随机密钥，绝不硬编码。
type KeyProvider interface {
	Key(ctx context.Context) ([]byte, error)
}

// ChapterRecord 是一章解密后的本地记录（仅驻留内存，落盘时整库加密）。
type ChapterRecord struct {
	BookID    string `json:"book_id"`
	Index     int    `json:"index"`
	Title     string `json:"title"`
	Text      string `json:"text"`
	ETag      string `json:"etag"`
	UpdatedAt string `json:"updated_at"`
}

// BookRef 是本地已缓存的书籍。
type BookRef struct {
	BookID   string `json:"book_id"`
	Title    string `json:"title"`
	Author   string `json:"author"`
	Chapters int    `json:"chapters"`
}

// Hit 是一条本地检索结果，Snippet 已带 <mark> 高亮。
type Hit struct {
	BookID       string  `json:"book_id"`
	BookTitle    string  `json:"book_title"`
	ChapterIndex int     `json:"chapter_index"`
	ChapterTitle string  `json:"chapter_title"`
	Snippet      string  `json:"snippet"`
	Score        float64 `json:"score"`
}

// Store 是本地索引库：内存 SQLite + FTS5，落盘为单个密文快照。
type Store struct {
	mu       sync.Mutex
	db       *sql.DB
	key      []byte
	path     string
	dirty    bool
	clientID string
}

var dbSeq int64

// Open 打开（或创建）本地索引库。path 是密文快照路径。
func Open(path string, kp KeyProvider, clientID string) (*Store, error) {
	if kp == nil {
		return nil, errors.New("clientcore: 需要 KeyProvider")
	}
	key, err := kp.Key(context.Background())
	if err != nil {
		return nil, fmt.Errorf("clientcore: 获取设备密钥失败: %w", err)
	}
	if len(key) != 32 {
		return nil, fmt.Errorf("clientcore: 设备密钥必须是 32 字节，收到 %d", len(key))
	}
	if clientID == "" {
		clientID = "default"
	}
	dsn := fmt.Sprintf("file:clientidx-%d?mode=memory&cache=shared", atomic.AddInt64(&dbSeq, 1))
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	s := &Store{db: db, key: key, path: path, clientID: clientID}
	if err := s.initSchema(); err != nil {
		db.Close()
		return nil, err
	}
	if err := s.load(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// Close 把内存库加密写回磁盘并释放连接。
func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	err := s.flushLocked()
	if cerr := s.db.Close(); err == nil {
		err = cerr
	}
	return err
}

// ClientID 返回本地客户端标识（用于 /ack 回执）。
func (s *Store) ClientID() string { return s.clientID }

func (s *Store) initSchema() error {
	stmts := []string{
		`CREATE TABLE chapters (
			book_id TEXT NOT NULL,
			chapter_index INTEGER NOT NULL,
			title TEXT NOT NULL DEFAULT '',
			text TEXT NOT NULL,
			etag TEXT NOT NULL DEFAULT '',
			updated_at TEXT NOT NULL DEFAULT '',
			PRIMARY KEY (book_id, chapter_index)
		)`,
		`CREATE TABLE book_meta (
			book_id TEXT PRIMARY KEY,
			title TEXT NOT NULL DEFAULT '',
			author TEXT NOT NULL DEFAULT ''
		)`,
		// FTS5：逐字分词（见 segmentText），中文短语查询可命中任意子串
		`CREATE VIRTUAL TABLE chapter_fts USING fts5(body, tokenize='unicode61')`,
	}
	for _, q := range stmts {
		if _, err := s.db.Exec(q); err != nil {
			return fmt.Errorf("clientcore: 初始化本地库失败: %w", err)
		}
	}
	return nil
}

// Chapter 返回本地缓存的章节文本（未缓存时 ok=false）。
func (s *Store) Chapter(bookID string, index int) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var text string
	if err := s.db.QueryRow(`SELECT text FROM chapters WHERE book_id = ? AND chapter_index = ?`,
		bookID, index).Scan(&text); err != nil {
		return "", false
	}
	return text, true
}

// PutBook 记录书名/作者（用于搜索结果展示）。
func (s *Store) PutBook(bookID, title, author string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(`INSERT INTO book_meta(book_id, title, author) VALUES (?,?,?)
		ON CONFLICT(book_id) DO UPDATE SET title=excluded.title, author=excluded.author`,
		bookID, title, author)
	if err == nil {
		s.dirty = true
	}
	return err
}

// CachedETag 返回本地已缓存章节的 ETag（未缓存返回空）。
func (s *Store) CachedETag(bookID string, index int) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var etag string
	err := s.db.QueryRow(`SELECT etag FROM chapters WHERE book_id = ? AND chapter_index = ?`,
		bookID, index).Scan(&etag)
	if err != nil {
		return "", false
	}
	return etag, true
}

// PutChapter 写入/更新一章，并重建该章的 FTS 索引（幂等）。
func (s *Store) PutChapter(rec ChapterRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if rec.UpdatedAt == "" {
		rec.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck

	var rowid int64
	err = tx.QueryRow(`SELECT rowid FROM chapters WHERE book_id = ? AND chapter_index = ?`,
		rec.BookID, rec.Index).Scan(&rowid)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		res, ierr := tx.Exec(`INSERT INTO chapters(book_id, chapter_index, title, text, etag, updated_at)
			VALUES (?,?,?,?,?,?)`, rec.BookID, rec.Index, rec.Title, rec.Text, rec.ETag, rec.UpdatedAt)
		if ierr != nil {
			return ierr
		}
		rowid, _ = res.LastInsertId()
	case err != nil:
		return err
	default:
		if _, err := tx.Exec(`UPDATE chapters SET title=?, text=?, etag=?, updated_at=? WHERE rowid=?`,
			rec.Title, rec.Text, rec.ETag, rec.UpdatedAt, rowid); err != nil {
			return err
		}
		if _, err := tx.Exec(`DELETE FROM chapter_fts WHERE rowid = ?`, rowid); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`INSERT INTO chapter_fts(rowid, body) VALUES (?, ?)`,
		rowid, segmentText(rec.Text)); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.dirty = true
	return nil
}

// Search 在本地 FTS5 索引上检索，返回带高亮的片段。
// 查询词、索引、正文都不离开本机。
func (s *Store) Search(q string, limit int) ([]Hit, error) {
	q = strings.TrimSpace(q)
	if q == "" {
		return nil, errors.New("clientcore: 空查询")
	}
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	expr := ftsPhrase(q)
	if expr == "" {
		return nil, errors.New("clientcore: 查询词无效")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.Query(`SELECT c.book_id, c.chapter_index, c.title, c.text,
			COALESCE(m.title, ''), bm25(chapter_fts)
		FROM chapter_fts
		JOIN chapters c ON c.rowid = chapter_fts.rowid
		LEFT JOIN book_meta m ON m.book_id = c.book_id
		WHERE chapter_fts MATCH ?
		ORDER BY bm25(chapter_fts) ASC
		LIMIT ?`, expr, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Hit{}
	for rows.Next() {
		var (
			h    Hit
			text string
			rank float64
		)
		if err := rows.Scan(&h.BookID, &h.ChapterIndex, &h.ChapterTitle, &text, &h.BookTitle, &rank); err != nil {
			return nil, err
		}
		h.Snippet = snippet(text, q, 48)
		h.Score = -rank // bm25 越小越相关，取负便于阅读
		out = append(out, h)
	}
	return out, rows.Err()
}

// Books 返回本地已缓存的书籍列表。
func (s *Store) Books() ([]BookRef, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.Query(`SELECT c.book_id, COALESCE(m.title,''), COALESCE(m.author,''), COUNT(*)
		FROM chapters c LEFT JOIN book_meta m ON m.book_id = c.book_id
		GROUP BY c.book_id ORDER BY c.book_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []BookRef{}
	for rows.Next() {
		var b BookRef
		if err := rows.Scan(&b.BookID, &b.Title, &b.Author, &b.Chapters); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// Stats 返回本地索引规模（章节数与正文字符数），供调试与「已缓存」提示。
func (s *Store) Stats() (chapters int, chars int64, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	err = s.db.QueryRow(`SELECT COUNT(*), COALESCE(SUM(LENGTH(text)),0) FROM chapters`).
		Scan(&chapters, &chars)
	return chapters, chars, err
}

// ---- FTS5 辅助 ----

// segmentText 把文本逐字加空格，使 unicode61 分词器对中文也按字建索引。
// 这样「推开门」这类任意子串都能用短语查询命中，不需要额外分词依赖。
func segmentText(s string) string {
	var b strings.Builder
	b.Grow(len(s) * 2)
	for _, r := range s {
		if unicode.IsSpace(r) {
			b.WriteByte(' ')
			continue
		}
		b.WriteRune(r)
		b.WriteByte(' ')
	}
	return b.String()
}

// ftsPhrase 把查询词转成 FTS5 短语表达式：逐字 token 之间用空格，整体加引号。
func ftsPhrase(q string) string {
	parts := make([]string, 0, len(q))
	for _, r := range q {
		if unicode.IsSpace(r) || r == '"' || r == '\'' {
			continue
		}
		parts = append(parts, string(r))
	}
	if len(parts) == 0 {
		return ""
	}
	return `"` + strings.Join(parts, " ") + `"`
}

// snippet 在原文里定位查询词并生成带 <mark> 的高亮片段。
func snippet(text, q string, window int) string {
	runes := []rune(text)
	qRunes := []rune(q)
	idx := indexRunes(runes, qRunes)
	if idx < 0 {
		// 命中可能来自分词差异：退回开头一段
		if len(runes) > window*2 {
			return string(runes[:window*2]) + "…"
		}
		return string(runes)
	}
	start := idx - window
	if start < 0 {
		start = 0
	}
	end := idx + len(qRunes) + window
	if end > len(runes) {
		end = len(runes)
	}
	prefix, suffix := "", ""
	if start > 0 {
		prefix = "…"
	}
	if end < len(runes) {
		suffix = "…"
	}
	return prefix + string(runes[start:idx]) + "<mark>" + string(runes[idx:idx+len(qRunes)]) +
		"</mark>" + string(runes[idx+len(qRunes):end]) + suffix
}

func indexRunes(hay, needle []rune) int {
	if len(needle) == 0 || len(needle) > len(hay) {
		return -1
	}
	for i := 0; i+len(needle) <= len(hay); i++ {
		match := true
		for j := range needle {
			if unicode.ToLower(hay[i+j]) != unicode.ToLower(needle[j]) {
				match = false
				break
			}
		}
		if match {
			return i
		}
	}
	return -1
}

// ---- 密文快照 ----

type snapshot struct {
	Version  int             `json:"version"`
	SavedAt  string          `json:"saved_at"`
	Books    []BookRef       `json:"books"`
	Chapters []ChapterRecord `json:"chapters"`
}

// flushLocked 把整库导出为 JSON 并用设备密钥加密写回（原子替换）。
func (s *Store) flushLocked() error {
	if !s.dirty {
		return nil
	}
	books, err := s.booksLocked()
	if err != nil {
		return err
	}
	chapters, err := s.chaptersLocked()
	if err != nil {
		return err
	}
	snap := snapshot{Version: snapshotVersion, SavedAt: time.Now().UTC().Format(time.RFC3339),
		Books: books, Chapters: chapters}
	plain, err := json.Marshal(snap)
	if err != nil {
		return err
	}
	sealed, err := seal(s.key, plain)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, sealed, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, s.path); err != nil {
		return err
	}
	s.dirty = false
	return nil
}

func (s *Store) booksLocked() ([]BookRef, error) {
	rows, err := s.db.Query(`SELECT book_id, title, author FROM book_meta ORDER BY book_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []BookRef{}
	for rows.Next() {
		var b BookRef
		if err := rows.Scan(&b.BookID, &b.Title, &b.Author); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

func (s *Store) chaptersLocked() ([]ChapterRecord, error) {
	rows, err := s.db.Query(`SELECT book_id, chapter_index, title, text, etag, updated_at
		FROM chapters ORDER BY book_id, chapter_index`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ChapterRecord{}
	for rows.Next() {
		var c ChapterRecord
		if err := rows.Scan(&c.BookID, &c.Index, &c.Title, &c.Text, &c.ETag, &c.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// load 读取并解密快照，然后把章节重新灌入内存库并重建 FTS 索引。
func (s *Store) load() error {
	raw, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	plain, err := open(s.key, raw)
	if err != nil {
		return fmt.Errorf("clientcore: 本地索引快照解密失败（密钥不匹配或被篡改）: %w", err)
	}
	var snap snapshot
	if err := json.Unmarshal(plain, &snap); err != nil {
		return err
	}
	if snap.Version != snapshotVersion {
		return fmt.Errorf("clientcore: 不支持的快照版本 %d", snap.Version)
	}
	for _, b := range snap.Books {
		if _, err := s.db.Exec(`INSERT OR REPLACE INTO book_meta(book_id, title, author) VALUES (?,?,?)`,
			b.BookID, b.Title, b.Author); err != nil {
			return err
		}
	}
	for _, c := range snap.Chapters {
		if err := s.putChapterLocked(c); err != nil {
			return err
		}
	}
	s.dirty = false
	return nil
}

// putChapterLocked 是 PutChapter 的无锁版本（仅启动装载时使用）。
func (s *Store) putChapterLocked(rec ChapterRecord) error {
	res, err := s.db.Exec(`INSERT OR REPLACE INTO chapters(book_id, chapter_index, title, text, etag, updated_at)
		VALUES (?,?,?,?,?,?)`, rec.BookID, rec.Index, rec.Title, rec.Text, rec.ETag, rec.UpdatedAt)
	if err != nil {
		return err
	}
	rowid, err := res.LastInsertId()
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`INSERT INTO chapter_fts(rowid, body) VALUES (?, ?)`, rowid, segmentText(rec.Text))
	return err
}

// seal 加密快照：magic || nonce || ciphertext+tag。
func seal(key, plain []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	out := append([]byte(snapshotMagic), nonce...)
	return gcm.Seal(out, nonce, plain, []byte(snapshotMagic)), nil
}

// open 解密快照。
func open(key, raw []byte) ([]byte, error) {
	if len(raw) < len(snapshotMagic) {
		return nil, errors.New("快照文件过短")
	}
	if string(raw[:len(snapshotMagic)]) != snapshotMagic {
		return nil, errors.New("快照 magic 不匹配")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	body := raw[len(snapshotMagic):]
	if len(body) < gcm.NonceSize() {
		return nil, errors.New("快照损坏")
	}
	nonce, ct := body[:gcm.NonceSize()], body[gcm.NonceSize():]
	return gcm.Open(nil, nonce, ct, []byte(snapshotMagic))
}

// ErrNotModified 表示服务端返回 304（本地已是最新）。
var ErrNotModified = errors.New("clientcore: not modified")
