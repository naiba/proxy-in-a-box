package crawler

import (
	"math/rand/v2"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/naiba/proxyinabox"
)

const defaultMaxCandidatesPerFetch = 200

type SourceCandidateStats struct {
	Checks        uint64  `json:"candidate_checks"`
	Success       uint64  `json:"candidate_success"`
	Failure       uint64  `json:"candidate_failure"`
	Yield         float64 `json:"candidate_yield"`
	RecentChecks  uint64  `json:"candidate_recent_checks"`
	RecentSuccess uint64  `json:"candidate_recent_success"`
	RecentYield   float64 `json:"candidate_recent_yield"`
	CurrentLimit  int     `json:"candidate_limit"`
	Admitted      uint64  `json:"candidate_admitted"`
	Capped        uint64  `json:"candidate_capped"`
	Filtered      uint64  `json:"candidate_filtered"`
	Duplicate     uint64  `json:"candidate_duplicate"`
}

type sourceCandidateCounter struct {
	checks         uint64
	success        uint64
	failure        uint64
	admitted       uint64
	capped         uint64
	filtered       uint64
	duplicate      uint64
	recentOutcomes [256]bool
	recentCount    int
	recentSuccess  int
	recentIndex    int
}

var sourceCandidateCounters = struct {
	sync.RWMutex
	bySource map[string]*sourceCandidateCounter
}{bySource: make(map[string]*sourceCandidateCounter)}

var queuedCandidates sync.Map
var queuedCandidateCount atomic.Int64

func configuredMaxCandidatesPerFetch() int {
	limit := proxyinabox.Config.SourceFetch.MaxCandidatesPerFetch
	if limit <= 0 {
		return defaultMaxCandidatesPerFetch
	}
	if limit > 10_000 {
		return 10_000
	}
	return limit
}

func sourceCandidateLimit(counter sourceCandidateCounter) int {
	limit := configuredMaxCandidatesPerFetch()
	if counter.recentCount < 100 {
		return limit
	}
	// Once a source has enough samples, spend less validation traffic on very
	// low-yield feeds while continuing to sample them for recovery.
	switch {
	case counter.recentSuccess*400 < counter.recentCount: // below 0.25%
		return max(1, limit/8)
	case counter.recentSuccess*200 < counter.recentCount: // below 0.5%
		return max(1, limit/4)
	case counter.recentSuccess*100 < counter.recentCount: // below 1%
		return max(1, limit/2)
	default:
		return limit
	}
}

func sourceCandidateSnapshot(source string) SourceCandidateStats {
	sourceCandidateCounters.RLock()
	counter := sourceCandidateCounters.bySource[source]
	var copy sourceCandidateCounter
	if counter != nil {
		copy = *counter
	}
	sourceCandidateCounters.RUnlock()
	yield := 0.0
	if copy.checks > 0 {
		yield = float64(copy.success) / float64(copy.checks)
	}
	recentYield := 0.0
	if copy.recentCount > 0 {
		recentYield = float64(copy.recentSuccess) / float64(copy.recentCount)
	}
	return SourceCandidateStats{
		Checks: copy.checks, Success: copy.success, Failure: copy.failure,
		Yield: yield, RecentChecks: uint64(copy.recentCount),
		RecentSuccess: uint64(copy.recentSuccess), RecentYield: recentYield,
		CurrentLimit: sourceCandidateLimit(copy),
		Admitted:     copy.admitted, Capped: copy.capped,
		Filtered: copy.filtered, Duplicate: copy.duplicate,
	}
}

func updateSourceCandidateCounter(source string, update func(*sourceCandidateCounter)) {
	if source == "" {
		source = "unknown"
	}
	sourceCandidateCounters.Lock()
	counter := sourceCandidateCounters.bySource[source]
	if counter == nil {
		counter = &sourceCandidateCounter{}
		sourceCandidateCounters.bySource[source] = counter
	}
	update(counter)
	sourceCandidateCounters.Unlock()
}

func recordSourceCandidateAttempt(source string) {
	updateSourceCandidateCounter(source, func(counter *sourceCandidateCounter) { counter.checks++ })
}

func recordSourceCandidateResult(source string, success bool) {
	updateSourceCandidateCounter(source, func(counter *sourceCandidateCounter) {
		if success {
			counter.success++
		} else {
			counter.failure++
		}
		if counter.recentCount == len(counter.recentOutcomes) {
			if counter.recentOutcomes[counter.recentIndex] {
				counter.recentSuccess--
			}
		} else {
			counter.recentCount++
		}
		counter.recentOutcomes[counter.recentIndex] = success
		if success {
			counter.recentSuccess++
		}
		counter.recentIndex = (counter.recentIndex + 1) % len(counter.recentOutcomes)
	})
}

func enqueueSourceCandidates(source string, proxies []proxyinabox.Proxy) {
	limit := sourceCandidateSnapshot(source).CurrentLimit
	rand.Shuffle(len(proxies), func(i, j int) { proxies[i], proxies[j] = proxies[j], proxies[i] })
	admitted := 0
	for i := range proxies {
		if admitted >= limit {
			remaining := uint64(len(proxies) - i)
			updateSourceCandidateCounter(source, func(counter *sourceCandidateCounter) { counter.capped += remaining })
			break
		}
		p := proxies[i]
		if p.Source == "" {
			p.Source = source
		}
		p.IP = strings.TrimSpace(p.IP)
		proxyURI := p.URI()
		if proxyinabox.CI.IsIPLocked(p.IP) {
			updateSourceCandidateCounter(source, func(counter *sourceCandidateCounter) { counter.filtered++ })
			continue
		}
		if proxyinabox.CI.HasProxy(proxyURI) {
			candidateFailures.clear(proxyURI)
			updateSourceCandidateCounter(source, func(counter *sourceCandidateCounter) { counter.filtered++ })
			continue
		}
		if !proxyinabox.CI.IsProxyValidationDue(proxyURI) || !candidateFailures.isDue(proxyURI, time.Now()) {
			updateSourceCandidateCounter(source, func(counter *sourceCandidateCounter) { counter.filtered++ })
			continue
		}
		if _, loaded := queuedCandidates.LoadOrStore(proxyURI, struct{}{}); loaded {
			runtimeMetrics.skipDuplicate.Add(1)
			updateSourceCandidateCounter(source, func(counter *sourceCandidateCounter) { counter.duplicate++ })
			continue
		}
		queuedCandidateCount.Add(1)
		ValidateJobs <- p
		admitted++
		updateSourceCandidateCounter(source, func(counter *sourceCandidateCounter) { counter.admitted++ })
	}
}
