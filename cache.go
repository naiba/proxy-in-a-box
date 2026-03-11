package proxyinabox

import (
	"net/http"
)

type Cache interface {
	RandomProxy() (string, bool)
	GetProxy() (string, bool)
	ProxyLength() int
	PickProxy(req *http.Request) (string, error)
	IPLimiter(req *http.Request) bool
	HostLimiter(req *http.Request) bool
	HasProxy(p string) bool
	SaveProxy(p Proxy) error
	DeleteProxy(p Proxy)
	RemoveFromCache(p Proxy)
	// UpdateProxyFields 更新内存缓存中指定代理的 Delay 和 LastVerify 字段（由定期验证调用）
	UpdateProxyFields(p Proxy)
	GetAllProxies() []Proxy
}
