package crawler

import (
	"fmt"
	"sync"
	"time"

	"github.com/naiba/proxyinabox"
	"github.com/naiba/proxyinabox/service"
)

var verifyJob chan proxyinabox.Proxy
var proxyServiceInstance proxyinabox.ProxyService
var pendingVerify sync.Map
var validationSlots chan struct{}
var candidateSlots chan struct{}
var validationAttemptLimiter *attemptRateLimiter

const (
	staleProxyThreshold                = 6 * 30 * 24 * time.Hour
	defaultProxyVerificationWorkers    = 20
	maxProxyVerificationWorkers        = 256
	defaultValidationAttemptsPerSecond = 50
	maxValidationAttemptsPerSecond     = 1000
)

func configuredVerificationWorkers() int {
	workers := proxyinabox.Config.Sys.ProxyVerifyWorker
	if workers <= 0 {
		return defaultProxyVerificationWorkers
	}
	if workers > maxProxyVerificationWorkers {
		return maxProxyVerificationWorkers
	}
	return workers
}

func configuredValidationRate() (int, int) {
	rate := proxyinabox.Config.Verification.MaxAttemptsPerSecond
	if rate <= 0 {
		rate = defaultValidationAttemptsPerSecond
	} else if rate > maxValidationAttemptsPerSecond {
		rate = maxValidationAttemptsPerSecond
	}
	burst := proxyinabox.Config.Verification.AttemptBurst
	if burst <= 0 {
		burst = min(rate, defaultProxyVerificationWorkers)
	} else if burst > rate {
		burst = rate
	}
	return rate, burst
}

func configuredRoutineReservedWorkers(workers int) int {
	if workers <= 1 {
		return 0
	}
	reserved := proxyinabox.Config.Verification.RoutineReservedWorkers
	if reserved <= 0 {
		reserved = max(1, workers/5)
	}
	if reserved >= workers {
		return workers - 1
	}
	return reserved
}

func acquireValidationSlot() chan struct{} {
	slots := validationSlots
	if slots != nil {
		slots <- struct{}{}
	}
	return slots
}

func releaseValidationSlot(slots chan struct{}) {
	if slots != nil {
		<-slots
	}
}

func acquireCandidateSlot() chan struct{} {
	slots := candidateSlots
	if slots != nil {
		slots <- struct{}{}
	}
	return slots
}

func releaseCandidateSlot(slots chan struct{}) {
	if slots != nil {
		<-slots
	}
}

func Init() {
	// BUG-FIX: test-source 子命令不初始化 CI（缓存实例），跳过依赖 CI 的操作避免 nil panic
	if proxyinabox.CI != nil {
		proxyinabox.CI.LoadLockedIPs()
	}

	workers := configuredVerificationWorkers()
	rate, burst := configuredValidationRate()
	reserved := configuredRoutineReservedWorkers(workers)
	validationSlots = make(chan struct{}, workers)
	candidateSlots = make(chan struct{}, workers-reserved)
	validationAttemptLimiter = newAttemptRateLimiter(rate, burst, time.Now())
	ValidateJobs = make(chan proxyinabox.Proxy, workers*2)
	for i := 1; i <= workers; i++ {
		go validator(i, ValidateJobs)
	}

	proxyServiceInstance = &service.ProxyService{DB: proxyinabox.DB}
	verifyJob = make(chan proxyinabox.Proxy, workers)
	for i := 0; i < workers; i++ {
		go getDelay(verifyJob)
	}
}

func Verify() {
	list, err := proxyServiceInstance.GetUnVerified()
	if err != nil {
		fmt.Printf("[PIAB] verify [❎] get unverified proxies: %v\n", err)
		return
	}
	for _, p := range list {
		uri := p.URI()
		if _, loaded := pendingVerify.LoadOrStore(uri, nil); loaded {
			runtimeMetrics.skipDuplicate.Add(1)
			continue
		}
		stillUnverified, err := proxyServiceInstance.IsUnVerified(p)
		if err != nil {
			pendingVerify.Delete(uri)
			fmt.Printf("[PIAB] verify [❎] refresh proxy %s state: %v\n", uri, err)
			continue
		}
		if !stillUnverified {
			pendingVerify.Delete(uri)
			continue
		}
		// BUG-FIX: 阻塞投递确保所有过期代理都被验证，避免 channel 满时直接 return 导致部分代理被跳过
		verifyJob <- p
	}
}

func CleanupStaleProxies() {
	proxyinabox.CI.CleanupStaleProxies(staleProxyThreshold)
}

func getDelay(pc chan proxyinabox.Proxy) {
	for p := range pc {
		proxy := p.URI()
		func() {
			// Verify acquires this key before enqueuing. Release it on every worker
			// exit path so future cron runs can retry the proxy.
			defer pendingVerify.Delete(proxy)

			if proxyinabox.CI.IsIPLocked(p.IP) {
				runtimeMetrics.skipIPLocked.Add(1)
				return
			}

			runtimeMetrics.routineWaiting.Add(1)
			slots := acquireValidationSlot()
			runtimeMetrics.routineWaiting.Add(-1)
			defer releaseValidationSlot(slots)
			runtimeMetrics.routineActive.Add(1)
			defer runtimeMetrics.routineActive.Add(-1)
			start := time.Now().Unix()
			body, requestErr := getURLThroughProxyWithRetryLimit(
				verifyEndpoint,
				time.Second*5,
				proxy,
				configuredHealthCheckRetries(),
				configuredHealthResponseBodyLimit(),
			)
			var trace cloudflareTraceResult
			var parseErr error
			if requestErr == nil {
				trace, parseErr = parseCloudflareTrace(body)
			}
			delay := time.Now().Unix() - start
			if requestErr != nil {
				recordNetworkFailure(requestErr)
				recordHealthCheckFailure(p)
				return
			}
			if parseErr != nil {
				runtimeMetrics.failResponse.Add(1)
				recordHealthCheckFailure(p)
				return
			}
			if trace.IP != p.IP {
				runtimeMetrics.failIPMismatch.Add(1)
				recordHealthCheckFailure(p)
				return
			}
			deepVerified := needsDeepCheck(p, time.Now())
			if deepVerified {
				waitForValidationAttempt()
				if hijackErr := probeTLSHijack(proxy); hijackErr != nil {
					runtimeMetrics.failTLSProbe.Add(1)
					fmt.Printf("[PIAB] verify [🔓] proxy %s failed TLS hijack probe: %v\n", proxy, hijackErr)
					recordHealthCheckFailure(p)
					return
				}
			}
			proxyinabox.CI.MarkVerifySuccess(p, delay, time.Now(), deepVerified)
			checkCounters.recordRoutine(true)
			candidateFailures.clear(proxy)
		}()
	}
}

func recordHealthCheckFailure(p proxyinabox.Proxy) {
	checkCounters.recordRoutine(false)
	failures := proxyinabox.CI.MarkVerifyFailed(p)
	candidateFailures.recordFailure(p.URI(), failures, time.Now())
	proxyinabox.CI.RecordFailure(p.IP)
}
