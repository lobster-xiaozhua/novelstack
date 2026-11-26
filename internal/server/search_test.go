package server_test

// 第三阶段集成测试：元数据检索分层、章节明文 ETag、回执覆盖度、Legado 网关双层认证、
// 客户端本地 FTS5 全文搜索（仅已缓存章节）。

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"novelstack/clientcore"
	"novelstack/internal/search"
)

// 书名/作者/标签命中，正文关键词不命中（正文不在服务端索引）。
const layerTestText = `第一章 夜航船
他推开门，屋里很暗。窗外的雨还在下，檐角的水一滴一滴落进石臼里。
那枚铜钥匙上刻着量子纠缠信标四个字，是他此行的全部理由。
第二章 归途
天亮的时候，雨停了。他沿着河堤往回走，河面上浮着一层薄雾。
`

func uploadBookMeta(t *testing.T, client *http.Client, base, title, author, tags, desc, body string) (string, int) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for _, kv := range [][2]string{
		{"title", title}, {"author", author}, {"tags", tags}, {"description", desc},
	} {
		if err := mw.WriteField(kv[0], kv[1]); err != nil {
			t.Fatal(err)
		}
	}
	fw, err := mw.CreateFormFile("file", "book.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fw.Write([]byte(body)); err != nil {
		t.Fatal(err)
	}
	mw.Close()
	resp, err := client.Post(base+"/api/books", mw.FormDataContentType(), &buf)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("upload status=%d body=%s", resp.StatusCode, raw)
	}
	var meta struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(raw, &meta); err != nil {
		t.Fatal(err)
	}
	return meta.ID, resp.StatusCode
}

func getJSON(t *testing.T, client *http.Client, url string, out any) int {
	t.Helper()
	resp, err := client.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if out != nil && resp.StatusCode == http.StatusOK {
		if err := json.Unmarshal(b, out); err != nil {
			t.Fatalf("decode %s: %v (%s)", url, err, b)
		}
	}
	return resp.StatusCode
}

type metaSearchResp struct {
	Source string       `json:"source"`
	Hits   []search.Hit `json:"hits"`
}

// 验收 1：正文关键词在服务端元数据搜索中必须无结果；书名/作者/标签必须命中。
func TestMetadataSearchExcludesBody(t *testing.T) {
	requireFullStack(t)
	s := newStack(t)
	client := loginOK(t, s)
	bookID, _ := uploadBookMeta(t, client, s.ts.URL, "夜航船", "阿来", "海上,民国", "一部关于夜航船的小说", layerTestText)

	var res metaSearchResp
	if code := getJSON(t, client, s.ts.URL+"/api/search/metadata?q=量子纠缠信标", &res); code != 200 {
		t.Fatalf("search status=%d", code)
	}
	if len(res.Hits) != 0 {
		t.Fatalf("正文关键词不应命中服务端索引: %+v", res.Hits)
	}

	if code := getJSON(t, client, s.ts.URL+"/api/search/metadata?q=夜航船", &res); code != 200 {
		t.Fatalf("search status=%d", code)
	}
	if len(res.Hits) != 1 || res.Hits[0].BookID != bookID {
		t.Fatalf("书名应命中: %+v", res.Hits)
	}
	if res.Hits[0].MatchedField != "title" || !strings.Contains(res.Hits[0].Highlight, "<mark>") {
		t.Fatalf("命中字段/高亮不对: %+v", res.Hits[0])
	}

	if code := getJSON(t, client, s.ts.URL+"/api/search/metadata?q=阿来", &res); code != 200 || len(res.Hits) != 1 {
		t.Fatalf("作者应命中: code=%d hits=%+v", code, res.Hits)
	}
	if code := getJSON(t, client, s.ts.URL+"/api/search/metadata?q=海上", &res); code != 200 || len(res.Hits) != 1 {
		t.Fatalf("标签应命中: code=%d hits=%+v", code, res.Hits)
	}
	// 章节标题属于允许索引的元数据
	if code := getJSON(t, client, s.ts.URL+"/api/search/metadata?q=归途", &res); code != 200 || len(res.Hits) != 1 {
		t.Fatalf("章节标题应命中: code=%d hits=%+v", code, res.Hits)
	}

	// 审计里不得出现查询词明文（只记长度）
	auditResp, err := client.Get(s.ts.URL + "/api/admin/audit?limit=50")
	if err != nil {
		t.Fatal(err)
	}
	auditBody := bodyString(t, auditResp)
	for _, q := range []string{"量子纠缠信标", "夜航船", "阿来"} {
		if strings.Contains(auditBody, `"detail":"q=`+q) {
			t.Fatalf("审计写入查询词明文: %s", q)
		}
	}
	if strings.Contains(auditBody, "量子纠缠信标") {
		t.Fatal("审计不应包含查询词")
	}
}

