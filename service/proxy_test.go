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
		LastVerify: time.Now().Add(-1 * time.Hour),
	})
	db.Create(&proxyinabox.Proxy{
		IP: "2.2.2.2", Port: "8080", Protocol: "http", Source: "test",
		LastVerify: time.Now().Add(-1 * time.Hour),
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
