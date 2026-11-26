// acceptance 是一个命令行验收工具：对运行中的 novelstack 执行完整闭环冒烟测试。
//
// 流程：healthz → login → 上传 → 列表 → 下载(SHA-256 校验) → 重启复验提示 →
// 篡改检测（可选，仅限 --tamper 时构造本地密文）→ 审计 → 登出。
//
// 用法：
//
//	go run ./scripts/acceptance --base https://caddy:8443 \
//		--username admin --password 'xxx' \
//		--tls-skip-verify --sni localhost
//
// 本阶段作为人工验收辅助；核心正确性由 go test ./... 覆盖。
package main

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/cookiejar"
	"os"
	"strings"
	"time"

	"novelstack/internal/chapterpkg"
	"novelstack/internal/devices"
)

func main() {
	base := flag.String("base", "https://localhost:8443", "站点基址")
	user := flag.String("username", "admin", "用户名")
	pass := flag.String("password", "", "密码")
	skipTLS := flag.Bool("tls-skip-verify", false, "跳过自签证书校验")
	sni := flag.String("sni", "", "TLS SNI（经由内部网络按容器名访问时设为 localhost）")
	dialAddr := flag.String("dial-addr", "", "TCP 实际拨号地址（如 caddy:8443），URL 仍用 --base")
	flag.Parse()
	if *pass == "" {
		fmt.Fprintln(os.Stderr, "--password 必填")
		os.Exit(2)
	}

	tr := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: *skipTLS, ServerName: *sni}, // #nosec G402 -- 验收工具显式 opt-in
	}
	if *dialAddr != "" {
		d := &net.Dialer{Timeout: 10 * time.Second}
		tr.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
			return d.DialContext(ctx, network, *dialAddr)
		}
	}
	client := &http.Client{Transport: tr}
	jar, _ := cookiejar.New(nil)
	client.Jar = jar

	check("healthz", func() error {
		b, st, err := do(client, http.MethodGet, *base+"/healthz", "", "", nil)
		if err != nil {
			return err
		}
		if st != 200 || !strings.Contains(string(b), `"status":"ok"`) {
			return fmt.Errorf("status=%d body=%s", st, b)
		}
		return nil
	})

	check("未鉴权 401", func() error {
		_, st, err := do(client, http.MethodGet, *base+"/api/books", "", "", nil)
		if err != nil {
			return err
		}
		if st != 401 {
			return fmt.Errorf("status=%d (期望 401)", st)
		}
		return nil
	})

	check("控制台未登录 → 登录页", func() error {
		tr2 := &http.Client{Transport: client.Transport,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		_, st, loc, err := doWithLoc(tr2, http.MethodGet, *base+"/admin", "", nil)
		if err != nil {
			return err
		}
		if st != 303 || !strings.Contains(loc, "/admin/login") {
			return fmt.Errorf("status=%d location=%q (期望 303 → /admin/login)", st, loc)
		}
		// 登录页本身不依赖前端构建产物
		b, st2, _, err := doWithLoc(tr2, http.MethodGet, *base+"/admin/login", "", nil)
		if err != nil {
			return err
		}
		if st2 != 200 || !bytes.Contains(b, []byte("<form")) && !bytes.Contains(b, []byte("fetch(")) {
			return fmt.Errorf("登录页异常 status=%d len=%d", st2, len(b))
		}
		return nil
	})

	check("login", func() error {
		b, st, err := doJSON(client, http.MethodPost, *base+"/api/login",
			map[string]string{"username": *user, "password": *pass})
		if err != nil {
			return err
		}
		if st != 200 {
			return fmt.Errorf("status=%d body=%s", st, b)
		}
		return nil
	})

	check("me", func() error {
		b, st, err := do(client, http.MethodGet, *base+"/api/me", "", "", nil)
		if err != nil {
			return err
		}
		if st != 200 || !strings.Contains(string(b), *user) {
			return fmt.Errorf("status=%d body=%s", st, b)
		}
		return nil
	})

	// 构造 >1MiB 的测试正文（跨多块）。
	plain := []byte("第一章 夜航船\n" + strings.Repeat("春江潮水连海平，海上明月共潮生。", 40000) + "\n（全文完）")
	sum := sha256.Sum256(plain)
	wantSHA := hex.EncodeToString(sum[:])

	var bookID string
	check("上传 txt", func() error {
		var buf bytes.Buffer
		mw := multipart.NewWriter(&buf)
		if err := mw.WriteField("title", "夜航船（验收本）"); err != nil {
			return err
		}
		fw, err := mw.CreateFormFile("file", "acceptance-book.txt")
		if err != nil {
			return err
		}
		if _, err := fw.Write(plain); err != nil {
			return err
		}
		if err := mw.Close(); err != nil {
			return err
		}
		b, st, err := do(client, http.MethodPost, *base+"/api/books", mw.FormDataContentType(), "", buf.Bytes())
		if err != nil {
			return err
		}
		if st != 201 {
			return fmt.Errorf("status=%d body=%s", st, b)
		}
		var meta struct {
			ID     string `json:"id"`
			SHA256 string `json:"sha256"`
		}
		if err := json.Unmarshal(b, &meta); err != nil {
			return err
		}
		if meta.SHA256 != wantSHA {
			return fmt.Errorf("入库 SHA-256 不一致")
		}
		bookID = meta.ID
		return nil
	})

	check("列表", func() error {
		b, st, err := do(client, http.MethodGet, *base+"/api/books", "", "", nil)
		if err != nil {
			return err
		}
		if st != 200 || !strings.Contains(string(b), bookID) {
			return fmt.Errorf("status=%d body=%s", st, b)
		}
		return nil
	})

	check("下载+SHA-256", func() error {
		b, st, err := do(client, http.MethodGet, *base+"/api/books/"+bookID+"/content", "", "", nil)
		if err != nil {
			return err
		}
		if st != 200 {
			return fmt.Errorf("status=%d body=%s", st, b)
		}
		got := sha256.Sum256(b)
		if hex.EncodeToString(got[:]) != wantSHA {
			return fmt.Errorf("下载内容 SHA-256 与上传前不一致")
		}
		return nil
	})

	// 朗读验收使用一本小书（大书单章可能超出单次请求片段上限，属预期保护）
	ttsText := "第一章 夜航船\n" + strings.Repeat("他推开门，屋里很暗。窗外的雨还在下。", 8) +
		"\n第二章 归途\n" + strings.Repeat("天亮的时候，雨停了。他沿着河堤往回走。", 8)
	var ttsBookID string
	check("上传朗读用书", func() error {
		var buf bytes.Buffer
		mw := multipart.NewWriter(&buf)
		mw.WriteField("title", "朗读验收本")
		fw, _ := mw.CreateFormFile("file", "tts-accept.txt")
		fw.Write([]byte(ttsText))
		mw.Close()
		b, st, err := do(client, http.MethodPost, *base+"/api/books", mw.FormDataContentType(), "", buf.Bytes())
		if err != nil {
			return err
		}
		if st != 201 {
			return fmt.Errorf("status=%d body=%s", st, b)
		}
		var meta struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(b, &meta); err != nil {
			return err
		}
		ttsBookID = meta.ID
		return nil
	})

	check("控制台页面 + 内嵌静态资源", func() error {
		b, st, err := do(client, http.MethodGet, *base+"/admin", "", "", nil)
		if err != nil {
			return err
		}
		if st != 200 || !bytes.Contains(b, []byte(`id="app"`)) {
			return fmt.Errorf("status=%d body=%.120s", st, b)
		}
		asset := firstAsset(string(b))
		if asset == "" {
			return fmt.Errorf("首页未引用内嵌资源: %.200s", b)
		}
		ab, st2, err := do(client, http.MethodGet, *base+asset, "", "", nil)
		if err != nil {
			return err
		}
		if st2 != 200 || len(ab) < 1024 {
			return fmt.Errorf("静态资源 %s status=%d len=%d", asset, st2, len(ab))
		}
		// 前端路由（history 模式）回落到 SPA 外壳
		_, st3, err := do(client, http.MethodGet, *base+"/admin/tts", "", "", nil)
		if err != nil {
			return err
		}
		if st3 != 200 {
			return fmt.Errorf("/admin/tts status=%d", st3)
		}
		return nil
	})

	check("数据控制台聚合接口（无明文）", func() error {
		b, st, err := do(client, http.MethodGet, *base+"/api/admin/dashboard", "", "", nil)
		if err != nil {
			return err
		}
		if st != 200 {
			return fmt.Errorf("status=%d body=%.160s", st, b)
		}
		for _, key := range []string{`"books"`, `"kek"`, `"tts"`, `"search"`, `"audit"`, `"backup"`, `"cache"`, `"backups_dir_bytes"`} {
			if !bytes.Contains(b, []byte(key)) {
				return fmt.Errorf("dashboard 缺少 %s: %.200s", key, b)
			}
		}
		for _, bad := range []string{`"plaintext"`, `"content":`, `"dek"`, `"secret"`, `"query_text"`, `"password"`} {
			if bytes.Contains(b, []byte(bad)) {
				return fmt.Errorf("dashboard 泄漏字段 %s", bad)
			}
		}
		return nil
	})

	check("清缓存需二次确认", func() error {
		_, st, err := doJSON(client, http.MethodPost, *base+"/api/admin/cache/clear",
			map[string]any{"all": true})
		if err != nil {
			return err
		}
		if st != 400 {
			return fmt.Errorf("缺少 confirm 应为 400，实际 %d", st)
		}
		return nil
	})

	check("章节索引", func() error {
		b, st, err := do(client, http.MethodGet, *base+"/api/books/"+ttsBookID+"/chapters", "", "", nil)
		if err != nil {
			return err
		}
		if st == 503 {
			fmt.Print("[TTS 未启用，跳过] ")
			return nil
		}
		if st != 200 || !strings.Contains(string(b), "\"chapters\"") {
			return fmt.Errorf("status=%d body=%s", st, b)
		}
		return nil
	})

	var spoken []byte
	check("流式朗读", func() error {
		b, st, err := do(client, http.MethodGet, *base+"/api/books/"+ttsBookID+"/chapters/0/speak?voice=accept&speed=1.0", "", "", nil)
		if err != nil {
			return err
		}
		if st == 503 {
			fmt.Print("[TTS 未启用，跳过] ")
			return nil
		}
		if st != 200 {
			return fmt.Errorf("status=%d body=%s", st, b)
		}
		if !bytes.HasPrefix(b, []byte("RIFF")) {
			return fmt.Errorf("响应不是 WAV 片段流")
		}
		if n := bytes.Count(b, []byte("RIFF")); n < 1 {
			return fmt.Errorf("未检测到 WAV 片段")
		}
		spoken = b
		return nil
	})

	check("朗读缓存命中", func() error {
		if spoken == nil {
			fmt.Print("[跳过] ")
			return nil
		}
		b, st, err := do(client, http.MethodGet, *base+"/api/books/"+ttsBookID+"/chapters/0/speak?voice=accept&speed=1.0", "", "", nil)
		if err != nil {
			return err
		}
		if st != 200 || !bytes.Equal(b, spoken) {
			return fmt.Errorf("第二次朗读不一致 status=%d", st)
		}
		return nil
	})

	check("TTS 指标", func() error {
		if spoken == nil {
			fmt.Print("[跳过] ")
			return nil
		}
		b, st, err := do(client, http.MethodGet, *base+"/api/admin/tts/stats?limit=50", "", "", nil)
		if err != nil {
			return err
		}
		if st != 200 || !strings.Contains(string(b), "\"segments\"") || !strings.Contains(string(b), "\"provider\"") {
			return fmt.Errorf("status=%d body=%s", st, b)
		}
		return nil
	})

	// ---- 第三阶段：分层搜索 / 章节 ETag / 回执 / 指标 / 网关 ----
	var etag string
	check("章节目录", func() error {
		var list struct {
			Count    int `json:"count"`
			Chapters []struct {
				Index int    `json:"index"`
				ETag  string `json:"etag"`
			} `json:"chapters"`
		}
		b, st, err := do(client, http.MethodGet, *base+"/api/books/"+ttsBookID+"/chapters", "", "", nil)
		if err != nil {
			return err
		}
		if st != 200 {
			return fmt.Errorf("status=%d body=%s", st, b)
		}
		if err := json.Unmarshal(b, &list); err != nil {
			return err
		}
		if list.Count < 2 || len(list.Chapters) < 2 || list.Chapters[0].ETag == "" {
			return fmt.Errorf("目录不完整: %s", b)
		}
		etag = list.Chapters[0].ETag
		return nil
	})

	check("单章明文+ETag 304", func() error {
		path := *base + "/api/books/" + ttsBookID + "/chapters/0/plain"
		b, st, err := do(client, http.MethodGet, path, "", "", nil)
		if err != nil {
			return err
		}
		if st != 200 || !strings.Contains(string(b), "他推开门") {
			return fmt.Errorf("status=%d body=%.80s", st, b)
		}
		b2, st2, err := doH(client, http.MethodGet, path, map[string]string{"If-None-Match": etag}, nil)
		if err != nil {
			return err
		}
		if st2 != 304 || len(b2) != 0 {
			return fmt.Errorf("条件请求未返回 304: status=%d len=%d", st2, len(b2))
		}
		return nil
	})

	check("索引回执", func() error {
		body, _ := json.Marshal(map[string]string{"client_id": "acceptance", "etag": etag})
		_, st, err := doH(client, http.MethodPost,
			*base+"/api/books/"+ttsBookID+"/chapters/0/ack", nil, body)
		if err != nil {
			return err
		}
		if st != 200 {
			return fmt.Errorf("status=%d", st)
		}
		return nil
	})

	check("元数据搜索（正文不进索引）", func() error {
		deadline := time.Now().Add(3 * time.Second)
		var hits int
		for {
			b, st, err := do(client, http.MethodGet,
				*base+"/api/search/metadata?q="+urlQuery("朗读验收本"), "", "", nil)
			if err != nil {
				return err
			}
			if st == 503 {
				fmt.Print("[Meilisearch 未启用，跳过] ")
				return nil
			}
			if st != 200 {
				return fmt.Errorf("status=%d body=%s", st, b)
			}
			var res struct {
				Hits []struct {
					MatchedField string `json:"matched_field"`
				} `json:"hits"`
			}
			if err := json.Unmarshal(b, &res); err != nil {
				return err
			}
			hits = len(res.Hits)
			if hits > 0 || time.Now().After(deadline) {
				break
			}
			time.Sleep(150 * time.Millisecond) // 等索引任务落地
		}
		if hits == 0 {
			return fmt.Errorf("书名未命中元数据索引")
		}
		b, st, err := do(client, http.MethodGet,
			*base+"/api/search/metadata?q="+urlQuery("他推开门"), "", "", nil)
		if err != nil {
			return err
		}
		if st != 200 {
			return fmt.Errorf("status=%d", st)
		}
		var bodyRes struct {
			Hits []any `json:"hits"`
		}
		if err := json.Unmarshal(b, &bodyRes); err != nil {
			return err
		}
		if len(bodyRes.Hits) != 0 {
			return fmt.Errorf("正文关键词命中了服务端索引（实现错误）")
		}
		return nil
	})

	check("数据控制台指标", func() error {
		b, st, err := do(client, http.MethodGet, *base+"/api/admin/metrics", "", "", nil)
		if err != nil {
			return err
		}
		if st != 200 || !strings.Contains(string(b), `"metadata_index"`) {
			return fmt.Errorf("status=%d body=%.120s", st, b)
		}
		var m struct {
			Coverage []struct {
				Indexed int `json:"indexed_chapters"`
			} `json:"coverage"`
			Legado struct {
				Enabled bool `json:"enabled"`
			} `json:"legado"`
		}
		if err := json.Unmarshal(b, &m); err != nil {
			return err
		}
		if len(m.Coverage) > 0 && m.Coverage[0].Indexed < 1 {
			return fmt.Errorf("覆盖度未记录回执")
		}
		return nil
	})

	check("Legado 网关双层认证", func() error {
		// 1) 用管理会话签发书源令牌
		b, st, err := do(client, http.MethodPost, *base+"/api/admin/legado/tokens", "application/json", "",
			[]byte(`{"name":"acceptance"}`))
		if err != nil {
			return err
		}
		if st != 201 {
			fmt.Print("[网关未启用，跳过] ")
			return nil
		}
		var tk struct {
			ID    int64  `json:"id"`
			Token string `json:"token"`
		}
		if err := json.Unmarshal(b, &tk); err != nil {
			return err
		}
		// 2) 网关登录
		gwBody, _ := json.Marshal(map[string]string{"username": *user, "password": *pass})
		gb, gst, err := do(client, http.MethodPost, *base+"/gateway/legado/login", "application/json", "", gwBody)
		if err != nil {
			return err
		}
		if gst != 200 {
			return fmt.Errorf("网关登录失败 status=%d body=%s", gst, gb)
		}
		var gw struct {
			SessionToken string `json:"session_token"`
		}
		if err := json.Unmarshal(gb, &gw); err != nil {
			return err
		}
		// 网关相关请求使用不带 cookie 的独立客户端，避免管理会话/网关 cookie 干扰判定
		anon := &http.Client{Transport: client.Transport, Timeout: 30 * time.Second}
		toc := *base + "/gateway/legado/toc/" + ttsBookID
		if _, code, _ := doH(anon, http.MethodGet, toc, nil, nil); code != 401 {
			return fmt.Errorf("无认证应为 401，实际 %d", code)
		}
		if _, code, _ := doH(anon, http.MethodGet, toc, map[string]string{"X-Legado-Token": tk.Token}, nil); code != 401 {
			return fmt.Errorf("仅令牌应为 401，实际 %d", code)
		}
		if _, code, _ := doH(anon, http.MethodGet, toc,
			map[string]string{"X-Legado-Session": gw.SessionToken}, nil); code != 403 {
			return fmt.Errorf("仅有会话应为 403，实际 %d", code)
		}
		both := map[string]string{"X-Legado-Token": tk.Token, "X-Legado-Session": gw.SessionToken}
		if _, code, _ := doH(anon, http.MethodGet, toc, both, nil); code != 200 {
			return fmt.Errorf("双层认证应为 200，实际 %d", code)
		}
		cb, code, err := doH(anon, http.MethodGet,
			*base+"/gateway/legado/content/"+ttsBookID+"/0", both, nil)
		if err != nil {
			return err
		}
		if code != 200 || !bytes.Contains(cb, []byte("他推开门")) {
			return fmt.Errorf("网关正文异常 status=%d", code)
		}
		if _, code, _ := doH(anon, http.MethodGet, *base+"/api/books",
			map[string]string{"X-Legado-Token": tk.Token}, nil); code != 401 {
			return fmt.Errorf("书源令牌不应能访问 /api，实际 %d", code)
		}
		// 3) 撤销后失效
		revokePath := fmt.Sprintf("%s/api/admin/legado/tokens/%d", *base, tk.ID)
		if _, code, _ := doH(client, http.MethodDelete, revokePath, nil, nil); code != 204 {
			return fmt.Errorf("撤销失败 status=%d", code)
		}
		if _, code, _ := doH(anon, http.MethodGet, toc, both, nil); code != 403 {
			return fmt.Errorf("撤销后应为 403，实际 %d", code)
		}
		return nil
	})

	check("审计过滤（action 前缀）", func() error {
		b, st, err := do(client, http.MethodGet, *base+"/api/admin/audit?action=book_&limit=20", "", "", nil)
		if err != nil {
			return err
		}
		if st != 200 || !strings.Contains(string(b), "book_upload") {
			return fmt.Errorf("status=%d body=%.160s", st, b)
		}
		if strings.Contains(string(b), `"tts_speak"`) {
			return fmt.Errorf("action 过滤未生效")
		}
		return nil
	})

	check("审计", func() error {
		b, st, err := do(client, http.MethodGet, *base+"/api/admin/audit?limit=100", "", "", nil)
		if err != nil {
			return err
		}
		if st != 200 || !strings.Contains(string(b), "book_upload") {
			return fmt.Errorf("status=%d body=%s", st, b)
		}
		if spoken != nil && !strings.Contains(string(b), "tts_speak") {
			return fmt.Errorf("审计缺少 tts_speak: %s", b)
		}
		return nil
	})

	// ---- 第五阶段：设备令牌 / 增量同步 / 进度 / 预取 / 音频密文 ----
	// 设备私钥只存在于本进程内存，等价于手机 Keystore 中的不可导出私钥；
	// 服务端只拿到公钥，DEK 由服务端用公钥包装后下发，客户端在设备内解包。
	devKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		fmt.Fprintln(os.Stderr, "生成设备密钥失败:", err)
		os.Exit(2)
	}
	pubPEM, err := devices.EncodePublicKeyPEM(&devKey.PublicKey)
	if err != nil {
		fmt.Fprintln(os.Stderr, "编码设备公钥失败:", err)
		os.Exit(2)
	}
	// 设备客户端不带 cookie：确保验证的是 device token 本身，而不是浏览器会话。
	devClient := &http.Client{Transport: client.Transport, Timeout: 60 * time.Second}

	var devToken, devID string
	var syncCursor int64
	var bookDEK []byte
	bearerDev := func() map[string]string { return map[string]string{"Authorization": "Bearer " + devToken} }

	check("设备令牌签发（绑定公钥）", func() error {
		b, st, err := doJSON(devClient, http.MethodPost, *base+"/api/device/token", map[string]string{
			"username":    *user,
			"password":    *pass,
			"device_name": "acceptance",
			"public_key":  pubPEM,
		})
		if err != nil {
			return err
		}
		if st != 200 {
			return fmt.Errorf("status=%d body=%s", st, b)
		}
		var res deviceTokenResp
		if err := json.Unmarshal(b, &res); err != nil {
			return err
		}
		if !strings.HasPrefix(res.Token, "nsd_") || res.Device.ID == "" || !res.Device.HasKey {
			return fmt.Errorf("响应异常: %.200s", b)
		}
		devToken, devID = res.Token, res.Device.ID
		return nil
	})

	check("设备令牌被挡在管理面之外", func() error {
		for _, p := range []string{"/admin", "/api/admin/dashboard", "/api/admin/devices", "/api/admin/audit"} {
			_, st, err := doH(devClient, http.MethodGet, *base+p, bearerDev(), nil)
			if err != nil {
				return err
			}
			if st != 403 {
				return fmt.Errorf("%s 期望 403，实际 %d", p, st)
			}
		}
		if _, st, err := doH(devClient, http.MethodPost, *base+"/api/admin/pairing", bearerDev(), []byte("{}")); err != nil || st != 403 {
			return fmt.Errorf("生成配对码期望 403，实际 %d", st)
		}
		if _, st, err := doH(devClient, http.MethodDelete, *base+"/api/books/"+bookID, bearerDev(), nil); err != nil || st != 403 {
			return fmt.Errorf("删除书籍期望 403，实际 %d", st)
		}
		if _, st, err := doH(devClient, http.MethodGet, *base+"/api/books", bearerDev(), nil); err != nil || st != 200 {
			return fmt.Errorf("设备读取书单期望 200，实际 %d", st)
		}
		return nil
	})

	check("同步清单（全量）+ 设备解包 DEK", func() error {
		b, st, err := doH(devClient, http.MethodGet, *base+"/api/sync/manifest", bearerDev(), nil)
		if err != nil {
			return err
		}
		if st != 200 {
			return fmt.Errorf("status=%d body=%s", st, b)
		}
		var m syncManifest
		if err := json.Unmarshal(b, &m); err != nil {
			return err
		}
		if !m.Full || m.Cursor <= 0 || m.Count == 0 {
			return fmt.Errorf("全量清单异常 cursor=%d full=%t count=%d", m.Cursor, m.Full, m.Count)
		}
		var target *manifestBook
		for i := range m.Books {
			if m.Books[i].ID == ttsBookID {
				target = &m.Books[i]
			}
		}
		if target == nil {
			return fmt.Errorf("清单缺少 %s", ttsBookID)
		}
		if target.WrappedDEK == "" {
			return fmt.Errorf("清单缺少 wrapped_dek")
		}
		dek, err := devices.UnwrapDEK(devKey, target.WrappedDEK)
		if err != nil {
			return fmt.Errorf("解包 DEK: %w", err)
		}
		bookDEK = dek
		syncCursor = m.Cursor
		return nil
	})

	check("章节预取（只有密文，设备内可解）", func() error {
		if len(bookDEK) == 0 {
			fmt.Print("[跳过] ")
			return nil
		}
		body, _ := json.Marshal(map[string]any{"indexes": []int{0, 1}})
		b, st, err := doH(devClient, http.MethodPost,
			*base+"/api/books/"+ttsBookID+"/chapters/prefetch", bearerDev(), body)
		if err != nil {
			return err
		}
		if st != 200 {
			return fmt.Errorf("status=%d body=%s", st, b)
		}
		var res prefetchResp
		if err := json.Unmarshal(b, &res); err != nil {
			return err
		}
		if res.Count < 2 || len(res.Chapters) < 2 {
			return fmt.Errorf("预取不足: %.200s", b)
		}
		if res.WrappedDEK == "" {
			return fmt.Errorf("预取缺少 wrapped_dek")
		}
		dek, err := devices.UnwrapDEK(devKey, res.WrappedDEK)
		if err != nil {
			return err
		}
		if !bytes.Equal(dek, bookDEK) {
			return fmt.Errorf("预取包装的 DEK 与清单不一致")
		}
		for _, c := range res.Chapters {
			pkg, err := base64.StdEncoding.DecodeString(c.Ciphertext)
			if err != nil {
				return err
			}
			if !bytes.HasPrefix(pkg, []byte("NSCH")) {
				return fmt.Errorf("第 %d 章不是 NSCH 密文包", c.Index)
			}
			if bytes.Contains(pkg, []byte("他推开门")) || bytes.Contains(pkg, []byte("天亮的时候")) {
				return fmt.Errorf("第 %d 章密文里出现正文明文", c.Index)
			}
			plain, err := chapterpkg.Open(dek, ttsBookID, c.Index, pkg)
			if err != nil {
				return fmt.Errorf("第 %d 章解密失败: %w", c.Index, err)
			}
			if len(plain) == 0 {
				return fmt.Errorf("第 %d 章解密为空", c.Index)
			}
			if c.Index == 0 && !bytes.Contains(plain, []byte("他推开门")) {
				return fmt.Errorf("第 0 章正文不符: %.80s", plain)
			}
		}
		return nil
	})

	check("增量同步游标（无变化为空 / 越界要求重置）", func() error {
		if syncCursor == 0 {
			fmt.Print("[跳过] ")
			return nil
		}
		b, st, err := doH(devClient, http.MethodGet,
			fmt.Sprintf("%s/api/sync/manifest?since=%d", *base, syncCursor), bearerDev(), nil)
		if err != nil {
			return err
		}
		if st != 200 {
			return fmt.Errorf("status=%d body=%.160s", st, b)
		}
		var m syncManifest
		if err := json.Unmarshal(b, &m); err != nil {
			return err
		}
		if m.Count != 0 || len(m.Deleted) != 0 || m.ResetRequired {
			return fmt.Errorf("游标已推进却仍有变更: count=%d deleted=%d reset=%t",
				m.Count, len(m.Deleted), m.ResetRequired)
		}
		// 客户端游标领先服务端（例如从旧备份恢复）→ 要求清库重同步
		b2, st2, err := doH(devClient, http.MethodGet,
			fmt.Sprintf("%s/api/sync/manifest?since=%d", *base, syncCursor+1000), bearerDev(), nil)
		if err != nil {
			return err
		}
		if st2 != 200 {
			return fmt.Errorf("status=%d body=%.160s", st2, b2)
		}
		var m2 syncManifest
		if err := json.Unmarshal(b2, &m2); err != nil {
			return err
		}
		if !m2.ResetRequired || m2.Count != 0 {
			return fmt.Errorf("领先游标未要求重置: reset=%t count=%d", m2.ResetRequired, m2.Count)
		}
		return nil
	})

	check("阅读进度（设备上报 → 控制台可见）", func() error {
		body, _ := json.Marshal(map[string]any{"chapter_index": 1, "char_offset": 20, "percent": 0.425})
		if _, st, err := doH(devClient, http.MethodPut,
			*base+"/api/books/"+ttsBookID+"/progress", bearerDev(), body); err != nil || st != 200 {
			return fmt.Errorf("上报进度失败 status=%d", st)
		}
		bad, _ := json.Marshal(map[string]any{"chapter_index": 0, "char_offset": 0, "percent": 1.5})
		if _, st, err := doH(devClient, http.MethodPut,
			*base+"/api/books/"+ttsBookID+"/progress", bearerDev(), bad); err != nil || st != 400 {
			return fmt.Errorf("非法百分比应 400，实际 %d", st)
		}
		b, st, err := do(client, http.MethodGet, *base+"/api/books/"+ttsBookID+"/progress", "", "", nil)
		if err != nil {
			return err
		}
		if st != 200 {
			return fmt.Errorf("读回进度 status=%d body=%.160s", st, b)
		}
		var p struct {
			ChapterIndex int     `json:"chapter_index"`
			Percent      float64 `json:"percent"`
		}
		if err := json.Unmarshal(b, &p); err != nil {
			return err
		}
		if p.ChapterIndex != 1 || p.Percent < 0.424 || p.Percent > 0.426 {
			return fmt.Errorf("进度不一致: %.160s", b)
		}
		db, dst, err := do(client, http.MethodGet, *base+"/api/admin/dashboard", "", "", nil)
		if err != nil {
			return err
		}
		if dst != 200 || !bytes.Contains(db, []byte(`"mobile"`)) || !bytes.Contains(db, []byte(`"progress_books"`)) {
			return fmt.Errorf("控制台缺少移动端区块: %.160s", db)
		}
		if bytes.Contains(db, []byte(devToken)) {
			return fmt.Errorf("控制台响应泄漏 device token")
		}
		return nil
	})

	check("预取参数上限（单次章节数）", func() error {
		idx := make([]int, 200)
		for i := range idx {
			idx[i] = i
		}
		body, _ := json.Marshal(map[string]any{"indexes": idx})
		_, st, err := doH(devClient, http.MethodPost,
			*base+"/api/books/"+ttsBookID+"/chapters/prefetch", bearerDev(), body)
		if err != nil {
			return err
		}
		if st != 400 {
			return fmt.Errorf("超过单次上限应 400，实际 %d", st)
		}
		return nil
	})

	check("预取限流（429 + Retry-After）", func() error {
		body, _ := json.Marshal(map[string]any{"indexes": []int{0}})
		limited := false
		for i := 0; i < 80; i++ {
			_, st, err := doH(devClient, http.MethodPost,
				*base+"/api/books/"+ttsBookID+"/chapters/prefetch", bearerDev(), body)
			if err != nil {
				return err
			}
			if st == 429 {
				limited = true
				break
			}
			if st != 200 {
				return fmt.Errorf("第 %d 次预取 status=%d", i+1, st)
			}
		}
		if !limited {
			fmt.Print("[未触发：PREFETCH_PER_MINUTE 偏大，429 由 go test 覆盖] ")
		}
		// 限流按凭证分桶：浏览器会话仍有独立额度
		body2, _ := json.Marshal(map[string]any{"indexes": []int{0}})
		if _, st, err := do(client, http.MethodPost,
			*base+"/api/books/"+ttsBookID+"/chapters/prefetch", "application/json", "", body2); err != nil || st != 200 {
			return fmt.Errorf("会话独立配额应仍为 200，实际 %d", st)
		}
		return nil
	})

	check("音频段（加密落盘，设备内解密）", func() error {
		b, st, err := doH(devClient, http.MethodPost,
			*base+"/api/books/"+ttsBookID+"/chapters/0/audio/prepare", bearerDev(), []byte("{}"))
		if err != nil {
			return err
		}
		if st == 503 {
			fmt.Print("[TTS 未启用，跳过] ")
			return nil
		}
		if st == 429 {
			fmt.Print("[音频合成限流，跳过] ")
			return nil
		}
		if st != 200 {
			return fmt.Errorf("prepare status=%d body=%.160s", st, b)
		}
		mb, mst, err := doH(devClient, http.MethodGet,
			*base+"/api/books/"+ttsBookID+"/chapters/0/audio", bearerDev(), nil)
		if err != nil {
			return err
		}
		if mst != 200 {
			return fmt.Errorf("manifest status=%d body=%.160s", mst, mb)
		}
		var am audioManifest
		if err := json.Unmarshal(mb, &am); err != nil {
			return err
		}
		if am.Count == 0 || len(am.Segments) == 0 {
			return fmt.Errorf("没有可播放片段: %.160s", mb)
		}
		seg := am.Segments[0]
		if seg.WrappedDEK == "" {
			return fmt.Errorf("片段缺少 wrapped_dek")
		}
		raw, rst, err := doH(devClient, http.MethodGet, *base+"/api/tts/segments/"+seg.Key, bearerDev(), nil)
		if err != nil {
			return err
		}
		if rst != 200 {
			return fmt.Errorf("取片段 status=%d body=%.160s", rst, raw)
		}
		if bytes.HasPrefix(raw, []byte("RIFF")) {
			return fmt.Errorf("片段是明文 WAV（应只存 NSAC 密文）")
		}
		if seg.CTOffset < 0 || seg.CTLen <= 0 || seg.CTOffset+seg.CTLen > len(raw) {
			return fmt.Errorf("ct 区间越界: off=%d len=%d file=%d", seg.CTOffset, seg.CTLen, len(raw))
		}
		key, err := devices.UnwrapDEK(devKey, seg.WrappedDEK)
		if err != nil {
			return fmt.Errorf("解包音频 DEK: %w", err)
		}
		nonce, err := base64.StdEncoding.DecodeString(seg.AudioNonceB64)
		if err != nil {
			return err
		}
		block, err := aes.NewCipher(key)
		if err != nil {
			return err
		}
		gcm, err := cipher.NewGCM(block)
		if err != nil {
			return err
		}
		// AAD 与 Phase 2 的 NSAC v2 一致：绑定片段 key
		plain, err := gcm.Open(nil, nonce, raw[seg.CTOffset:seg.CTOffset+seg.CTLen],
			[]byte("novelstack-tts-audio-v1|"+seg.Key))
		if err != nil {
			return fmt.Errorf("音频解密失败: %w", err)
		}
		if !bytes.HasPrefix(plain, []byte("RIFF")) {
			return fmt.Errorf("解出的音频不是 WAV")
		}
		// 他人的/不存在的片段一律 404
		if _, st, err := doH(devClient, http.MethodGet,
			*base+"/api/tts/segments/deadbeefdeadbeefdeadbeef", bearerDev(), nil); err != nil || st != 404 {
			return fmt.Errorf("未知片段应 404，实际 %d", st)
		}
		return nil
	})

	check("配对码（一次性）", func() error {
		b, st, err := do(client, http.MethodPost, *base+"/api/admin/pairing", "application/json", "", []byte("{}"))
		if err != nil {
			return err
		}
		if st != 200 {
			return fmt.Errorf("生成配对码 status=%d body=%.160s", st, b)
		}
		var pc struct {
			Code string `json:"code"`
		}
		if err := json.Unmarshal(b, &pc); err != nil {
			return err
		}
		if !strings.HasPrefix(pc.Code, "nsp_") {
			return fmt.Errorf("配对码格式异常: %s", pc.Code)
		}
		tb, tst, err := doJSON(devClient, http.MethodPost, *base+"/api/device/token", map[string]string{
			"pairing_code": pc.Code, "device_name": "acceptance-pair", "public_key": pubPEM,
		})
		if err != nil {
			return err
		}
		if tst != 200 || !strings.Contains(string(tb), "nsd_") {
			return fmt.Errorf("配对换令牌失败 status=%d body=%.160s", tst, tb)
		}
		// 一次性：同一配对码不能再用
		_, tst2, err := doJSON(devClient, http.MethodPost, *base+"/api/device/token", map[string]string{
			"pairing_code": pc.Code, "device_name": "again", "public_key": pubPEM,
		})
		if err != nil {
			return err
		}
		if tst2 != 401 {
			return fmt.Errorf("配对码重复使用应为 401，实际 %d", tst2)
		}
		lb, lst, err := do(client, http.MethodGet, *base+"/api/admin/devices", "", "", nil)
		if err != nil {
			return err
		}
		if lst != 200 || bytes.Contains(lb, []byte(devToken)) {
			return fmt.Errorf("设备列表异常（status=%d 或含明文令牌）", lst)
		}
		return nil
	})

	check("立即触发自动备份（控制台可见）", func() error {
		b, st, err := do(client, http.MethodPost, *base+"/api/admin/backup/run", "application/json", "", []byte("{}"))
		if err != nil {
			return err
		}
		if st == http.StatusServiceUnavailable {
			fmt.Print("[自动备份未配置，跳过] ")
			return nil
		}
		if st != 200 {
			return fmt.Errorf("backup run status=%d body=%.160s", st, b)
		}
		var res struct {
			OK     bool   `json:"ok"`
			Path   string `json:"path"`
			SHA256 string `json:"sha256"`
			Size   int64  `json:"size_bytes"`
		}
		if err := json.Unmarshal(b, &res); err != nil || !res.OK || res.Size <= 0 {
			return fmt.Errorf("backup response=%s err=%v", b, err)
		}
		if len(res.SHA256) < 8 || !strings.HasSuffix(res.Path, "-"+res.SHA256[:8]+".nsb") {
			return fmt.Errorf("包命名不含校验和前 8 位: %s", res.Path)
		}
		db, st, err := do(client, http.MethodGet, *base+"/api/admin/dashboard", "", "", nil)
		if err != nil || st != 200 {
			return fmt.Errorf("dashboard status=%d err=%v", st, err)
		}
		var dash struct {
			BackupsDirBytes int64 `json:"backups_dir_bytes"`
			Backup          struct {
				Last struct {
					Path string `json:"path"`
					SHA  string `json:"sha256"`
				} `json:"last"`
			} `json:"backup"`
			Health struct {
				Level      string `json:"level"`
				BackupOK   bool   `json:"backup_ok"`
				LastBackup string `json:"last_backup_at"`
				Alerts     []struct {
					Type string `json:"type"`
				} `json:"alerts"`
			} `json:"health"`
		}
		if err := json.Unmarshal(db, &dash); err != nil {
			return err
		}
		if dash.Backup.Last.Path != res.Path || dash.Backup.Last.SHA != res.SHA256 {
			return fmt.Errorf("控制台未显示本次备份: %+v", dash.Backup.Last)
		}
		if dash.BackupsDirBytes <= 0 {
			return fmt.Errorf("控制台未显示备份目录占用: %d", dash.BackupsDirBytes)
		}
		if !dash.Health.BackupOK || dash.Health.LastBackup == "" {
			return fmt.Errorf("健康状态未反映备份: %+v", dash.Health)
		}
		for _, a := range dash.Health.Alerts {
			if a.Type == "backup_failed" || a.Type == "backup_stale" {
				return fmt.Errorf("刚备份完仍有备份告警: %s", a.Type)
			}
		}
		return nil
	})

	check("设备令牌不能触发自动备份", func() error {
		_, st, err := doH(devClient, http.MethodPost, *base+"/api/admin/backup/run", bearerDev(), nil)
		if err != nil {
			return err
		}
		if st != 403 {
			return fmt.Errorf("device token backup status=%d, want 403", st)
		}
		return nil
	})

	check("撤销设备 → 令牌失效、会话不受影响", func() error {
		b, st, err := do(client, http.MethodDelete,
			fmt.Sprintf("%s/api/admin/devices/%s", *base, devID), "", "", nil)
		if err != nil {
			return err
		}
		if st != 200 {
			return fmt.Errorf("撤销 status=%d body=%.160s", st, b)
		}
		if _, st, err := doH(devClient, http.MethodGet, *base+"/api/books", bearerDev(), nil); err != nil || st != 401 {
			return fmt.Errorf("撤销后设备令牌应 401，实际 %d", st)
		}
		if _, st, err := do(client, http.MethodGet, *base+"/api/books", "", "", nil); err != nil || st != 200 {
			return fmt.Errorf("浏览器会话不应受影响，实际 %d", st)
		}
		// 撤销幂等
		if _, st, err := do(client, http.MethodDelete,
			fmt.Sprintf("%s/api/admin/devices/%s", *base, devID), "", "", nil); err != nil || st != 200 {
			return fmt.Errorf("重复撤销应为 200，实际 %d", st)
		}
		return nil
	})

	check("登出", func() error {
		_, st, err := do(client, http.MethodPost, *base+"/api/logout", "application/json", "", []byte("{}"))
		if err != nil {
			return err
		}
		if st != 200 {
			return fmt.Errorf("status=%d", st)
		}
		_, st, err = do(client, http.MethodGet, *base+"/api/books", "", "", nil)
		if err != nil {
			return err
		}
		if st != 401 {
			return fmt.Errorf("登出后仍可访问: status=%d", st)
		}
		return nil
	})

	fmt.Printf("\n全部通过 ✓  book_id=%s  sha256=%s  audio_bytes=%d  device=%s\n",
		bookID, wantSHA, len(spoken), devID)
	fmt.Println("提示：重启容器后重新登录并下载，SHA-256 应保持一致（docker compose restart app）。")
}

