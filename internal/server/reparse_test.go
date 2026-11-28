package server_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/transform"
)

// TestReparseGBKStuckHeadings：端到端验证三件事：
//  1. GBK 编码上传后，章节正则仍能正确识别（编码归一化）；
//  2. 标题与正文粘连的排版，正文不会被塞进"前言"、不产生碎片章（切分修复）；
//  3. Reparse 能在密文不变的前提下重建目录（覆盖更新）。
func TestReparseGBKStuckHeadings(t *testing.T) {
	requireFullStack(t)
	s := newStack(t)
	client := loginOK(t, s)

	// 构造 GBK 文本：无书前目录，每章一整行且标题粘连正文。
	var plain strings.Builder
	for i := 1; i <= 3; i++ {
		plain.WriteString("第" + string(rune('0'+i)) + "章 标题" + string(rune('A'+i)))
		plain.WriteString(strings.Repeat("这是正文内容。", 30)) // 每章约 210 字
		plain.WriteString("\n")
	}
	gbk, _, err := transform.Bytes(simplifiedchinese.GB18030.NewEncoder(), []byte(plain.String()))
	if err != nil {
		t.Fatal(err)
	}

	resp := uploadBook(t, client, s.ts.URL, "GBK粘连标题测试", gbk, "gbk.txt")
	body := bodyString(t, resp)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("upload status=%d body=%s", resp.StatusCode, body)
	}
	var book struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(body), &book); err != nil || book.ID == "" {
		t.Fatalf("upload response: %s (%v)", body, err)
	}

	// 目录应切出 3 章，每章 >100 字，标题为干净的"第N章 标题X"。
	chapters := fetchChapters(t, client, s, book.ID)
	if len(chapters) != 3 {
		t.Fatalf("chapters=%d want 3: %+v", len(chapters), chapters)
	}
	for i, c := range chapters {
		if c.Chars < 100 {
			t.Fatalf("第%d章是碎片: %+v", i, c)
		}
		if !strings.HasPrefix(c.Title, "第"+string(rune('0'+(i+1)))+"章") {
			t.Fatalf("第%d章标题不含章号: %q", i, c.Title)
		}
	}

	// 解密后的正文应是 UTF-8（编码归一化生效）。
	preq, _ := http.NewRequest(http.MethodGet, s.ts.URL+"/api/books/"+book.ID+"/chapters/0/plain", nil)
	presp, err := client.Do(preq)
	if err != nil {
		t.Fatal(err)
	}
	pbody, _ := io.ReadAll(presp.Body)
	presp.Body.Close()
	if !rpUTF8Valid(pbody) {
		t.Fatalf("解密正文不是合法 UTF-8")
	}
	if !bytes.Contains(pbody, []byte("这是正文内容")) {
		t.Fatalf("解密正文丢失中文: %s", string(pbody[:rpMin(len(pbody), 80)]))
	}

	// 手动破坏目录（删成 1 章），再 Reparse 恢复。
	if _, err := s.db.Exec(`DELETE FROM book_chapters WHERE book_id = ?`, book.ID); err != nil {
		t.Fatal(err)
	}
	rreq, _ := http.NewRequest(http.MethodPost, s.ts.URL+"/api/books/"+book.ID+"/reparse", nil)
	rresp, err := client.Do(rreq)
	if err != nil {
		t.Fatal(err)
	}
	rbody := bodyString(t, rresp)
	if rresp.StatusCode != http.StatusOK {
		t.Fatalf("reparse status=%d body=%s", rresp.StatusCode, rbody)
	}
	restored := fetchChapters(t, client, s, book.ID)
	if len(restored) != 3 {
		t.Fatalf("reparse 后 chapters=%d want 3: %+v", len(restored), restored)
	}
}

type chapRow struct {
	Title string `json:"title"`
	Chars int    `json:"chars"`
}

func fetchChapters(t *testing.T, client *http.Client, s *stack, bookID string) []chapRow {
	t.Helper()
	resp, err := client.Get(s.ts.URL + "/api/books/" + bookID + "/chapters")
	if err != nil {
		t.Fatal(err)
	}
	body := bodyString(t, resp)
	var d struct {
		Chapters []chapRow `json:"chapters"`
	}
	if err := json.Unmarshal([]byte(body), &d); err != nil {
		t.Fatalf("chapters json: %s (%v)", body, err)
	}
	return d.Chapters
}

func rpUTF8Valid(b []byte) bool {
	for i := 0; i < len(b); {
		if b[i] < 0x80 {
			i++
			continue
		}
		n := 0
		switch {
		case b[i]&0xE0 == 0xC0:
			n = 2
		case b[i]&0xF0 == 0xE0:
			n = 3
		case b[i]&0xF8 == 0xF0:
			n = 4
		default:
			return false
		}
		if i+n > len(b) {
			return false
		}
		for j := 1; j < n; j++ {
			if b[i+j]&0xC0 != 0x80 {
				return false
			}
		}
		i += n
	}
	return true
}

func rpMin(a, b int) int {
	if a < b {
		return a
	}
	return b
}
