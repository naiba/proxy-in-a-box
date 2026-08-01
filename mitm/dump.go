package mitm

import (
	"compress/gzip"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"

	xproxy "golang.org/x/net/proxy"
)

// Dump rt
func (m *MITM) Dump(clientResponse http.ResponseWriter, clientRequest *http.Request) {
	var clientRequestDump []byte
	var remoteResponseDump []byte
	var remoteResponse *http.Response
	var err error
	var upstreamProto string

	defer func() {
		if err != nil {
			GlobalRequestStats.FailedRequests.Add(1)
			if upstreamProto != "" {
				GlobalUpstreamStats.Get(upstreamProto).FailedRequests.Add(1)
			}
			clientResponse.WriteHeader(http.StatusBadGateway)
			clientResponse.Write([]byte(err.Error()))
		}
	}()

	clientRequestDump, err = httputil.DumpRequestOut(clientRequest, true)
	if err != nil {
		fmt.Println("[MITM]", "DumpRequest", "[❎]", err)
		return
	}

	var selectedProxyURI string
	remoteResponse, upstreamProto, selectedProxyURI, err = m.replayRequest(clientRequest)
	if err != nil {
		fmt.Println("[MITM]", "remoteResponse", "[❎]", err)
		return
	}
	defer remoteResponse.Body.Close()

	if upstreamProto != "" {
		GlobalUpstreamStats.Get(upstreamProto).TotalRequests.Add(1)
	}

	// HTTP 转发中的 403 也可能是目标站的正常响应，必须原样转发。CONNECT
	// 阶段的 403/407 由 tunnelHTTPS 中的 ProxyConnectError 单独处理。
	if remoteResponse.StatusCode == http.StatusProxyAuthRequired {
		if m.OnProxyFailure != nil {
			m.OnProxyFailure(selectedProxyURI)
		}
		err = fmt.Errorf("upstream proxy %s returned %d", selectedProxyURI, remoteResponse.StatusCode)
		return
	}

	remoteResponseDump, err = httputil.DumpResponse(remoteResponse, true)
	if err != nil {
		fmt.Println("[MITM]", "respDump", "[❎]", err)
		return
	}

	// copy response header
	copyResponseHeader(remoteResponse, clientResponse)

	// decompress gzip page
	var body []byte
	switch remoteResponse.Header.Get("Content-Encoding") {
	case "gzip":
		clientResponse.Header().Del("Content-Encoding")
		body, err = gzipDecompression(remoteResponse.Body)
	default:
		body, err = io.ReadAll(remoteResponse.Body)
	}
	if err != nil {
		fmt.Println("[MITM]", "read body", "[❎]", err)
		return
	}

	// write response code
	clientResponse.WriteHeader(remoteResponse.StatusCode)
	// write response body
	n, err := clientResponse.Write(body)
	if err != nil {
		fmt.Println("[MITM]", "connIn write", "[❎]", err)
		return
	}
	GlobalRequestStats.SuccessRequests.Add(1)
	if upstreamProto != "" {
		upstreamStats := GlobalUpstreamStats.Get(upstreamProto)
		upstreamStats.SuccessRequests.Add(1)
	}
	// BUG-FIX: 之前只统计了响应体字节数，漏掉了请求体，导致流量统计偏低
	if clientRequest.ContentLength > 0 {
		GlobalRequestStats.BytesTransferred.Add(clientRequest.ContentLength)
		if upstreamProto != "" {
			GlobalUpstreamStats.Get(upstreamProto).BytesTransferred.Add(clientRequest.ContentLength)
		}
	}
	GlobalRequestStats.BytesTransferred.Add(int64(n))
	if upstreamProto != "" {
		GlobalUpstreamStats.Get(upstreamProto).BytesTransferred.Add(int64(n))
	}
	// show http dump
	if m.Print {
		fmt.Println("[MITM]", "REQUEST-DUMP", "[📮]", string(clientRequestDump))
		fmt.Println("[MITM]", "RESPONSE-DUMP", "[📮]", string(remoteResponseDump))
	}
}

func (m *MITM) replayRequest(clientRequest *http.Request) (resp *http.Response, upstreamProtocol string, proxyURI string, err error) {
	transport := http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	}
	proxyURI, err = m.Scheduler(clientRequest)
	if err != nil {
		fmt.Println("[MITM]", "proxy scheduler", "[❎]", err)
		return
	}
	var p *url.URL
	p, err = url.Parse(proxyURI)
	if err != nil {
		fmt.Println("[MITM]", "proxy parse", "[❎]", err)
		return
	}

	upstreamProtocol = strings.ToLower(p.Scheme)

	// BUG-FIX: http.ProxyURL 只支持 HTTP/HTTPS scheme 的代理，SOCKS 代理在此路径下
	// 会被忽略导致直连。改用 x/net/proxy.FromURL 统一处理所有代理协议的拨号
	if p.Scheme == "http" || p.Scheme == "https" {
		transport.Proxy = http.ProxyURL(p)
	} else {
		dialer, dialErr := xproxy.FromURL(p, xproxy.Direct)
		if dialErr != nil {
			err = dialErr
			return
		}
		transport.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
			return dialer.Dial(network, addr)
		}
	}

	request := clientRequest.Clone(clientRequest.Context())
	request.RequestURI = ""
	cli := http.Client{
		Transport: &transport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return fmt.Errorf("")
		},
	}

	resp, err = cli.Do(request)
	return
}

func copyResponseHeader(r *http.Response, c http.ResponseWriter) {
	for k, v := range r.Header {
		var vb []byte
		for i := 0; i < len(v); i++ {
			if i == len(v)-1 {
				vb = append(vb, []byte(v[i])...)
			} else {
				vb = append(vb, []byte(v[i]+"; ")...)
			}
		}
		c.Header().Set(k, string(vb))
	}
}

func gzipDecompression(r io.Reader) ([]byte, error) {
	body := make([]byte, 0)
	var err error
	reader, _ := gzip.NewReader(r)
	var n int
	for {
		buf := make([]byte, 102400)
		n, err = reader.Read(buf)
		if err != nil && err != io.EOF {
			fmt.Println("[MITM]", "decompress gzip", "[❎]", err)
			break
		}
		if n == 0 {
			break
		}
		body = append(body, buf...)
	}
	return body, err
}
