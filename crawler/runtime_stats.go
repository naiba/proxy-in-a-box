package crawler

import (
	"errors"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

// RuntimeStats exposes the bounded worker pools and the crawler's own network
// cost. Counters are process-lifetime values, just like request statistics.
// Queue sizes are point-in-time snapshots and do not include quarantined rows
// waiting in SQLite.
type RuntimeStats struct {
	UptimeSeconds           int64            `json:"uptime_seconds"`
	CandidateWorkers        int              `json:"candidate_workers"`
	RoutineWorkers          int              `json:"routine_workers"`
	MaxConcurrent           int              `json:"max_concurrent"`
	CandidateMaxConcurrent  int              `json:"candidate_max_concurrent"`
	RoutineReserved         int              `json:"routine_reserved"`
	MaxAttemptsPerSecond    int              `json:"max_attempts_per_second"`
	AttemptBurst            int              `json:"attempt_burst"`
	CandidateActive         int64            `json:"candidate_active"`
	RoutineActive           int64            `json:"routine_active"`
	CandidateWaiting        int64            `json:"candidate_waiting_for_slot"`
	RoutineWaiting          int64            `json:"routine_waiting_for_slot"`
	CandidateQueue          int              `json:"candidate_queue"`
	CandidateQueueCap       int              `json:"candidate_queue_capacity"`
	RoutineQueue            int              `json:"routine_queue"`
	RoutineQueueCap         int              `json:"routine_queue_capacity"`
	NegativeCacheEntries    int              `json:"negative_cache_entries"`
	QueuedDedupEntries      int64            `json:"queued_dedup_entries"`
	NetworkAttempts         uint64           `json:"network_attempts"`
	NetworkResponseBytes    uint64           `json:"network_response_bytes"`
	AttemptsLastSecond      uint64           `json:"attempts_last_second"`
	AttemptsAverage60s      float64          `json:"attempts_average_60s"`
	AttemptsPeak60s         uint64           `json:"attempts_peak_60s"`
	ResponseBytesAverage60s float64          `json:"response_bytes_average_60s"`
	RateLimited             uint64           `json:"rate_limited"`
	RateLimitWaitSeconds    float64          `json:"rate_limit_wait_seconds"`
	Skipped                 RuntimeSkipStats `json:"skipped"`
	Failures                RuntimeFailStats `json:"failures"`
}

type RuntimeSkipStats struct {
	Duplicate uint64 `json:"duplicate"`
	IPLocked  uint64 `json:"ip_locked"`
	Available uint64 `json:"already_available"`
	Backoff   uint64 `json:"backoff"`
}

type RuntimeFailStats struct {
	Timeout     uint64 `json:"timeout"`
	Network     uint64 `json:"network"`
	Response    uint64 `json:"response"`
	IPMismatch  uint64 `json:"ip_mismatch"`
	TLSProbe    uint64 `json:"tls_probe"`
	Persistence uint64 `json:"persistence"`
}

type runtimeCounters struct {
	startedAt            time.Time
	candidateActive      atomic.Int64
	routineActive        atomic.Int64
	candidateWaiting     atomic.Int64
	routineWaiting       atomic.Int64
	networkAttempts      atomic.Uint64
	networkResponseBytes atomic.Uint64
	rateLimited          atomic.Uint64
	rateLimitWaitNanos   atomic.Uint64
	skipDuplicate        atomic.Uint64
	skipIPLocked         atomic.Uint64
	skipAvailable        atomic.Uint64
	skipBackoff          atomic.Uint64
	failTimeout          atomic.Uint64
	failNetwork          atomic.Uint64
	failResponse         atomic.Uint64
	failIPMismatch       atomic.Uint64
	failTLSProbe         atomic.Uint64
	failPersistence      atomic.Uint64
}

var runtimeMetrics = runtimeCounters{startedAt: time.Now()}
var recentNetworkRates networkRateWindow

type networkRateBucket struct {
	second   int64
	attempts uint64
	bytes    uint64
}

type networkRateWindow struct {
	mu      sync.Mutex
	buckets [60]networkRateBucket
}

type networkRateSnapshot struct {
	lastSecond      uint64
	average60s      float64
	peak60s         uint64
	bytesAverage60s float64
}

func (w *networkRateWindow) record(at time.Time, attempts, bytes uint64) {
	second := at.Unix()
	w.mu.Lock()
	bucket := &w.buckets[second%int64(len(w.buckets))]
	if bucket.second > second {
		w.mu.Unlock()
		return
	}
	if bucket.second != second {
		*bucket = networkRateBucket{second: second}
	}
	bucket.attempts += attempts
	bucket.bytes += bytes
	w.mu.Unlock()
}

func (w *networkRateWindow) snapshot(now time.Time, startedAt time.Time) networkRateSnapshot {
	second := now.Unix()
	windowSeconds := int64(60)
	if uptime := second - startedAt.Unix() + 1; uptime < windowSeconds {
		windowSeconds = max(1, uptime)
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	var result networkRateSnapshot
	var attempts, bytes uint64
	for _, bucket := range w.buckets {
		if bucket.second < second-59 || bucket.second > second {
			continue
		}
		attempts += bucket.attempts
		bytes += bucket.bytes
		if bucket.attempts > result.peak60s {
			result.peak60s = bucket.attempts
		}
		if bucket.second == second-1 {
			result.lastSecond = bucket.attempts
		}
	}
	result.average60s = float64(attempts) / float64(windowSeconds)
	result.bytesAverage60s = float64(bytes) / float64(windowSeconds)
	return result
}

func recordNetworkAttempt(at time.Time, validation bool) {
	runtimeMetrics.networkAttempts.Add(1)
	if validation {
		recentNetworkRates.record(at, 1, 0)
	}
}

func recordNetworkResponseBytes(at time.Time, bytes uint64, validation bool) {
	runtimeMetrics.networkResponseBytes.Add(bytes)
	if validation {
		recentNetworkRates.record(at, 0, bytes)
	}
}

func GetRuntimeStats() RuntimeStats {
	rates := recentNetworkRates.snapshot(time.Now(), runtimeMetrics.startedAt)
	rate, burst := configuredValidationRate()
	workers := configuredVerificationWorkers()
	reserved := configuredRoutineReservedWorkers(workers)
	stats := RuntimeStats{
		UptimeSeconds:           int64(time.Since(runtimeMetrics.startedAt) / time.Second),
		CandidateActive:         runtimeMetrics.candidateActive.Load(),
		RoutineActive:           runtimeMetrics.routineActive.Load(),
		CandidateWaiting:        runtimeMetrics.candidateWaiting.Load(),
		RoutineWaiting:          runtimeMetrics.routineWaiting.Load(),
		NetworkAttempts:         runtimeMetrics.networkAttempts.Load(),
		NetworkResponseBytes:    runtimeMetrics.networkResponseBytes.Load(),
		AttemptsLastSecond:      rates.lastSecond,
		AttemptsAverage60s:      rates.average60s,
		AttemptsPeak60s:         rates.peak60s,
		ResponseBytesAverage60s: rates.bytesAverage60s,
		RateLimited:             runtimeMetrics.rateLimited.Load(),
		RateLimitWaitSeconds:    float64(runtimeMetrics.rateLimitWaitNanos.Load()) / float64(time.Second),
		MaxAttemptsPerSecond:    rate,
		AttemptBurst:            burst,
		RoutineReserved:         reserved,
		NegativeCacheEntries:    candidateFailures.size(),
		QueuedDedupEntries:      queuedCandidateCount.Load(),
		Skipped: RuntimeSkipStats{
			Duplicate: runtimeMetrics.skipDuplicate.Load(),
			IPLocked:  runtimeMetrics.skipIPLocked.Load(),
			Available: runtimeMetrics.skipAvailable.Load(),
			Backoff:   runtimeMetrics.skipBackoff.Load(),
		},
		Failures: RuntimeFailStats{
			Timeout:     runtimeMetrics.failTimeout.Load(),
			Network:     runtimeMetrics.failNetwork.Load(),
			Response:    runtimeMetrics.failResponse.Load(),
			IPMismatch:  runtimeMetrics.failIPMismatch.Load(),
			TLSProbe:    runtimeMetrics.failTLSProbe.Load(),
			Persistence: runtimeMetrics.failPersistence.Load(),
		},
	}
	if ValidateJobs != nil {
		stats.CandidateQueue = len(ValidateJobs)
		stats.CandidateQueueCap = cap(ValidateJobs)
	}
	if verifyJob != nil {
		stats.RoutineQueue = len(verifyJob)
		stats.RoutineQueueCap = cap(verifyJob)
	}
	stats.CandidateWorkers = stats.CandidateQueueCap / 2
	stats.RoutineWorkers = stats.RoutineQueueCap
	if validationSlots != nil {
		stats.MaxConcurrent = cap(validationSlots)
	}
	if candidateSlots != nil {
		stats.CandidateMaxConcurrent = cap(candidateSlots)
	}
	return stats
}

func recordNetworkFailure(err error) {
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		runtimeMetrics.failTimeout.Add(1)
		return
	}
	runtimeMetrics.failNetwork.Add(1)
}
