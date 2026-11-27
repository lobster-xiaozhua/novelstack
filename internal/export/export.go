// Package export 把解密后的书籍打包成标准 TXT / EPUB 的 ZIP，
// 作为「不再使用本系统」时的数据退路。
//
// 设计要点：
//   - 只在内存中处理「一本书」，写完立刻释放，不在磁盘落任何中间明文；
//   - 输出为流式 ZIP，浏览器开始下载时不等待整库处理完；
//   - 文件名与内容都做转义/清洗，杜绝路径穿越与 XML 注入；
//   - 导出内容同样经过净化规则（与在线阅读看到的一致）。
package export

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"html"
	"io"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"novelstack/internal/books"
	"novelstack/internal/chapterscan"
)

// Format 是导出格式。
type Format string

const (
	FormatTXT  Format = "txt"
	FormatEPUB Format = "epub"
)

// Item 是一本待导出的书。
type Item struct {
	ID     string
	Title  string
	Author string
	// Chapters 是章节正文（已净化，不含标题行）。
	Chapters []Chapter
}

// Chapter 是导出用的一章。
type Chapter struct {
	Title string
	Text  string
}

// Builder 从 books.Service 读取明文并组装 Item。
type Builder struct {
	Books *books.Service
}

// Build 解密并按章节切分一本书。返回的正文已应用净化规则。
func (b *Builder) Build(ctx context.Context, ownerID, bookID string) (*Item, error) {
	meta, plain, err := b.Books.LoadPlaintextFor(ctx, bookID, ownerID)
	if err != nil {
		return nil, err
	}
	item := &Item{ID: meta.ID, Title: meta.Title, Author: meta.Author}
	if item.Title == "" {
		item.Title = meta.OrigName
	}
	raw := string(plain)
	chapters := chapterscan.Split(raw)
	if len(chapters) == 0 {
		item.Chapters = []Chapter{{Title: "正文", Text: b.Books.PurgeText(raw)}}
		return item, nil
	}
	runes := []rune(raw)
	item.Chapters = make([]Chapter, 0, len(chapters))
	for _, c := range chapters {
		if c.Start < 0 || c.End > len(runes) || c.Start >= c.End {
			continue
		}
		item.Chapters = append(item.Chapters, Chapter{
			Title: c.Title,
			Text:  b.Books.PurgeText(string(runes[c.Start:c.End])),
		})
	}
	return item, nil
}

// ---- TXT ----

// WriteTXT 把一本书写成纯文本：书名、作者、章节标题与正文。
func WriteTXT(w io.Writer, it *Item) error {
	var sb strings.Builder
	sb.Grow(64 << 10)
	sb.WriteString(it.Title)
	sb.WriteString("\n")
	if it.Author != "" {
		sb.WriteString("作者：")
		sb.WriteString(it.Author)
		sb.WriteString("\n")
	}
	sb.WriteString("\n")
	for _, ch := range it.Chapters {
		sb.WriteString(strings.TrimSpace(ch.Title))
		sb.WriteString("\n\n")
		sb.WriteString(strings.TrimSpace(ch.Text))
		sb.WriteString("\n\n")
		if sb.Len() > 1<<20 {
			if _, err := io.WriteString(w, sb.String()); err != nil {
				return err
			}
			sb.Reset()
		}
	}
	if sb.Len() > 0 {
		_, err := io.WriteString(w, sb.String())
		return err
	}
	return nil
}

// ---- EPUB ----

