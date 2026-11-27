package server_test

// 集成测试：在临时目录构建完整栈（SQLite + KEK + HTTP 路由），
// 覆盖验收标准：认证、限流、上传/下载 SHA-256 一致、重启后可解密、
// 篡改返回 409、DB 与密文目录无明文、审计。

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"novelstack/internal/audit"
	"novelstack/internal/auth"
	"novelstack/internal/backup"
	"novelstack/internal/blocklist"
	"novelstack/internal/books"
	"novelstack/internal/config"
	"novelstack/internal/cryptobox"
	"novelstack/internal/db"
	"novelstack/internal/devices"
	"novelstack/internal/lan"
	"novelstack/internal/legado"
	"novelstack/internal/ops"
	"novelstack/internal/purge"
	"novelstack/internal/search"
	"novelstack/internal/server"
	"novelstack/internal/stats"
	"novelstack/internal/telemetry"
	"novelstack/internal/tts"
	"novelstack/internal/ttscache"
)

const testPassword = "correct-horse-battery"

type stack struct {
	ts      *httptest.Server
	dataDir string
	sched   *backup.Scheduler
	// 第九阶段：测试需要直接检查数据库与缓存回收结果。
	db    *sql.DB
	books *books.Service
	tts   *tts.Service
	// 终端防御阶段：暴露熔断 store 供测试直接检查黑名单。
	blocklist *blocklist.Store
}

// stackOpts 控制测试栈的组件配置。
type stackOpts struct {
	provider    tts.Provider // nil 表示 TTS 关闭
	maxSegRunes int
	// backend 为 nil 时元数据检索走 SQLite 降级匹配；也可注入假 Meilisearch。
	backend search.Backend
	legado  bool
	// 第五阶段：可控的预取限制，便于测试 429 与超限 400。
	prefetchPerMinute   int
	prefetchMaxChapters int
	// 第六阶段：是否装配自动备份调度器 / 告警 webhook。
	autoBackup bool
	webhook    string
	// 第九阶段：正文净化规则（为空表示不净化）。
	purgeRules []string
	// 终端防御阶段：是否启用 IP 熔断。默认关闭，避免普通测试里的 401/404 互相污染。
	blocklistOn bool
	// 熔断阈值（1 小时窗口内的 401/404 次数）。
	blocklistThreshold int
	// 登录失败限流（次/分钟）；熔断测试需调高，否则先撞 429。
	loginFailPerMinute int
}

// buildStack 组装完整服务。可以重复调用以模拟容器重启（同一 dataDir）。
func buildStack(t *testing.T, dataDir string) *stack {
	t.Helper()
	return buildStackWith(t, dataDir, stackOpts{provider: tts.NewMock(0)})
}

func buildStackWith(t *testing.T, dataDir string, opts stackOpts) *stack {
	t.Helper()
	return buildStackWithCleanup(t, dataDir, opts, t.Cleanup)
}

