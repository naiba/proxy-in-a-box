package crawler

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/naiba/proxyinabox"
	utls "github.com/refraction-networking/utls"
	xproxy "golang.org/x/net/proxy"
)

var ValidateJobs chan proxyinabox.Proxy
var pendingValidate sync.Map

// cloudflareTraceResult 表示 Cloudflare cdn-cgi/trace 端点的解析结果
type cloudflareTraceResult struct {
	IP  string
	Loc string
}

// verifyEndpoint 是 HTTPS 端点，HTTP 代理必须支持 CONNECT 隧道才能通过验证
const verifyEndpoint = "https://blog.cloudflare.com/cdn-cgi/trace"

// parseCloudflareTrace 解析 cdn-cgi/trace 返回的 key=value 纯文本（如 ip=1.2.3.4\nloc=JP\n...）
func parseCloudflareTrace(body []byte) (cloudflareTraceResult, error) {
	var result cloudflareTraceResult
	lines := strings.Split(string(body), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if k, v, ok := strings.Cut(line, "="); ok {
			switch k {
			case "ip":
				result.IP = v
			case "loc":
				result.Loc = v
			}
		}
	}
	if result.IP == "" {
		return result, fmt.Errorf("cloudflare trace: ip field not found in response")
	}
	return result, nil
}

// GetDocFromURL fetches a URL body as string, optionally through a random proxy.
// 优先通过代理池中的随机 proxy 抓取，若代理抓取失败则 fallback 到直连重试，确保源站可达性最大化。
func GetDocFromURL(url string, customHeaders ...http.Header) (string, error) {
	var proxy string
	if proxyinabox.CI != nil {
		proxy, _ = proxyinabox.CI.RandomProxy()
	}

	if proxy != "" {
		body, err := GetURLThroughProxyWithRetry(url, time.Second*20, proxy, 3, customHeaders...)
		if err == nil {
			return string(body), nil
		}
		if proxyinabox.Config.Debug {
			fmt.Printf("[PIAB] fetch [⚠️] proxy fetch failed for %s, fallback to direct: %v\n", url, err)
		}
	}

	body, err := GetURLThroughProxyWithRetry(url, time.Second*20, "", 3, customHeaders...)
	if err != nil {
		return "", err
	}
	return string(body), nil
}

func validator(id int, validateJobs chan proxyinabox.Proxy) {
	for p := range validateJobs {
		p.IP = strings.TrimSpace(p.IP)
		proxy := p.URI()

		if proxyinabox.CI.IsIPLocked(p.IP) {
			continue
		}

		_, has := pendingValidate.Load(proxy)
		if !has && !proxyinabox.CI.HasProxy(p.URI()) {
			pendingValidate.Store(proxy, nil)
			start := time.Now().Unix()

			body, err := GetURLThroughProxyWithRetry(verifyEndpoint, time.Second*7, proxy, 3)
			var trace cloudflareTraceResult
			if err == nil {
				trace, err = parseCloudflareTrace(body)
			}

			if err == nil && trace.IP == p.IP {
				p.Country = trace.Loc
				p.Delay = time.Now().Unix() - start
				p.LastVerify = time.Now()

				if e := proxyinabox.CI.UpsertProxy(p); e == nil {
					if proxyinabox.Config.Debug {
						fmt.Println("[PIAB]", "crawler", "[✅]", id, "find a available proxy", p)
					}
				} else {
					fmt.Println("[PIAB]", "crawler", "[❎]", id, "error save proxy", e.Error())
				}
			}
			pendingValidate.Delete(proxy)
		}
	}
}

// ValidateProxy 通过代理访问 Cloudflare trace 端点验证代理可用性，返回验证结果
// 不依赖 DB/Cache，仅做网络验证，供 test-source 命令使用
func ValidateProxy(p proxyinabox.Proxy) (country string, delay int64, err error) {
	p.IP = strings.TrimSpace(p.IP)
	proxy := p.URI()
	start := time.Now().Unix()

	body, err := GetURLThroughProxyWithRetry(verifyEndpoint, time.Second*7, proxy, 2)
	if err != nil {
		return "", 0, fmt.Errorf("connect failed: %w", err)
	}

	trace, err := parseCloudflareTrace(body)
	if err != nil {
		return "", 0, err
	}

	if trace.IP != p.IP {
		return "", 0, fmt.Errorf("ip mismatch: expected %s, got %s", p.IP, trace.IP)
	}

	return trace.Loc, time.Now().Unix() - start, nil
}

// GetURLThroughProxyWithRetry fetches a URL through the given proxy with retry logic
func GetURLThroughProxyWithRetry(u string, timeout time.Duration, proxyAddr string, retry int, customHeaders ...http.Header) ([]byte, error) {
	transport := &http.Transport{}

	if proxyAddr != "" {
		proxyUrl, err := url.Parse(proxyAddr)
		if err != nil {
			return nil, err
		}
		dialer, err := xproxy.FromURL(proxyUrl, xproxy.Direct)
		if err != nil {
			return nil, err
		}
		transport.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
			return dialer.Dial(network, addr)
		}
		// uTLS 指纹伪装：模拟 Chrome TLS ClientHello，防止被目标网站识别为爬虫
		// BUG-FIX: HelloChrome_Auto 的预设 ALPN 扩展包含 h2，会覆盖 Config.NextProtos，
		// 导致服务器协商 HTTP/2，而 http.Transport 通过自定义 DialTLSContext 时只支持
		// HTTP/1.x，收到 HTTP/2 二进制帧后报 "malformed HTTP response"。
		// 解决方案：用 HelloCustom + ApplyPreset 先获取 Chrome spec，再修改 ALPN 为仅 http/1.1
		transport.DialTLSContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
			conn, err := dialer.Dial(network, addr)
			if err != nil {
				return nil, err
			}
			serverName, _, _ := net.SplitHostPort(addr)
			spec, err := utls.UTLSIdToSpec(utls.HelloChrome_Auto)
			if err != nil {
				conn.Close()
				return nil, err
			}
			for _, ext := range spec.Extensions {
				if alpn, ok := ext.(*utls.ALPNExtension); ok {
					alpn.AlpnProtocols = []string{"http/1.1"}
				}
			}
			uconn := utls.UClient(conn, &utls.Config{
				ServerName: serverName,
			}, utls.HelloCustom)
			if err := uconn.ApplyPreset(&spec); err != nil {
				conn.Close()
				return nil, err
			}
			if err := uconn.Handshake(); err != nil {
				conn.Close()
				return nil, err
			}
			return uconn, nil
		}
	}

	httpClient := &http.Client{
		Timeout:   timeout,
		Transport: transport,
	}
	request, err := http.NewRequest("GET", u, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/128.0.0.0 Safari/537.36")
	for _, h := range customHeaders {
		for k, v := range h {
			request.Header.Set(k, strings.Join(v, ";"))
		}
	}
	var lastErr error
	for i := 0; i < retry; i++ {
		resp, err := httpClient.Do(request)
		if err != nil {
			lastErr = err
			continue
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			lastErr = err
			continue
		}
		return body, nil
	}
	return nil, lastErr
}
