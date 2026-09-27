package service

import (
	"time"

	"github.com/naiba/proxyinabox"
	"gorm.io/gorm"
)

// defaultProxyVerifyInterval keeps healthy proxies fresh without continuously
// spending bandwidth on endpoints that are already known to work.
const defaultProxyVerifyInterval = 2 * time.Hour

func proxyVerifyInterval() time.Duration {
	if proxyinabox.Config.Verification.Interval > 0 {
		return proxyinabox.Config.Verification.Interval
	}
	return defaultProxyVerifyInterval
}

// ProxyService mysql proxy service
type ProxyService struct {
	DB *gorm.DB
}

// MaintenanceStats describes endpoints waiting for a periodic health check.
// Locked IPs are excluded from due counts, just as in GetUnVerified.
type MaintenanceStats struct {
	HealthyDue         int64 `json:"healthy_due"`
	QuarantinedReady   int64 `json:"quarantined_ready"`
	QuarantinedWaiting int64 `json:"quarantined_waiting"`
}

func (ps *ProxyService) GetMaintenanceStats() (MaintenanceStats, error) {
	var stats MaintenanceStats
	now := time.Now()
	unlocked := "ip NOT IN (?)"
	lockedIPs := ps.DB.Table("blocked_ips").Select("ip").Where("locked_until > ?", now)
	if err := ps.DB.Model(&proxyinabox.Proxy{}).
		Where("available = ? AND last_verify < ?", true, now.Add(-proxyVerifyInterval())).
		Where(unlocked, lockedIPs).Count(&stats.HealthyDue).Error; err != nil {
		return stats, err
	}
	if err := ps.DB.Model(&proxyinabox.Proxy{}).
		Where("available = ? AND next_verify_at <= ?", false, now).
		Where(unlocked, lockedIPs).Count(&stats.QuarantinedReady).Error; err != nil {
		return stats, err
	}
	var quarantinedTotal int64
	if err := ps.DB.Model(&proxyinabox.Proxy{}).
		Where("available = ?", false).Count(&quarantinedTotal).Error; err != nil {
		return stats, err
	}
	stats.QuarantinedWaiting = quarantinedTotal - stats.QuarantinedReady
	return stats, nil
}

// GetUnVerified get un verified proxies
func (ps *ProxyService) GetUnVerified() (p []proxyinabox.Proxy, e error) {
	// BUG-FIX: 必须包含 protocol 字段，否则所有代理在重新验证时 Protocol 为空，
	// Proxy.URI() 会将其默认为 http，导致 SOCKS 代理被当作 HTTP 代理验证必然失败
	now := time.Now()
	e = ps.DB.Select("ip,port,id,protocol,available,last_verify,last_deep_verify,consecutive_failures,next_verify_at").
		Where(
			"(available = ? AND last_verify < ?) OR (available = ? AND next_verify_at <= ?)",
			true, now.Add(-proxyVerifyInterval()), false, now,
		).
		Where("ip NOT IN (?)",
			ps.DB.Table("blocked_ips").Select("ip").Where("locked_until > ?", now),
		).
		Find(&p).Error
	return
}

func (ps *ProxyService) IsUnVerified(p proxyinabox.Proxy) (bool, error) {
	var count int64
	now := time.Now()
	err := ps.DB.Model(&proxyinabox.Proxy{}).
		Where("id = ?", p.ID).
		Where(
			"(available = ? AND last_verify < ?) OR (available = ? AND next_verify_at <= ?)",
			true, now.Add(-proxyVerifyInterval()), false, now,
		).
		Where("ip NOT IN (?)",
			ps.DB.Table("blocked_ips").Select("ip").Where("locked_until > ?", now),
		).
		Count(&count).Error
	return count > 0, err
}
