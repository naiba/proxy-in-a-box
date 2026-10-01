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

const (
	staleProxyThreshold             = 6 * 30 * 24 * time.Hour
	defaultProxyVerificationWorkers = 20
	maxProxyVerificationWorkers     = 256
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

func Init() {
	// BUG-FIX: test-source 子命令不初始化 CI（缓存实例），跳过依赖 CI 的操作避免 nil panic
	if proxyinabox.CI != nil {
		proxyinabox.CI.LoadLockedIPs()
	}

	workers := configuredVerificationWorkers()
	validationSlots = make(chan struct{}, workers)
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
