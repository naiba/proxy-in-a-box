package proxyinabox

import (
	"fmt"
	"time"

	"gorm.io/gorm"
)

// Proxy proxy model
type Proxy struct {
	gorm.Model
	IP         string `gorm:"type:varchar(15);unique_index"`
	Port       string `gorm:"type:varchar(5)"`
	Country    string `gorm:"type:varchar(15)"`
	Provence   string `gorm:"type:varchar(15)"`
	Source     string
	Protocol   string
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
