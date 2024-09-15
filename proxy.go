package proxyinabox

import (
	"fmt"
	"time"

	"gorm.io/gorm"
)

const (
	_            = iota
	PlatformKuai // 快代理
)

// Proxy proxy model
type Proxy struct {
	gorm.Model
	IP         string `gorm:"type:varchar(15);unique_index"`
	Port       string `gorm:"type:varchar(5)"`
	Country    string `gorm:"type:varchar(15)"`
	Provence   string `gorm:"type:varchar(15)"`
	Platform   int
	HTTPS      bool
	Delay      int64
	LastVerify time.Time
}

// ProxyService proxy service
type ProxyService interface {
	GetUnVerified() ([]Proxy, error)
}

func (p Proxy) String() string {
	return fmt.Sprintf("[PIAB] proxy [🐲] { id:%d %s:%s country:%s provence:%s HTTPS:%t delay:%d platform:%d }",
		p.ID, p.IP, p.Port, p.Country, p.Provence, !p.HTTPS, p.Delay, p.Platform)
}

// URI get uri
func (p Proxy) URI() string {
	var proxy string
	if p.HTTPS {
		proxy = "https://"
	} else {
		proxy = "http://"
	}
	return proxy + p.IP + ":" + p.Port
}

// ProxyCrawler proxy crawler
type ProxyCrawler interface {
	Fetch() error
}
