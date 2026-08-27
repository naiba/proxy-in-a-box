package proxyinabox

import (
	"net/http"
	"time"
)

type Cache interface {
	// --- 代理池读取 ---
	RandomProxy() (string, bool)
	GetProxy() (string, bool)
	ProxyLength() int
	PickProxy(req *http.Request) (string, error)
	HasProxy(p string) bool
	GetAllProxies() []Proxy

	// --- 代理生命周期（DB + 内存原子操作） ---

	// UpsertProxy 新代理首次验证成功时调用。
	// 原子完成：清除 blocked_ips 旧记录 → 写入/更新 DB → 替换内存 entry。
	UpsertProxy(p Proxy) error

	// MarkVerifySuccess 已有代理定期验证成功时调用。
	// 原子完成：清除 blocked_ips → 更新 DB delay/last_verify → 同步内存。
	MarkVerifySuccess(p Proxy, delay int64, verifyTime time.Time)

	// MarkVerifyFailed 已有代理定期验证失败但未达锁定阈值时调用。
	// 原子完成：在 DB 中持久化不可用状态 → 从内存移除。
	MarkVerifyFailed(p Proxy)

	// MarkProxyUnavailable 处理代理自身明确返回 403/407 的情况。
	// 按 URI 隔离单个端点，等待定期健康检查恢复，不累计为整 IP 封禁。
	MarkProxyUnavailable(proxyURI string)

	// MarkProxyTargetFailure 对某个代理与目标的组合做短期熔断。它不会把
	// 仍可服务其他目标的代理从全局池中删除。
	MarkProxyTargetFailure(proxyURI string, target string)

	// RecordFailure 代理请求或验证失败时调用，累计失败次数。
	// 达到阈值时锁定 IP，并将该 IP 的代理持久化为不可用；记录不会删除，
	// 锁定到期后仍可由健康检查或数据源重新验证恢复。
	// 返回 true 表示触发了锁定。
	RecordFailure(ip string) bool

	// IsIPLocked 检查 IP 是否在锁定期内。
	IsIPLocked(ip string) bool

	// LoadLockedIPs 启动时从 DB 加载锁定状态到内存。
	LoadLockedIPs()

	// CleanupStaleProxies 删除 last_verify 超过阈值的陈旧代理，同步清理内存。
	CleanupStaleProxies(threshold time.Duration)
}
