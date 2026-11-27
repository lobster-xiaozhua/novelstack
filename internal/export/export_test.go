package export

import (
	"archive/zip"
	"bytes"
	"io"
	"strings"
	"testing"
)

func sampleItem() *Item {
	return &Item{
		ID:     "book-1",
		Title:  "小说/测试:书名",
		Author: "作者 <A&B>",
		Chapters: []Chapter{
			{Title: "第一章 开端", Text: "他推开门。\n“你来了。”"},
			{Title: "第二章 继续", Text: "风很大。"},
		},
	}
}

func TestWriteTXT(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteTXT(&buf, sampleItem()); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{"小说/测试", "作者", "第一章 开端", "他推开门", "第二章 继续", "风很大"} {
		if !strings.Contains(out, want) {
			t.Fatalf("TXT 缺少 %q:\n%s", want, out)
		}
	}
}

func TestBuildEPUBStructure(t *testing.T) {
	data, err := BuildEPUB(sampleItem())
	if err != nil {
		t.Fatal(err)
	}
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	if len(zr.File) == 0 {
		t.Fatal("空 zip")
	}
	// mimetype 必须是第一个条目且不压缩。
	first := zr.File[0]
	if first.Name != "mimetype" {
		t.Fatalf("第一个条目 = %s", first.Name)
	}
	if first.Method != zip.Store {
		t.Fatalf("mimetype 必须 STORED，实际 method=%d", first.Method)
	}
	read := func(name string) string {
		t.Helper()
		for _, f := range zr.File {
			if f.Name != name {
				continue
			}
			rc, err := f.Open()
			if err != nil {
				t.Fatal(err)
			}
			defer rc.Close()
			b, err := io.ReadAll(rc)
			if err != nil {
				t.Fatal(err)
			}
			return string(b)
		}
		t.Fatalf("缺少条目 %s", name)
		return ""
	}
	if got := read("mimetype"); got != "application/epub+zip" {
		t.Fatalf("mimetype 内容 = %q", got)
	}
	if !strings.Contains(read("META-INF/container.xml"), "OEBPS/content.opf") {
		t.Fatal("container.xml 未指向 content.opf")
	}
	opf := read("OEBPS/content.opf")
	if !strings.Contains(opf, "chapter-0001.xhtml") || !strings.Contains(opf, "chapter-0002.xhtml") {
		t.Fatalf("opf manifest 缺章节: %s", opf)
	}
	ch1 := read("OEBPS/text/chapter-0001.xhtml")
	if !strings.Contains(ch1, "第一章 开端") || !strings.Contains(ch1, "他推开门。") {
		t.Fatalf("章节内容不完整: %s", ch1)
	}
	// XML 特殊字符必须转义（作者名里带 <A&B>）。
	if !strings.Contains(opf, "&lt;A&amp;B&gt;") {
		t.Fatalf("作者名未转义: %s", opf)
	}
	if strings.Contains(opf, "<A&B>") {
		t.Fatal("存在未转义的 XML 字符")
	}
	if !strings.Contains(read("OEBPS/toc.ncx"), "navPoint") {
		t.Fatal("toc.ncx 缺 navPoint")
	}
}

func TestSafeFileName(t *testing.T) {
	cases := map[string]string{
		"../../etc/passwd": "etcpasswd",
		"小说:第一部?":          "小说第一部",
		"  .  ":            "untitled",
		"":                 "untitled",
	}
	for in, want := range cases {
		if got := SafeFileName(in); got != want {
			t.Fatalf("SafeFileName(%q) = %q, want %q", in, got, want)
		}
	}
	long := strings.Repeat("字", 200)
	if n := len([]rune(SafeFileName(long))); n > 80 {
		t.Fatalf("文件名未截断: %d", n)
	}
}

func TestEntryNameDedupes(t *testing.T) {
	used := map[string]int{}
	a := EntryName(used, "同名", "txt")
	b := EntryName(used, "同名", "txt")
	if a != "同名.txt" {
		t.Fatalf("首个 = %s", a)
	}
	if b != "同名 (2).txt" {
		t.Fatalf("重名 = %s", b)
	}
}

func TestSplitParagraphs(t *testing.T) {
	out := splitParagraphs("第一段\n\n第二段\r\n第三段\n   \n")
	if len(out) != 3 {
		t.Fatalf("段数 = %d: %v", len(out), out)
	}
}
