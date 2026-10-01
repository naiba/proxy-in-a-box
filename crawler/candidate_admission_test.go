package crawler

import (
	"fmt"
	"sync"
	"testing"

	"github.com/naiba/proxyinabox"
)

func TestSourceCandidateLimitAdaptsToYield(t *testing.T) {
	previous := proxyinabox.Config.SourceFetch.MaxCandidatesPerFetch
	proxyinabox.Config.SourceFetch.MaxCandidatesPerFetch = 200
	t.Cleanup(func() { proxyinabox.Config.SourceFetch.MaxCandidatesPerFetch = previous })

	tests := []struct {
		name    string
		counter sourceCandidateCounter
		want    int
	}{
		{"learning", sourceCandidateCounter{recentCount: 99}, 200},
		{"below quarter percent", sourceCandidateCounter{recentCount: 256, recentSuccess: 0}, 25},
		{"below half percent", sourceCandidateCounter{recentCount: 256, recentSuccess: 1}, 50},
		{"below one percent", sourceCandidateCounter{recentCount: 256, recentSuccess: 2}, 100},
		{"healthy yield", sourceCandidateCounter{recentCount: 256, recentSuccess: 3}, 200},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := sourceCandidateLimit(tt.counter); got != tt.want {
				t.Fatalf("limit = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestEnqueueSourceCandidatesCapsAndDeduplicates(t *testing.T) {
	previousCache := proxyinabox.CI
	previousJobs := ValidateJobs
	previousLimit := proxyinabox.Config.SourceFetch.MaxCandidatesPerFetch
	proxyinabox.CI = &testCache{}
	ValidateJobs = make(chan proxyinabox.Proxy, 100)
	proxyinabox.Config.SourceFetch.MaxCandidatesPerFetch = 10
	queuedCandidates = sync.Map{}
	queuedCandidateCount.Store(0)
	t.Cleanup(func() {
		proxyinabox.CI = previousCache
		ValidateJobs = previousJobs
		proxyinabox.Config.SourceFetch.MaxCandidatesPerFetch = previousLimit
		queuedCandidates = sync.Map{}
		queuedCandidateCount.Store(0)
	})

	proxies := make([]proxyinabox.Proxy, 50)
	for i := range proxies {
		proxies[i] = proxyinabox.Proxy{IP: fmt.Sprintf("10.0.0.%d", i+1), Port: "8080", Protocol: "http", Source: "admission-test"}
	}
	enqueueSourceCandidates("admission-test", proxies)
	if got := len(ValidateJobs); got != 10 {
		t.Fatalf("queued candidates = %d, want 10", got)
	}
	firstBatch := make([]proxyinabox.Proxy, 0, 10)
	for len(ValidateJobs) > 0 {
		firstBatch = append(firstBatch, <-ValidateJobs)
	}
	enqueueSourceCandidates("admission-test", firstBatch)
	if got := len(ValidateJobs); got != 0 {
		t.Fatalf("duplicate candidates queued = %d, want 0", got)
	}
}

func TestSourceCandidateLimitRecoversWithRollingResults(t *testing.T) {
	previous := proxyinabox.Config.SourceFetch.MaxCandidatesPerFetch
	proxyinabox.Config.SourceFetch.MaxCandidatesPerFetch = 200
	t.Cleanup(func() { proxyinabox.Config.SourceFetch.MaxCandidatesPerFetch = previous })
	const source = "rolling-recovery-test"
	for i := 0; i < 256; i++ {
		recordSourceCandidateAttempt(source)
		recordSourceCandidateResult(source, false)
	}
	if got := sourceCandidateSnapshot(source).CurrentLimit; got != 25 {
		t.Fatalf("failed-source limit = %d, want 25", got)
	}
	for i := 0; i < 3; i++ {
		recordSourceCandidateAttempt(source)
		recordSourceCandidateResult(source, true)
	}
	stats := sourceCandidateSnapshot(source)
	if stats.RecentChecks != 256 || stats.RecentSuccess != 3 || stats.CurrentLimit != 200 {
		t.Fatalf("recovered source stats = %+v", stats)
	}
}
