package crawler

import (
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/naiba/proxyinabox"
)

type testCache struct {
	hasProxy     bool
	locked       bool
	hasProxyHit  chan struct{}
	lockedHit    chan struct{}
	hasProxyOnce sync.Once
	lockedOnce   sync.Once
}

func (c *testCache) RandomProxy() (string, bool)                                 { return "", false }
func (c *testCache) GetProxy() (string, bool)                                    { return "", false }
func (c *testCache) ProxyLength() int                                            { return 0 }
func (c *testCache) PickProxy(*http.Request) (string, error)                     { return "", nil }
func (c *testCache) IsProxyValidationDue(string) bool                            { return true }
func (c *testCache) GetAllProxies() []proxyinabox.Proxy                          { return nil }
func (c *testCache) UpsertProxy(proxyinabox.Proxy) error                         { return nil }
func (c *testCache) MarkVerifySuccess(proxyinabox.Proxy, int64, time.Time, bool) {}
func (c *testCache) MarkVerifyFailed(proxyinabox.Proxy) int                      { return 0 }
func (c *testCache) MarkProxyUnavailable(string)                                 {}
func (c *testCache) MarkProxyTargetFailure(string, string)                       {}
func (c *testCache) RecordFailure(string) bool                                   { return false }
func (c *testCache) LoadLockedIPs()                                              {}
func (c *testCache) CleanupStaleProxies(time.Duration)                           {}

func (c *testCache) HasProxy(string) bool {
	if c.hasProxyHit != nil {
		c.hasProxyOnce.Do(func() { close(c.hasProxyHit) })
	}
	return c.hasProxy
}

func (c *testCache) IsIPLocked(string) bool {
	if c.lockedHit != nil {
		c.lockedOnce.Do(func() { close(c.lockedHit) })
	}
	return c.locked
}

type staticProxyService struct {
	proxies         []proxyinabox.Proxy
	err             error
	stillUnverified bool
	refreshErr      error
}

func (s staticProxyService) GetUnVerified() ([]proxyinabox.Proxy, error) {
	return s.proxies, s.err
}

func (s staticProxyService) IsUnVerified(proxyinabox.Proxy) (bool, error) {
	return s.stillUnverified, s.refreshErr
}

func waitFor(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		if condition() {
			return
		}
		select {
		case <-deadline.C:
			t.Fatal("timed out waiting for condition")
		case <-ticker.C:
		}
	}
}

// TestValidator_LoadOrStoreAtomic 验证 LoadOrStore 原子操作防止多个 validator 同时验证同一代理
func TestValidator_LoadOrStoreAtomic(t *testing.T) {
	p := proxyinabox.Proxy{
		IP:       "2.2.2.2",
		Port:     "8080",
		Protocol: "http",
		Source:   "test",
	}

	pendingValidate = sync.Map{}

	const numValidators = 5
	var wg sync.WaitGroup
	wg.Add(numValidators)

	validatedCount := 0
	var mu sync.Mutex

	for i := 0; i < numValidators; i++ {
		go func(id int) {
			defer wg.Done()
			proxy := p.URI()
			// 使用 LoadOrStore 原子操作
			_, loaded := pendingValidate.LoadOrStore(proxy, id)
			if !loaded {
				time.Sleep(10 * time.Millisecond)
				mu.Lock()
				validatedCount++
				mu.Unlock()
				pendingValidate.Delete(proxy)
			}
		}(i)
	}

	// 等待所有 goroutine
	wg.Wait()

	// 使用 LoadOrStore 应该只验证一次
	if validatedCount != 1 {
		t.Errorf("With LoadOrStore: validated %d times, want 1", validatedCount)
	}
}

func TestValidator_ReleasesPendingValidationForCachedProxy(t *testing.T) {
	previousCache := proxyinabox.CI
	previousJobs := ValidateJobs
	pendingValidate = sync.Map{}
	t.Cleanup(func() {
		proxyinabox.CI = previousCache
		ValidateJobs = previousJobs
		pendingValidate = sync.Map{}
	})

	cache := &testCache{hasProxy: true, hasProxyHit: make(chan struct{})}
	proxyinabox.CI = cache
	jobs := make(chan proxyinabox.Proxy, 1)
	ValidateJobs = jobs
	p := proxyinabox.Proxy{IP: "2.2.2.3", Port: "8080", Protocol: "http"}

	go validator(1, jobs)
	jobs <- p
	close(jobs)

	select {
	case <-cache.hasProxyHit:
	case <-time.After(time.Second):
		t.Fatal("validator did not check the cached proxy")
	}
	waitFor(t, func() bool {
		_, pending := pendingValidate.Load(p.URI())
		return !pending
	})
}

