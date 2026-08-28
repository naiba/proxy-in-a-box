package crawler

import (
	"sync"
	"time"

	"github.com/naiba/proxyinabox"
)

const (
	defaultDeepCheckInterval       = 24 * time.Hour
	defaultHealthCheckRetries      = 2
	defaultHealthResponseBodyLimit = int64(16 * 1024)
	candidateFailureRetention      = 7 * 24 * time.Hour
)

var candidateRetryBackoff = [...]time.Duration{
	30 * time.Minute,
	2 * time.Hour,
	6 * time.Hour,
	24 * time.Hour,
}

func configuredDeepCheckInterval() time.Duration {
	if proxyinabox.Config.Verification.DeepCheckInterval > 0 {
		return proxyinabox.Config.Verification.DeepCheckInterval
	}
	return defaultDeepCheckInterval
}

func configuredHealthCheckRetries() int {
	retries := proxyinabox.Config.Verification.Retries
	if retries <= 0 {
		return defaultHealthCheckRetries
	}
	if retries > 5 {
		return 5
	}
	return retries
}

func configuredHealthResponseBodyLimit() int64 {
	if proxyinabox.Config.Verification.ResponseBodyLimit > 0 {
		return proxyinabox.Config.Verification.ResponseBodyLimit
	}
	return defaultHealthResponseBodyLimit
}

func needsDeepCheck(p proxyinabox.Proxy, now time.Time) bool {
	return !p.Available || p.LastDeepVerify.IsZero() || now.Sub(p.LastDeepVerify) >= configuredDeepCheckInterval()
}

type candidateFailure struct {
	failures    int
	retryAt     time.Time
	lastFailure time.Time
}

type candidateFailureCache struct {
	mu      sync.Mutex
	entries map[string]candidateFailure
}

func newCandidateFailureCache() *candidateFailureCache {
	return &candidateFailureCache{entries: make(map[string]candidateFailure)}
}

func (c *candidateFailureCache) isDue(proxyURI string, now time.Time) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[proxyURI]
	return !ok || !now.Before(entry.retryAt)
}

// recordFailure advances an endpoint through 30m, 2h, 6h and 24h retry
// windows. minimumFailures aligns source validation with a persisted DB retry
// count when a periodic health check failed first.
func (c *candidateFailureCache) recordFailure(proxyURI string, minimumFailures int, now time.Time) time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()

	entry := c.entries[proxyURI]
	entry.failures++
	if entry.failures < minimumFailures {
		entry.failures = minimumFailures
	}
	backoffIndex := entry.failures - 1
	if backoffIndex < 0 {
		backoffIndex = 0
	}
	if backoffIndex >= len(candidateRetryBackoff) {
		backoffIndex = len(candidateRetryBackoff) - 1
	}
	entry.lastFailure = now
	entry.retryAt = now.Add(candidateRetryBackoff[backoffIndex])
	c.entries[proxyURI] = entry
	return entry.retryAt
}

func (c *candidateFailureCache) clear(proxyURI string) {
	c.mu.Lock()
	delete(c.entries, proxyURI)
	c.mu.Unlock()
}

func (c *candidateFailureCache) prune(now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for proxyURI, entry := range c.entries {
		if now.Sub(entry.lastFailure) >= candidateFailureRetention {
			delete(c.entries, proxyURI)
		}
	}
}

var candidateFailures = newCandidateFailureCache()
