package crawler

import (
	"fmt"
	"time"

	"github.com/naiba/proxyinabox"
	"github.com/naiba/proxyinabox/service"
)

var verifyJob chan proxyinabox.Proxy
var proxyServiceInstance proxyinabox.ProxyService

const staleProxyThreshold = 6 * 30 * 24 * time.Hour

func Init() {
	// BUG-FIX: test-source 子命令不初始化 CI（缓存实例），跳过依赖 CI 的操作避免 nil panic
	if proxyinabox.CI != nil {
		proxyinabox.CI.LoadLockedIPs()
	}

	ValidateJobs = make(chan proxyinabox.Proxy, proxyinabox.Config.Sys.ProxyVerifyWorker*2)
	for i := 1; i <= proxyinabox.Config.Sys.ProxyVerifyWorker; i++ {
		go validator(i, ValidateJobs)
	}

	proxyServiceInstance = &service.ProxyService{DB: proxyinabox.DB}
	verifyJob = make(chan proxyinabox.Proxy, proxyinabox.Config.Sys.ProxyVerifyWorker)
	for i := 0; i < proxyinabox.Config.Sys.ProxyVerifyWorker; i++ {
		go getDelay(verifyJob)
	}
}

func Verify() {
	list, _ := proxyServiceInstance.GetUnVerified()
	for _, p := range list {
		// BUG-FIX: 阻塞投递确保所有过期代理都被验证，避免 channel 满时直接 return 导致部分代理被跳过
		verifyJob <- p
	}
}

func CleanupStaleProxies() {
	proxyinabox.CI.CleanupStaleProxies(staleProxyThreshold)
}

func getDelay(pc chan proxyinabox.Proxy) {
	for p := range pc {
		if proxyinabox.CI.IsIPLocked(p.IP) {
			continue
		}

		proxy := p.URI()
		start := time.Now().Unix()
		body, err := GetURLThroughProxyWithRetry(verifyEndpoint, time.Second*5, proxy, 3)
		var trace cloudflareTraceResult
		if err == nil {
			trace, err = parseCloudflareTrace(body)
		}
		delay := time.Now().Unix() - start
		if err != nil || trace.IP != p.IP {
			locked := proxyinabox.CI.RecordFailure(p.IP)
			if !locked {
				proxyinabox.CI.MarkVerifyFailed(p)
			}
			continue
		}
		if hijackErr := probeTLSHijack(proxy); hijackErr != nil {
			fmt.Printf("[PIAB] verify [🔓] proxy %s failed TLS hijack probe: %v\n", proxy, hijackErr)
			locked := proxyinabox.CI.RecordFailure(p.IP)
			if !locked {
				proxyinabox.CI.MarkVerifyFailed(p)
			}
			continue
		}
		proxyinabox.CI.MarkVerifySuccess(p, delay, time.Now())
	}
}
