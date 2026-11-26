package server_test

import (
	"crypto/rand"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"novelstack/internal/cryptobox"
	"novelstack/internal/tts"
)

// requireFullStack 把「需要完整 HTTP 栈」的集成测试与单测分层：
// go test -short ./... 只跑单测与不依赖完整栈的用例。
func requireFullStack(t *testing.T) {
	t.Helper()
	if testing.Short() {
		t.Skip("完整栈集成测试：-short 模式跳过")
	}
}

var (
	sharedOnce    sync.Once
	sharedStackIn *stack
	sharedCleanup []func()
)

// sharedFullStack 用 sync.Once 复用一份只读的全栈实例（SQLite + KEK + HTTP）。
// 只用于不修改数据的集成测试；会上传/撤销/改配置的测试仍用独立栈。
func sharedFullStack(t *testing.T) *stack {
	t.Helper()
	requireFullStack(t)
	sharedOnce.Do(func() {
		dir, err := os.MkdirTemp("", "novelstack-shared-*")
		if err != nil {
			t.Fatal(err)
		}
		sharedCleanup = append(sharedCleanup, func() { _ = os.RemoveAll(dir) })
		kek := make([]byte, cryptobox.KeySize)
		if _, err := rand.Read(kek); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "kek"), kek, 0o600); err != nil {
			t.Fatal(err)
		}
		sharedStackIn = buildStackWithCleanup(t, dir, stackOpts{provider: tts.NewMock(0)},
			func(f func()) { sharedCleanup = append(sharedCleanup, f) })
	})
	return sharedStackIn
}

func TestMain(m *testing.M) {
	code := m.Run()
	for i := len(sharedCleanup) - 1; i >= 0; i-- {
		sharedCleanup[i]()
	}
	os.Exit(code)
}
