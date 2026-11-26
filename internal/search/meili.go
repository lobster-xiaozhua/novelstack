package search

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Meili 是 Meilisearch 的最小 HTTP 客户端（不引入 SDK，保持依赖面最小）。
// 只使用文档 API 与设置 API；主后端不依赖 Meilisearch 的内部实现细节。
type Meili struct {
	BaseURL string
	APIKey  string
	Index   string
	HTTP    *http.Client
}

// NewMeili 构造客户端；baseURL 形如 http://meilisearch:7700。
func NewMeili(baseURL, apiKey, index string, timeout time.Duration) *Meili {
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	return &Meili{
		BaseURL: strings.TrimRight(baseURL, "/"),
		APIKey:  apiKey,
		Index:   index,
		HTTP:    &http.Client{Timeout: timeout},
	}
}

func (m *Meili) Name() string           { return "meilisearch" }
func (m *Meili) PublishedIndex() string { return m.Index }

// do 发送请求并返回 (状态码, 响应体)。
func (m *Meili) do(ctx context.Context, method, path string, body any) (int, []byte, error) {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, nil, err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, m.BaseURL+path, rdr)
	if err != nil {
		return 0, nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if m.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+m.APIKey)
	}
	resp, err := m.HTTP.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return resp.StatusCode, nil, err
	}
	if resp.StatusCode >= 300 {
		return resp.StatusCode, b, fmt.Errorf("search: meilisearch %s %s -> %d", method, path, resp.StatusCode)
	}
	return resp.StatusCode, b, nil
}

// EnsureSchema 先确保索引存在，再固定索引设置。
// Meilisearch 对「不存在的索引」执行 PUT settings 会返回 405，因此必须先创建索引。
// 关键点：searchableAttributes 只列元数据字段——即使有人误写入正文，
// 也不会被检索到；displayedAttributes 同样限制，避免接口回吐非预期字段。
func (m *Meili) EnsureSchema(ctx context.Context) error {
	// 幂等创建索引：已存在时返回 409（code=index_already_exists），忽略即可
	if status, body, err := m.do(ctx, http.MethodPost, "/indexes",
		map[string]any{"uid": m.Index, "primaryKey": "id"}); err != nil {
		if status != http.StatusConflict && !strings.Contains(string(body), "index_already_exists") {
			return err
		}
	}
	settings := map[string]any{
		"searchableAttributes": []string{
			"title", "author", "tags", "description", "chapter_titles",
		},
		"displayedAttributes": []string{
			"id", "title", "author", "tags", "description",
			"chapter_titles", "chapter_count", "size_bytes", "created_at",
		},
		"filterableAttributes": []string{"tags"},
		"sortableAttributes":   []string{"created_at"},
		// 中文没有空格分词，Meilisearch 自带 CJK 分段；保留前缀搜索与容错
		"prefixSearch":  "indexingTime",
		"typoTolerance": map[string]any{"enabled": true, "minWordSizeForTypos": map[string]int{"oneTypo": 4, "twoTypos": 8}},
		"pagination":    map[string]any{"maxTotalHits": 200},
		"faceting":      map[string]any{"maxValuesPerFacet": 20},
	}
	// Meilisearch v1.x 用 PATCH 更新设置（PUT 会返回 405）
	_, _, err := m.do(ctx, http.MethodPatch, "/indexes/"+m.Index+"/settings", settings)
	return err
}

// Upsert 批量写入/更新文档。发出前逐条校验字段白名单。
func (m *Meili) Upsert(ctx context.Context, docs []Document) error {
	if len(docs) == 0 {
		return nil
	}
	raw := make([]json.RawMessage, 0, len(docs))
	for _, d := range docs {
		b, err := json.Marshal(d)
		if err != nil {
			return err
		}
		if err := ValidateDocumentJSON(b); err != nil {
			return err
		}
		raw = append(raw, b)
	}
	body := json.RawMessage("[" + joinRaw(raw) + "]")
	_, _, err := m.do(ctx, http.MethodPost, "/indexes/"+m.Index+"/documents?primaryKey=id", body)
	return err
}