// 验收 4/5：未登录 401；ETag 304；章节内容变化后 ETag 变化。
func TestChapterPlainETagAndAuth(t *testing.T) {
	requireFullStack(t)
	s := newStack(t)

	anon := &http.Client{}
	for _, c := range []struct{ method, path string }{
		{http.MethodGet, "/api/search/metadata?q=x"},
		{http.MethodGet, "/api/books/whatever/chapters/0/plain"},
		{http.MethodGet, "/api/books/whatever/chapters"},
		{http.MethodPost, "/api/books/whatever/chapters/0/ack"},
	} {
		req, _ := http.NewRequest(c.method, s.ts.URL+c.path, strings.NewReader("{}"))
		resp, err := anon.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("%s %s: want 401, got %d", c.method, c.path, resp.StatusCode)
		}
	}

	client := loginOK(t, s)
	bookID, _ := uploadBookMeta(t, client, s.ts.URL, "夜航船", "阿来", "", "", layerTestText)

	var list struct {
		Count    int `json:"count"`
		Chapters []struct {
			Index     int    `json:"index"`
			Title     string `json:"title"`
			WordCount int    `json:"word_count"`
			ETag      string `json:"etag"`
		} `json:"chapters"`
	}
	if code := getJSON(t, client, s.ts.URL+"/api/books/"+bookID+"/chapters", &list); code != 200 {
		t.Fatalf("chapters status=%d", code)
	}
	if list.Count != 2 || len(list.Chapters) != 2 {
		t.Fatalf("章节数不对: %+v", list)
	}
	if list.Chapters[0].WordCount == 0 || list.Chapters[0].ETag == "" {
		t.Fatalf("章节元数据缺失: %+v", list.Chapters[0])
	}

	// 首次拉取正文
	resp, err := client.Get(s.ts.URL + "/api/books/" + bookID + "/chapters/0/plain")
	if err != nil {
		t.Fatal(err)
	}
	etag := resp.Header.Get("ETag")
	text := bodyString(t, resp)
	if resp.StatusCode != 200 || !strings.Contains(text, "量子纠缠信标") {
		t.Fatalf("正文拉取异常: %d %q", resp.StatusCode, text)
	}
	if etag == "" {
		t.Fatal("缺少 ETag")
	}

	// 二次请求带 If-None-Match → 304 且无正文
	req, _ := http.NewRequest(http.MethodGet, s.ts.URL+"/api/books/"+bookID+"/chapters/0/plain", nil)
	req.Header.Set("If-None-Match", etag)
	resp2, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body := bodyString(t, resp2)
	if resp2.StatusCode != http.StatusNotModified || body != "" {
		t.Fatalf("want 304 empty, got %d %q", resp2.StatusCode, body)
	}

	// 内容不同的同一章节：ETag 必须变化（新书新密文）
	otherID, _ := uploadBookMeta(t, client, s.ts.URL, "夜航船", "阿来", "", "",
		strings.Replace(layerTestText, "量子纠缠信标", "另一处暗号", 1))
	var list2 struct {
		Chapters []struct {
			ETag string `json:"etag"`
		} `json:"chapters"`
	}
	getJSON(t, client, s.ts.URL+"/api/books/"+otherID+"/chapters", &list2)
	if len(list2.Chapters) == 0 || list2.Chapters[0].ETag == list.Chapters[0].ETag {
		t.Fatalf("内容变化后 ETag 未变化: %q vs %q", list.Chapters[0].ETag, list2.Chapters[0].ETag)
	}

	// 回执 → 覆盖度
	ackBody, _ := json.Marshal(map[string]string{"client_id": "test-client", "etag": etag})
	areq, _ := http.NewRequest(http.MethodPost, s.ts.URL+"/api/books/"+bookID+"/chapters/0/ack",
		bytes.NewReader(ackBody))
	areq.Header.Set("Content-Type", "application/json")
	aresp, err := client.Do(areq)
	if err != nil {
		t.Fatal(err)
	}
	aresp.Body.Close()
	if aresp.StatusCode != http.StatusOK {
		t.Fatalf("ack status=%d", aresp.StatusCode)
	}

	var metrics struct {
		Coverage []struct {
			BookID   string  `json:"book_id"`
			Indexed  int     `json:"indexed_chapters"`
			Total    int     `json:"total_chapters"`
			Coverage float64 `json:"coverage"`
		} `json:"coverage"`
		Search struct {
			Samples int     `json:"samples"`
			P50     float64 `json:"p50_ms"`
		} `json:"search"`
		MetadataIndex struct {
			Backend string `json:"backend"`
			Books   int    `json:"books"`
		} `json:"metadata_index"`
	}
	if code := getJSON(t, client, s.ts.URL+"/api/admin/metrics", &metrics); code != 200 {
		t.Fatalf("metrics status=%d", code)
	}
	found := false
	for _, c := range metrics.Coverage {
		if c.BookID == bookID {
			found = true
			if c.Indexed != 1 || c.Total != 2 || c.Coverage != 0.5 {
				t.Fatalf("覆盖度不对: %+v", c)
			}
		}
	}
	if !found {
		t.Fatalf("指标缺少书籍覆盖度: %+v", metrics.Coverage)
	}
	if metrics.MetadataIndex.Backend != "local" || metrics.MetadataIndex.Books != 2 {
		t.Fatalf("索引指标不对: %+v", metrics.MetadataIndex)
	}
}

