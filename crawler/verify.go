package crawler

import (
	"time"

	"github.com/naiba/proxyinabox"
	"github.com/naiba/proxyinabox/service"
)

var verifyJob chan proxyinabox.Proxy
var proxyServiceInstance proxyinabox.ProxyService

// Init initializes both the validation workers and verify workers
func Init() {
	CleanupStaleSessions()

	// 初始化 proxy 验证 workers
	ValidateJobs = make(chan proxyinabox.Proxy, proxyinabox.Config.Sys.ProxyVerifyWorker*2)
	for i := 1; i <= proxyinabox.Config.Sys.ProxyVerifyWorker; i++ {
		go validator(i, ValidateJobs)
	}

	// 初始化 verify workers
	proxyServiceInstance = &service.ProxyService{DB: proxyinabox.DB}
	verifyJob = make(chan proxyinabox.Proxy, proxyinabox.Config.Sys.ProxyVerifyWorker)
	for i := 0; i < proxyinabox.Config.Sys.ProxyVerifyWorker; i++ {
		go getDelay(verifyJob)
	}
}

func Verify() {
	list, _ := proxyServiceInstance.GetUnVerified()
	for _, p := range list {
		verifyJob <- p
	}
}

func getDelay(pc chan proxyinabox.Proxy) {
	for p := range pc {
		proxy := p.URI()
		start := time.Now().Unix()
		body, err := GetURLThroughProxyWithRetry(verifyEndpoint, time.Second*5, proxy, 3)
		var trace cloudflareTraceResult
		if err == nil {
			trace, err = parseCloudflareTrace(body)
		}
		delay := time.Now().Unix() - start
		if err != nil || trace.IP != p.IP {
			proxyinabox.CI.DeleteProxy(p)
			continue
		}
		proxyinabox.DB.Model(&p).Updates(map[string]interface{}{"delay": delay, "last_verify": time.Now()})
	}
}
