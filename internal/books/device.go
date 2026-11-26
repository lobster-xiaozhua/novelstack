package books

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"

	"novelstack/internal/chapterpkg"
	"novelstack/internal/chapterscan"
)

// WrapDEKFor 用调用方提供的包装函数处理某本书的 DEK。
// DEK 不会返回给调用方，只在栈上短暂存在；用于把书籍密钥包装给已注册设备。
func (svc *Service) WrapDEKFor(ctx context.Context, bookID, ownerID string, wrap func([]byte) (string, error)) (string, error) {
	if _, err := svc.Store.GetOwned(ctx, bookID, ownerID, false); err != nil {
		return "", err
	}
	dek, err := svc.unwrapDEK(ctx, bookID)
	if err != nil {
		return "", err
	}
	defer zeroBytes(dek)
	return wrap(dek)
}

// SealChapters 解密一次整本，切出指定章节并用同一 DEK 重新封装成 NSCH 包。
//
// indexes 必须非空且不超过 max；重复索引会被去重（保留首次出现顺序的升序结果）。
// 返回的包与书籍分块格式无关，客户端可单章缓存、单章校验。
func (svc *Service) SealChapters(ctx context.Context, bookID, ownerID string, indexes []int, max int) ([]chapterpkg.Sealed, error) {
	if len(indexes) == 0 {
		return nil, fmt.Errorf("%w: no chapters requested", ErrBadRequest)
	}
	if max > 0 && len(indexes) > max {
		return nil, fmt.Errorf("%w: too many chapters", ErrBadRequest)
	}
	meta, plain, err := svc.LoadPlaintextFor(ctx, bookID, ownerID)
	if err != nil {
		return nil, err
	}
	defer zeroBytes(plain)

	chapters := chapterscan.Split(string(plain))
	uniq := make(map[int]struct{}, len(indexes))
	for _, idx := range indexes {
		if idx < 0 || idx >= len(chapters) {
			return nil, fmt.Errorf("%w: chapter %d out of range", ErrBadRequest, idx)
		}
		uniq[idx] = struct{}{}
	}
	ordered := make([]int, 0, len(uniq))
	for idx := range uniq {
		ordered = append(ordered, idx)
	}
	sort.Ints(ordered)

	dek, err := svc.unwrapDEK(ctx, bookID)
	if err != nil {
		return nil, err
	}
	defer zeroBytes(dek)

	runes := []rune(string(plain))
	out := make([]chapterpkg.Sealed, 0, len(ordered))
	for _, idx := range ordered {
		ch := chapters[idx]
		pkg, serr := chapterpkg.Seal(dek, bookID, idx, []byte(string(runes[ch.Start:ch.End])))
		if serr != nil {
			return nil, serr
		}
		out = append(out, chapterpkg.Sealed{
			Index: idx, Title: ch.Title, Package: pkg,
			ETag: ChapterETag(meta, ch),
		})
	}
	return out, nil
}

// zeroBytes 尽力清零敏感内存（Go 不保证编译器不优化，但保持习惯）。
func zeroBytes(b []byte) {
	for i := range b {
		b[i] = 0
	}
}

// ErrBadRequest 表示请求参数不合法（handler 映射为 400）。
var ErrBadRequest = errors.New("books: bad request")

// 保证 ErrBadRequest 与既有错误映射兼容：handler 直接判断 errors.Is。
var _ = http.StatusBadRequest