// 验收 2：发送到检索后端的文档只有白名单字段，绝无正文。
type recordingBackend struct {
	mu   sync.Mutex
	docs []search.Document
	raw  []map[string]any
}

func (b *recordingBackend) Name() string                       { return "fake" }
func (b *recordingBackend) PublishedIndex() string             { return "books" }
func (b *recordingBackend) EnsureSchema(context.Context) error { return nil }

func (b *recordingBackend) Upsert(_ context.Context, docs []search.Document) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, d := range docs {
		raw, err := json.Marshal(d)
		if err != nil {
			return err
		}
		var m map[string]any
		if err := json.Unmarshal(raw, &m); err != nil {
			return err
		}
		b.docs = append(b.docs, d)
		b.raw = append(b.raw, m)
	}
	return nil
}

func (b *recordingBackend) Delete(context.Context, string) error { return nil }

func (b *recordingBackend) Search(_ context.Context, q string, _ int) ([]search.Hit, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := []search.Hit{}
	for _, d := range b.docs {
		if strings.Contains(d.Title, q) || strings.Contains(d.Author, q) {
			out = append(out, search.Hit{
				BookID: d.ID, Title: d.Title, Author: d.Author,
				MatchedField: "title", Highlight: strings.ReplaceAll(d.Title, q, "<mark>"+q+"</mark>"),
			})
		}
	}
	return out, nil
}

func (b *recordingBackend) DocIDs(context.Context) ([]string, error) { return nil, nil }
func (b *recordingBackend) DocCount(context.Context) (int64, error)  { return 0, nil }
func (b *recordingBackend) Health(context.Context) error             { return nil }

func TestMetadataIndexDocumentsContainNoBody(t *testing.T) {
	requireFullStack(t)
	backend := &recordingBackend{}
	s := newStackWith(t, stackOpts{provider: nil, backend: backend, legado: false})
	client := loginOK(t, s)
	bookID, _ := uploadBookMeta(t, client, s.ts.URL, "夜航船", "阿来", "海上", "简介", layerTestText)

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		backend.mu.Lock()
		n := len(backend.raw)
		backend.mu.Unlock()
		if n > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	backend.mu.Lock()
	docs := append([]map[string]any(nil), backend.raw...)
	backend.mu.Unlock()
	if len(docs) == 0 {
		t.Fatal("索引后端未收到文档")
	}
	doc := docs[0]
	if doc["id"] != bookID {
		t.Fatalf("文档 id 不对: %v", doc["id"])
	}
	banned := []string{"content", "text", "body", "plain", "chapter_body", "chapters_text"}
	for _, b := range banned {
		if _, ok := doc[b]; ok {
			t.Fatalf("索引文档出现正文字段 %q", b)
		}
	}
	allowed := map[string]bool{}
	for _, f := range search.AllowedDocFields {
		allowed[f] = true
	}
	for k := range doc {
		if !allowed[k] {
			t.Fatalf("索引文档出现白名单外字段 %q", k)
		}
	}
	// 文档内容也不能夹带正文：标题类字段拼起来不应包含正文句子
	joined, _ := json.Marshal(doc)
	if bytes.Contains(joined, []byte("他推开门")) {
		t.Fatal("索引文档夹带了正文")
	}

	// 搜索应走后端
	var res metaSearchResp
	if code := getJSON(t, client, s.ts.URL+"/api/search/metadata?q=夜航船", &res); code != 200 {
		t.Fatalf("search status=%d", code)
	}
	if res.Source != "fake" || len(res.Hits) == 0 {
		t.Fatalf("未使用检索后端: %+v", res)
	}
}

