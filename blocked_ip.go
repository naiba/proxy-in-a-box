package proxyinabox

import "time"

type BlockedIP struct {
	IP                  string    `gorm:"type:varchar(15);primaryKey"`
	ConsecutiveFailures int       `gorm:"default:0"`
	LockedUntil         time.Time `gorm:"index"`
}
