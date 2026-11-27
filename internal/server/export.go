package server

import (
	"archive/zip"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"novelstack/internal/books"
	"novelstack/internal/export"
)

// exportPreview 返回可导出的书籍清单（不含正文），供控制台勾选。
func (s *Server) handleExportPreview(w http.ResponseWriter, r *http.Request, id identity) {
	list, err := s.books.Store.List(r.Context(), id.username)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	type item struct {
		ID        string `json:"id"`
		Title     string `json:"title"`
		Author    string `json:"author,omitempty"`
		Chapters  int    `json:"chapter_count"`
		SizeBytes int64  `json:"size_bytes"`
	}
	out := make([]item, 0, len(list))
	for _, b := range list {
		out = append(out, item{
			ID: b.ID, Title: b.Title, Author: b.Author,
			Chapters: b.ChapterCount, SizeBytes: b.SizeBytes,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"books":     out,
		"max_books": s.cfg.ExportMaxBooks,
		"formats":   []string{string(export.FormatTXT), string(export.FormatEPUB)},
	})
}

// handleExport 处理 POST /api/admin/export：
// 管理员会话 + 当前账户密码二次验证，把指定书籍解密后打包为 TXT/EPUB 的 ZIP 流。
//
// 安全约束：
//   - 密码错误按 IP 限流，返回统一 401，不区分「密码错」与「用户不存在」；
//   - 审计只记录书数与格式，绝不记录密码、书名或正文；
//   - ZIP 内文件名经过清洗，杜绝路径穿越。
func (s *Server) handleExport(w http.ResponseWriter, r *http.Request, id identity) {
	if !isJSONContentType(r.Header.Get("Content-Type")) {
		writeErr(w, http.StatusUnsupportedMediaType, "application/json required")
		return
	}
	var req struct {
		Password string   `json:"password"`
		BookIDs  []string `json:"book_ids"`
		Format   string   `json:"format"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json body")
		return
	}
	if !s.limiter.Allow(id.ip) {
		s.audit.Record(r.Context(), id.username, id.ip, "export_ratelimited", "", "ratelimited", "")
		writeErr(w, http.StatusTooManyRequests, "too many attempts, try later")
		return
	}
	if strings.TrimSpace(req.Password) == "" {
		writeErr(w, http.StatusBadRequest, "password required")
		return
	}
	ok, err := verifyLogin(r.Context(), s.sessions, id.username, req.Password)
	if err != nil || !ok {
		s.audit.Record(r.Context(), id.username, id.ip, "export_denied", "", "denied", "bad password")
		writeErr(w, http.StatusUnauthorized, "invalid password")
		return
	}

	format := export.Format(strings.ToLower(strings.TrimSpace(req.Format)))
	if format == "" {
		format = export.FormatTXT
	}
	if format != export.FormatTXT && format != export.FormatEPUB {
		writeErr(w, http.StatusBadRequest, "unsupported format")
		return
	}

	all, err := s.books.Store.List(r.Context(), id.username)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	selected, err := pickBooks(all, req.BookIDs, s.cfg.ExportMaxBooks)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if len(selected) == 0 {
		writeErr(w, http.StatusNotFound, "no books to export")
		return
	}

	browser := &export.Builder{Books: s.books}
	zipName := fmt.Sprintf("novelstack-%s-%s.zip", time.Now().Format("20060102-1504"), format)
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", zipName))
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)

	zw := zip.NewWriter(w)
	used := make(map[string]int, len(selected))
	written := 0
	for _, b := range selected {
		item, berr := browser.Build(r.Context(), id.username, b.ID)
		if berr != nil {
			// 单本失败不中断整包：客户端仍能拿到其余可读的书。
			s.audit.Record(r.Context(), id.username, id.ip, "export_book_error", b.ID, "error", "decrypt failed")
			continue
		}
		name := export.EntryName(used, item.Title, string(format))
		fw, cerr := zw.Create(name)
		if cerr != nil {
			break
		}
		if format == export.FormatTXT {
			if err := export.WriteTXT(fw, item); err != nil {
				break
			}
		} else {
			data, eerr := export.BuildEPUB(item)
			if eerr != nil {
				break
			}
			if _, werr := fw.Write(data); werr != nil {
				break
			}
		}
		written++
	}
	_ = zw.Close()
	s.audit.Record(r.Context(), id.username, id.ip, "export", "", "ok",
		fmt.Sprintf("format=%s books=%d/%d", format, written, len(selected)))
}

// pickBooks 按请求挑选书籍：ids 为空表示全部；同时施加数量上限。
func pickBooks(all []books.Book, ids []string, max int) ([]books.Book, error) {
	if max <= 0 {
		max = 200
	}
	if len(ids) == 0 {
		if len(all) > max {
			return nil, fmt.Errorf("最多一次导出 %d 本，请分批选择", max)
		}
		out := append([]books.Book(nil), all...)
		sort.SliceStable(out, func(i, j int) bool { return out[i].CreatedAt < out[j].CreatedAt })
		return out, nil
	}
	if len(ids) > max {
		return nil, fmt.Errorf("最多一次导出 %d 本，请分批选择", max)
	}
	byID := make(map[string]books.Book, len(all))
	for _, b := range all {
		byID[b.ID] = b
	}
	out := make([]books.Book, 0, len(ids))
	seen := make(map[string]bool, len(ids))
	for _, want := range ids {
		want = strings.TrimSpace(want)
		if want == "" || seen[want] {
			continue
		}
		b, ok := byID[want]
		if !ok {
			continue // 不存在或不属于当前账户：静默跳过，避免泄漏其他用户的书是否存在
		}
		seen[want] = true
		out = append(out, b)
	}
	return out, nil
}
