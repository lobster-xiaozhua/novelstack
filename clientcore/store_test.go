package clientcore

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newTestStore(t *testing.T, dir string) (*Store, *FileKeyProvider) {
	t.Helper()
	kp := &FileKeyProvider{
		SaltPath:   filepath.Join(dir, "device.salt"),
		Passphrase: []byte("unit-test-passphrase"),
	}
	s, err := Open(filepath.Join(dir, "index.enc"), kp, "unit")
	if err != nil {
		t.Fatal(err)
	}
	return s, kp
}

// 中文任意子串可命中（逐字分词 + 短语查询），并返回带 <mark> 的片段。
func TestSearchChineseSubstring(t *testing.T) {
	dir := t.TempDir()
	s, _ := newTestStore(t, dir)
	defer s.Close()

	if err := s.PutBook("b1", "夜航船", "阿来"); err != nil {
		t.Fatal(err)
	}
	if err := s.PutChapter(ChapterRecord{
		BookID: "b1", Index: 0, Title: "第一章 起",
		Text: "他推开门，屋里很暗。窗外的雨还在下。",
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutChapter(ChapterRecord{
		BookID: "b1", Index: 1, Title: "第二章 归途",
		Text: "天亮的时候，雨停了。他在船头看见一座琥珀色灯塔。",
	}); err != nil {
		t.Fatal(err)
	}

	for _, q := range []string{"推开门", "开门", "雨", "琥珀色灯塔", "灯塔"} {
		hits, err := s.Search(q, 10)
		if err != nil {
			t.Fatalf("%s: %v", q, err)
		}
		if len(hits) == 0 {
			t.Fatalf("%q 应命中", q)
		}
		if !strings.Contains(hits[0].Snippet, "<mark>"+q+"</mark>") {
			t.Fatalf("%q 高亮缺失: %q", q, hits[0].Snippet)
		}
		if hits[0].BookTitle != "夜航船" {
			t.Fatalf("书名缺失: %+v", hits[0])
		}
	}

	// 不存在的词
	if hits, _ := s.Search("量子纠缠", 10); len(hits) != 0 {
		t.Fatalf("不应命中: %+v", hits)
	}
	// 更新章节后旧词失效（索引同步更新）
	if err := s.PutChapter(ChapterRecord{
		BookID: "b1", Index: 1, Title: "第二章 归途",
		Text: "天亮的时候，雨停了。他看见一座灰色灯塔。",
	}); err != nil {
		t.Fatal(err)
	}
	if hits, _ := s.Search("琥珀色", 10); len(hits) != 0 {
		t.Fatalf("旧内容不应再命中: %+v", hits)
	}
}

// 落盘只有密文快照；重开后仍可检索。
func TestSnapshotEncryptedAndReload(t *testing.T) {
	dir := t.TempDir()
	s, kp := newTestStore(t, dir)
	if err := s.PutChapter(ChapterRecord{
		BookID: "b1", Index: 0, Title: "第一章", Text: "量子纠缠信标藏在铜钥匙上。",
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(dir, "index.enc")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(raw, []byte(snapshotMagic)) {
		t.Fatalf("magic 不对: %q", raw[:6])
	}
	for _, marker := range []string{"量子纠缠信标", "铜钥匙"} {
		if bytes.Contains(raw, []byte(marker)) {
			t.Fatalf("快照出现明文: %s", marker)
		}
	}

	s2, err := Open(path, kp, "unit")
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	hits, err := s2.Search("量子纠缠信标", 5)
	if err != nil || len(hits) != 1 {
		t.Fatalf("重开检索失败: %v %+v", err, hits)
	}
	if text, ok := s2.Chapter("b1", 0); !ok || !strings.Contains(text, "铜钥匙") {
		t.Fatalf("本地章节读取异常: %v", ok)
	}
}

// 篡改快照或使用错误密钥必须失败。
func TestSnapshotTamperAndWrongKey(t *testing.T) {
	dir := t.TempDir()
	s, _ := newTestStore(t, dir)
	if err := s.PutChapter(ChapterRecord{BookID: "b", Index: 0, Text: "正文"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "index.enc")

	raw, _ := os.ReadFile(path)
	raw[len(raw)-1] ^= 0x01
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(path, &FileKeyProvider{SaltPath: filepath.Join(dir, "device.salt"),
		Passphrase: []byte("unit-test-passphrase")}, "unit"); err == nil {
		t.Fatal("篡改的快照必须解密失败")
	}

	// 用不同口令（不同派生密钥）也失败
	if _, err := Open(path, &FileKeyProvider{SaltPath: filepath.Join(dir, "device.salt"),
		Passphrase: []byte("another-passphrase")}, "unit"); err == nil {
		t.Fatal("错误密钥必须解密失败")
	}
}

// 逐字分词与短语构造的基本约束。
func TestSegmentAndPhrase(t *testing.T) {
	if got := segmentText("他推开门"); got != "他 推 开 门 " {
		t.Fatalf("segmentText=%q", got)
	}
	if got := ftsPhrase(`他"推开`); got != `"他 推 开"` {
		t.Fatalf("ftsPhrase=%q", got)
	}
	if got := ftsPhrase("   "); got != "" {
		t.Fatalf("空查询应返回空串: %q", got)
	}
}
