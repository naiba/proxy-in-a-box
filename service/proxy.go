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
	// BUG-FIX: 必须包含 protocol 字段，否则所有代理在重新验证时 Protocol 为空，
	// Proxy.URI() 会将其默认为 http，导致 SOCKS 代理被当作 HTTP 代理验证必然失败
	e = ps.DB.Select("ip,port,id,protocol,last_verify").
		Where("last_verify < ?", time.Now().Add(time.Minute*time.Duration((proxyinabox.Config.Sys.VerifyDuration-5))*-1)).
		Where("ip NOT IN (?)",
			proxyinabox.DB.Table("blocked_ips").Select("ip").Where("locked_until > ?", time.Now()),
		).
		Find(&p).Error
	return
}
