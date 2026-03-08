package crawler

import (
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/naiba/proxyinabox"
	"gorm.io/gorm"
)

func setupTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("failed to open test database: %v", err)
	}
	db.AutoMigrate(&proxyinabox.Proxy{}, &proxyinabox.BlockedIP{})
	proxyinabox.DB = db
	return db
}

func TestRecordProxyFailure_BelowThreshold(t *testing.T) {
	setupTestDB(t)
	lockedIPs.Delete("1.2.3.4")

	RecordProxyFailure("1.2.3.4")
	RecordProxyFailure("1.2.3.4")

	var b proxyinabox.BlockedIP
	proxyinabox.DB.First(&b, "ip = ?", "1.2.3.4")
	if b.ConsecutiveFailures != 2 {
		t.Errorf("ConsecutiveFailures = %d, want 2", b.ConsecutiveFailures)
	}
	if !b.LockedUntil.IsZero() {
		t.Error("should not be locked below threshold")
	}
	if IsIPLocked("1.2.3.4") {
		t.Error("IP should not be locked with only 2 failures")
	}
}

func TestRecordProxyFailure_AtThreshold(t *testing.T) {
	setupTestDB(t)
	lockedIPs.Delete("5.6.7.8")

	for i := 0; i < proxyFailureLockThreshold; i++ {
		RecordProxyFailure("5.6.7.8")
	}

	var b proxyinabox.BlockedIP
	proxyinabox.DB.First(&b, "ip = ?", "5.6.7.8")
	if b.ConsecutiveFailures != proxyFailureLockThreshold {
		t.Errorf("ConsecutiveFailures = %d, want %d", b.ConsecutiveFailures, proxyFailureLockThreshold)
	}
	if b.LockedUntil.Before(time.Now()) {
		t.Error("LockedUntil should be in the future")
	}
	if !IsIPLocked("5.6.7.8") {
		t.Error("IP should be locked after reaching threshold")
	}
}

func TestClearProxyFailure(t *testing.T) {
	setupTestDB(t)
	lockedIPs.Delete("10.0.0.1")

	for i := 0; i < proxyFailureLockThreshold; i++ {
		RecordProxyFailure("10.0.0.1")
	}
	if !IsIPLocked("10.0.0.1") {
		t.Fatal("IP should be locked before clear")
	}

	ClearProxyFailure("10.0.0.1")

	if IsIPLocked("10.0.0.1") {
		t.Error("IP should not be locked after clear")
	}
	var count int64
	proxyinabox.DB.Model(&proxyinabox.BlockedIP{}).Where("ip = ?", "10.0.0.1").Count(&count)
	if count != 0 {
		t.Errorf("blocked_ips record count = %d, want 0 after clear", count)
	}
}

func TestIsIPLocked_Expired(t *testing.T) {
	lockedIPs.Store("expired.ip", time.Now().Add(-1*time.Hour))

	if IsIPLocked("expired.ip") {
		t.Error("expired lock should return false")
	}
	if _, ok := lockedIPs.Load("expired.ip"); ok {
		t.Error("expired entry should be removed from cache")
	}
}

func TestIsIPLocked_NotLocked(t *testing.T) {
	lockedIPs.Delete("unknown.ip")
	if IsIPLocked("unknown.ip") {
		t.Error("unknown IP should not be locked")
	}
}

func TestLoadLockedIPs(t *testing.T) {
	setupTestDB(t)

	proxyinabox.DB.Create(&proxyinabox.BlockedIP{
		IP:                  "active.lock",
		ConsecutiveFailures: 3,
		LockedUntil:         time.Now().Add(24 * time.Hour),
	})
	proxyinabox.DB.Create(&proxyinabox.BlockedIP{
		IP:                  "expired.lock",
		ConsecutiveFailures: 3,
		LockedUntil:         time.Now().Add(-24 * time.Hour),
	})

	lockedIPs.Delete("active.lock")
	lockedIPs.Delete("expired.lock")

	LoadLockedIPs()

	if !IsIPLocked("active.lock") {
		t.Error("active lock should be loaded")
	}
	if IsIPLocked("expired.lock") {
		t.Error("expired lock should not be loaded")
	}
}
