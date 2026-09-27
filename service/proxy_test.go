package service

import (
	"testing"
	"time"

	"github.com/naiba/proxyinabox"
)

func TestGetUnVerified_ExcludesBlockedIPs(t *testing.T) {
	db := setupTestDB(t)

	db.Create(&proxyinabox.Proxy{
		IP: "1.1.1.1", Port: "8080", Protocol: "http", Source: "test",
		LastVerify: time.Now().Add(-3 * time.Hour),
	})
	db.Create(&proxyinabox.Proxy{
		IP: "2.2.2.2", Port: "8080", Protocol: "http", Source: "test",
		LastVerify: time.Now().Add(-3 * time.Hour),
	})
	db.Create(&proxyinabox.BlockedIP{
		IP:                  "2.2.2.2",
		ConsecutiveFailures: 3,
		LockedUntil:         time.Now().Add(24 * time.Hour),
	})

	ps := &ProxyService{DB: db}
	proxies, err := ps.GetUnVerified()
	if err != nil {
		t.Fatalf("GetUnVerified error: %v", err)
	}

	for _, p := range proxies {
		if p.IP == "2.2.2.2" {
			t.Error("blocked IP 2.2.2.2 should be excluded from unverified list")
		}
	}
	found := false
	for _, p := range proxies {
		if p.IP == "1.1.1.1" {
			found = true
		}
	}
	if !found {
		t.Error("non-blocked IP 1.1.1.1 should be in unverified list")
	}
}

func TestGetUnVerified_RecentlyVerifiedExcluded(t *testing.T) {
	db := setupTestDB(t)

	db.Create(&proxyinabox.Proxy{
		IP: "3.3.3.3", Port: "8080", Protocol: "http", Source: "test",
		LastVerify: time.Now(),
	})

	ps := &ProxyService{DB: db}
	proxies, err := ps.GetUnVerified()
	if err != nil {
		t.Fatalf("GetUnVerified error: %v", err)
	}

	for _, p := range proxies {
		if p.IP == "3.3.3.3" {
			t.Error("recently verified proxy should not be in unverified list")
		}
	}
}

func TestGetUnVerified_IncludesUnavailableProxyWhenRetryIsDue(t *testing.T) {
	db := setupTestDB(t)
	p := proxyinabox.Proxy{
		IP: "3.3.3.4", Port: "8080", Protocol: "http", Source: "test",
		LastVerify: time.Now().Add(-time.Hour),
	}
	if err := db.Create(&p).Error; err != nil {
		t.Fatalf("create proxy: %v", err)
	}
	if err := db.Model(&p).Update("available", false).Error; err != nil {
		t.Fatalf("mark unavailable: %v", err)
	}

	proxies, err := (&ProxyService{DB: db}).GetUnVerified()
	if err != nil {
		t.Fatalf("GetUnVerified error: %v", err)
	}
	if len(proxies) != 1 || proxies[0].ID != p.ID {
		t.Fatalf("GetUnVerified = %#v, want quarantined proxy ID %d", proxies, p.ID)
	}
}

func TestGetUnVerified_DefersUnavailableProxyUntilNextRetry(t *testing.T) {
	db := setupTestDB(t)
	p := proxyinabox.Proxy{
		IP: "3.3.3.5", Port: "8080", Protocol: "http", Source: "test",
		Available: false, LastVerify: time.Now().Add(-24 * time.Hour),
		NextVerifyAt: time.Now().Add(time.Hour),
	}
	if err := db.Create(&p).Error; err != nil {
		t.Fatalf("create proxy: %v", err)
	}
	if err := db.Model(&p).Update("available", false).Error; err != nil {
		t.Fatalf("mark unavailable: %v", err)
	}

	proxies, err := (&ProxyService{DB: db}).GetUnVerified()
	if err != nil {
		t.Fatalf("GetUnVerified error: %v", err)
	}
	if len(proxies) != 0 {
		t.Fatalf("GetUnVerified = %#v, want no proxy before next retry", proxies)
	}
}

