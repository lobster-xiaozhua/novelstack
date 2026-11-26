package clientcore

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"strings"
	"time"
)

// Client 是阅读器的 HTTP 客户端：只处理「已加密传输 + 服务端已解密」的接口。
// 它不做任何解密：章节明文由服务端在 TLS 内返回，随后写入本地加密索引。
type Client struct {
	BaseURL string
	HTTP    *http.Client
	tlsCfg  *tls.Config
}

// SetDialAddr 把 TCP 拨号固定到指定地址（URL/Host 保持不变）。
// 用于「URL 是 localhost、实际连到容器名」的内部验收场景。
func (c *Client) SetDialAddr(addr string) {
	d := &net.Dialer{Timeout: 10 * time.Second}
	tr := &http.Transport{
		TLSClientConfig: c.tlsCfg,
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return d.DialContext(ctx, network, addr)
		},
	}
	c.HTTP.Transport = tr
}

// BookMeta 是服务端返回的书籍元数据（子集）。
type BookMeta struct {
	ID           string   `json:"id"`
	Title        string   `json:"title"`
	Author       string   `json:"author"`
	Tags         []string `json:"tags"`
	ChapterCount int      `json:"chapter_count"`
}

// ChapterInfo 是章节目录的一行。
type ChapterInfo struct {
	Index int    `json:"index"`
	Title string `json:"title"`
	Chars int    `json:"chars"`
	ETag  string `json:"etag"`
}

// NewClient 构造客户端（带 cookie jar）。insecure 仅用于自签证书的本地部署。
func NewClient(baseURL string, insecure bool) (*Client, error) {
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, err
	}
	tlsCfg := &tls.Config{InsecureSkipVerify: insecure} // #nosec G402 -- 本地自签证书需显式 opt-in
	return &Client{
		BaseURL: strings.TrimRight(baseURL, "/"),
		tlsCfg:  tlsCfg,
		HTTP:    &http.Client{Transport: &http.Transport{TLSClientConfig: tlsCfg}, Jar: jar, Timeout: 120 * time.Second},
	}, nil
}

func (c *Client) do(ctx context.Context, method, path, ctype string, body []byte) (int, http.Header, []byte, error) {
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, rd)
	if err != nil {
		return 0, nil, nil, err
	}
	if ctype != "" {
		req.Header.Set("Content-Type", ctype)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return 0, nil, nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 256<<20))
	return resp.StatusCode, resp.Header, b, err
}

// Login 登录并保存 session cookie。
func (c *Client) Login(ctx context.Context, username, password string) error {
	body, _ := json.Marshal(map[string]string{"username": username, "password": password})
	status, _, _, err := c.do(ctx, http.MethodPost, "/api/login", "application/json", body)
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return fmt.Errorf("login failed: status=%d", status)
	}
	return nil
}

// ListBooks 列出服务端书籍元数据。
func (c *Client) ListBooks(ctx context.Context) ([]BookMeta, error) {
	status, _, b, err := c.do(ctx, http.MethodGet, "/api/books", "", nil)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("list books: status=%d", status)
	}
	var out []BookMeta
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// Chapters 获取章节目录（不含正文）。
func (c *Client) Chapters(ctx context.Context, bookID string) ([]ChapterInfo, error) {
	status, _, b, err := c.do(ctx, http.MethodGet, "/api/books/"+bookID+"/chapters", "", nil)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("chapters: status=%d", status)
	}
	var wrap struct {
		Chapters []ChapterInfo `json:"chapters"`
	}
	if err := json.Unmarshal(b, &wrap); err != nil {
		return nil, err
	}
	return wrap.Chapters, nil
}

// ChapterPlain 拉取单章明文，带 If-None-Match 协商。
// 返回 ErrNotModified 表示本地缓存仍然有效（304）。
func (c *Client) ChapterPlain(ctx context.Context, bookID string, index int, etag string) (string, string, error) {
	path := fmt.Sprintf("/api/books/%s/chapters/%d/plain", bookID, index)
	var rd io.Reader
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+path, rd)
	if err != nil {
		return "", "", err
	}
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotModified {
		return "", etag, ErrNotModified
	}
	if resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("chapter plain: status=%d", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return "", "", err
	}
	return string(b), resp.Header.Get("ETag"), nil
}

