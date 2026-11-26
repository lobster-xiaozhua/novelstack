package search

import (
	"context"
	"errors"
	"reflect"
	"sort"
	"strings"
	"testing"

	"novelstack/internal/db"
)

// 白名单与结构体字段必须严格一致：新增字段若忘记登记，这里会失败。
func TestDocumentFieldsMatchAllowlist(t *testing.T) {
	typ := reflect.TypeOf(Document{})
	got := make([]string, 0, typ.NumField())
	for i := 0; i < typ.NumField(); i++ {
		tag := strings.Split(typ.Field(i).Tag.Get("json"), ",")[0]
		if tag == "" || tag == "-" {
			t.Fatalf("字段 %s 缺少 json tag", typ.Field(i).Name)
		}
		got = append(got, tag)
	}
	sort.Strings(got)
	want := append([]string(nil), AllowedDocFields...)
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Document 字段与白名单不一致\nstruct=%v\nallow =%v", got, want)
	}
}

func TestValidateDocumentJSONRejectsContent(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		ok   bool
	}{
		{"合法元数据", `{"id":"a","title":"夜航船","author":"阿来","tags":["海上"],"description":"简介","chapter_titles":["第一章"],"chapter_count":2,"size_bytes":10,"created_at":"2026"}`, true},
		{"含 content", `{"id":"a","title":"x","content":"正文"}`, false},
		{"含 text", `{"id":"a","title":"x","text":"正文"}`, false},
		{"含 chapter_body", `{"id":"a","chapter_body":"正文"}`, false},
	}
	for _, c := range cases {
		err := ValidateDocumentJSON([]byte(c.raw))
		if c.ok && err != nil {
			t.Fatalf("%s: 不应失败: %v", c.name, err)
		}
		if !c.ok {
			if err == nil {
				t.Fatalf("%s: 应被拒绝", c.name)
			}
			if !errors.Is(err, ErrForbiddenField) {
				t.Fatalf("%s: 错误类型不对: %v", c.name, err)
			}
		}
	}
}

// 降级路径同样只查元数据：正文关键词查不到，标题/作者可以。
func TestLocalSearchOnlyMetadata(t *testing.T) {
	database, err := db.Open(t.TempDir() + "/t.db")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	_, err = database.Exec(`INSERT INTO books(id,title,author,tags,description,size_bytes,sha256,
		wrapped_dek,dek_nonce,chunk_size,storage_path,chapter_count,index_state,owner_id)
		VALUES ('b1','夜航船','阿来','海上,民国','一部关于夜航的小说',100,'aa',
		x'00',x'00',1048576,'ciphertext/b1',2,'indexed','admin')`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO book_chapters(book_id,chapter_index,title,chars,start_rune,end_rune)
		VALUES ('b1',0,'第一章 起',10,0,10),('b1',1,'第二章 量子纠缠信标',10,10,20)`); err != nil {
		t.Fatal(err)
	}
	s := NewService(database, nil, nil, 0, []byte("0123456789abcdef"))
	ctx := context.Background()

	res, err := s.Search(ctx, "量子纠缠信标", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Hits) != 1 || res.Hits[0].MatchedField != "chapter_titles" {
		t.Fatalf("章节标题应命中: %+v", res.Hits)
	}

	res, err = s.Search(ctx, "夜航船", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Hits) != 1 || res.Hits[0].MatchedField != "title" {
		t.Fatalf("标题应命中: %+v", res.Hits)
	}
	if !strings.Contains(res.Hits[0].Highlight, "<mark>夜航船</mark>") {
		t.Fatalf("高亮缺失: %q", res.Hits[0].Highlight)
	}

	// 正文（哪怕是真的正文词）在元数据列中不存在 → 必须无结果
	res, err = s.Search(ctx, "他推开门", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Hits) != 0 {
		t.Fatalf("正文关键词不应命中: %+v", res.Hits)
	}
}

// 查询日志只存长度与 HMAC 指纹，不含查询词明文。
func TestSearchLogHasNoPlaintext(t *testing.T) {
	database, err := db.Open(t.TempDir() + "/t.db")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	key := []byte("0123456789abcdef0123456789abcdef")
	s := NewService(database, nil, nil, 0, key)
	if _, err := s.Search(context.Background(), "秘密查询词", 5); err != nil {
		t.Fatal(err)
	}
	var qLen int
	var qHash string
	if err := database.QueryRow(`SELECT q_len, q_hash FROM search_logs`).Scan(&qLen, &qHash); err != nil {
		t.Fatal(err)
	}
	if qLen != len([]rune("秘密查询词")) {
		t.Fatalf("长度记录不对: %d", qLen)
	}
	if strings.Contains(qHash, "秘密") || len(qHash) != 16 {
		t.Fatalf("哈希格式不对: %q", qHash)
	}
}
