package lan

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Discovery 在局域网内广播本机服务信息，供 App 的 NsdManager / UDP 发现。
//
// 双通道：
//  1. mDNS 组播 224.0.0.251:5353（标准 NsdManager 能发现 _novelstack._tcp）；
//  2. UDP 广播端口（默认 5359），自研协议 {"type":"probe"} -> {"type":"answer",...}，
//     作为国内 ROM 屏蔽组播时的兜底。
//
// 只广播「地址 + 端口 + 服务名」，不含任何书籍数据或凭证。
type Discovery struct {
	instanceName  string
	host          string // 广播出去的主机名（mDNS SRV target）
	addr          net.IP // 广播出去的 IPv4
	port          uint16 // 广播出去的 HTTP 端口
	broadcastPort int
	txt           []string
	endpoint      string // HTTP 基础地址，如 http://192.168.1.100:8080

	mu       sync.Mutex
	closed   bool
	stop     chan struct{}
	wg       sync.WaitGroup
	udpConn  *net.UDPConn
	mdnsConn *net.UDPConn
}

// DiscoveryOptions 构造参数。
type DiscoveryOptions struct {
	InstanceName string
	// Host 为空时自动探测本机第一个非回环 IPv4，并以其点分十进制作为主机名。
	Host string
	// Port 是对外 HTTP 端口；<=0 时从 Addr 解析。
	Port          int
	BroadcastPort int
	// Path 是服务端挂载路径（通常 "/"），用于拼接 endpoint。
	Path string
}

// NewDiscovery 根据选项构造发现服务。host/port 为空时自动探测。
func NewDiscovery(ctx context.Context, o DiscoveryOptions) (*Discovery, error) {
	addr, err := pickIPv4(o.Host)
	if err != nil {
		return nil, err
	}
	port := o.Port
	if port <= 0 {
		port = 8080
	}
	host := strings.TrimSuffix(o.Host, ".")
	if host == "" {
		host = strings.ReplaceAll(addr.String(), ".", "-") // mDNS 主机名用连字符，避免被当成 IP 反解
	}
	if strings.Contains(host, ":") {
		host = strings.ReplaceAll(host, ":", "-")
	}
	path := strings.TrimSpace(o.Path)
	if path == "" {
		path = "/"
	}
	bp := o.BroadcastPort
	if bp <= 0 {
		bp = 5359
	}
	inst := strings.TrimSpace(o.InstanceName)
	if inst == "" {
		inst = "NovelStack-" + strconv.Itoa(port)
	}
	// 主机名标签长度上限 63；IP 替换后一般远小于此，仍做防御性截断。
	if len(host) > 63 {
		host = host[:63]
	}
	endpoint := "http://" + addr.String() + ":" + strconv.Itoa(port) + strings.TrimSuffix(path, "/")
	d := &Discovery{
		instanceName:  inst,
		host:          host,
		addr:          addr,
		port:          uint16(port),
		broadcastPort: bp,
		endpoint:      endpoint,
		txt: []string{
			"path=" + path,
			"proto=http",
			"v=1",
		},
		stop: make(chan struct{}),
	}
	_ = ctx
	return d, nil
}

// Start 启动两个监听 goroutine。重复调用无副作用。
func (d *Discovery) Start() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return fmt.Errorf("lan: discovery already closed")
	}
	if d.udpConn != nil {
		return nil
	}

	// UDP 广播监听（兜底通道）。
	lc := &net.ListenConfig{}
	uc, err := lc.ListenPacket(context.Background(), "udp4", fmt.Sprintf("0.0.0.0:%d", d.broadcastPort))
	if err != nil {
		return fmt.Errorf("lan: listen udp %d: %w", d.broadcastPort, err)
	}
	d.udpConn = uc.(*net.UDPConn)

	// mDNS 组播监听。失败不致命（部分容器禁组播），UDP 广播仍可用。
	mc, merr := net.ListenMulticastUDP("udp4", nil,
		&net.UDPAddr{IP: net.IPv4(224, 0, 0, 251), Port: 5353})
	if merr == nil {
		d.mdnsConn = mc
	} else {
		log.Printf("lan: mDNS 组播不可用（UDP 广播仍工作）: %v", merr)
	}

	d.wg.Add(1)
	go d.serveBroadcast(d.udpConn)
	if d.mdnsConn != nil {
		d.wg.Add(1)
		go d.serveMDNS(d.mdnsConn)
	}
	// 启动即公告一次，让已在监听的 App 立即发现。
	d.announceMDNS()
	return nil
}

// Close 停止全部监听。
func (d *Discovery) Close() {
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		return
	}
	d.closed = true
	close(d.stop)
	if d.udpConn != nil {
		_ = d.udpConn.Close()
	}
	if d.mdnsConn != nil {
		_ = d.mdnsConn.Close()
	}
	d.mu.Unlock()
	d.wg.Wait()
}

