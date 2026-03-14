package mitm

import (
	"bufio"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"

	xproxy "golang.org/x/net/proxy"
)

// ProxyConnectError 上游代理在 CONNECT 握手阶段返回的非 200 响应，
// 携带 StatusCode 以便上层区分 407（需要认证）等可识别的失败原因
type ProxyConnectError struct {
	StatusCode int
	Status     string
}

func (e *ProxyConnectError) Error() string {
	return fmt.Sprintf("proxy CONNECT returned %s", e.Status)
}

// httpConnectDialer 通过 HTTP CONNECT 方法建立 TCP 隧道
type httpConnectDialer struct {
	proxyAddr string
	forward   xproxy.Dialer
}

func (d *httpConnectDialer) Dial(network, addr string) (net.Conn, error) {
	conn, err := d.forward.Dial("tcp", d.proxyAddr)
	if err != nil {
		return nil, err
	}

	connectReq := &http.Request{
		Method: "CONNECT",
		URL:    &url.URL{Opaque: addr},
		Host:   addr,
		Header: make(http.Header),
	}
	if err := connectReq.Write(conn); err != nil {
		conn.Close()
		return nil, err
	}

	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, connectReq)
	if err != nil {
		conn.Close()
		return nil, err
	}
	if resp.StatusCode != 200 {
		conn.Close()
		return nil, &ProxyConnectError{StatusCode: resp.StatusCode, Status: resp.Status}
	}

	return conn, nil
}

// socks4Dialer 实现 SOCKS4 协议拨号
// BUG-FIX: golang.org/x/net/proxy 只内置 SOCKS5 支持，socks4:// scheme 未注册会
// 导致 "unknown scheme: socks4" 错误，所有 SOCKS4 代理验证必然失败
type socks4Dialer struct {
	proxyAddr string
	forward   xproxy.Dialer
}

func (d *socks4Dialer) Dial(network, addr string) (net.Conn, error) {
	conn, err := d.forward.Dial("tcp", d.proxyAddr)
	if err != nil {
		return nil, err
	}

	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		conn.Close()
		return nil, err
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("socks4: invalid port %q", portStr)
	}

	ip := net.ParseIP(host)
	if ip == nil {
		ips, err := net.ResolveIPAddr("ip4", host)
		if err != nil {
			conn.Close()
			return nil, fmt.Errorf("socks4: cannot resolve %s: %w", host, err)
		}
		ip = ips.IP
	}
	ip4 := ip.To4()
	if ip4 == nil {
		conn.Close()
		return nil, fmt.Errorf("socks4: IPv6 not supported, got %s", ip)
	}

	// SOCKS4 请求: VER(1) CMD(1) DSTPORT(2) DSTIP(4) USERID(variable) NULL(1)
	req := []byte{
		0x04,                        // SOCKS version 4
		0x01,                        // CONNECT command
		byte(port >> 8), byte(port), // destination port (big-endian)
		ip4[0], ip4[1], ip4[2], ip4[3], // destination IP
		0x00, // empty user ID, null terminator
	}
	if _, err := conn.Write(req); err != nil {
		conn.Close()
		return nil, err
	}

	// SOCKS4 响应: VN(1) CD(1) DSTPORT(2) DSTIP(4) — 共 8 字节
	var resp [8]byte
	if _, err := conn.Read(resp[:]); err != nil {
		conn.Close()
		return nil, err
	}

	// CD: 0x5A (90) = granted
	if resp[1] != 0x5A {
		conn.Close()
		return nil, fmt.Errorf("socks4: request rejected, code=0x%02X", resp[1])
	}

	return conn, nil
}

func init() {
	// 注册 HTTP CONNECT dialer，使 x/net/proxy.FromURL 能处理 http:// 代理
	// 注册后 FromURL 自动根据 scheme 选择 SOCKS5 或 HTTP CONNECT
	xproxy.RegisterDialerType("http", func(u *url.URL, forward xproxy.Dialer) (xproxy.Dialer, error) {
		return &httpConnectDialer{
			proxyAddr: u.Host,
			forward:   forward,
		}, nil
	})
	xproxy.RegisterDialerType("https", func(u *url.URL, forward xproxy.Dialer) (xproxy.Dialer, error) {
		return &httpConnectDialer{
			proxyAddr: u.Host,
			forward:   forward,
		}, nil
	})
	xproxy.RegisterDialerType("socks4", func(u *url.URL, forward xproxy.Dialer) (xproxy.Dialer, error) {
		return &socks4Dialer{
			proxyAddr: u.Host,
			forward:   forward,
		}, nil
	})
}
