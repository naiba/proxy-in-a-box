package mitm

import (
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/patrickmn/go-cache"
	xproxy "golang.org/x/net/proxy"
)

const (
	version                              = "1.0"
	basename                             = "NBMITM"
	defaultMaxUpstreamAttempts           = 3
	maxConfiguredUpstreamAttempts        = 10
	defaultUpstreamConnectTimeout        = 5 * time.Second
	defaultUpstreamHandshakeTimeout      = 7 * time.Second
	defaultUpstreamResponseHeaderTimeout = 12 * time.Second
	defaultUpstreamRequestTimeout        = 20 * time.Second
)

// TLSConfig TLS配置
type TLSConfig struct {
	PrivateKeyFile  string
	CertFile        string
	Organization    string
	CommonName      string
	ServerTLSConfig *tls.Config
}

// MITM 中间人
type MITM struct {
	ListenHTTPS bool   //开启 HTTPS 代理服务器
	EnableMITM  bool   //启用 HTTPS 中间人解密，关闭时 CONNECT 走 TCP 隧道透传
	HTTPAddr    string //HTTP listen addr
	HTTPSAddr   string //HTTPS listen addr
	TLSConf     *TLSConfig
	Print       bool //打印请求详情

	Scheduler      func(req *http.Request) (proxy string, err error) //代理调度 func
	Filter         func(req *http.Request) error                     //请求鉴权、清洗、限流
	OnProxyFailure func(proxyURI string)                             //上游代理不可用时的回调（如 407 需要认证）
	// OnProxyTargetFailure temporarily excludes one upstream for one target.
	// Target-specific failures must not quarantine an otherwise healthy proxy.
	OnProxyTargetFailure func(proxyURI string, target string)

	MaxUpstreamAttempts           int
	UpstreamConnectTimeout        time.Duration
	UpstreamHandshakeTimeout      time.Duration
	UpstreamResponseHeaderTimeout time.Duration
	UpstreamRequestTimeout        time.Duration

	cache       *cache.Cache
	pk          *rsa.PrivateKey
	pkPem       []byte
	issuingCert *x509.Certificate
}

// Init mitm
func (m *MITM) Init() {
	m.cache = cache.New(time.Hour, time.Minute)

	if !m.EnableMITM {
		return
	}

	m.GenerateCA()

	if m.TLSConf.CommonName == "" {
		m.TLSConf.CommonName = basename
	}
	if m.TLSConf.Organization == "" {
		m.TLSConf.Organization = basename + "/v" + version
	}

	if m.TLSConf.ServerTLSConfig == nil {
		m.TLSConf.ServerTLSConfig = &tls.Config{
			CipherSuites: []uint16{
				tls.TLS_RSA_WITH_RC4_128_SHA,
				tls.TLS_RSA_WITH_3DES_EDE_CBC_SHA,
				tls.TLS_RSA_WITH_AES_128_CBC_SHA,
				tls.TLS_RSA_WITH_AES_256_CBC_SHA,
				tls.TLS_RSA_WITH_AES_128_CBC_SHA256,
				tls.TLS_RSA_WITH_AES_128_GCM_SHA256,
				tls.TLS_RSA_WITH_AES_256_GCM_SHA384,
				tls.TLS_ECDHE_ECDSA_WITH_RC4_128_SHA,
				tls.TLS_ECDHE_ECDSA_WITH_AES_128_CBC_SHA,
				tls.TLS_ECDHE_ECDSA_WITH_AES_256_CBC_SHA,
				tls.TLS_ECDHE_RSA_WITH_RC4_128_SHA,
				tls.TLS_ECDHE_RSA_WITH_3DES_EDE_CBC_SHA,
				tls.TLS_ECDHE_RSA_WITH_AES_128_CBC_SHA,
				tls.TLS_ECDHE_RSA_WITH_AES_256_CBC_SHA,
				tls.TLS_ECDHE_ECDSA_WITH_AES_128_CBC_SHA256,
				tls.TLS_ECDHE_RSA_WITH_AES_128_CBC_SHA256,
				tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256,
				tls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256,
				tls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384,
				tls.TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384,
				tls.TLS_ECDHE_RSA_WITH_CHACHA20_POLY1305,
				tls.TLS_ECDHE_ECDSA_WITH_CHACHA20_POLY1305,
				tls.TLS_FALLBACK_SCSV,
			},
			PreferServerCipherSuites: true,
			InsecureSkipVerify:       true,
		}
	}
}

func (m *MITM) newServer(addr string) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           http.HandlerFunc(m.serve),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       90 * time.Second,
		// Disable HTTP/2.
		TLSNextProto: make(map[string]func(*http.Server, *tls.Conn, http.Handler)),
	}
}

func positiveDuration(value time.Duration, fallback time.Duration) time.Duration {
	if value > 0 {
		return value
	}
	return fallback
}

func (m *MITM) upstreamAttemptLimit() int {
	limit := m.MaxUpstreamAttempts
	if limit <= 0 {
		limit = defaultMaxUpstreamAttempts
	}
	if limit > maxConfiguredUpstreamAttempts {
		return maxConfiguredUpstreamAttempts
	}
	return limit
}

