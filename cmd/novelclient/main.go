// novelclient 是第三阶段的参考阅读客户端（命令行）。
//
// 它验证三件事：
//  1. 章节明文按 ETag 协商拉取（304 不重复传输）；
//  2. 明文只写入「整库加密」的本地索引（磁盘上无明文索引）；
//  3. 全文检索在本地 FTS5 完成，查询词不经过服务端。
//
// 用法：
//
//	novelclient -base https://localhost:8443 -insecure -store ./local sync
//	novelclient ... search "夜航船"
//	novelclient ... cat -book <id> -chapter 1
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"novelstack/clientcore"
)

func main() {
	log.SetFlags(0)
	base := flag.String("base", "https://localhost:8443", "服务端基址")
	insecure := flag.Bool("insecure", false, "跳过自签证书校验（仅本地部署）")
	storeDir := flag.String("store", "./local-reader", "本地索引目录（只存放密文快照）")
	user := flag.String("user", "admin", "用户名")
	pass := flag.String("password", "", "口令（也可用 NOVELCLIENT_PASSWORD）")
	passFile := flag.String("passphrase-file", "", "本地索引口令文件（也可用 NOVELCLIENT_PASSPHRASE）")
	clientID := flag.String("client-id", "desktop", "客户端标识（写回执用）")
	dialAddr := flag.String("dial-addr", "", "TCP 实际拨号地址（如 caddy:8443），URL 仍用 -base")
	bookID := flag.String("book", "", "书籍 ID（sync/cat 使用）")
	chapter := flag.Int("chapter", 0, "章节序号（cat 使用）")
	flag.Parse()

	args := flag.Args()
	if len(args) == 0 {
		usage()
	}
	if *pass == "" {
		*pass = os.Getenv("NOVELCLIENT_PASSWORD")
	}
	passphrase := []byte(os.Getenv("NOVELCLIENT_PASSPHRASE"))
	if *passFile != "" {
		b, err := os.ReadFile(*passFile)
		if err != nil {
			log.Fatalf("读取 passphrase 文件失败: %v", err)
		}
		passphrase = []byte(strings.TrimSpace(string(b)))
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	kp := &clientcore.FileKeyProvider{
		SaltPath:   filepath.Join(*storeDir, "device.salt"),
		Passphrase: passphrase,
	}
	store, err := clientcore.Open(filepath.Join(*storeDir, "index.enc"), kp, *clientID)
	if err != nil {
		log.Fatalf("打开本地索引失败: %v", err)
	}
	defer func() {
		if err := store.Close(); err != nil {
			log.Printf("保存本地索引失败: %v", err)
		}
	}()

	switch args[0] {
	case "sync":
		runSync(ctx, *base, *insecure, *user, *pass, *dialAddr, *bookID, store)
	case "search":
		if len(args) < 2 {
			log.Fatal("用法: novelclient ... search <关键词>")
		}
		runSearch(store, strings.Join(args[1:], " "))
	case "cat":
		runCat(ctx, *base, *insecure, *user, *pass, *dialAddr, *bookID, *chapter, store)
	case "local":
		runLocal(store)
	default:
		usage()
	}
}

func usage() {
	fmt.Fprintf(os.Stderr, `用法: novelclient [flags] <sync|search|cat|local> [args]

  sync            拉取书籍章节写入本地加密索引（-book 指定单本，缺省同步全部）
  search <词>     在本地 FTS5 索引中检索（不上传查询词）
  cat             从本地索引输出某章文本（-book -chapter）
  local           列出本地已缓存的书籍与索引规模
`)
	os.Exit(2)
}

func login(ctx context.Context, base string, insecure bool, user, pass, dialAddr string) *clientcore.Client {
	if pass == "" {
		log.Fatal("缺少口令：-password 或 NOVELCLIENT_PASSWORD")
	}
	c, err := clientcore.NewClient(base, insecure)
	if err != nil {
		log.Fatalf("初始化客户端失败: %v", err)
	}
	if dialAddr != "" {
		c.SetDialAddr(dialAddr)
	}
	if err := c.Login(ctx, user, pass); err != nil {
		log.Fatalf("登录失败: %v", err)
	}
	return c
}

func runSync(ctx context.Context, base string, insecure bool, user, pass, dialAddr, want string, store *clientcore.Store) {
	c := login(ctx, base, insecure, user, pass, dialAddr)
	books, err := c.ListBooks(ctx)
	if err != nil {
		log.Fatalf("获取书籍列表失败: %v", err)
	}
	if want != "" {
		filtered := books[:0]
		for _, b := range books {
			if b.ID == want {
				filtered = append(filtered, b)
			}
		}
		books = filtered
	}
	if len(books) == 0 {
		log.Fatal("没有可同步的书籍")
	}
	total := clientcore.SyncResult{}
	for _, b := range books {
		res, err := store.SyncBook(ctx, c, b)
		if err != nil {
			log.Fatalf("同步《%s》失败: %v", b.Title, err)
		}
		fmt.Printf("《%s》 拉取=%d 跳过(304/已缓存)=%d 回执=%d\n", b.Title, res.Fetched, res.Skipped, res.Acked)
		total.Fetched += res.Fetched
		total.Skipped += res.Skipped
		total.Acked += res.Acked
	}
	fmt.Printf("完成：拉取 %d 章，跳过 %d 章，回执 %d 章\n", total.Fetched, total.Skipped, total.Acked)
}

func runSearch(store *clientcore.Store, q string) {
	hits, err := store.Search(q, 20)
	if err != nil {
		log.Fatalf("本地检索失败: %v", err)
	}
	if len(hits) == 0 {
		fmt.Println("本地索引中没有命中（未缓存章节不参与全文搜索）")
		return
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetEscapeHTML(false)
	for _, h := range hits {
		_ = enc.Encode(h)
	}
}

func runCat(ctx context.Context, base string, insecure bool, user, pass, dialAddr, book string, idx int, store *clientcore.Store) {
	if book == "" {
		log.Fatal("cat 需要 -book")
	}
	// 本地优先：已缓存则直接输出，不经过网络
	if text, ok := store.Chapter(book, idx); ok {
		fmt.Print(text)
		return
	}
	c := login(ctx, base, insecure, user, pass, dialAddr)
	chapters, err := c.Chapters(ctx, book)
	if err != nil {
		log.Fatalf("获取目录失败: %v", err)
	}
	if idx < 0 || idx >= len(chapters) {
		log.Fatalf("章节序号越界（0..%d）", len(chapters)-1)
	}
	text, etag, err := c.ChapterPlain(ctx, book, idx, "")
	if err != nil {
		log.Fatalf("拉取章节失败: %v", err)
	}
	_ = store.PutChapter(clientcore.ChapterRecord{
		BookID: book, Index: idx, Title: chapters[idx].Title, Text: text, ETag: etag,
	})
	fmt.Print(text)
}

func runLocal(store *clientcore.Store) {
	books, err := store.Books()
	if err != nil {
		log.Fatalf("读取本地索引失败: %v", err)
	}
	chapters, chars, _ := store.Stats()
	for _, b := range books {
		fmt.Printf("%s  《%s》 %s  已缓存章节=%d\n", b.BookID, b.Title, b.Author, b.Chapters)
	}
	fmt.Printf("本地索引：%d 章，%d 字符（磁盘上仅密文快照）\n", chapters, chars)
}
