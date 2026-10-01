package crawler

import (
	"context"
	"fmt"
	"io"
	"math/rand/v2"
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

const tlsHijackProbeTimeout = 5 * time.Second

const (
	defaultSourceFetchRetries  = 2
	defaultSourceFetchTimeout  = 20 * time.Second
	defaultSourceResponseLimit = int64(4 * 1024 * 1024)
)

// deadlineDialer bounds both the connection to the proxy and its protocol
// handshake, which runs inside x/net/proxy's Dial call.
type deadlineDialer struct {
	timeout time.Duration
}

func (d deadlineDialer) Dial(network, address string) (net.Conn, error) {
	conn, err := net.DialTimeout(network, address, d.timeout)
	if err != nil {
		return nil, err
	}
	if err := conn.SetDeadline(time.Now().Add(d.timeout)); err != nil {
		conn.Close()
		return nil, err
	}
	return conn, nil
}

// cloudflareTraceResult 表示 Cloudflare cdn-cgi/trace 端点的解析结果
type cloudflareTraceResult struct {
	IP  string
	Loc string
}

const verifyEndpoint = "https://blog.cloudflare.com/cdn-cgi/trace"

// tlsHijackProbeHosts 用于检测代理是否选择性劫持 HTTPS 流量。
// 某些代理对 Cloudflare 等大型 CDN 的 IP 正常透传，但对非 CDN 站点做 MITM
// 并返回过期/自签名证书。每次验证随机选一个非 CDN 站点做 TLS 握手探测。
var tlsHijackProbeHosts = []string{
	"www.google.com:443",
	"www.apple.com:443",
	"www.microsoft.com:443",
	"www.amazon.com:443",
	"www.wikipedia.org:443",
	"www.github.com:443",
}

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

// probeTLSHijack 通过代理对非 CDN 站点发起 TLS 握手，检测代理是否选择性劫持 HTTPS。
// 只做握手不做 HTTP 请求，失败说明代理会篡改 TLS 证书。
func probeTLSHijack(proxyAddr string) error {
	proxyUrl, err := url.Parse(proxyAddr)
	if err != nil {
		return err
	}
	dialer, err := xproxy.FromURL(proxyUrl, deadlineDialer{timeout: tlsHijackProbeTimeout})
	if err != nil {
		return err
	}
	probeHost := tlsHijackProbeHosts[rand.IntN(len(tlsHijackProbeHosts))]
	conn, err := dialer.Dial("tcp", probeHost)
	if err != nil {
		return err
	}
	defer conn.Close()
	serverName, _, _ := net.SplitHostPort(probeHost)
	return probeTLSHandshake(conn, serverName, tlsHijackProbeTimeout)
}

func probeTLSHandshake(conn net.Conn, serverName string, timeout time.Duration) error {
	if err := conn.SetDeadline(time.Now().Add(timeout)); err != nil {
		return err
	}
	spec, err := utls.UTLSIdToSpec(utls.HelloChrome_Auto)
	if err != nil {
		return err
	}
	for _, ext := range spec.Extensions {
		if alpn, ok := ext.(*utls.ALPNExtension); ok {
			alpn.AlpnProtocols = []string{"http/1.1"}
		}
	}
	uconn := utls.UClient(conn, &utls.Config{ServerName: serverName}, utls.HelloCustom)
	if err := uconn.ApplyPreset(&spec); err != nil {
		return err
	}
	return uconn.Handshake()
}

func configuredSourceFetchRetries() int {
	retries := proxyinabox.Config.SourceFetch.Retries
	if retries <= 0 {
		return defaultSourceFetchRetries
	}
	if retries > 5 {
		return 5
	}
	return retries
}

func configuredSourceFetchTimeout() time.Duration {
	if proxyinabox.Config.SourceFetch.Timeout > 0 {
		return proxyinabox.Config.SourceFetch.Timeout
	}
	return defaultSourceFetchTimeout
}

func configuredSourceResponseLimit() int64 {
	if proxyinabox.Config.SourceFetch.ResponseBodyLimit > 0 {
		return proxyinabox.Config.SourceFetch.ResponseBodyLimit
	}
	return defaultSourceResponseLimit
}

// GetDocFromURL fetches source documents directly with a bounded body. An
// optional single proxy fallback is available for sources that cannot be
// reached directly; a bad free proxy is never allowed to delay every source
// refresh by several retries.
func GetDocFromURL(url string, customHeaders ...http.Header) (string, error) {
	timeout := configuredSourceFetchTimeout()
	limit := configuredSourceResponseLimit()
	body, err := getURLThroughProxyWithRetryLimit(url, timeout, "", configuredSourceFetchRetries(), limit, customHeaders...)
	if err == nil {
		return string(body), nil
	}
	if !proxyinabox.Config.SourceFetch.ProxyFallback || proxyinabox.CI == nil {
		return "", err
	}
	proxy, ok := proxyinabox.CI.RandomProxy()
	if !ok || proxy == "" {
		return "", err
	}
	if proxyinabox.Config.Debug {
		fmt.Printf("[PIAB] fetch [⚠️] direct fetch failed for %s, trying one proxy: %v\n", url, err)
	}
	body, proxyErr := getURLThroughProxyWithRetryLimit(url, timeout, proxy, 1, limit, customHeaders...)
	if proxyErr != nil {
		return "", fmt.Errorf("direct fetch: %v; proxy fallback: %w", err, proxyErr)
	}
	return string(body), nil
}

func validator(id int, validateJobs chan proxyinabox.Proxy) {
	for p := range validateJobs {
		p.IP = strings.TrimSpace(p.IP)
		proxy := p.URI()

		// BUG-FIX: 使用 LoadOrStore 原子操作，防止多个 validator 同时验证同一代理。
		// 每个成功获取的所有权都必须在本次循环结束时释放，包括代理已在缓存中的路径。
		if _, loaded := pendingValidate.LoadOrStore(proxy, nil); loaded {
			runtimeMetrics.skipDuplicate.Add(1)
			continue
		}
		func() {
			defer pendingValidate.Delete(proxy)

			if proxyinabox.CI.IsIPLocked(p.IP) {
				runtimeMetrics.skipIPLocked.Add(1)
				return
			}

			if proxyinabox.CI.HasProxy(proxy) {
				runtimeMetrics.skipAvailable.Add(1)
				candidateFailures.clear(proxy)
				return
			}

			// Persisted endpoint delays and the in-memory negative cache both
			// prevent a frequent source refresh from bypassing retry backoff.
			if !proxyinabox.CI.IsProxyValidationDue(proxy) ||
				!candidateFailures.isDue(proxy, time.Now()) {
				runtimeMetrics.skipBackoff.Add(1)
				return
			}

			runtimeMetrics.candidateWaiting.Add(1)
			slots := acquireValidationSlot()
			runtimeMetrics.candidateWaiting.Add(-1)
			defer releaseValidationSlot(slots)
			runtimeMetrics.candidateActive.Add(1)
			defer runtimeMetrics.candidateActive.Add(-1)
			start := time.Now().Unix()

			body, requestErr := getURLThroughProxyWithRetryLimit(
				verifyEndpoint,
				time.Second*7,
				proxy,
				configuredHealthCheckRetries(),
				configuredHealthResponseBodyLimit(),
			)
			var trace cloudflareTraceResult
			var parseErr error
			if requestErr == nil {
				trace, parseErr = parseCloudflareTrace(body)
			}

			if requestErr != nil {
				checkCounters.recordCandidate(false)
				recordNetworkFailure(requestErr)
				candidateFailures.recordFailure(proxy, 1, time.Now())
				return
			}
			if parseErr != nil {
				checkCounters.recordCandidate(false)
				runtimeMetrics.failResponse.Add(1)
				candidateFailures.recordFailure(proxy, 1, time.Now())
				return
			}
			if trace.IP != p.IP {
				checkCounters.recordCandidate(false)
				runtimeMetrics.failIPMismatch.Add(1)
				candidateFailures.recordFailure(proxy, 1, time.Now())
				return
			}

			// New and recovered source candidates always receive a deep TLS
			// integrity check before entering the live pool.
			if hijackErr := probeTLSHijack(proxy); hijackErr != nil {
				checkCounters.recordCandidate(false)
				runtimeMetrics.failTLSProbe.Add(1)
				fmt.Printf("[PIAB] crawler [🔓] %d proxy %s passed Cloudflare but failed TLS hijack probe: %v\n", id, proxy, hijackErr)
				candidateFailures.recordFailure(proxy, 1, time.Now())
				proxyinabox.CI.RecordFailure(p.IP)
				return
			}
			verifiedAt := time.Now()
			p.Country = trace.Loc
			p.Delay = verifiedAt.Unix() - start
			p.LastVerify = verifiedAt
			p.LastDeepVerify = verifiedAt

			if e := proxyinabox.CI.UpsertProxy(p); e == nil {
				checkCounters.recordCandidate(true)
				candidateFailures.clear(proxy)
				if proxyinabox.Config.Debug {
					fmt.Println("[PIAB]", "crawler", "[✅]", id, "find a available proxy", p)
				}
			} else {
				checkCounters.recordCandidate(false)
				runtimeMetrics.failPersistence.Add(1)
				fmt.Println("[PIAB]", "crawler", "[❎]", id, "error save proxy", e.Error())
			}
		}()
	}
}

// ValidateProxy 通过代理访问 Cloudflare trace 端点验证代理可用性，返回验证结果
// 不依赖 DB/Cache，仅做网络验证，供 test-source 命令使用
func ValidateProxy(p proxyinabox.Proxy) (country string, delay int64, err error) {
	p.IP = strings.TrimSpace(p.IP)
	proxy := p.URI()
	start := time.Now().Unix()

	body, err := getURLThroughProxyWithRetryLimit(
		verifyEndpoint,
		time.Second*7,
		proxy,
		configuredHealthCheckRetries(),
		configuredHealthResponseBodyLimit(),
	)
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
	return getURLThroughProxyWithRetryLimit(u, timeout, proxyAddr, retry, 0, customHeaders...)
}

func getURLThroughProxyWithRetryLimit(
	u string,
	timeout time.Duration,
	proxyAddr string,
	retry int,
	responseBodyLimit int64,
	customHeaders ...http.Header,
) ([]byte, error) {
	transport := &http.Transport{}
	defer transport.CloseIdleConnections()

	if proxyAddr != "" {
		proxyUrl, err := url.Parse(proxyAddr)
		if err != nil {
			return nil, err
		}
		// Client.Timeout cannot interrupt a custom Dialer that is blocked in a
		// proxy protocol handshake. Bound the underlying connection and handshake
		// so failed candidates do not leave dial goroutines and sockets behind.
		dialer, err := xproxy.FromURL(proxyUrl, deadlineDialer{timeout: timeout})
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
		runtimeMetrics.networkAttempts.Add(1)
		resp, err := httpClient.Do(request)
		if err != nil {
			lastErr = err
			continue
		}
		reader := io.Reader(resp.Body)
		if responseBodyLimit > 0 {
			reader = io.LimitReader(resp.Body, responseBodyLimit+1)
		}
		body, err := io.ReadAll(reader)
		runtimeMetrics.networkResponseBytes.Add(uint64(len(body)))
		closeErr := resp.Body.Close()
		if err != nil {
			lastErr = err
			continue
		}
		if closeErr != nil {
			lastErr = closeErr
			continue
		}
		if responseBodyLimit > 0 && int64(len(body)) > responseBodyLimit {
			lastErr = fmt.Errorf("response body exceeds %d bytes", responseBodyLimit)
			continue
		}
		return body, nil
	}
	return nil, lastErr
}
