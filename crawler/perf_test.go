package crawler

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func BenchmarkGetRuntimeStats(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = GetRuntimeStats()
	}
}

func BenchmarkNetworkRateWindowRecord(b *testing.B) {
	var window networkRateWindow
	now := time.Now()
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			window.record(now, 1, 0)
		}
	})
}

func BenchmarkAttemptRateLimiterReserve(b *testing.B) {
	now := time.Now()
	limiter := newAttemptRateLimiter(1_000_000_000, 1_000_000_000, now)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = limiter.Reserve(now)
	}
}

func BenchmarkSourceCandidateLimit(b *testing.B) {
	counter := sourceCandidateCounter{checks: 10_000, success: 20}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = sourceCandidateLimit(counter)
	}
}

func BenchmarkParseTextResponseTenThousand(b *testing.B) {
	var input strings.Builder
	for i := 0; i < 10_000; i++ {
		fmt.Fprintf(&input, "10.%d.%d.%d:%d\n", (i>>16)&255, (i>>8)&255, i&255, 1000+i%50_000)
	}
	body := input.String()
	source := Source{Name: "benchmark", Protocol: "http"}
	b.ReportAllocs()
	b.SetBytes(int64(len(body)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		proxies := parseTextResponse(body, source)
		if len(proxies) != 10_000 {
			b.Fatalf("parsed %d proxies", len(proxies))
		}
	}
}

func BenchmarkBoundedSourceDownload(b *testing.B) {
	payload := []byte(strings.Repeat("x", 64*1024))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(payload)
	}))
	b.Cleanup(server.Close)
	b.ReportAllocs()
	b.SetBytes(int64(len(payload)))
	for i := 0; i < b.N; i++ {
		body, err := getURLThroughProxyWithRetryLimit(server.URL, time.Second, "", 1, int64(len(payload)))
		if err != nil || len(body) != len(payload) {
			b.Fatalf("download = %d bytes, %v", len(body), err)
		}
	}
}
