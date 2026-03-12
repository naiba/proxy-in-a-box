package crawler

import (
	"fmt"
	"time"

	"github.com/naiba/proxyinabox"
	"github.com/naiba/proxyinabox/service"
)

var verifyJob chan proxyinabox.Proxy
var proxyServiceInstance proxyinabox.ProxyService

// staleProxyThreshold 代理最后验证超过此时间视为不活跃，将被清理
const staleProxyThreshold = 6 * 30 * 24 * time.Hour // 约6个月

// Init initializes both the validation workers and verify workers
func Init() {
	CleanupStaleSessions()
	LoadLockedIPs()

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
		// BUG-FIX: 非阻塞发送，channel 满时跳过剩余代理，避免 Verify() 长时间阻塞导致 cron 调度堆积，
		// 未被处理的代理会在下一轮 Verify() 中被重新选中
		select {
		case verifyJob <- p:
		default:
			return
		}
	}
}

// CleanupStaleProxies 删除最后验证时间超过 6 个月的代理记录，回收长期不活跃的陈旧数据
func CleanupStaleProxies() {
	cutoff := time.Now().Add(-staleProxyThreshold)
	result := proxyinabox.DB.Unscoped().Where("last_verify < ? AND last_verify > ?", cutoff, time.Time{}).Delete(&proxyinabox.Proxy{})
	if result.Error != nil {
		fmt.Printf("[PIAB] stale cleanup [❎] error: %v\n", result.Error)
		return
	}
	if result.RowsAffected > 0 {
		fmt.Printf("[PIAB] stale cleanup [🧹] removed %d proxies inactive for 6+ months\n", result.RowsAffected)
	}
}

func getDelay(pc chan proxyinabox.Proxy) {
	for p := range pc {
		if IsIPLocked(p.IP) {
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
			locked := RecordProxyFailure(p.IP)
			proxyinabox.CI.RemoveFromCache(p)
			if !locked {
				// BUG-FIX: 未锁定时更新 last_verify，防止失败代理反复被 GetUnVerified() 选中挤占 verify worker。
				// 锁定时 RecordProxyFailure 已删除 proxies 记录，无需更新。
				proxyinabox.DB.Model(&p).Update("last_verify", time.Now())
			}
			continue
		}
		ClearProxyFailure(p.IP)
		now := time.Now()
		proxyinabox.DB.Model(&p).Updates(map[string]interface{}{"delay": delay, "last_verify": now})
		// BUG-FIX: 数据库更新后必须同步内存缓存，否则 dashboard 展示的 LastVerify 永远停留在初始加载时的值
		p.Delay = delay
		p.LastVerify = now
		proxyinabox.CI.UpdateProxyFields(p)
	}
}