// BuildEPUB 生成一个最小但合规的 EPUB 3 文件（同时带 NCX 兼容老阅读器）。
//
// EPUB 自身就是一个 ZIP，因此必须先完整构建再写入外层 ZIP 的条目。
// 单本上限由上传大小决定（默认 20MB 明文），内存占用可控。
func BuildEPUB(it *Item) ([]byte, error) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	// mimetype 必须是第一个条目且不压缩，否则部分阅读器拒绝识别。
	hdr := &zip.FileHeader{Name: "mimetype", Method: zip.Store}
	hdr.Modified = time.Now()
	mw, err := zw.CreateHeader(hdr)
	if err != nil {
		return nil, err
	}
	if _, err := io.WriteString(mw, "application/epub+zip"); err != nil {
		return nil, err
	}

	files := []struct {
		name string
		body string
	}{
		{"META-INF/container.xml", containerXML},
		{"OEBPS/style.css", styleCSS},
		{"OEBPS/nav.xhtml", navXHTML(it)},
		{"OEBPS/toc.ncx", tocNCX(it)},
		{"OEBPS/content.opf", contentOPF(it)},
	}
	for _, f := range files {
		w, err := zw.Create(f.name)
		if err != nil {
			return nil, err
		}
		if _, err := io.WriteString(w, f.body); err != nil {
			return nil, err
		}
	}
	for i, ch := range it.Chapters {
		w, err := zw.Create(fmt.Sprintf("OEBPS/text/chapter-%04d.xhtml", i+1))
		if err != nil {
			return nil, err
		}
		if _, err := io.WriteString(w, chapterXHTML(it, ch, i+1)); err != nil {
			return nil, err
		}
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

const containerXML = `<?xml version="1.0" encoding="UTF-8"?>
<container version="1.0" xmlns="urn:oasis:names:tc:opendocument:xmlns:container">
  <rootfiles>
    <rootfile full-path="OEBPS/content.opf" media-type="application/oebps-package+xml"/>
  </rootfiles>
</container>
`

const styleCSS = `body { font-family: serif; line-height: 1.7; margin: 1em; }
h1 { font-size: 1.3em; margin: 1.2em 0 1em; }
p { text-indent: 2em; margin: 0.6em 0; }
`

// bookUID 由书籍 ID 派生，保证同一本书每次导出 uuid 稳定。
func bookUID(it *Item) string {
	sum := sha1.Sum([]byte("novelstack-export|" + it.ID))
	h := hex.EncodeToString(sum[:])
	return fmt.Sprintf("%s-%s-%s-%s-%s", h[0:8], h[8:12], h[12:16], h[16:20], h[20:32])
}

func chapterFile(i int) string { return fmt.Sprintf("text/chapter-%04d.xhtml", i+1) }

func navXHTML(it *Item) string {
	var sb strings.Builder
	sb.WriteString(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE html>
<html xmlns="http://www.w3.org/1999/xhtml" xmlns:epub="http://www.idpf.org/2007/ops" lang="zh">
<head><meta charset="utf-8"/><title>`)
	sb.WriteString(html.EscapeString(it.Title))
	sb.WriteString(`</title></head><body>
<nav epub:type="toc" id="toc"><h1>目录</h1><ol>
`)
	for i, ch := range it.Chapters {
		sb.WriteString(`<li><a href="`)
		sb.WriteString(chapterFile(i))
		sb.WriteString(`">`)
		sb.WriteString(html.EscapeString(strings.TrimSpace(ch.Title)))
		sb.WriteString("</a></li>\n")
	}
	sb.WriteString("</ol></nav></body></html>\n")
	return sb.String()
}

func tocNCX(it *Item) string {
	var sb strings.Builder
	sb.WriteString(`<?xml version="1.0" encoding="UTF-8"?>
<ncx xmlns="http://www.daisy.org/z3986/2005/ncx/" version="2005-1">
<head><meta name="dtb:uid" content="urn:uuid:`)
	sb.WriteString(bookUID(it))
	sb.WriteString(`"/></head>
<docTitle><text>`)
	sb.WriteString(html.EscapeString(it.Title))
	sb.WriteString(`</text></docTitle>
<navMap>
`)
	for i, ch := range it.Chapters {
		order := i + 1
		fmt.Fprintf(&sb, `<navPoint id="nav-%d" playOrder="%d"><navLabel><text>%s</text></navLabel><content src="%s"/></navPoint>`+"\n",
			order, order, html.EscapeString(strings.TrimSpace(ch.Title)), chapterFile(i))
	}
	sb.WriteString("</navMap></ncx>\n")
	return sb.String()
}

func contentOPF(it *Item) string {
	var sb strings.Builder
	sb.WriteString(`<?xml version="1.0" encoding="UTF-8"?>
<package xmlns="http://www.idpf.org/2007/opf" version="3.0" unique-identifier="bookid" xml:lang="zh">
<metadata xmlns:dc="http://purl.org/dc/elements/1.1/">
<dc:identifier id="bookid">urn:uuid:`)
	sb.WriteString(bookUID(it))
	sb.WriteString(`</dc:identifier>
<dc:title>`)
	sb.WriteString(html.EscapeString(it.Title))
	sb.WriteString(`</dc:title>
<dc:language>zh-CN</dc:language>
`)
	if it.Author != "" {
		sb.WriteString("<dc:creator>")
		sb.WriteString(html.EscapeString(it.Author))
		sb.WriteString("</dc:creator>\n")
	}
	sb.WriteString(`<meta property="dcterms:modified">`)
	sb.WriteString(time.Now().UTC().Format("2006-01-02T15:04:05Z"))
	sb.WriteString(`</meta>
</metadata>
<manifest>
<item id="nav" href="nav.xhtml" media-type="application/xhtml+xml" properties="nav"/>
<item id="ncx" href="toc.ncx" media-type="application/x-dtbncx+xml"/>
<item id="css" href="style.css" media-type="text/css"/>
`)
	for i := range it.Chapters {
		fmt.Fprintf(&sb, `<item id="ch%d" href="%s" media-type="application/xhtml+xml"/>`+"\n", i+1, chapterFile(i))
	}
	sb.WriteString(`</manifest>
<spine toc="ncx">
`)
	for i := range it.Chapters {
		fmt.Fprintf(&sb, `<itemref idref="ch%d"/>`+"\n", i+1)
	}
	sb.WriteString("</spine></package>\n")
	return sb.String()
}

func chapterXHTML(it *Item, ch Chapter, order int) string {
	var sb strings.Builder
	sb.WriteString(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE html>
<html xmlns="http://www.w3.org/1999/xhtml" lang="zh">
<head><meta charset="utf-8"/><title>`)
	sb.WriteString(html.EscapeString(strings.TrimSpace(ch.Title)))
	sb.WriteString(`</title><link rel="stylesheet" type="text/css" href="../style.css"/></head>
<body>
`)
	fmt.Fprintf(&sb, `<h1 id="ch%d">%s</h1>`+"\n", order, html.EscapeString(strings.TrimSpace(ch.Title)))
	for _, p := range splitParagraphs(ch.Text) {
		sb.WriteString("<p>")
		sb.WriteString(html.EscapeString(p))
		sb.WriteString("</p>\n")
	}
	sb.WriteString("</body></html>\n")
	return sb.String()
}

// splitParagraphs 按空行/换行切段；过长的段不再拆分（保留原始断行语义）。
func splitParagraphs(text string) []string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	raw := strings.Split(text, "\n")
	out := make([]string, 0, len(raw))
	for _, line := range raw {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		out = append(out, line)
	}
	return out
}

// SafeFileName 把书名转成安全的文件名：去掉路径分隔符、控制字符与 Windows 保留字符，
// 并限制长度（按 UTF-8 字符数），避免 zip slip 与超长路径。
func SafeFileName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		name = "untitled"
	}
	var sb strings.Builder
	count := 0
	for _, r := range name {
		if count >= 80 {
			break
		}
		if r == utf8.RuneError || unicode.IsControl(r) {
			continue
		}
		switch r {
		case '/', '\\', ':', '*', '?', '"', '<', '>', '|', 0:
			continue
		}
		sb.WriteRune(r)
		count++
	}
	out := strings.Trim(sb.String(), " .")
	if out == "" {
		out = "untitled"
	}
	return out
}

// EntryName 生成 ZIP 内唯一的条目名（重名时追加 -2、-3）。
func EntryName(used map[string]int, base, ext string) string {
	name := SafeFileName(base)
	n := used[name]
	used[name] = n + 1
	if n > 0 {
		return fmt.Sprintf("%s (%d).%s", name, n+1, ext)
	}
	return name + "." + ext
}
