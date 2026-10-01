package crawler

import (
	"sync"
	"time"
)

// attemptRateLimiter is a process-wide token bucket for outbound validation
// attempts. Concurrency bounds open sockets; this independently bounds request
// rate when bad endpoints fail immediately.
type attemptRateLimiter struct {
	mu     sync.Mutex
	rate   float64
	burst  float64
	tokens float64
	last   time.Time
}

func newAttemptRateLimiter(rate, burst int, now time.Time) *attemptRateLimiter {
	return &attemptRateLimiter{
		rate:   float64(rate),
		burst:  float64(burst),
		tokens: float64(burst),
		last:   now,
	}
}

// Reserve consumes a token and returns how long the caller must wait before
// using it. Reservations are ordered, so simultaneous callers cannot all wake
// for the same token.
func (l *attemptRateLimiter) Reserve(now time.Time) time.Duration {
	if l == nil || l.rate <= 0 {
		return 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()

	base := now
	if now.Before(l.last) {
		base = l.last
	} else {
		l.tokens = min(l.burst, l.tokens+now.Sub(l.last).Seconds()*l.rate)
		l.last = now
	}
	if l.tokens >= 1 {
		l.tokens--
		return 0
	}

	wait := time.Duration((1-l.tokens)/l.rate*float64(time.Second) + 0.5)
	l.tokens = 0
	l.last = base.Add(wait)
	return l.last.Sub(now)
}

func (l *attemptRateLimiter) Wait() time.Duration {
	wait := l.Reserve(time.Now())
	if wait > 0 {
		timer := time.NewTimer(wait)
		<-timer.C
	}
	return wait
}

func waitForValidationAttempt() {
	if validationAttemptLimiter != nil {
		if waited := validationAttemptLimiter.Wait(); waited > 0 {
			runtimeMetrics.rateLimited.Add(1)
			runtimeMetrics.rateLimitWaitNanos.Add(uint64(waited))
		}
	}
	recordNetworkAttempt(time.Now(), true)
}