func (m *MITM) upstreamConnectTimeout() time.Duration {
	return positiveDuration(m.UpstreamConnectTimeout, defaultUpstreamConnectTimeout)
}

func (m *MITM) upstreamHandshakeTimeout() time.Duration {
	return positiveDuration(m.UpstreamHandshakeTimeout, defaultUpstreamHandshakeTimeout)
}

func (m *MITM) upstreamResponseHeaderTimeout() time.Duration {
	return positiveDuration(m.UpstreamResponseHeaderTimeout, defaultUpstreamResponseHeaderTimeout)
}

func (m *MITM) upstreamRequestTimeout() time.Duration {
	return positiveDuration(m.UpstreamRequestTimeout, defaultUpstreamRequestTimeout)
}

func (m *MITM) nextProxy(r *http.Request, tried map[string]struct{}) (string, error) {
	if m.Scheduler == nil {
		return "", errors.New("proxy scheduler is not configured")
	}
	var lastDuplicate string
	for range m.upstreamAttemptLimit() * 2 {
		proxyURI, err := m.Scheduler(r)
		if err != nil {
			return "", err
		}
		if _, exists := tried[proxyURI]; exists {
			lastDuplicate = proxyURI
			continue
		}
		tried[proxyURI] = struct{}{}
		return proxyURI, nil
	}
	return "", fmt.Errorf("proxy scheduler repeatedly selected an already attempted proxy %q", lastDuplicate)
}

func (m *MITM) reportTargetFailure(proxyURI string, target string) {
	if m.OnProxyTargetFailure != nil {
		m.OnProxyTargetFailure(proxyURI, target)
	}
}

func (m *MITM) ServeHTTP() {
	//start http proxy server
	httpServer := m.newServer(m.HTTPAddr)
	go func() {
		fmt.Println("[MITM]", "proxy server", "[💖]", "http://"+m.HTTPAddr)
		if e := httpServer.ListenAndServe(); e != nil {
			panic(e)
		}
	}()

	//start https proxy server
	if m.ListenHTTPS {
		httpsServer := m.newServer(m.HTTPSAddr)
		go func() {
			fmt.Println("[MITM]", "proxy server", "[💖]", "https://"+m.HTTPSAddr)
			if e := httpsServer.ListenAndServeTLS(m.TLSConf.CertFile, m.TLSConf.PrivateKeyFile); e != nil {
				panic(e)
			}
		}()
	}
}

func (m *MITM) serve(w http.ResponseWriter, r *http.Request) {
	GlobalRequestStats.TotalRequests.Add(1)

	if m.Filter != nil {
		if e := m.Filter(r); e != nil {
			GlobalRequestStats.FailedRequests.Add(1)
			http.Error(w, e.Error(), http.StatusProxyAuthRequired)
			return
		}
	}
	if r.Method == http.MethodConnect {
		if m.EnableMITM {
			m.injectHTTPS(w, r)
		} else {
			m.tunnelHTTPS(w, r)
		}
	} else {
		m.Dump(w, r)
	}
}

func (m *MITM) injectHTTPS(resp http.ResponseWriter, req *http.Request) {
	addr := req.Host
	host := strings.Split(addr, ":")[0]

	cert, err := m.FakeCert(host)
	if err != nil {
		// BUG-FIX: 之前 injectHTTPS 的失败路径漏掉了 FailedRequests 计数，导致 Total ≠ Success + Failed
		GlobalRequestStats.FailedRequests.Add(1)
		msg := fmt.Sprintf("[MITM] injectHTTPS [💖] Could not get mitm cert for name: %s\nerror: %s", host, err)
		badGateWay(resp, msg)
		return
	}

	// handle connection
	connIn, _, err := resp.(http.Hijacker).Hijack()
	if err != nil {
		GlobalRequestStats.FailedRequests.Add(1)
		msg := fmt.Sprintf("[MITM] injectHTTPS [💖] Unable to access underlying connection from client: %s", err)
		badGateWay(resp, msg)
		return
	}
	tlsConfig := copyTLSConfig(m.TLSConf.ServerTLSConfig)
	tlsConfig.Certificates = []tls.Certificate{*cert}
	tlsConnIn := tls.Server(connIn, tlsConfig)
	listener := &mitmListener{tlsConnIn}
	handler := http.HandlerFunc(func(resp2 http.ResponseWriter, req2 *http.Request) {
		req2.URL.Scheme = "https"
		req2.URL.Host = req2.Host
		m.Dump(resp2, req2)
	})

	go func() {
		err = http.Serve(listener, handler)
		if err != nil && err != io.EOF {
			fmt.Printf("[MITM] injectHTTPS [💖] Error serving mitm'ed connection: %s", err)
		}
	}()

	connIn.Write([]byte("HTTP/1.1 200 OK\r\n\r\n"))
}

