package service

import (
	"time"

	"github.com/naiba/proxyinabox"
	"gorm.io/gorm"
)

// ProxyService mysql proxy service
type ProxyService struct {
	DB *gorm.DB
}

// GetUnVerified get un verified proxies
func (ps *ProxyService) GetUnVerified() (p []proxyinabox.Proxy, e error) {
	e = ps.DB.Select("ip,port,id,last_verify").
		Where("last_verify < ?", time.Now().Add(time.Minute*time.Duration((proxyinabox.Config.Sys.VerifyDuration-5))*-1)).
		Where("ip NOT IN (?)",
			proxyinabox.DB.Table("blocked_ips").Select("ip").Where("locked_until > ?", time.Now()),
		).
		Find(&p).Error
	return
}