// buildStackWithCleanup 把资源释放方式抽象出来：单测用 t.Cleanup，
// 共享栈用全局 cleanup 列表（由 TestMain 统一释放）。
func buildStackWithCleanup(t *testing.T, dataDir string, opts stackOpts, cleanup func(func())) *stack {
	t.Helper()
	if opts.maxSegRunes == 0 {
		opts.maxSegRunes = 100
	}
	cfg := &config.Config{
		Addr:           ":0",
		DataDir:        dataDir,
		CiphertextDir:  filepath.Join(dataDir, "ciphertext"),
		BackupDir:      filepath.Join(dataDir, "backups"),
		DBPath:         filepath.Join(dataDir, "app.db"),
		KEKPath:        filepath.Join(dataDir, "kek"),
		CookieSecure:   false, // 开发环境
		MaxUploadBytes: 20 << 20,
		ChunkSize:      1 << 20,
		SessionTTL:     24 * time.Hour,
		LoginFailPerMinute: func() int {
			if opts.loginFailPerMinute > 0 {
				return opts.loginFailPerMinute
			}
			return 5
		}(),
		TTSCacheDir:        filepath.Join(dataDir, "tts-cache"),
		TTSMaxSegmentRunes: opts.maxSegRunes,
		TTSDefaultSpeed:    1.0,
		TTSMaxAudioBytes:   16 << 20,

		ReadingTZ:      "UTC",
		ExportMaxBooks: 200,

		PrefetchMaxChapters: opts.prefetchMaxChapters,
		PrefetchPerMinute:   opts.prefetchPerMinute,
		PrefetchMaxBytes:    8 << 20,
		DeviceTokenTTL:      180 * 24 * time.Hour,
		KekOfflineMarker:    filepath.Join(dataDir, ".kek-offline-backup-ok"),
	}
	database, err := db.Open(cfg.DBPath)
	if err != nil {
		t.Fatalf("db: %v", err)
	}
	cleanup(func() { database.Close() })

	sessions := auth.NewStore(database)
	if _, err := sessions.EnsureAdmin(context.Background(), "admin", testPassword); err != nil {
		t.Fatalf("ensure admin: %v", err)
	}
	kek, err := cryptobox.LoadKEK(cfg.KEKPath)
	if err != nil {
		t.Fatalf("kek: %v", err)
	}
	a := audit.New(database)

	var sched *backup.Scheduler
	if opts.autoBackup {
		sched = backup.NewScheduler(backup.SchedulerOptions{
			Enabled: true, Hour: 3, Minute: 0, RetentionDays: 7,
			DataDir: dataDir, OutDir: filepath.Join(dataDir, "backups"),
			Passphrase: []byte("integration-test-passphrase"),
			Params:     backup.KDFParams{Time: 1, Memory: 8 * 1024, Threads: 1},
			DB:         database, Audit: a,
		})
	}
	health := ops.NewHealthStore(ops.HealthOptions{Webhook: opts.webhook, Audit: a, Cooldown: time.Hour})

	purgeEngine, perr := purge.New(opts.purgeRules)
	if perr != nil {
		t.Fatalf("purge rules: %v", perr)
	}
	bookSvc := &books.Service{
		Store:         books.NewStore(database),
		KEK:           kek,
		CiphertextDir: cfg.CiphertextDir,
		ChunkSize:     cfg.ChunkSize,
		MaxUpload:     cfg.MaxUploadBytes,
		Audit:         a,
		Purge:         purgeEngine,
	}
	cache, err := ttscache.New(cfg.TTSCacheDir, kek, cfg.TTSMaxAudioBytes)
	if err != nil {
		t.Fatalf("tts cache: %v", err)
	}
	ttsSvc := &tts.Service{
		Books:                 bookSvc,
		Cache:                 cache,
		Repo:                  tts.NewRepo(database),
		Provider:              opts.provider,
		ProviderKind:          "test",
		Audit:                 a,
		MaxSegmentRunes:       opts.maxSegRunes,
		DefaultSpeed:          1.0,
		SegmentTimeout:        10 * time.Second,
		MaxSegmentsPerRequest: 1000,
	}
	searchSvc := search.NewService(database, opts.backend, a, 5*time.Second,
		[]byte("0123456789abcdef0123456789abcdef"))
	searchSvc.Start()
	cleanup(searchSvc.Close)
	bookSvc.Index = searchSvc

	var legadoSvc *legado.Service
	if opts.legado {
		legadoSvc = &legado.Service{
			Sessions:     sessions,
			Books:        bookSvc,
			Search:       searchSvc,
			Audit:        a,
			Tokens:       legado.NewTokenStore(database),
			SessionTTL:   720 * time.Hour,
			CookieSecure: false,
		}
	}
	threshold := opts.blocklistThreshold
	if threshold <= 0 {
		threshold = 10
	}
	blockSvc := blocklist.New(database, opts.blocklistOn, time.Hour, threshold, 24*time.Hour, 5000)
	srv := server.New(server.Deps{
		Config: cfg, Sessions: sessions, Books: bookSvc, TTS: ttsSvc,
		Search: searchSvc, Legado: legadoSvc, Audit: a, DB: database,
		Devices: devices.NewStore(database), Health: health, Scheduler: sched,
		Telemetry: telemetry.NewStore(database),
		Stats:     stats.New(database, time.UTC),
		LANPairs:  lan.NewStore(database, 5*time.Minute, 8),
		Blocklist: blockSvc,
	})
	ts := httptest.NewServer(srv.Handler())
	cleanup(ts.Close)
	return &stack{ts: ts, dataDir: dataDir, sched: sched, db: database, books: bookSvc, tts: ttsSvc, blocklist: blockSvc}
}

