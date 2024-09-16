package mitm

import (
	"crypto/tls"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/getlantern/keyman"
	"github.com/patrickmn/go-cache"
)

const (
	version  = "1.0"
	basename = "NBMITM"
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
	HTTPAddr    string //HTTP listen addr
	HTTPSAddr   string //HTTPS listen addr
	TLSConf     *TLSConfig
	Print       bool //打印请求详情

	IsDirect  bool                                              //是否直连，不通过代理
	Scheduler func(req *http.Request) (proxy string, err error) //代理调度 func
	Filter    func(req *http.Request) error                     //请求鉴权、清洗、限流

	cache       *cache.Cache
	pk          *keyman.PrivateKey
	pkPem       []byte
	issuingCert *keyman.Certificate
}

// Init mitm
func (m *MITM) Init() {
	m.cache = cache.New(time.Hour, time.Minute)
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
		Addr:    addr,
		Handler: http.HandlerFunc(m.serve),
		// Disable HTTP/2.
		TLSNextProto: make(map[string]func(*http.Server, *tls.Conn, http.Handler)),
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
	//鉴权、清洗、限流
	if e := m.Filter(r); e != nil {
		http.Error(w, e.Error(), http.StatusProxyAuthRequired)
		return
	}
	if r.Method == http.MethodConnect {
		m.injectHTTPS(w, r)
	} else {
		m.Dump(w, r)
	}
}

func (m *MITM) injectHTTPS(resp http.ResponseWriter, req *http.Request) {
	addr := req.Host
	host := strings.Split(addr, ":")[0]

	cert, err := m.FakeCert(host)
	if err != nil {
		msg := fmt.Sprintf("[MITM] injectHTTPS [💖] Could not get mitm cert for name: %s\nerror: %s", host, err)
		badGateWay(resp, msg)
		return
	}

	// handle connection
	connIn, _, err := resp.(http.Hijacker).Hijack()
	if err != nil {
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
