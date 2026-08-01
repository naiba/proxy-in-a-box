package mitm

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
)

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