// 网关：会话 + 书源令牌双层认证，令牌不能绕过会话，也不能用于管理接口。
func TestLegadoGatewayAuth(t *testing.T) {
	requireFullStack(t)
	s := newStackWith(t, stackOpts{provider: nil, legado: true})
	client := loginOK(t, s)
	bookID, _ := uploadBookMeta(t, client, s.ts.URL, "夜航船", "阿来", "", "", layerTestText)

	// 用 api 会话签发书源令牌
	resp, err := client.Post(s.ts.URL+"/api/admin/legado/tokens", "application/json",
		strings.NewReader(`{"name":"legado-test"}`))
	if err != nil {
		t.Fatal(err)
	}
	var tok struct {
		ID    int64  `json:"id"`
		Token string `json:"token"`
	}
	if err := json.Unmarshal(bodyBytes(t, resp), &tok); err != nil || tok.Token == "" {
		t.Fatalf("签发令牌失败: %v", err)
	}

	// 网关登录（独立会话）
	gwLogin, _ := json.Marshal(map[string]string{"username": "admin", "password": testPassword})
	lr, err := http.Post(s.ts.URL+"/gateway/legado/login", "application/json", bytes.NewReader(gwLogin))
	if err != nil {
		t.Fatal(err)
	}
	var gw struct {
		SessionToken string `json:"session_token"`
	}
	if err := json.Unmarshal(bodyBytes(t, lr), &gw); err != nil || gw.SessionToken == "" {
		t.Fatalf("网关登录失败: %v", err)
	}

	do := func(path string, headers map[string]string) int {
		req, _ := http.NewRequest(http.MethodGet, s.ts.URL+path, nil)
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		return resp.StatusCode
	}
	toc := "/gateway/legado/toc/" + bookID
	content := "/gateway/legado/content/" + bookID + "/0"
	searchPath := "/gateway/legado/search?key=" + "夜航船"

	if code := do(toc, nil); code != 401 {
		t.Fatalf("无认证: want 401, got %d", code)
	}
	if code := do(toc, map[string]string{"X-Legado-Token": tok.Token}); code != 401 {
		t.Fatalf("仅令牌（无会话）: want 401, got %d", code)
	}
	if code := do(toc, map[string]string{"X-Legado-Session": gw.SessionToken}); code != 403 {
		t.Fatalf("仅有会话（无令牌）: want 403, got %d", code)
	}
	both := map[string]string{"X-Legado-Session": gw.SessionToken, "X-Legado-Token": tok.Token}
	if code := do(toc, both); code != 200 {
		t.Fatalf("双层认证: want 200, got %d", code)
	}
	if code := do(searchPath, both); code != 200 {
		t.Fatalf("网关搜索: want 200, got %d", code)
	}
	if code := do(content, both); code != 200 {
		t.Fatalf("网关正文: want 200, got %d", code)
	}
	// 网关会话不能用于管理接口；书源令牌也不能
	if code := do("/api/books", map[string]string{"X-Legado-Session": gw.SessionToken}); code != 401 {
		t.Fatalf("网关会话访问 /api: want 401, got %d", code)
	}
	if code := do("/api/books", map[string]string{"X-Legado-Token": tok.Token}); code != 401 {
		t.Fatalf("书源令牌访问 /api: want 401, got %d", code)
	}
	// 撤销后立即失效
	revoke, _ := http.NewRequest(http.MethodDelete,
		fmt.Sprintf("%s/api/admin/legado/tokens/%d", s.ts.URL, tok.ID), nil)
	rresp, err := client.Do(revoke)
	if err != nil {
		t.Fatal(err)
	}
	rresp.Body.Close()
	if rresp.StatusCode != http.StatusNoContent {
		t.Fatalf("撤销失败: %d", rresp.StatusCode)
	}
	if code := do(toc, both); code != 403 {
		t.Fatalf("撤销后: want 403, got %d", code)
	}
}

