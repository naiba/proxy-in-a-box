package crawler

import "sync/atomic"

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
}

var checkCounters verificationCounters

func GetVerificationStats() VerificationStats {
	return checkCounters.snapshot()
}

func (c *verificationCounters) snapshot() VerificationStats {
	return VerificationStats{
		RoutineSuccess:   c.routineSuccess.Load(),
		RoutineFailure:   c.routineFailure.Load(),
		CandidateSuccess: c.candidateSuccess.Load(),
		CandidateFailure: c.candidateFailure.Load(),
	}
}
