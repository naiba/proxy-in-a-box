package proxyinabox

import (
	"net/http"
)

// Cache rt
type Cache interface {
	RandomProxy() (string, bool)
	PickProxy(req *http.Request) (string, error)
	IPLimiter(req *http.Request) bool
	HostLimiter(req *http.Request) bool
	HasProxy(p string) bool
	SaveProxy(p Proxy) error
	DeleteProxy(p Proxy)
}