func TestGetUnVerified_UsesConfiguredHealthyInterval(t *testing.T) {
	previousConfig := proxyinabox.Config
	proxyinabox.Config.Verification.Interval = 4 * time.Hour
	t.Cleanup(func() { proxyinabox.Config = previousConfig })

	db := setupTestDB(t)
	for _, p := range []proxyinabox.Proxy{
		{IP: "3.3.3.6", Port: "8080", Protocol: "http", LastVerify: time.Now().Add(-3 * time.Hour)},
		{IP: "3.3.3.7", Port: "8080", Protocol: "http", LastVerify: time.Now().Add(-5 * time.Hour)},
	} {
		if err := db.Create(&p).Error; err != nil {
			t.Fatalf("create proxy: %v", err)
		}
	}

	proxies, err := (&ProxyService{DB: db}).GetUnVerified()
	if err != nil {
		t.Fatalf("GetUnVerified error: %v", err)
	}
	if len(proxies) != 1 || proxies[0].IP != "3.3.3.7" {
		t.Fatalf("GetUnVerified = %#v, want only the proxy older than 4h", proxies)
	}
}

func TestGetMaintenanceStats_MatchesEligibleProxiesAndLocks(t *testing.T) {
	previousConfig := proxyinabox.Config
	proxyinabox.Config.Verification.Interval = 4 * time.Hour
	t.Cleanup(func() { proxyinabox.Config = previousConfig })

	db := setupTestDB(t)
	now := time.Now()
	proxies := []proxyinabox.Proxy{
		{IP: "1.1.1.1", Port: "8080", Protocol: "http", LastVerify: now.Add(-5 * time.Hour)}, // healthy due
		{IP: "1.1.1.2", Port: "8080", Protocol: "http", LastVerify: now.Add(-3 * time.Hour)}, // healthy fresh
		{IP: "1.1.1.3", Port: "8080", Protocol: "http", LastVerify: now.Add(-5 * time.Hour)}, // healthy locked
		{IP: "1.1.1.4", Port: "8080", Protocol: "http", NextVerifyAt: now.Add(-time.Minute)}, // quarantined due
		{IP: "1.1.1.5", Port: "8080", Protocol: "http", NextVerifyAt: now.Add(time.Hour)},    // retry deferred
		{IP: "1.1.1.6", Port: "8080", Protocol: "http", NextVerifyAt: now.Add(-time.Minute)}, // retry locked
	}
	for i := range proxies {
		if err := db.Create(&proxies[i]).Error; err != nil {
			t.Fatalf("create proxy: %v", err)
		}
		if i >= 3 {
			if err := db.Model(&proxies[i]).Update("available", false).Error; err != nil {
				t.Fatalf("quarantine proxy: %v", err)
			}
		}
	}
	for _, ip := range []string{"1.1.1.3", "1.1.1.6"} {
		if err := db.Create(&proxyinabox.BlockedIP{IP: ip, LockedUntil: now.Add(time.Hour)}).Error; err != nil {
			t.Fatalf("lock IP: %v", err)
		}
	}
	ps := &ProxyService{DB: db}
	stats, err := ps.GetMaintenanceStats()
	if err != nil {
		t.Fatal(err)
	}
	if stats.HealthyDue != 1 || stats.QuarantinedReady != 1 || stats.QuarantinedWaiting != 2 {
		t.Fatalf("maintenance stats = %+v, want healthy=1 ready=1 waiting=2", stats)
	}
	eligible, err := ps.GetUnVerified()
	if err != nil || len(eligible) != 2 {
		t.Fatalf("eligible proxies = %v, err = %v; want 2", eligible, err)
	}
}

func TestIsUnVerified_RefreshesCurrentState(t *testing.T) {
	db := setupTestDB(t)
	p := proxyinabox.Proxy{
		IP: "4.4.4.4", Port: "8080", Protocol: "http", Source: "test",
		LastVerify: time.Now().Add(-3 * time.Hour),
	}
	if err := db.Create(&p).Error; err != nil {
		t.Fatalf("create proxy: %v", err)
	}

	ps := &ProxyService{DB: db}
	stale, err := ps.IsUnVerified(p)
	if err != nil {
		t.Fatalf("IsUnVerified error: %v", err)
	}
	if !stale {
		t.Fatal("stale proxy should be unverified")
	}

	if err := db.Model(&proxyinabox.Proxy{}).Where("id = ?", p.ID).Update("last_verify", time.Now()).Error; err != nil {
		t.Fatalf("refresh proxy: %v", err)
	}
	stale, err = ps.IsUnVerified(p)
	if err != nil {
		t.Fatalf("IsUnVerified after refresh error: %v", err)
	}
	if stale {
		t.Fatal("fresh proxy should not be unverified")
	}
}
