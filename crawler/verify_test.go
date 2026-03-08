package crawler

import (
	"testing"
	"time"

	"github.com/naiba/proxyinabox"
)

func TestCleanupStaleProxies(t *testing.T) {
	setupTestDB(t)

	fresh := proxyinabox.Proxy{
		IP: "1.1.1.1", Port: "8080", Protocol: "http",
		Source: "test", LastVerify: time.Now().Add(-24 * time.Hour),
	}
	stale := proxyinabox.Proxy{
		IP: "2.2.2.2", Port: "8080", Protocol: "http",
		Source: "test", LastVerify: time.Now().Add(-7 * 30 * 24 * time.Hour),
	}
	proxyinabox.DB.Create(&fresh)
	proxyinabox.DB.Create(&stale)

	var countBefore int64
	proxyinabox.DB.Model(&proxyinabox.Proxy{}).Count(&countBefore)
	if countBefore != 2 {
		t.Fatalf("expected 2 proxies before cleanup, got %d", countBefore)
	}

	CleanupStaleProxies()

	var countAfter int64
	proxyinabox.DB.Model(&proxyinabox.Proxy{}).Count(&countAfter)
	if countAfter != 1 {
		t.Errorf("expected 1 proxy after cleanup, got %d", countAfter)
	}

	var remaining proxyinabox.Proxy
	proxyinabox.DB.First(&remaining)
	if remaining.IP != "1.1.1.1" {
		t.Errorf("remaining proxy IP = %q, want 1.1.1.1", remaining.IP)
	}
}

func TestCleanupStaleProxies_NoStale(t *testing.T) {
	setupTestDB(t)

	fresh := proxyinabox.Proxy{
		IP: "3.3.3.3", Port: "8080", Protocol: "http",
		Source: "test", LastVerify: time.Now(),
	}
	proxyinabox.DB.Create(&fresh)

	CleanupStaleProxies()

	var count int64
	proxyinabox.DB.Model(&proxyinabox.Proxy{}).Count(&count)
	if count != 1 {
		t.Errorf("expected 1 proxy (no stale), got %d", count)
	}
}
