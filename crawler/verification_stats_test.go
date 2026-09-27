package crawler

import "testing"

func TestVerificationCountersSnapshot(t *testing.T) {
	var counters verificationCounters
	counters.routineSuccess.Add(2)
	counters.routineFailure.Add(1)
	counters.candidateSuccess.Add(3)
	counters.candidateFailure.Add(4)
	got := counters.snapshot()
	if got.RoutineSuccess != 2 || got.RoutineFailure != 1 ||
		got.CandidateSuccess != 3 || got.CandidateFailure != 4 {
		t.Fatalf("verification stats = %+v", got)
	}
}