// Ack 回执：告诉服务端该章节已写入本地索引。
func (c *Client) Ack(ctx context.Context, bookID string, index int, etag, clientID string) error {
	body, _ := json.Marshal(map[string]string{"client_id": clientID, "etag": etag})
	path := fmt.Sprintf("/api/books/%s/chapters/%d/ack", bookID, index)
	status, _, _, err := c.do(ctx, http.MethodPost, path, "application/json", body)
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return fmt.Errorf("ack: status=%d", status)
	}
	return nil
}

// SearchMetadata 调服务端元数据检索（只查书名/作者/标签/简介/章节标题）。
func (c *Client) SearchMetadata(ctx context.Context, q string) ([]Hit, error) {
	path := "/api/search/metadata?q=" + urlEncode(q)
	status, _, b, err := c.do(ctx, http.MethodGet, path, "", nil)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("metadata search: status=%d", status)
	}
	var wrap struct {
		Hits []struct {
			BookID    string `json:"book_id"`
			Title     string `json:"title"`
			Author    string `json:"author"`
			Highlight string `json:"highlight"`
		} `json:"hits"`
	}
	if err := json.Unmarshal(b, &wrap); err != nil {
		return nil, err
	}
	out := make([]Hit, 0, len(wrap.Hits))
	for _, h := range wrap.Hits {
		out = append(out, Hit{BookID: h.BookID, BookTitle: h.Title, Snippet: h.Highlight})
	}
	return out, nil
}

func urlEncode(s string) string {
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

// SyncResult 汇总一次同步结果。
type SyncResult struct {
	Fetched int `json:"fetched"`
	Skipped int `json:"skipped"`
	Acked   int `json:"acked"`
}

// SyncBook 拉取一本书的章节并写入本地索引；已缓存且 ETag 相同的章节直接跳过。
// 未缓存章节不进入本地全文搜索索引——这是分层模型的必然结果。
func (s *Store) SyncBook(ctx context.Context, c *Client, book BookMeta) (SyncResult, error) {
	var res SyncResult
	chapters, err := c.Chapters(ctx, book.ID)
	if err != nil {
		return res, err
	}
	title := book.Title
	if title != "" {
		if err := s.PutBook(book.ID, title, book.Author); err != nil {
			return res, err
		}
	}
	for _, ch := range chapters {
		etag, ok := s.CachedETag(book.ID, ch.Index)
		if ok && etag == ch.ETag {
			res.Skipped++
			continue
		}
		text, newETag, err := c.ChapterPlain(ctx, book.ID, ch.Index, etag)
		if errors.Is(err, ErrNotModified) {
			res.Skipped++
			continue
		}
		if err != nil {
			return res, err
		}
		if err := s.PutChapter(ChapterRecord{
			BookID: book.ID, Index: ch.Index, Title: ch.Title, Text: text, ETag: newETag,
		}); err != nil {
			return res, err
		}
		res.Fetched++
		// 回执失败不影响本地可用性（服务端也不强制）
		if err := c.Ack(ctx, book.ID, ch.Index, newETag, s.ClientID()); err == nil {
			res.Acked++
		}
	}
	return res, nil
}

// FileKeyProvider 是桌面/演示用的设备密钥来源：Argon2id(口令, 盐文件)。
// 移动端应替换为 Keystore/Keychain 实现——本类型只用于在没有安全硬件时跑通演示。
type FileKeyProvider struct {
	SaltPath   string
	Passphrase []byte
	once       bool
	cached     []byte
}

// Key 实现 KeyProvider：首次调用生成/读取盐文件并派生密钥，之后复用。
func (p *FileKeyProvider) Key(ctx context.Context) ([]byte, error) {
	if p.once {
		return p.cached, nil
	}
	if len(p.Passphrase) < 8 {
		return nil, errors.New("clientcore: 演示口令至少 8 字节")
	}
	salt, err := ensureSalt(p.SaltPath)
	if err != nil {
		return nil, err
	}
	p.cached = deriveKey(p.Passphrase, salt)
	p.once = true
	return p.cached, nil
}
