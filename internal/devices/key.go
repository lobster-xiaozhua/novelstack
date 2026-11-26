package devices

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
)

// 设备密钥约定：
//   - 客户端在 Android Keystore 内生成 RSA-2048 密钥对，私钥不可导出；
//   - 注册设备时只上传 SPKI(PEM) 公钥；
//   - 服务端用 RSA-OAEP(SHA-256) 包装每本书的 32 字节 DEK，客户端在设备内解包。
//
// 服务端因此从不接触设备私钥，也不向客户端输出明文 DEK。
// 注意：Go 的 EncryptOAEP 用同一个 hash 作为 OAEP 与 MGF1 摘要；
// Android 侧必须显式指定 MGF1ParameterSpec.SHA256（默认实现常用 SHA-1）。

const (
	minRSAKeyBits = 2048
	maxRSAKeyBits = 4096
)

// ParseRSAPublicKey 解析 PEM 编码的 RSA 公钥（PKIX 或 PKCS#1）。
// 只接受 2048–4096 位：过短不安全，过长会让移动端解包变慢。
func ParseRSAPublicKey(pemStr string) (*rsa.PublicKey, error) {
	block, _ := pem.Decode([]byte(pemStr))
	if block == nil {
		return nil, fmt.Errorf("%w: pem decode failed", ErrBadPubKey)
	}
	var pub *rsa.PublicKey
	if k, err := x509.ParsePKIXPublicKey(block.Bytes); err == nil {
		p, ok := k.(*rsa.PublicKey)
		if !ok {
			return nil, fmt.Errorf("%w: not an RSA key", ErrBadPubKey)
		}
		pub = p
	} else if k, err2 := x509.ParsePKCS1PublicKey(block.Bytes); err2 == nil {
		pub = k
	} else {
		return nil, fmt.Errorf("%w: parse failed", ErrBadPubKey)
	}
	if pub.N.BitLen() < minRSAKeyBits || pub.N.BitLen() > maxRSAKeyBits {
		return nil, fmt.Errorf("%w: key size %d", ErrBadPubKey, pub.N.BitLen())
	}
	return pub, nil
}

// WrapDEK 用设备公钥包装 32 字节 DEK，返回 base64(RSA-OAEP 密文)。
// dek 长度不是 32 时直接报错：这里只包装数据密钥，不包装任意内容。
func WrapDEK(pub *rsa.PublicKey, dek []byte) (string, error) {
	if pub == nil {
		return "", ErrBadPubKey
	}
	if len(dek) != 32 {
		return "", errors.New("devices: DEK must be 32 bytes")
	}
	ct, err := rsa.EncryptOAEP(sha256.New(), rand.Reader, pub, dek, nil)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(ct), nil
}

// UnwrapDEK 是 WrapDEK 的逆操作，仅用于测试与命令行诊断；
// 服务端运行路径永远不会调用它（它没有设备私钥）。
func UnwrapDEK(priv *rsa.PrivateKey, wrappedB64 string) ([]byte, error) {
	ct, err := base64.StdEncoding.DecodeString(wrappedB64)
	if err != nil {
		return nil, err
	}
	return rsa.DecryptOAEP(sha256.New(), nil, priv, ct, nil)
}

// EncodePublicKeyPEM 把公钥编码为 SPKI PEM（测试与文档示例用）。
func EncodePublicKeyPEM(pub *rsa.PublicKey) (string, error) {
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		return "", err
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})), nil
}
