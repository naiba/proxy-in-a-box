package proxyinabox

import (
	"fmt"
	"time"

	"gorm.io/gorm"
)

// Proxy proxy model
type Proxy struct {
	gorm.Model
	// BUG-FIX: GORM v2 不识别 unique_index（v1 语法），导致 IP 唯一约束从未生效，
	// 同 IP 不同端口/协议的代理在 DB 中可以重复插入。改用 uniqueIndex 复合索引，
	// 以 (IP, Port, Protocol) 为唯一粒度，允许同 IP 不同端口合法共存。
	IP         string `gorm:"type:varchar(15);uniqueIndex:idx_proxy_endpoint"`
	Port       string `gorm:"type:varchar(5);uniqueIndex:idx_proxy_endpoint"`
	Country    string `gorm:"type:varchar(15)"`
	Provence   string `gorm:"type:varchar(15)"`
	Source     string
	Protocol   string `gorm:"uniqueIndex:idx_proxy_endpoint"`
	Delay      int64
	LastVerify time.Time
}

// ProxyService proxy service
type ProxyService interface {
	GetUnVerified() ([]Proxy, error)
}

func (p *Proxy) String() string {
	return fmt.Sprintf("[PIAB] proxy [🐲] { id:%d %s:%s country:%s provence:%s Protocol:%s delay:%d source:%s }",
		p.ID, p.IP, p.Port, p.Country, p.Provence, p.Protocol, p.Delay, p.Source)
}

func (p *Proxy) URI() string {
	protocol := p.Protocol
	if protocol == "" {
		protocol = "http"
	}
	return protocol + "://" + p.IP + ":" + p.Port
}