// 验收 3：客户端解密章节 → 本地加密索引 → FTS5 命中并高亮；未缓存章节不可搜索。
func TestClientLocalFullTextSearch(t *testing.T) {
	requireFullStack(t)
	s := newStack(t)
	admin := loginOK(t, s)
	body := layerTestText + "第三章 未缓存\n这一章写了独有词：琥珀色灯塔，但客户端不会同步它。\n"
	bookID, _ := uploadBookMeta(t, admin, s.ts.URL, "夜航船", "阿来", "", "", body)
	otherID, _ := uploadBookMeta(t, admin, s.ts.URL, "另一本", "某人", "", "", "第一章 无关\n只有无关内容。\n")

	dir := t.TempDir()
	kp := &clientcore.FileKeyProvider{
		SaltPath:   filepath.Join(dir, "device.salt"),
		Passphrase: []byte("local-index-passphrase"),
	}
	store, err := clientcore.Open(filepath.Join(dir, "index.enc"), kp, "test-desktop")
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	c, err := clientcore.NewClient(s.ts.URL, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Login(ctx, "admin", testPassword); err != nil {
		t.Fatal(err)
	}
	books, err := c.ListBooks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var target clientcore.BookMeta
	for _, b := range books {
		if b.ID == bookID {
			target = b
		}
	}
	if target.ID == "" {
		t.Fatal("未找到目标书籍")
	}
	res, err := store.SyncBook(ctx, c, target)
	if err != nil {
		t.Fatal(err)
	}
	if res.Fetched != 3 || res.Acked != 3 {
		t.Fatalf("同步结果异常: %+v", res)
	}
	// 第二次同步全部命中 ETag → 0 次拉取
	res2, err := store.SyncBook(ctx, c, target)
	if err != nil {
		t.Fatal(err)
	}
	if res2.Fetched != 0 || res2.Skipped != 3 {
		t.Fatalf("二次同步应全部跳过: %+v", res2)
	}

	hits, err := store.Search("量子纠缠信标", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].ChapterIndex != 0 {
		t.Fatalf("本地全文搜索未命中: %+v", hits)
	}
	if !strings.Contains(hits[0].Snippet, "<mark>量子纠缠信标</mark>") {
		t.Fatalf("高亮缺失: %q", hits[0].Snippet)
	}
	if hits[0].BookTitle != "夜航船" {
		t.Fatalf("书名缺失: %+v", hits[0])
	}

	// 未同步的书（另一本）不参与本地搜索
	otherHits, err := store.Search("无关内容", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(otherHits) != 0 {
		t.Fatalf("未缓存书籍不应命中: %+v %s", otherHits, otherID)
	}

	// 落盘为密文快照：无明文、无 RIFF
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "index.enc"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(raw, []byte("NSIDX1")) {
		t.Fatalf("快照 magic 不对: %q", raw[:6])
	}
	for _, marker := range []string{"量子纠缠信标", "他推开门", "夜航船"} {
		if bytes.Contains(raw, []byte(marker)) {
			t.Fatalf("本地快照出现明文: %s", marker)
		}
	}

	// 重新打开仍可检索（密钥来自同一 Keystore 抽象）
	store2, err := clientcore.Open(filepath.Join(dir, "index.enc"), kp, "test-desktop")
	if err != nil {
		t.Fatal(err)
	}
	defer store2.Close()
	hits2, err := store2.Search("琥珀色灯塔", 10)
	if err != nil || len(hits2) != 1 {
		t.Fatalf("重开后检索失败: %v %+v", err, hits2)
	}
}

// 篡改本地快照必须解密失败（GCM tag 校验）。
func TestClientSnapshotTamper(t *testing.T) {
	requireFullStack(t)
	dir := t.TempDir()
	kp := &clientcore.FileKeyProvider{SaltPath: filepath.Join(dir, "s"), Passphrase: []byte("passphrase-123")}
	store, err := clientcore.Open(filepath.Join(dir, "i.enc"), kp, "c")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PutChapter(clientcore.ChapterRecord{BookID: "b", Index: 0, Title: "t", Text: "正文"}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, "i.enc"))
	raw[len(raw)-1] ^= 0x01
	if err := os.WriteFile(filepath.Join(dir, "i.enc"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := clientcore.Open(filepath.Join(dir, "i.enc"), kp, "c"); err == nil {
		t.Fatal("篡改的快照必须解密失败")
	}
	// 换口令（不同密钥）也必须失败
	kp2 := &clientcore.FileKeyProvider{SaltPath: filepath.Join(dir, "s"), Passphrase: []byte("other-passphrase")}
	if _, err := clientcore.Open(filepath.Join(dir, "i.enc"), kp2, "c"); err == nil {
		t.Fatal("错误密钥必须解密失败")
	}
}
