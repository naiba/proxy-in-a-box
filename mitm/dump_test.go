package mitm

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
)

func TestDump_GzipResponsePreservesBodyAndHeaders(t *testing.T) {
	var compressed bytes.Buffer
	zw := gzip.NewWriter(&compressed)
	if _, err := zw.Write([]byte("small response")); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Encoding", "gzip")
		w.Header().Add("Set-Cookie", "a=1; Path=/")
		w.Header().Add("Set-Cookie", "b=2; Path=/")
		_, _ = w.Write(compressed.Bytes())
	}))
	defer proxy.Close()

	m := &MITM{Scheduler: func(*http.Request) (string, error) { return proxy.URL, nil }}
	request := httptest.NewRequest(http.MethodGet, "http://example.com/", nil)
	request.Header.Set("Accept-Encoding", "gzip")
	recorder := httptest.NewRecorder()
	m.Dump(recorder, request)
	response := recorder.Result()
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body = %q", response.StatusCode, recorder.Body.String())
	}
	if body := recorder.Body.String(); body != "small response" {
		t.Errorf("body = %q", body)
	}
	if encoding := response.Header.Get("Content-Encoding"); encoding != "" {
		t.Errorf("Content-Encoding = %q, want none after decompression", encoding)
	}
	if length := response.Header.Get("Content-Length"); length != "" && length != "14" {
		t.Errorf("Content-Length = %q, want uncompressed length or unset", length)
	}
	if cookies := response.Header.Values("Set-Cookie"); len(cookies) != 2 || cookies[0] != "a=1; Path=/" || cookies[1] != "b=2; Path=/" {
		t.Errorf("Set-Cookie = %q, want two separate headers", cookies)
	}
}

func TestGzipDecompressionRejectsInvalidData(t *testing.T) {
	if _, err := gzipDecompression(strings.NewReader("not gzip")); err == nil {
		t.Fatal("invalid gzip was accepted")
	}
}

func TestForwardHTTPSRejectsUntrustedCertificate(t *testing.T) {
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("secret"))
	}))
	defer target.Close()
	proxy := newConnectProxy(t)
	defer proxy.Close()
	proxyURL, err := url.Parse(proxy.URL)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, target.URL, nil)
	m := &MITM{}
	response, err := m.doRequestThroughProxy(request, proxyURL)
	if response != nil {
		response.Body.Close()
	}
	if err == nil || !strings.Contains(err.Error(), "certificate") {
		t.Fatalf("expected certificate verification failure, got %v", err)
	}
}

func TestDump_ForwardsTargetForbiddenWithoutProxyFailure(t *testing.T) {
	proxyErrors := make(chan error, 1)
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte("target denied request"))
	}))
	defer target.Close()

	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		targetURL, err := url.Parse(target.URL)
		if err != nil {
			proxyErrors <- err
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		forwarded := r.Clone(r.Context())
		forwarded.URL.Scheme = targetURL.Scheme
		forwarded.URL.Host = targetURL.Host
		forwarded.Host = targetURL.Host
		forwarded.RequestURI = ""
		response, err := http.DefaultTransport.RoundTrip(forwarded)
		if err != nil {
			proxyErrors <- err
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer response.Body.Close()
		for key, values := range response.Header {
			for _, value := range values {
				w.Header().Add(key, value)
			}
		}
		w.WriteHeader(response.StatusCode)
		_, _ = io.Copy(w, response.Body)
	}))
	defer proxy.Close()

	var failures atomic.Int32
	m := &MITM{
		Scheduler:      func(*http.Request) (string, error) { return proxy.URL, nil },
		OnProxyFailure: func(string) { failures.Add(1) },
	}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, target.URL, nil)

	m.Dump(recorder, request)
	select {
	case err := <-proxyErrors:
		t.Fatalf("proxy handler error: %v", err)
	default:
	}

	if recorder.Code != http.StatusForbidden {
		t.Errorf("status = %d, want %d", recorder.Code, http.StatusForbidden)
	}
	if recorder.Body.String() != "target denied request" {
		t.Errorf("body = %q, want target response", recorder.Body.String())
	}
	if got := failures.Load(); got != 0 {
		t.Errorf("OnProxyFailure calls = %d, want 0 for a target 403", got)
	}
}

func TestDump_StillTreatsProxyAuthRequiredAsProxyFailure(t *testing.T) {
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusProxyAuthRequired)
	}))
	defer proxy.Close()

	var failures atomic.Int32
	m := &MITM{
		Scheduler:      func(*http.Request) (string, error) { return proxy.URL, nil },
		OnProxyFailure: func(string) { failures.Add(1) },
	}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "http://target.test/resource", nil)

	m.Dump(recorder, request)

	if recorder.Code != http.StatusBadGateway {
		t.Errorf("status = %d, want %d", recorder.Code, http.StatusBadGateway)
	}
	if got := failures.Load(); got != 1 {
		t.Errorf("OnProxyFailure calls = %d, want 1 for a proxy 407", got)
	}
}

func TestTunnelHTTPS_ProxyConnectErrorsTriggerProxyFailure(t *testing.T) {
	for _, status := range []int{http.StatusForbidden, http.StatusProxyAuthRequired} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodConnect {
					t.Errorf("method = %s, want CONNECT", r.Method)
				}
				w.WriteHeader(status)
			}))
			defer proxy.Close()

			var failures atomic.Int32
			m := &MITM{
				Scheduler:      func(*http.Request) (string, error) { return proxy.URL, nil },
				OnProxyFailure: func(string) { failures.Add(1) },
			}
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodConnect, "https://target.test:443", nil)

			m.tunnelHTTPS(recorder, request)

			if recorder.Code != http.StatusBadGateway {
				t.Errorf("status = %d, want %d", recorder.Code, http.StatusBadGateway)
			}
			if got := failures.Load(); got != 1 {
				t.Errorf("OnProxyFailure calls = %d, want 1 for CONNECT %d", got, status)
			}
		})
	}
}
