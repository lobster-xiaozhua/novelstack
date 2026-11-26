package search

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"novelstack/internal/db"
)

// fakeMeili 记录收到的请求，用于断言「只发元数据字段」。
type fakeMeili struct {
	mu       sync.Mutex
	settings map[string]any
	docs     []map[string]any
	searches []map[string]any
	deleted  []string
}

func (f *fakeMeili) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/indexes", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"code":"index_already_exists","message":"Index already exists."}`))
	})
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"status":"available"}`))
	})
	mux.HandleFunc("/indexes/books/settings", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		f.settings = body
		f.mu.Unlock()
		_, _ = w.Write([]byte(`{"taskUid":1}`))
	})
	mux.HandleFunc("/indexes/books/documents", func(w http.ResponseWriter, r *http.Request) {
		var docs []map[string]any
		_ = json.NewDecoder(r.Body).Decode(&docs)
		f.mu.Lock()
		f.docs = append(f.docs, docs...)
		f.mu.Unlock()
		_, _ = w.Write([]byte(`{"taskUid":2}`))
	})
	mux.HandleFunc("/indexes/books/documents/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			f.mu.Lock()
			f.deleted = append(f.deleted, strings.TrimPrefix(r.URL.Path, "/indexes/books/documents/"))
			f.mu.Unlock()
			_, _ = w.Write([]byte(`{"taskUid":3}`))
			return
		}
		http.NotFound(w, r)
	})
	mux.HandleFunc("/indexes/books/search", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		f.searches = append(f.searches, body)
		f.mu.Unlock()
		if body["q"] == "" { // DocIDs 调用
			_, _ = w.Write([]byte(`{"hits":[{"id":"b1"},{"id":"b2"}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"hits":[{"id":"b1","title":"夜航船","author":"阿来",
			"_formatted":{"title":"<mark>夜航船</mark>","author":"阿来"}}]}`))
	})
	mux.HandleFunc("/indexes/books/stats", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"numberOfDocuments":2}`))
	})
	return mux
}

func TestMeiliSchemaAndDocumentsExcludeContent(t *testing.T) {
	f := &fakeMeili{}
	ts := httptest.NewServer(f.handler())
	defer ts.Close()

	m := NewMeili(ts.URL, "test-key", "books", 3*time.Second)
	ctx := context.Background()
	if err := m.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	settings := f.settings
	f.mu.Unlock()

	searchable, _ := settings["searchableAttributes"].([]any)
	if len(searchable) == 0 {
		t.Fatal("searchableAttributes 缺失")
	}
	banned := []string{"content", "text", "body", "plain", "chapter_body"}
	for _, a := range searchable {
		for _, b := range banned {
			if strings.EqualFold(a.(string), b) {
				t.Fatalf("索引设置包含正文字段: %v", a)
			}
		}
	}

	docs := []Document{{
		ID: "b1", Title: "夜航船", Author: "阿来", Tags: []string{"海上"},
		Description: "简介", ChapterTitles: []string{"第一章 起"}, ChapterCount: 2,
		SizeBytes: 10, CreatedAt: "2026-01-01",
	}}
	if err := m.Upsert(ctx, docs); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.docs) != 1 {
		t.Fatalf("文档未送达: %d", len(f.docs))
	}
	for k := range f.docs[0] {
		found := false
		for _, allowed := range AllowedDocFields {
			if k == allowed {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("索引文档出现白名单外字段: %q", k)
		}
	}
	if _, ok := f.docs[0]["content"]; ok {
		t.Fatal("索引文档不得包含 content")
	}
}

func TestMeiliSearchMapsHighlight(t *testing.T) {
	f := &fakeMeili{}
	ts := httptest.NewServer(f.handler())
	defer ts.Close()
	m := NewMeili(ts.URL, "", "books", 3*time.Second)
	hits, err := m.Search(context.Background(), "夜航船", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 {
		t.Fatalf("hits=%d", len(hits))
	}
	if hits[0].BookID != "b1" || hits[0].MatchedField != "title" {
		t.Fatalf("映射错误: %+v", hits[0])
	}
	if !strings.Contains(hits[0].Highlight, "<mark>夜航船</mark>") {
		t.Fatalf("高亮丢失: %q", hits[0].Highlight)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.searches) != 1 {
		t.Fatalf("搜索请求数=%d", len(f.searches))
	}
	attrs, _ := f.searches[0]["attributesToHighlight"].([]any)
	for _, a := range attrs {
		if a == "content" || a == "text" {
			t.Fatalf("不应请求正文高亮: %v", attrs)
		}
	}
}

// 后端不可用时，Service 必须降级而不是把错误抛给客户端。
func TestServiceDegradesWhenBackendDown(t *testing.T) {
	f := &fakeMeili{}
	ts := httptest.NewServer(f.handler())
	m := NewMeili(ts.URL, "", "books", 50*time.Millisecond)
	ts.Close() // 直接关掉后端

	database, err := db.Open(t.TempDir() + "/t.db")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	s := NewService(database, m, nil, 50*time.Millisecond, []byte("0123456789abcdef"))
	// 不插入任何书籍：降级到本地匹配，返回空结果而不是错误
	res, err := s.Search(context.Background(), "任意", 5)
	if err != nil {
		t.Fatalf("降级失败: %v", err)
	}
	if !res.Degraded || res.Source != "local" {
		t.Fatalf("未标记降级: %+v", res)
	}
}