func newStack(t *testing.T) *stack {
	t.Helper()
	dir := t.TempDir()
	kek := make([]byte, cryptobox.KeySize)
	if _, err := rand.Read(kek); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "kek"), kek, 0o600); err != nil {
		t.Fatal(err)
	}
	return buildStack(t, dir)
}

func login(t *testing.T, s *stack, username, password string) *http.Response {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"username": username, "password": password})
	resp, err := http.Post(s.ts.URL+"/api/login", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp
}

func loginOK(t *testing.T, s *stack) *http.Client {
	t.Helper()
	resp := login(t, s, "admin", testPassword)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("login status = %d", resp.StatusCode)
	}
	client := &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error { return http.ErrUseLastResponse },
	}
	// 取出 cookie 手动携带（httptest 默认 Secure=false）。
	for _, c := range resp.Cookies() {
		if c.Name == auth.SessionCookieName {
			client.Jar = newJar(c)
		}
	}
	if client.Jar == nil {
		t.Fatal("no session cookie returned")
	}
	return client
}

// 极简 cookie jar：固定携带一个 cookie。
type jar struct{ cookie *http.Cookie }

func newJar(c *http.Cookie) http.CookieJar { return &jar{cookie: c} }

func (j *jar) Cookies(*url.URL) []*http.Cookie { return []*http.Cookie{j.cookie} }
func (j *jar) SetCookies(_ *url.URL, cs []*http.Cookie) {
	for _, c := range cs {
		if c.Name == auth.SessionCookieName {
			j.cookie = c
		}
	}
}

func uploadBook(t *testing.T, client *http.Client, baseURL, title string, content []byte, filename string) *http.Response {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	if err := mw.WriteField("title", title); err != nil {
		t.Fatal(err)
	}
	fw, err := mw.CreateFormFile("file", filename)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fw.Write(content); err != nil {
		t.Fatal(err)
	}
	mw.Close()
	resp, err := client.Post(baseURL+"/api/books", mw.FormDataContentType(), &buf)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func bodyString(t *testing.T, resp *http.Response) string {
	t.Helper()
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// 未登录访问 /api/books 返回 401。
func TestUnauthorizedBooks401(t *testing.T) {
	requireFullStack(t)
	s := newStack(t)
	resp, err := http.Get(s.ts.URL + "/api/books")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", resp.StatusCode)
	}
	for _, path := range []string{"/api/me", "/api/admin/audit"} {
		r, _ := http.Get(s.ts.URL + path)
		r.Body.Close()
		if r.StatusCode != http.StatusUnauthorized {
			t.Fatalf("%s: want 401, got %d", path, r.StatusCode)
		}
	}
}

// 错误密码连续 6 次：第 6 次触发限流（429）。
func TestLoginRateLimit(t *testing.T) {
	requireFullStack(t)
	s := newStack(t)
	for i := 0; i < 5; i++ {
		if code := login(t, s, "admin", "wrong-password").StatusCode; code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: want 401, got %d", i+1, code)
		}
	}
	if code := login(t, s, "admin", "wrong-password").StatusCode; code != http.StatusTooManyRequests {
		t.Fatalf("6th attempt: want 429, got %d", code)
	}
	// 正确密码此时也被限流（按 IP），符合保守设计。
	if code := login(t, s, "admin", testPassword).StatusCode; code != http.StatusTooManyRequests {
		t.Fatalf("correct password while limited: want 429, got %d", code)
	}
}

