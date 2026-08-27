package mitm

import (
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func sequenceScheduler(proxyURIs ...string) (func(*http.Request) (string, error), *atomic.Int32) {
	var calls atomic.Int32
	return func(*http.Request) (string, error) {
		index := int(calls.Add(1)) - 1
		if index >= len(proxyURIs) {
			return "", errors.New("no more test proxies")
		}
		return proxyURIs[index], nil
	}, &calls
}

func newForwardProxy(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		forwarded := r.Clone(r.Context())
		forwarded.RequestURI = ""
		response, err := http.DefaultTransport.RoundTrip(forwarded)
		if err != nil {
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
}

func newConnectProxy(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodConnect {
			http.Error(w, "CONNECT required", http.StatusMethodNotAllowed)
			return
		}
		remote, err := net.DialTimeout("tcp", r.Host, time.Second)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		client, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			remote.Close()
			return
		}
		_, _ = client.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n"))
		go func() {
			_, _ = io.Copy(remote, client)
			remote.Close()
		}()
		go func() {
			_, _ = io.Copy(client, remote)
			client.Close()
		}()
	}))
}

func TestDump_RetriesSafeRequestWithDifferentProxy(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("target reached"))
	}))
	defer target.Close()

	badProxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusProxyAuthRequired)
	}))
	defer badProxy.Close()
	goodProxy := newForwardProxy(t)
	defer goodProxy.Close()

	scheduler, calls := sequenceScheduler(badProxy.URL, goodProxy.URL)
	var proxyFailures atomic.Int32
	var targetFailures atomic.Int32
	m := &MITM{
		Scheduler:           scheduler,
		MaxUpstreamAttempts: 2,
		OnProxyFailure: func(string) {
			proxyFailures.Add(1)
		},
		OnProxyTargetFailure: func(string, string) {
			targetFailures.Add(1)
		},
	}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, target.URL, nil)

	m.Dump(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%q", recorder.Code, recorder.Body.String())
	}
	if recorder.Body.String() != "target reached" {
		t.Errorf("body = %q, want target response", recorder.Body.String())
	}
	if got := calls.Load(); got != 2 {
		t.Errorf("scheduler calls = %d, want 2", got)
	}
	if got := proxyFailures.Load(); got != 1 {
		t.Errorf("proxy failure callbacks = %d, want 1", got)
	}
	if got := targetFailures.Load(); got != 1 {
		t.Errorf("target failure callbacks = %d, want 1", got)
	}
}

func TestDump_DoesNotRetryRequestWithBody(t *testing.T) {
	badProxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusProxyAuthRequired)
	}))
	defer badProxy.Close()
	unusedProxy := newForwardProxy(t)
	defer unusedProxy.Close()

	scheduler, calls := sequenceScheduler(badProxy.URL, unusedProxy.URL)
	m := &MITM{Scheduler: scheduler, MaxUpstreamAttempts: 2}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "http://target.test/resource", io.NopCloser(&zeroReader{}))

	m.Dump(recorder, request)

	if recorder.Code != http.StatusBadGateway {
		t.Errorf("status = %d, want 502", recorder.Code)
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("scheduler calls = %d, want 1 for a non-replayable request", got)
	}
}

type zeroReader struct{}

func (*zeroReader) Read([]byte) (int, error) { return 0, io.EOF }

func TestTunnelHTTPS_TimesOutAndRetriesDifferentProxy(t *testing.T) {
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("secure target reached"))
	}))
	defer target.Close()

	var silentHits atomic.Int32
	silentProxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		silentHits.Add(1)
		<-r.Context().Done()
	}))
	defer silentProxy.Close()
	goodProxy := newConnectProxy(t)
	defer goodProxy.Close()

	scheduler, calls := sequenceScheduler(silentProxy.URL, goodProxy.URL)
	var failureMu sync.Mutex
	var failedTargets []string
	m := &MITM{
		Scheduler:                scheduler,
		Filter:                   func(*http.Request) error { return nil },
		MaxUpstreamAttempts:      2,
		UpstreamConnectTimeout:   100 * time.Millisecond,
		UpstreamHandshakeTimeout: 100 * time.Millisecond,
		OnProxyTargetFailure: func(_ string, target string) {
			failureMu.Lock()
			failedTargets = append(failedTargets, target)
			failureMu.Unlock()
		},
	}
	frontProxy := httptest.NewServer(http.HandlerFunc(m.serve))
	defer frontProxy.Close()
	frontURL, err := url.Parse(frontProxy.URL)
	if err != nil {
		t.Fatalf("parse front proxy URL: %v", err)
	}
	transport := &http.Transport{
		Proxy:           http.ProxyURL(frontURL),
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 2 * time.Second}

	started := time.Now()
	response, err := client.Get(target.URL)
	if err != nil {
		t.Fatalf("GET through retrying proxy: %v", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	if response.StatusCode != http.StatusOK || string(body) != "secure target reached" {
		t.Fatalf("response = %d %q, want 200 target body", response.StatusCode, body)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Errorf("retry took %s, want bounded failure and fast failover", elapsed)
	}
	if got := silentHits.Load(); got != 1 {
		t.Errorf("silent proxy hits = %d, want 1", got)
	}
	if got := calls.Load(); got != 2 {
		t.Errorf("scheduler calls = %d, want 2", got)
	}
	failureMu.Lock()
	defer failureMu.Unlock()
	if len(failedTargets) != 1 || failedTargets[0] == "" {
		t.Errorf("target failure callbacks = %v, want one target", failedTargets)
	}
}
