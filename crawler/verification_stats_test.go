package crawler

import (
	"sync"
	"testing"
	"time"
)

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

func TestRecentVerificationStatsConcurrentUpdates(t *testing.T) {
	var counters verificationCounters
	now := time.Now()
	var workers sync.WaitGroup
	for range 8 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for range 100 {
				counters.recordRoutineAt(false, now)
			}
		}()
	}
	workers.Wait()
	if got := counters.recentSnapshot(now).RoutineFailure; got != 800 {
		t.Fatalf("recent failures = %d, want 800", got)
	}
	if got := counters.snapshot().RoutineFailure; got != 800 {
		t.Fatalf("cumulative failures = %d, want 800", got)
	}
}

func TestRecentVerificationStatsExpiresAndSeparatesChecks(t *testing.T) {
	var counters verificationCounters
	now := time.Unix(1_800_000_000, 0).Truncate(time.Minute)
	counters.recordRoutineAt(true, now.Add(-60*time.Minute))
	counters.recordRoutineAt(false, now.Add(-59*time.Minute))
	counters.recordCandidateAt(true, now.Add(-5*time.Minute))
	counters.recordCandidateAt(false, now)

	recent := counters.recentSnapshot(now)
	if recent.RoutineSuccess != 0 || recent.RoutineFailure != 1 ||
		recent.CandidateSuccess != 1 || recent.CandidateFailure != 1 {
		t.Fatalf("last 60 minutes = %+v", recent)
	}
	if got := counters.snapshot(); got.RoutineSuccess != 1 {
		t.Fatalf("cumulative success was lost: %+v", got)
	}
	recent = counters.recentSnapshot(now.Add(60 * time.Minute))
	if recent.RoutineSuccess != 0 || recent.RoutineFailure != 0 ||
		recent.CandidateSuccess != 0 || recent.CandidateFailure != 0 {
		t.Fatalf("expired checks still counted: %+v", recent)
	}
}
