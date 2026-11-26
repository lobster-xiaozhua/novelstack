package clientcore

import (
	"crypto/rand"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/crypto/argon2"
)

// 演示用 KDF 参数：16MiB / t=1。真实移动端不需要 KDF——
// 随机数据密钥由 Keystore/Keychain 直接保管，这里只是没有安全硬件时的替代方案。
const (
	kdfMemory  uint32 = 16 * 1024
	kdfTime    uint32 = 1
	kdfThreads uint8  = 1
	kdfKeyLen  uint32 = 32
	saltLen           = 16
)

// ensureSalt 读取盐文件；不存在时生成并写入（0600）。
func ensureSalt(path string) ([]byte, error) {
	b, err := os.ReadFile(path)
	if err == nil && len(b) >= saltLen {
		return b[:saltLen], nil
	}
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	if err := os.WriteFile(path, salt, 0o600); err != nil {
		return nil, fmt.Errorf("写入盐文件失败: %w", err)
	}
	return salt, nil
}

func deriveKey(passphrase, salt []byte) []byte {
	return argon2.IDKey(passphrase, salt, kdfTime, kdfMemory, kdfThreads, kdfKeyLen)
}
