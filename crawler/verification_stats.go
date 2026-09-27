package crawler

import (
	"sync"
	"sync/atomic"
	"time"
)

const recentCheckMinutes = 60

// VerificationStats counts completed health checks since this process started.
// A check can make more than one HTTP attempt when retries are configured.
type VerificationStats struct {
	RoutineSuccess   uint64 `json:"routine_success"`
	RoutineFailure   uint64 `json:"routine_failure"`
	CandidateSuccess uint64 `json:"candidate_success"`
	CandidateFailure uint64 `json:"candidate_failure"`
}

type verificationCounters struct {
	routineSuccess   atomic.Uint64
	routineFailure   atomic.Uint64
	candidateSuccess atomic.Uint64
	candidateFailure atomic.Uint64
	recentMu         sync.Mutex
	recent           [recentCheckMinutes]checkMinute
}

type checkMinute struct {
	minute int64
	counts VerificationStats
}

var checkCounters verificationCounters

func GetVerificationStats() VerificationStats {
	return checkCounters.snapshot()
}

// GetRecentVerificationStats returns checks in the current and previous 59
// calendar minutes. Like the cumulative counters, it counts checks, not retries.
func GetRecentVerificationStats() VerificationStats {
	return checkCounters.recentSnapshot(time.Now())
}

func (c *verificationCounters) recordRoutine(success bool) {
	c.recordRoutineAt(success, time.Now())
}

func (c *verificationCounters) recordRoutineAt(success bool, at time.Time) {
	if success {
		c.routineSuccess.Add(1)
	} else {
		c.routineFailure.Add(1)
	}
	c.recordRecent(at, true, success)
}

func (c *verificationCounters) recordCandidate(success bool) {
	c.recordCandidateAt(success, time.Now())
}

func (c *verificationCounters) recordCandidateAt(success bool, at time.Time) {
	if success {
		c.candidateSuccess.Add(1)
	} else {
		c.candidateFailure.Add(1)
	}
	c.recordRecent(at, false, success)
}

func (c *verificationCounters) recordRecent(at time.Time, routine, success bool) {
	minute := at.Unix() / 60
	c.recentMu.Lock()
	defer c.recentMu.Unlock()
	bucket := &c.recent[minute%recentCheckMinutes]
	if bucket.minute != minute {
		*bucket = checkMinute{minute: minute}
	}
	switch {
	case routine && success:
		bucket.counts.RoutineSuccess++
	case routine:
		bucket.counts.RoutineFailure++
	case success:
		bucket.counts.CandidateSuccess++
	default:
		bucket.counts.CandidateFailure++
	}
}

func (c *verificationCounters) recentSnapshot(now time.Time) VerificationStats {
	c.recentMu.Lock()
	defer c.recentMu.Unlock()
	minute := now.Unix() / 60
	var result VerificationStats
	for _, bucket := range c.recent {
		if bucket.minute < minute-recentCheckMinutes+1 || bucket.minute > minute {
			continue
		}
		result.RoutineSuccess += bucket.counts.RoutineSuccess
		result.RoutineFailure += bucket.counts.RoutineFailure
		result.CandidateSuccess += bucket.counts.CandidateSuccess
		result.CandidateFailure += bucket.counts.CandidateFailure
	}
	return result
}

func (c *verificationCounters) snapshot() VerificationStats {
	return VerificationStats{
		RoutineSuccess:   c.routineSuccess.Load(),
		RoutineFailure:   c.routineFailure.Load(),
		CandidateSuccess: c.candidateSuccess.Load(),
		CandidateFailure: c.candidateFailure.Load(),
	}
}
