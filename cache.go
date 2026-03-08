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
	GetAllProxies() []Proxy
}