// 上传 → 下载：SHA-256 一致；重启后仍可解密；篡改返回 409；DB 与密文目录无明文。
func TestUploadDownloadRoundtripAndRestart(t *testing.T) {
	requireFullStack(t)
	dir := t.TempDir()
	kek := make([]byte, cryptobox.KeySize)
	rand.Read(kek)
	os.WriteFile(filepath.Join(dir, "kek"), kek, 0o600)

	plain := []byte("第一章\n夜色渐深，他在灯下翻开那本旧书。\n" + strings.Repeat("正文内容。", 100000)) // >2MiB 多块
	sum := sha256.Sum256(plain)
	wantSHA := hex.EncodeToString(sum[:])

	s := buildStack(t, dir)
	client := loginOK(t, s)

	resp := uploadBook(t, client, s.ts.URL, "旧书", plain, "old-book.txt")
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("upload: want 201, got %d: %s", resp.StatusCode, bodyString(t, resp))
	}
	var book struct {
		ID        string `json:"id"`
		SizeBytes int64  `json:"size_bytes"`
		SHA256    string `json:"sha256"`
		Title     string `json:"title"`
	}
	if err := json.Unmarshal(bodyBytes(t, resp), &book); err != nil {
		t.Fatal(err)
	}
	if book.SHA256 != wantSHA {
		t.Fatalf("stored sha mismatch")
	}

	// 列表与详情。
	list, _ := client.Get(s.ts.URL + "/api/books")
	listBody := bodyString(t, list)
	if !strings.Contains(listBody, "旧书") {
		t.Fatalf("list missing title: %s", listBody)
	}
	detail, _ := client.Get(s.ts.URL + "/api/books/" + book.ID)
	if detail.StatusCode != http.StatusOK {
		t.Fatalf("detail: %d", detail.StatusCode)
	}
	detail.Body.Close()

	// 下载解密内容，SHA-256 与上传前一致。
	dl, err := client.Get(s.ts.URL + "/api/books/" + book.ID + "/content")
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(dl.Body)
	dl.Body.Close()
	if dl.StatusCode != http.StatusOK {
		t.Fatalf("download: want 200, got %d: %s", dl.StatusCode, string(got))
	}
	if err != nil {
		t.Fatal(err)
	}
	gotSum := sha256.Sum256(got)
	if hex.EncodeToString(gotSum[:]) != wantSHA {
		t.Fatal("downloaded sha256 mismatch")
	}
	if !bytes.Equal(got, plain) {
		t.Fatal("downloaded content mismatch")
	}

	// 检查密文目录文件不含明文。
	entries, _ := os.ReadDir(filepath.Join(dir, "ciphertext"))
	if len(entries) == 0 {
		t.Fatal("no ciphertext file")
	}
	for _, e := range entries {
		data, _ := os.ReadFile(filepath.Join(dir, "ciphertext", e.Name()))
		if bytes.Contains(data, []byte("夜色渐深")) || bytes.Contains(data, []byte("正文内容")) {
			t.Fatalf("plaintext found in ciphertext file %s", e.Name())
		}
	}

	// 登出后再访问 401。
	lg, _ := client.Post(s.ts.URL+"/api/logout", "application/json", strings.NewReader("{}"))
	lg.Body.Close()
	if code := login(t, s, "admin", "nope").StatusCode; code != http.StatusUnauthorized {
		t.Fatalf("post-logout wrong password: want 401, got %d", code)
	}
	afterLogout, _ := client.Get(s.ts.URL + "/api/books")
	afterLogout.Body.Close()
	if afterLogout.StatusCode != http.StatusUnauthorized {
		t.Fatalf("after logout: want 401, got %d", afterLogout.StatusCode)
	}

	// ---- 模拟容器重启（同一 dataDir 重新 buildStack，登录限流计数清零）----
	s2 := buildStack(t, dir)
	client2 := loginOK(t, s2)
	dl2, err := client2.Get(s2.ts.URL + "/api/books/" + book.ID + "/content")
	if err != nil {
		t.Fatal(err)
	}
	got2, _ := io.ReadAll(dl2.Body)
	dl2.Body.Close()
	if dl2.StatusCode != http.StatusOK || sha256.Sum256(got2) != sum {
		t.Fatalf("restart decrypt failed: %d", dl2.StatusCode)
	}

	// ---- 篡改密文任意一个字节 → 下载 409 ----
	ctPath := filepath.Join(dir, "ciphertext", book.ID)
	data, _ := os.ReadFile(ctPath)
	data[len(data)/2] ^= 0x01
	if err := os.WriteFile(ctPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	dl3, _ := client2.Get(s2.ts.URL + "/api/books/" + book.ID + "/content")
	dl3.Body.Close()
	if dl3.StatusCode != http.StatusConflict && dl3.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("tampered download: want 409/422, got %d", dl3.StatusCode)
	}

	// 审计里有篡改记录。
	au, _ := client2.Get(s2.ts.URL + "/api/admin/audit?limit=100")
	auBody := bodyString(t, au)
	if !strings.Contains(auBody, "book_download_tamper") {
		t.Fatalf("audit missing tamper entry: %s", auBody)
	}
	au.Body.Close()

	// ---- 软删除 ----
	req, _ := http.NewRequest(http.MethodDelete, s2.ts.URL+"/api/books/"+book.ID, nil)
	del, _ := client2.Do(req)
	del.Body.Close()
	if del.StatusCode != http.StatusNoContent {
		t.Fatalf("delete: want 204, got %d", del.StatusCode)
	}
	list2, _ := client2.Get(s2.ts.URL + "/api/books")
	if strings.Contains(bodyString(t, list2), book.ID) {
		t.Fatal("deleted book still listed")
	}
	list2.Body.Close()
	detail2, _ := client2.Get(s2.ts.URL + "/api/books/" + book.ID)
	detail2.Body.Close()
	if detail2.StatusCode != http.StatusNotFound {
		t.Fatalf("deleted detail: want 404, got %d", detail2.StatusCode)
	}
}

