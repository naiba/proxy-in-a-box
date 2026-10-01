package crawler

import (
	"errors"
	"net"
	"sync/atomic"
	"time"
)

// RuntimeStats exposes the bounded worker pools and the crawler's own network
// cost. Counters are process-lifetime values, just like request statistics.
// Queue sizes are point-in-time snapshots and do not include quarantined rows
// waiting in SQLite.
type RuntimeStats struct {
	UptimeSeconds        int64            `json:"uptime_seconds"`
	CandidateWorkers     int              `json:"candidate_workers"`
	RoutineWorkers       int              `json:"routine_workers"`
	MaxConcurrent        int              `json:"max_concurrent"`
	CandidateActive      int64            `json:"candidate_active"`
	RoutineActive        int64            `json:"routine_active"`
	CandidateWaiting     int64            `json:"candidate_waiting_for_slot"`
	RoutineWaiting       int64            `json:"routine_waiting_for_slot"`
	CandidateQueue       int              `json:"candidate_queue"`
	CandidateQueueCap    int              `json:"candidate_queue_capacity"`
	RoutineQueue         int              `json:"routine_queue"`
	RoutineQueueCap      int              `json:"routine_queue_capacity"`
	NetworkAttempts      uint64           `json:"network_attempts"`
	NetworkResponseBytes uint64           `json:"network_response_bytes"`
	Skipped              RuntimeSkipStats `json:"skipped"`
	Failures             RuntimeFailStats `json:"failures"`
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

func GetRuntimeStats() RuntimeStats {
	stats := RuntimeStats{
		UptimeSeconds:        int64(time.Since(runtimeMetrics.startedAt) / time.Second),
		CandidateActive:      runtimeMetrics.candidateActive.Load(),
		RoutineActive:        runtimeMetrics.routineActive.Load(),
		CandidateWaiting:     runtimeMetrics.candidateWaiting.Load(),
		RoutineWaiting:       runtimeMetrics.routineWaiting.Load(),
		NetworkAttempts:      runtimeMetrics.networkAttempts.Load(),
		NetworkResponseBytes: runtimeMetrics.networkResponseBytes.Load(),
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
