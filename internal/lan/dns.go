package lan

import (
	"errors"
	"fmt"
	"strings"
)

// 本文件是 mDNS 应答所需的最小 DNS 报文编解码实现。
// 只处理 A / PTR / SRV / TXT 四类记录，不引入第三方依赖。
// 协议参考 RFC 1035 与 RFC 6762/6763（DNS-SD）。

const (
	dnsTypeA   uint16 = 1
	dnsTypePTR uint16 = 12
	dnsTypeTXT uint16 = 16
	dnsTypeSRV uint16 = 33
	dnsClassIN uint16 = 1
	// cacheFlush 是 mDNS 的「唯一记录」标志：SRV/TXT/A 属于单播唯一记录，
	// 收到后应刷新缓存中同名同类的旧记录。
	dnsCacheFlush uint16 = 0x8000
)

// dnsServiceType 是服务类型标签，NsdManager 侧按 "_novelstack._tcp." 注册。
const dnsServiceType = "_novelstack._tcp.local."

// dnsQuestion 是一条 DNS 查询。
type dnsQuestion struct {
	name  string
	qtype uint16
}

// parseDNSQuestions 解析报文里的全部 Question。
// 不做名称压缩解析——mDNS 查询里的 QNAME 几乎总是未压缩的完整名。
func parseDNSQuestions(pkt []byte) ([]dnsQuestion, error) {
	if len(pkt) < 12 {
		return nil, errors.New("dns: short header")
	}
	qd := int(pkt[4])<<8 | int(pkt[5])
	off := 12
	out := make([]dnsQuestion, 0, qd)
	for i := 0; i < qd; i++ {
		name, next, err := readDNSName(pkt, off)
		if err != nil {
			return nil, err
		}
		off = next
		if off+4 > len(pkt) {
			return nil, errors.New("dns: truncated question")
		}
		qtype := uint16(pkt[off])<<8 | uint16(pkt[off+1])
		out = append(out, dnsQuestion{name: name, qtype: qtype})
		off += 4
	}
	return out, nil
}

// readDNSName 读取一串 label，返回小写、以点结尾的名字与下一个字节偏移。
func readDNSName(pkt []byte, off int) (string, int, error) {
	var parts []string
	seen := 0
	for {
		if off >= len(pkt) {
			return "", 0, errors.New("dns: name out of bounds")
		}
		l := int(pkt[off])
		if l == 0 {
			off++
			break
		}
		// 压缩指针（0xC0）：查询里少见，这里保守跳过，避免误答。
		if l&0xC0 == 0xC0 {
			return "", 0, errors.New("dns: compressed name not supported")
		}
		off++
		if off+l > len(pkt) {
			return "", 0, errors.New("dns: label out of bounds")
		}
		parts = append(parts, string(pkt[off:off+l]))
		off += l
		seen++
		if seen > 32 {
			return "", 0, errors.New("dns: too many labels")
		}
	}
	return strings.ToLower(strings.Join(parts, ".") + "."), off, nil
}

// writeDNSName 写出未压缩的名字。
func writeDNSName(buf *[]byte, name string) {
	name = strings.TrimSuffix(strings.ToLower(name), ".")
	if name == "" {
		*buf = append(*buf, 0)
		return
	}
	for _, label := range strings.Split(name, ".") {
		if label == "" {
			continue
		}
		*buf = append(*buf, byte(len(label)))
		*buf = append(*buf, []byte(label)...)
	}
	*buf = append(*buf, 0)
}

// writeDNSRR 写一条资源记录。ttl 单位为秒，unique 表示设置 cache-flush 位。
func writeDNSRR(buf *[]byte, name string, typ uint16, ttl uint32, unique bool, rdata []byte) {
	writeDNSName(buf, name)
	*buf = append(*buf,
		byte(typ>>8), byte(typ),
		0, byte(dnsClassIN),
	)
	if unique {
		*buf = append(*buf, 0x80, 0)
	} else {
		*buf = append(*buf, 0, 1)
	}
	*buf = append(*buf,
		byte(ttl>>24), byte(ttl>>16), byte(ttl>>8), byte(ttl),
		byte(len(rdata)>>8), byte(len(rdata)),
	)
	*buf = append(*buf, rdata...)
}

// buildMDNSResponse 针对查询构造 mDNS 应答。
// instanceName 形如 "NovelStack-1234"，host 为可解析的主机名/IP，addr 为 IPv4。
func buildMDNSResponse(id uint16, instanceName, host string, addr []byte, port uint16, txt []string) ([]byte, error) {
	if len(addr) != 4 {
		return nil, fmt.Errorf("dns: invalid ipv4 addr")
	}
	inst := instanceName + "." + dnsServiceType
	hostFQDN := strings.TrimSuffix(host, ".") + ".local."

	buf := make([]byte, 0, 512)
	// Header: response + authoritative answer。
	buf = append(buf,
		byte(id>>8), byte(id),
		0x84, 0x00, // QR=1, AA=1
		0, 0, // QDCOUNT
		0, 4, // ANCOUNT: PTR + SRV + TXT + A
		0, 0, // NSCOUNT
		0, 0, // ARCOUNT
	)

	// PTR: _novelstack._tcp.local. -> instance._novelstack._tcp.local.
	ptr := make([]byte, 0, 64)
	writeDNSName(&ptr, inst)
	writeDNSRR(&buf, dnsServiceType, dnsTypePTR, 4500, false, ptr)

	// SRV: 优先级 0、权重 0、port、target host。
	srv := []byte{0, 0, 0, 0, byte(port >> 8), byte(port)}
	writeDNSName(&srv, hostFQDN)
	writeDNSRR(&buf, inst, dnsTypeSRV, 120, true, srv)

	// TXT: 每条 "k=v"，长度前缀单字节。
	txtData := make([]byte, 0, 256)
	for _, kv := range txt {
		if len(kv) > 255 {
			kv = kv[:255]
		}
		txtData = append(txtData, byte(len(kv)))
		txtData = append(txtData, []byte(kv)...)
	}
	if len(txtData) == 0 {
		txtData = append(txtData, 0)
	}
	writeDNSRR(&buf, inst, dnsTypeTXT, 4500, true, txtData)

	// A: host.local. -> IPv4。
	writeDNSRR(&buf, hostFQDN, dnsTypeA, 120, true, addr)

	if len(buf) > 9000 {
		return nil, errors.New("dns: response too large")
	}
	return buf, nil
}
