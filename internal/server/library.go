package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"

	"novelstack/internal/books"
)

// 第八阶段：书库运营与客户端体验的服务端接口。
//
// 安全边界（本文件全部接口共同遵守）：
//   - 只读 books / reading_progress / book_chapters 的元数据列；
//   - 不查询 storage_path、wrapped_dek、任何密文文件，也不解密任何内容；
//   - 响应里不含正文字段（text / ciphertext / content）。
// 集成测试 TestLibraryNoPlaintext 会断言 JSON 中不出现这些字段名。

// ---- 收藏 ----

// handleFavoritePatch 处理 PATCH /api/books/{id}/favorite。
// 请求体 {"is_favorite": true|false}；device token 与浏览器会话都可调用。
func (s *Server) handleFavoritePatch(w http.ResponseWriter, r *http.Request, id identity) {
	if !isJSONContentType(r.Header.Get("Content-Type")) {
		writeErr(w, http.StatusUnsupportedMediaType, "application/json required")
		return
	}
	var req struct {
		IsFavorite *bool `json:"is_favorite"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<10)).Decode(&req); err != nil || req.IsFavorite == nil {
		writeErr(w, http.StatusBadRequest, "invalid json body: is_favorite required")
		return
	}
	bookID := r.PathValue("id")
	fav, err := s.books.Store.SetFavorite(r.Context(), id.username, bookID, *req.IsFavorite)
	if err != nil {
		if errors.Is(err, books.ErrNotFound) {
			s.audit.Record(r.Context(), id.username, id.ip, "book_favorite", bookID, "denied", "not found")
			writeErr(w, http.StatusNotFound, "book not found")
			return
		}
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	s.audit.Record(r.Context(), id.username, id.ip, "book_favorite", bookID, "ok", fmt.Sprintf("is_favorite=%t", fav))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "book_id": bookID, "is_favorite": fav})
}

// ---- 阅读足迹 ----

// historyItem 是足迹列表的一行：只有元数据与进度，绝无正文。
type historyItem struct {
	BookID       string  `json:"book_id"`
	Title        string  `json:"title"`
	Cover        string  `json:"cover"` // 预留：当前为空，客户端用书名首字生成占位封面
	ChapterIndex int     `json:"chapter_index"`
	ChapterTitle string  `json:"chapter_title"`
	Percent      float64 `json:"percent"`
	UpdatedAt    string  `json:"updated_at"`
}

// handleHistory 处理 GET /api/history?limit=50。
// 按最近阅读时间倒序返回；数据取自 reading_progress，join 书名与章节标题（均为元数据）。
func (s *Server) handleHistory(w http.ResponseWriter, r *http.Request, id identity) {
	limit := 50
	rows, err := s.db.QueryContext(r.Context(), `
SELECT p.book_id, b.title, p.chapter_index,
       COALESCE(bc.title, ''),
       p.percent, p.updated_at
FROM reading_progress p
JOIN books b ON b.id = p.book_id AND b.deleted_at IS NULL
LEFT JOIN book_chapters bc
       ON bc.book_id = p.book_id AND bc.chapter_index = p.chapter_index
WHERE p.owner_id = ?
ORDER BY p.updated_at DESC
LIMIT ?`, id.username, limit)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	defer rows.Close()
	out := make([]historyItem, 0, 32)
	for rows.Next() {
		var it historyItem
		if err := rows.Scan(&it.BookID, &it.Title, &it.ChapterIndex,
			&it.ChapterTitle, &it.Percent, &it.UpdatedAt); err != nil {
			writeErr(w, http.StatusInternalServerError, "internal error")
			return
		}
		out = append(out, it)
	}
	if err := rows.Err(); err != nil {
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	s.audit.Record(r.Context(), id.username, id.ip, "history_list", "", "ok", fmt.Sprintf("count=%d", len(out)))
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

// ---- 发现页 ----

// discoverBook 是发现页卡片：只有元数据，无正文、无密文路径。
type discoverBook struct {
	ID           string   `json:"id"`
	Title        string   `json:"title"`
	Author       string   `json:"author,omitempty"`
	Tags         []string `json:"tags,omitempty"`
	ChapterCount int      `json:"chapter_count"`
	CreatedAt    string   `json:"created_at"`
	IsFavorite   bool     `json:"is_favorite"`
}

type tagCount struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

type discoverResponse struct {
	Recent []discoverBook `json:"recent"`
	Tags   []tagCount     `json:"tags"`
	Random []discoverBook `json:"random"`
}

// handleDiscover 处理 GET /api/discover：最近更新、标签聚合、随机 5 本。
func (s *Server) handleDiscover(w http.ResponseWriter, r *http.Request, id identity) {
	ctx := r.Context()
	favs, _ := s.books.Store.FavoriteSet(ctx, id.username)

	recent, err := s.queryDiscoverBooks(ctx, id.username,
		`ORDER BY b.created_at DESC LIMIT 12`, favs)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	random, err := s.queryDiscoverBooks(ctx, id.username,
		`ORDER BY RANDOM() LIMIT 5`, favs)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	tags, err := s.aggregateTags(ctx, id.username)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	s.audit.Record(ctx, id.username, id.ip, "discover", "", "ok",
		fmt.Sprintf("recent=%d random=%d tags=%d", len(recent), len(random), len(tags)))
	writeJSON(w, http.StatusOK, discoverResponse{Recent: recent, Tags: tags, Random: random})
}

// queryDiscoverBooks 执行带固定 ORDER/LIMIT 片段的元数据查询。
// order 只接受本文件内的常量，绝不拼接外部输入。
func (s *Server) queryDiscoverBooks(ctx context.Context, ownerID, order string, favs map[string]bool) ([]discoverBook, error) {
	q := `SELECT b.id, b.title, b.author, b.tags, b.chapter_count, b.created_at
FROM books b WHERE b.deleted_at IS NULL AND (? = '' OR b.owner_id = ?) ` + order
	rows, err := s.db.QueryContext(ctx, q, ownerID, ownerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]discoverBook, 0, 12)
	for rows.Next() {
		var d discoverBook
		var tags string
		if err := rows.Scan(&d.ID, &d.Title, &d.Author, &tags, &d.ChapterCount, &d.CreatedAt); err != nil {
			return nil, err
		}
		d.Tags = books.SplitTags(tags)
		d.IsFavorite = favs[d.ID]
		out = append(out, d)
	}
	return out, rows.Err()
}

// aggregateTags 聚合全部标签的出现次数，取前 20 个（按次数降序、同次数按名称）。
func (s *Server) aggregateTags(ctx context.Context, ownerID string) ([]tagCount, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT tags FROM books WHERE deleted_at IS NULL AND (? = '' OR owner_id = ?)`, ownerID, ownerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	counts := map[string]int{}
	for rows.Next() {
		var tags string
		if err := rows.Scan(&tags); err != nil {
			return nil, err
		}
		for _, t := range books.SplitTags(tags) {
			counts[t]++
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]tagCount, 0, len(counts))
	for name, n := range counts {
		out = append(out, tagCount{Name: name, Count: n})
	}
	// 稳定排序：次数降序，名称升序。
	sortTags(out)
	if len(out) > 20 {
		out = out[:20]
	}
	return out, nil
}

// ---- App 版本 ----

type appVersionResponse struct {
	Version   string   `json:"version"`
	Changelog string   `json:"changelog"`
	Mirrors   []string `json:"mirrors"`
}

// handleAPKDownload 处理 GET /download/novelstack.apk。
//
// 这是唯一一个「服务端主动暴露文件」的口子，因此限制极严：
//   - 路径只从 APP_APK_FILE 读，永远不接请求里的 path/name 参数；
//   - 未配置或文件不存在直接 404，不回退到目录列表；
//   - 用 http.ServeFile，天然支持 Range（Android 更新器会先探测前 100KB）。
func (s *Server) handleAPKDownload(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimSpace(s.cfg.AppAPKFile)
	if path == "" {
		writeErr(w, http.StatusNotFound, "apk not configured")
		return
	}
	st, err := os.Stat(path)
	if err != nil || st.IsDir() {
		writeErr(w, http.StatusNotFound, "apk not found")
		return
	}
	w.Header().Set("Content-Type", "application/vnd.android.package-archive")
	w.Header().Set("Content-Length", strconv.FormatInt(st.Size(), 10))
	w.Header().Set("Content-Disposition", "attachment; filename=novelstack.apk")
	w.Header().Set("Cache-Control", "public, max-age=300")
	http.ServeFile(w, r, path)
}

// handleAppVersion 处理 GET /api/app/version。镜像列表来自配置（环境变量注入）。
func (s *Server) handleAppVersion(w http.ResponseWriter, r *http.Request, id identity) {
	ver := strings.TrimSpace(s.cfg.AppVersion)
	if ver == "" {
		ver = "0.1.0" // 配置缺省时的兜底，与 FromEnv 默认一致
	}
	mirrors := s.cfg.AppMirrors
	if mirrors == nil {
		mirrors = []string{}
	}
	writeJSON(w, http.StatusOK, appVersionResponse{
		Version:   ver,
		Changelog: s.cfg.AppChangelog,
		Mirrors:   mirrors,
	})
}

// handleAppLatestHash 处理 GET /api/app/version/latest-hash。
// 未配置哈希时返回 404，客户端据此提示「服务端未提供校验值」而不是安装未校验的包。
func (s *Server) handleAppLatestHash(w http.ResponseWriter, r *http.Request, id identity) {
	hash := strings.TrimSpace(s.cfg.AppAPKSHA256)
	if hash == "" {
		writeErr(w, http.StatusNotFound, "latest hash not configured")
		return
	}
	ver := strings.TrimSpace(s.cfg.AppVersion)
	if ver == "" {
		ver = "0.1.0"
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"version": ver,
		"sha256":  hash,
	})
}

// sortTags 对标签聚合结果排序：次数降序，名称升序。
func sortTags(items []tagCount) {
	sort.Slice(items, func(i, j int) bool {
		if items[i].Count != items[j].Count {
			return items[i].Count > items[j].Count
		}
		return items[i].Name < items[j].Name
	})
}
