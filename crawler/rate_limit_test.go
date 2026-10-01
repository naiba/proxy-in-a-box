package crawler

import (
	"testing"
	"time"

	"github.com/naiba/proxyinabox"
)

func TestAttemptRateLimiterReservesOrderedTokens(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	limiter := newAttemptRateLimiter(10, 2, now)
	if wait := limiter.Reserve(now); wait != 0 {
		t.Fatalf("first burst token wait = %s", wait)
	}
	if wait := limiter.Reserve(now); wait != 0 {
		t.Fatalf("second burst token wait = %s", wait)
	}
	if wait := limiter.Reserve(now); wait != 100*time.Millisecond {
		t.Fatalf("third token wait = %s, want 100ms", wait)
	}
	if wait := limiter.Reserve(now); wait != 200*time.Millisecond {
		t.Fatalf("fourth token wait = %s, want 200ms", wait)
	}
}

func TestAttemptRateLimiterSpreadsTenThousandImmediateAttempts(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	limiter := newAttemptRateLimiter(50, 20, now)
	var finalWait time.Duration
	for i := 0; i < 10_000; i++ {
		finalWait = limiter.Reserve(now)
	}
	if want := 199*time.Second + 600*time.Millisecond; finalWait != want {
		t.Fatalf("final reservation wait = %s, want %s", finalWait, want)
	}
}

func TestNetworkRateWindowTracksLastAverageAndPeak(t *testing.T) {
	now := time.Unix(1_800_000_060, 0)
	var window networkRateWindow
	window.record(now.Add(-2*time.Second), 3, 300)
	window.record(now.Add(-time.Second), 7, 700)
	window.record(now, 5, 500)
	window.record(now.Add(-61*time.Second), 1000, 1000)

	got := window.snapshot(now, now.Add(-time.Hour))
	if got.lastSecond != 7 || got.peak60s != 7 {
		t.Fatalf("rate snapshot = %+v, want last=7 peak=7", got)
	}
	if got.average60s != 0.25 || got.bytesAverage60s != 25 {
		t.Fatalf("rate averages = %+v, want 0.25 attempts/s and 25 bytes/s", got)
	}
}

func TestConfiguredValidationProtectionDefaultsAndBounds(t *testing.T) {
	previous := proxyinabox.Config.Verification
	t.Cleanup(func() { proxyinabox.Config.Verification = previous })

	proxyinabox.Config.Verification.MaxAttemptsPerSecond = 0
	proxyinabox.Config.Verification.AttemptBurst = 0
	if rate, burst := configuredValidationRate(); rate != 50 || burst != 20 {
		t.Fatalf("default rate/burst = %d/%d, want 50/20", rate, burst)
	}
	proxyinabox.Config.Verification.MaxAttemptsPerSecond = 50_000
	proxyinabox.Config.Verification.AttemptBurst = 50_000
	if rate, burst := configuredValidationRate(); rate != 1000 || burst != 1000 {
		t.Fatalf("bounded rate/burst = %d/%d, want 1000/1000", rate, burst)
	}
	proxyinabox.Config.Verification.RoutineReservedWorkers = 0
	if got := configuredRoutineReservedWorkers(20); got != 4 {
		t.Fatalf("default routine reserve = %d, want 4", got)
	}
	proxyinabox.Config.Verification.RoutineReservedWorkers = 20
	if got := configuredRoutineReservedWorkers(20); got != 19 {
		t.Fatalf("bounded routine reserve = %d, want 19", got)
	}
}