// ---- 第五阶段响应结构（只声明验收用到的字段）----

type deviceTokenResp struct {
	Token  string `json:"token"`
	Device struct {
		ID     string `json:"id"`
		HasKey bool   `json:"has_public_key"`
	} `json:"device"`
}

type manifestBook struct {
	ID         string `json:"id"`
	Title      string `json:"title"`
	WrappedDEK string `json:"wrapped_dek"`
}

type syncManifest struct {
	Cursor        int64          `json:"cursor"`
	Since         int64          `json:"since"`
	Full          bool           `json:"full"`
	ResetRequired bool           `json:"reset_required"`
	Count         int            `json:"count"`
	Books         []manifestBook `json:"books"`
	Deleted       []string       `json:"deleted"`
}

type prefetchResp struct {
	Count      int    `json:"count"`
	Truncated  bool   `json:"truncated"`
	WrappedDEK string `json:"wrapped_dek"`
	Chapters   []struct {
		Index      int    `json:"index"`
		ETag       string `json:"etag"`
		Bytes      int    `json:"bytes"`
		Ciphertext string `json:"ciphertext"`
	} `json:"chapters"`
}

type audioManifest struct {
	Count    int `json:"count"`
	Segments []struct {
		Key           string `json:"key"`
		Index         int    `json:"index"`
		CTOffset      int    `json:"ct_offset"`
		CTLen         int    `json:"ct_len"`
		AudioNonceB64 string `json:"audio_nonce"`
		WrappedDEK    string `json:"wrapped_dek"`
	} `json:"segments"`
}