func TestValidator_ReleasesPendingValidationWhenIPLocked(t *testing.T) {
	previousCache := proxyinabox.CI
	previousJobs := ValidateJobs
	pendingValidate = sync.Map{}
	t.Cleanup(func() {
		proxyinabox.CI = previousCache
		ValidateJobs = previousJobs
		pendingValidate = sync.Map{}
	})

	cache := &testCache{locked: true, lockedHit: make(chan struct{})}
	proxyinabox.CI = cache
	jobs := make(chan proxyinabox.Proxy, 1)
	ValidateJobs = jobs
	p := proxyinabox.Proxy{IP: "2.2.2.4", Port: "8080", Protocol: "http"}

	go validator(1, jobs)
	jobs <- p
	close(jobs)

	select {
	case <-cache.lockedHit:
	case <-time.After(time.Second):
		t.Fatal("validator did not check the lock state")
	}
	waitFor(t, func() bool {
		_, pending := pendingValidate.Load(p.URI())
		return !pending
	})
}

func TestVerify_DeduplicatesConcurrentRunsByURI(t *testing.T) {
	previousService := proxyServiceInstance
	previousJobs := verifyJob
	pendingVerify = sync.Map{}
	t.Cleanup(func() {
		proxyServiceInstance = previousService
		verifyJob = previousJobs
		pendingVerify = sync.Map{}
	})

	p := proxyinabox.Proxy{IP: "3.3.3.3", Port: "8080", Protocol: "http"}
	proxyServiceInstance = staticProxyService{proxies: []proxyinabox.Proxy{p}, stillUnverified: true}
	verifyJob = make(chan proxyinabox.Proxy, 1)

	const runs = 16
	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(runs)
	for range runs {
		go func() {
			defer wg.Done()
			<-start
			Verify()
		}()
	}
	close(start)
	wg.Wait()

	if got := len(verifyJob); got != 1 {
		t.Fatalf("queued verifications = %d, want 1", got)
	}
	queued := <-verifyJob
	if queued.URI() != p.URI() {
		t.Errorf("queued URI = %q, want %q", queued.URI(), p.URI())
	}
}

func TestVerify_DeduplicationUsesURI(t *testing.T) {
	previousService := proxyServiceInstance
	previousJobs := verifyJob
	pendingVerify = sync.Map{}
	t.Cleanup(func() {
		proxyServiceInstance = previousService
		verifyJob = previousJobs
		pendingVerify = sync.Map{}
	})

	p1 := proxyinabox.Proxy{IP: "4.4.4.4", Port: "8080", Protocol: "http"}
	p2 := proxyinabox.Proxy{IP: "4.4.4.4", Port: "3128", Protocol: "http"}
	proxyServiceInstance = staticProxyService{proxies: []proxyinabox.Proxy{p1, p2}, stillUnverified: true}
	verifyJob = make(chan proxyinabox.Proxy, 2)

	Verify()
	if got := len(verifyJob); got != 2 {
		t.Fatalf("queued verifications = %d, want 2 for distinct URIs", got)
	}
}

func TestVerify_SkipsProxyThatBecameFreshAfterSnapshot(t *testing.T) {
	previousService := proxyServiceInstance
	previousJobs := verifyJob
	pendingVerify = sync.Map{}
	t.Cleanup(func() {
		proxyServiceInstance = previousService
		verifyJob = previousJobs
		pendingVerify = sync.Map{}
	})

	p := proxyinabox.Proxy{IP: "4.4.4.5", Port: "8080", Protocol: "http"}
	p.ID = 1
	proxyServiceInstance = staticProxyService{proxies: []proxyinabox.Proxy{p}, stillUnverified: false}
	verifyJob = make(chan proxyinabox.Proxy, 1)

	Verify()
	if got := len(verifyJob); got != 0 {
		t.Fatalf("queued verifications = %d, want 0 for a fresh proxy", got)
	}
	if _, pending := pendingVerify.Load(p.URI()); pending {
		t.Fatal("fresh proxy should release pending verification ownership")
	}
}

func TestGetDelay_ReleasesPendingVerificationWhenIPLocked(t *testing.T) {
	previousCache := proxyinabox.CI
	pendingVerify = sync.Map{}
	t.Cleanup(func() {
		proxyinabox.CI = previousCache
		pendingVerify = sync.Map{}
	})

	cache := &testCache{locked: true, lockedHit: make(chan struct{})}
	proxyinabox.CI = cache
	p := proxyinabox.Proxy{IP: "5.5.5.5", Port: "8080", Protocol: "http"}
	pendingVerify.Store(p.URI(), nil)
	jobs := make(chan proxyinabox.Proxy, 1)
	go getDelay(jobs)
	jobs <- p
	close(jobs)

	select {
	case <-cache.lockedHit:
	case <-time.After(time.Second):
		t.Fatal("verification worker did not check the lock state")
	}
	waitFor(t, func() bool {
		_, pending := pendingVerify.Load(p.URI())
		return !pending
	})
}