func joinRaw(items []json.RawMessage) string {
	var b strings.Builder
	for i, it := range items {
		if i > 0 {
			b.WriteByte(',')
		}
		b.Write(it)
	}
	return b.String()
}

// Delete 删除单个文档（书籍软删除时调用）。
func (m *Meili) Delete(ctx context.Context, id string) error {
	_, _, err := m.do(ctx, http.MethodDelete, "/indexes/"+m.Index+"/documents/"+id, nil)
	if err != nil && strings.Contains(err.Error(), "-> 404") {
		return nil // 索引或文档不存在视为已删除
	}
	return err
}

// searchResponse 只取需要的字段：_formatted 提供高亮。
type searchResponse struct {
	Hits []map[string]json.RawMessage `json:"hits"`
}

// Search 执行查询并归一化为 Hit 列表。
func (m *Meili) Search(ctx context.Context, q string, limit int) ([]Hit, error) {
	body := map[string]any{
		"q":                     q,
		"limit":                 limit,
		"attributesToHighlight": []string{"title", "author", "tags", "description", "chapter_titles"},
		"highlightPreTag":       "<mark>",
		"highlightPostTag":      "</mark>",
		"attributesToRetrieve":  []string{"id", "title", "author"},
		"showRankingScore":      false,
	}
	status, b, err := m.do(ctx, http.MethodPost, "/indexes/"+m.Index+"/search", body)
	if err != nil {
		return nil, fmt.Errorf("%w (status=%d)", err, status)
	}
	var sr searchResponse
	if err := json.Unmarshal(b, &sr); err != nil {
		return nil, err
	}
	out := make([]Hit, 0, len(sr.Hits))
	for _, h := range sr.Hits {
		hit := Hit{
			BookID: str(h["id"]),
			Title:  str(h["title"]),
			Author: str(h["author"]),
		}
		hit.MatchedField, hit.Highlight = pickHighlight(h["_formatted"])
		out = append(out, hit)
	}
	return out, nil
}

// pickHighlight 找出第一个命中的字段并提供其高亮文本。
func pickHighlight(raw json.RawMessage) (field, text string) {
	if len(raw) == 0 {
		return "", ""
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return "", ""
	}
	order := []string{"title", "author", "tags", "description", "chapter_titles"}
	for _, f := range order {
		v, ok := m[f]
		if !ok {
			continue
		}
		s := str(v)
		if strings.Contains(s, "<mark>") {
			return f, s
		}
	}
	// 没有高亮时退回标题原文
	if v, ok := m["title"]; ok {
		return "title", str(v)
	}
	return "", ""
}

// str 兼容字符串与字符串数组两种 _formatted 形态。
func str(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var arr []string
	if err := json.Unmarshal(raw, &arr); err == nil {
		return strings.Join(arr, " / ")
	}
	return ""
}

// DocIDs 返回索引内全部文档 ID（用于全量同步后清理残留）。
func (m *Meili) DocIDs(ctx context.Context) ([]string, error) {
	body := map[string]any{"q": "", "limit": 1000, "attributesToRetrieve": []string{"id"}}
	_, b, err := m.do(ctx, http.MethodPost, "/indexes/"+m.Index+"/search", body)
	if err != nil {
		return nil, err
	}
	var sr searchResponse
	if err := json.Unmarshal(b, &sr); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(sr.Hits))
	for _, h := range sr.Hits {
		if id := str(h["id"]); id != "" {
			out = append(out, id)
		}
	}
	return out, nil
}

// DocCount 读取索引文档数。
func (m *Meili) DocCount(ctx context.Context) (int64, error) {
	_, b, err := m.do(ctx, http.MethodGet, "/indexes/"+m.Index+"/stats", nil)
	if err != nil {
		return 0, err
	}
	var st struct {
		NumberOfDocuments int64 `json:"numberOfDocuments"`
	}
	if err := json.Unmarshal(b, &st); err != nil {
		return 0, err
	}
	return st.NumberOfDocuments, nil
}

// Health 轻量健康检查（/health，不需要鉴权）。
func (m *Meili) Health(ctx context.Context) error {
	_, _, err := m.do(ctx, http.MethodGet, "/health", nil)
	return err
}