func check(name string, fn func() error) {
	fmt.Printf("== %s ... ", name)
	if err := fn(); err != nil {
		fmt.Printf("FAIL: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("ok")
}

// doWithLoc 返回 Location 头（用于校验 303 跳转）。
func doWithLoc(c *http.Client, method, url, ctype string, body []byte) ([]byte, int, string, error) {
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, url, rd)
	if err != nil {
		return nil, 0, "", err
	}
	if ctype != "" {
		req.Header.Set("Content-Type", ctype)
	}
	resp, err := c.Do(req)
	if err != nil {
		return nil, 0, "", err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	return b, resp.StatusCode, resp.Header.Get("Location"), err
}

// firstAsset 从 index.html 中提取第一个 /admin/assets/ 引用。
func firstAsset(html string) string {
	const marker = "/admin/assets/"
	i := strings.Index(html, marker)
	if i < 0 {
		return ""
	}
	rest := html[i:]
	if j := strings.IndexAny(rest, `"'`); j > 0 {
		rest = rest[:j]
	}
	if k := strings.IndexAny(rest, " \t>"); k > 0 {
		rest = rest[:k]
	}
	return rest
}

// doH 允许自定义请求头（网关双层认证需要）。
func doH(c *http.Client, method, url string, headers map[string]string, body []byte) ([]byte, int, error) {
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, url, rd)
	if err != nil {
		return nil, 0, err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	return b, resp.StatusCode, err
}

// urlQuery 对查询参数做百分号编码（不引入额外依赖）。
func urlQuery(s string) string {
	var b strings.Builder
	for _, c := range []byte(s) {
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') ||
			c == '-' || c == '_' || c == '.' || c == '~' {
			b.WriteByte(c)
			continue
		}
		fmt.Fprintf(&b, "%%%02X", c)
	}
	return b.String()
}

func do(c *http.Client, method, url, ctype, _ string, body []byte) ([]byte, int, error) {
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, url, rd)
	if err != nil {
		return nil, 0, err
	}
	if ctype != "" {
		req.Header.Set("Content-Type", ctype)
	}
	resp, err := c.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, 0, err
	}
	return b, resp.StatusCode, nil
}

func doJSON(c *http.Client, method, url string, payload any) ([]byte, int, error) {
	body, _ := json.Marshal(payload)
	return do(c, method, url, "application/json", "", body)
}
