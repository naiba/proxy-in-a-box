package mitm

import (
	"bufio"
	"fmt"
	"net"
	"net/http"
	"net/url"

	xproxy "golang.org/x/net/proxy"
)

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
		return nil, fmt.Errorf("proxy CONNECT returned %s", resp.Status)
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
}