// Endpoint 返回对外广播的 HTTP 基础地址。
func (d *Discovery) Endpoint() string { return d.endpoint }

// Host 与 Port 返回对外广播的地址信息（供 /api/lan/info 使用）。
func (d *Discovery) Host() string { return d.addr.String() }
func (d *Discovery) Port() int    { return int(d.port) }
func (d *Discovery) Name() string { return d.instanceName }
func (d *Discovery) UDPPort() int { return d.broadcastPort }

// probeRequest / probeAnswer 是 UDP 广播通道的自研协议。
type probeRequest struct {
	Type string `json:"type"`
}

type probeAnswer struct {
	Type     string `json:"type"`
	Name     string `json:"name"`
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Endpoint string `json:"endpoint"`
	Path     string `json:"path"`
}

func (d *Discovery) serveBroadcast(conn *net.UDPConn) {
	defer d.wg.Done()
	buf := make([]byte, 2048)
	for {
		n, from, err := conn.ReadFromUDP(buf)
		if err != nil {
			select {
			case <-d.stop:
				return
			default:
			}
			continue
		}
		var req probeRequest
		if json.Unmarshal(buf[:n], &req) != nil || req.Type != "probe" {
			continue
		}
		ans := probeAnswer{
			Type: "answer", Name: d.instanceName, Host: d.addr.String(),
			Port: int(d.port), Endpoint: d.endpoint, Path: "/",
		}
		if data, e := json.Marshal(ans); e == nil {
			_, _ = conn.WriteToUDP(data, from)
		}
	}
}

func (d *Discovery) serveMDNS(conn *net.UDPConn) {
	defer d.wg.Done()
	buf := make([]byte, 9000)
	for {
		n, from, err := conn.ReadFromUDP(buf)
		if err != nil {
			select {
			case <-d.stop:
				return
			default:
			}
			continue
		}
		qs, err := parseDNSQuestions(buf[:n])
		if err != nil || len(qs) == 0 {
			continue
		}
		wantPTRSRV := false
		id := uint16(buf[0])<<8 | uint16(buf[1])
		for _, q := range qs {
			if strings.HasSuffix(q.name, dnsServiceType) {
				wantPTRSRV = true
			}
		}
		if !wantPTRSRV {
			continue
		}
		resp, err := buildMDNSResponse(id, d.instanceName, d.host, d.addr.To4(), d.port, d.txt)
		if err != nil {
			continue
		}
		// 组播应答；若查询来自单播端口（QU 位），也回组播以兼容多数实现。
		dest := &net.UDPAddr{IP: net.IPv4(224, 0, 0, 251), Port: 5353}
		if conn != nil {
			_, _ = conn.WriteToUDP(resp, dest)
		}
		if from != nil && from.Port != 5353 {
			// 部分实现期望单播回包到源端口。
			_, _ = conn.WriteToUDP(resp, from)
		}
	}
}

// announceMDNS 主动发一次 PTR/SRV/TXT/A 公告。
func (d *Discovery) announceMDNS() {
	if d.mdnsConn == nil {
		return
	}
	resp, err := buildMDNSResponse(0, d.instanceName, d.host, d.addr.To4(), d.port, d.txt)
	if err != nil {
		return
	}
	dest := &net.UDPAddr{IP: net.IPv4(224, 0, 0, 251), Port: 5353}
	_, _ = d.mdnsConn.WriteToUDP(resp, dest)
}

// pickIPv4 返回用于广播的 IPv4。
// host 非空时优先解析它；否则遍历网卡取第一个非回环、已 up 的 IPv4。
func pickIPv4(host string) (net.IP, error) {
	if strings.TrimSpace(host) != "" {
		if ip := net.ParseIP(strings.TrimSpace(host)); ip != nil && ip.To4() != nil {
			return ip.To4(), nil
		}
		ifs, err := net.LookupIP(strings.TrimSpace(host))
		if err == nil {
			for _, ip := range ifs {
				if ip.To4() != nil && !ip.IsLoopback() {
					return ip.To4(), nil
				}
			}
		}
		return nil, fmt.Errorf("lan: 无法解析广播地址 %q", host)
	}
	ifs, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	for _, ifi := range ifs {
		if ifi.Flags&net.FlagUp == 0 || ifi.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, _ := ifi.Addrs()
		for _, a := range addrs {
			var ip net.IP
			switch v := a.(type) {
			case *net.IPNet:
				ip = v.IP
			case *net.IPAddr:
				ip = v.IP
			}
			if ip == nil || ip.IsLoopback() {
				continue
			}
			if v4 := ip.To4(); v4 != nil && !v4.IsLinkLocalUnicast() {
				return v4, nil
			}
		}
	}
	return nil, fmt.Errorf("lan: 未找到可用的非回环 IPv4 地址")
}

// ensure 包在时间被使用，避免某些极简构建下的未用告警。
var _ = time.Second