// tunnelHTTPS 不解密 HTTPS 流量，直接建立 TCP 隧道透传，客户端与目标服务器直接完成 TLS 握手
func (m *MITM) tunnelHTTPS(w http.ResponseWriter, r *http.Request) {
	targetKey := r.Host
	targetAddr := targetKey
	if !strings.Contains(targetAddr, ":") {
		targetAddr += ":443"
	}
	if targetKey == "" {
		targetKey = targetAddr
	}

	tried := make(map[string]struct{})
	var attemptErrors []error
	var remoteConn net.Conn
	var upstreamProto string
	var upstreamStats *RequestStats

	for attempt := 1; attempt <= m.upstreamAttemptLimit(); attempt++ {
		proxyURI, schedErr := m.nextProxy(r, tried)
		if schedErr != nil {
			attemptErrors = append(attemptErrors, fmt.Errorf("schedule attempt %d: %w", attempt, schedErr))
			break
		}
		proxyURL, parseErr := url.Parse(proxyURI)
		if parseErr != nil {
			m.reportTargetFailure(proxyURI, targetKey)
			attemptErrors = append(attemptErrors, fmt.Errorf("parse upstream %s: %w", proxyURI, parseErr))
			continue
		}

		upstreamProto = proxyURL.Scheme
		upstreamStats = GlobalUpstreamStats.Get(upstreamProto)
		upstreamStats.TotalRequests.Add(1)

		// Bound both the TCP connection to the public proxy and its protocol
		// handshake. A silent CONNECT endpoint must never hold the client forever.
		dialer, dialErr := xproxy.FromURL(proxyURL, deadlineDialer{
			connectTimeout:   m.upstreamConnectTimeout(),
			handshakeTimeout: m.upstreamHandshakeTimeout(),
		})
		if dialErr == nil {
			remoteConn, dialErr = dialer.Dial("tcp", targetAddr)
		}
		if dialErr != nil {
			var connectErr *ProxyConnectError
			if errors.As(dialErr, &connectErr) &&
				(connectErr.StatusCode == http.StatusProxyAuthRequired || connectErr.StatusCode == http.StatusForbidden) {
				if m.OnProxyFailure != nil {
					m.OnProxyFailure(proxyURI)
				}
			}
			m.reportTargetFailure(proxyURI, targetKey)
			upstreamStats.FailedRequests.Add(1)
			attemptErrors = append(attemptErrors, fmt.Errorf("attempt %d through %s: %w", attempt, proxyURI, dialErr))
			continue
		}
		if deadlineErr := remoteConn.SetDeadline(time.Time{}); deadlineErr != nil {
			remoteConn.Close()
			m.reportTargetFailure(proxyURI, targetKey)
			upstreamStats.FailedRequests.Add(1)
			attemptErrors = append(attemptErrors, fmt.Errorf("clear deadline through %s: %w", proxyURI, deadlineErr))
			remoteConn = nil
			continue
		}
		break
	}

	if remoteConn == nil {
		GlobalRequestStats.FailedRequests.Add(1)
		badGateWay(w, fmt.Sprintf("[MITM] tunnelHTTPS failed after %d upstream attempt(s): %v", len(attemptErrors), errors.Join(attemptErrors...)))
		return
	}

	clientConn, _, err := w.(http.Hijacker).Hijack()
	if err != nil {
		remoteConn.Close()
		GlobalRequestStats.FailedRequests.Add(1)
		upstreamStats.FailedRequests.Add(1)
		badGateWay(w, fmt.Sprintf("[MITM] tunnelHTTPS hijack error: %s", err))
		return
	}

	GlobalRequestStats.SuccessRequests.Add(1)
	upstreamStats.SuccessRequests.Add(1)
	clientConn.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n"))

	// BUG-FIX: 之前 io.Copy 直接搬运数据没有统计字节数，导致 HTTPS 隧道模式流量不增长
	go func() {
		io.Copy(&trafficCountingWriter{inner: remoteConn, upstreamProtocol: upstreamProto}, clientConn)
		remoteConn.Close()
	}()
	go func() {
		io.Copy(&trafficCountingWriter{inner: clientConn, upstreamProtocol: upstreamProto}, remoteConn)
		clientConn.Close()
	}()
}

func copyTLSConfig(c *tls.Config) *tls.Config {
	return &tls.Config{
		Certificates:             c.Certificates,
		NameToCertificate:        c.NameToCertificate,
		GetCertificate:           c.GetCertificate,
		RootCAs:                  c.RootCAs,
		NextProtos:               c.NextProtos,
		ServerName:               c.ServerName,
		ClientAuth:               c.ClientAuth,
		ClientCAs:                c.ClientCAs,
		InsecureSkipVerify:       c.InsecureSkipVerify,
		CipherSuites:             c.CipherSuites,
		PreferServerCipherSuites: c.PreferServerCipherSuites,
		SessionTicketsDisabled:   c.SessionTicketsDisabled,
		SessionTicketKey:         c.SessionTicketKey,
		ClientSessionCache:       c.ClientSessionCache,
		MinVersion:               c.MinVersion,
		MaxVersion:               c.MaxVersion,
		CurvePreferences:         c.CurvePreferences,
	}
}

func badGateWay(w http.ResponseWriter, msg string) {
	w.WriteHeader(http.StatusBadGateway)
	w.Write([]byte(msg))
}