// books 表结构不存明文正文：检查列集合。
func TestBooksTableHasNoContentColumn(t *testing.T) {
	requireFullStack(t)
	dir := t.TempDir()
	kek := make([]byte, cryptobox.KeySize)
	rand.Read(kek)
	os.WriteFile(filepath.Join(dir, "kek"), kek, 0o600)
	buildStack(t, dir) // 初始化一次以创建表与管理员

	database, err := db.Open(filepath.Join(dir, "app.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	rows, err := database.Query(`SELECT name FROM pragma_table_info('books')`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var cols []string
	for rows.Next() {
		var c string
		rows.Scan(&c)
		cols = append(cols, c)
	}
	for _, c := range cols {
		lc := strings.ToLower(c)
		if strings.Contains(lc, "content") || strings.Contains(lc, "body") || strings.Contains(lc, "text") {
			t.Fatalf("books 表存在疑似正文字段: %s", c)
		}
	}
}

// 非 .txt 上传被拒绝；超限返回 413。
func TestUploadValidation(t *testing.T) {
	requireFullStack(t)
	s := newStack(t)
	client := loginOK(t, s)

	// 非 txt。
	resp := uploadBook(t, client, s.ts.URL, "bad", []byte("x"), "book.epub")
	if resp.StatusCode != http.StatusUnsupportedMediaType {
		t.Fatalf("epub: want 415, got %d", resp.StatusCode)
	}
	resp.Body.Close()

	// 超限（2MiB 上限小服务这里用 20MB 上限 + 25MB 内容太大，改用直接压小上限的服务）。
	// 为避免大内存，这里验证 MaxBytesReader 生效：构造略超上限的假体。
	big := bytes.Repeat([]byte("a"), 1<<20)
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	mw.WriteField("title", "big")
	fw, _ := mw.CreateFormFile("file", "big.txt")
	// 写入超过 20MB 上限的内容（分块写入避免一次性大内存）。
	for i := 0; i < 21; i++ {
		fw.Write(big)
	}
	mw.Close()
	r, _ := client.Post(s.ts.URL+"/api/books", mw.FormDataContentType(), &buf)
	r.Body.Close()
	if r.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversize: want 413, got %d", r.StatusCode)
	}
}

// healthz 不鉴权。
func TestHealthz(t *testing.T) {
	requireFullStack(t)
	s := newStack(t)
	resp, err := http.Get(s.ts.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("healthz: %d", resp.StatusCode)
	}
	var m map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
		t.Fatal(err)
	}
	if m["status"] != "ok" {
		t.Fatalf("healthz body: %v", m)
	}
}

func bodyBytes(t *testing.T, resp *http.Response) []byte {
	t.Helper()
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
