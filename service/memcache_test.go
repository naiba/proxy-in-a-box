package service

import (
	"net/http"
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

func newTestCache(t *testing.T) *MemCache {
	t.Helper()
	return &MemCache{
		proxies: &proxyList{
			pl:    make([]*proxyEntry, 0),
			index: make(map[string]struct{}),
		},
		domains: &domainScheduling{
			dl: make(map[string][]*proxyEntry),
		},
		ips: &ipActivity{
			list: make(map[string]*ipActivityEntry),
		},
		domainLimit: &domainActivityList{
			list: make(map[string]*domainActivity),
		},
	}
}

func TestSaveProxy(t *testing.T) {
	setupTestDB(t)
	c := newTestCache(t)

	p := proxyinabox.Proxy{
		IP: "1.2.3.4", Port: "8080", Protocol: "http",
		Source: "test", LastVerify: time.Now(),
	}
	if err := c.SaveProxy(p); err != nil {
		t.Fatalf("SaveProxy failed: %v", err)
	}

	if c.ProxyLength() != 1 {
		t.Errorf("ProxyLength = %d, want 1", c.ProxyLength())
	}
	if !c.HasProxy("http://1.2.3.4:8080") {
		t.Error("HasProxy should return true for saved proxy")
	}

	var dbProxy proxyinabox.Proxy
	proxyinabox.DB.First(&dbProxy, "ip = ?", "1.2.3.4")
	if dbProxy.IP != "1.2.3.4" {
		t.Errorf("DB proxy IP = %q, want 1.2.3.4", dbProxy.IP)
	}
}

func TestRemoveFromCache(t *testing.T) {
	setupTestDB(t)
	c := newTestCache(t)

	p := proxyinabox.Proxy{
		IP: "1.2.3.4", Port: "8080", Protocol: "http",
		Source: "test", LastVerify: time.Now(),
	}
	c.SaveProxy(p)

	c.RemoveFromCache(p)

	if c.ProxyLength() != 0 {
		t.Errorf("ProxyLength = %d, want 0 after RemoveFromCache", c.ProxyLength())
	}
	if c.HasProxy("http://1.2.3.4:8080") {
		t.Error("HasProxy should return false after RemoveFromCache")
	}

	var count int64
	proxyinabox.DB.Model(&proxyinabox.Proxy{}).Where("ip = ?", "1.2.3.4").Count(&count)
	if count != 1 {
		t.Errorf("DB record count = %d, want 1 (RemoveFromCache should not delete from DB)", count)
	}
}

func TestDeleteProxy(t *testing.T) {
	setupTestDB(t)
	c := newTestCache(t)

	p := proxyinabox.Proxy{
		IP: "1.2.3.4", Port: "8080", Protocol: "http",
		Source: "test", LastVerify: time.Now(),
	}
	c.SaveProxy(p)

	var dbProxy proxyinabox.Proxy
	proxyinabox.DB.First(&dbProxy, "ip = ?", "1.2.3.4")
	c.DeleteProxy(dbProxy)

	if c.ProxyLength() != 0 {
		t.Errorf("ProxyLength = %d, want 0 after DeleteProxy", c.ProxyLength())
	}

	var count int64
	proxyinabox.DB.Model(&proxyinabox.Proxy{}).Where("ip = ?", "1.2.3.4").Count(&count)
	if count != 0 {
		t.Errorf("DB record count = %d, want 0 (DeleteProxy should remove from DB)", count)
	}
}

func TestGetAllProxies(t *testing.T) {
	setupTestDB(t)
	c := newTestCache(t)

	proxies := []proxyinabox.Proxy{
		{IP: "1.1.1.1", Port: "8080", Protocol: "http", Source: "a", LastVerify: time.Now()},
		{IP: "2.2.2.2", Port: "3128", Protocol: "https", Source: "b", LastVerify: time.Now()},
	}
	for _, p := range proxies {
		c.SaveProxy(p)
	}

	all := c.GetAllProxies()
	if len(all) != 2 {
		t.Fatalf("GetAllProxies returned %d, want 2", len(all))
	}

	ips := map[string]bool{}
	for _, p := range all {
		ips[p.IP] = true
	}
	if !ips["1.1.1.1"] || !ips["2.2.2.2"] {
		t.Errorf("GetAllProxies missing expected IPs, got %v", ips)
	}
}

func TestGetProxy_RoundRobin(t *testing.T) {
	setupTestDB(t)
	c := newTestCache(t)

	for _, p := range []proxyinabox.Proxy{
		{IP: "1.1.1.1", Port: "8080", Protocol: "http", Source: "test", LastVerify: time.Now()},
		{IP: "2.2.2.2", Port: "8080", Protocol: "http", Source: "test", LastVerify: time.Now()},
	} {
		c.SaveProxy(p)
	}

	first, ok := c.GetProxy()
	if !ok {
		t.Fatal("GetProxy returned not ok")
	}
	second, ok := c.GetProxy()
	if !ok {
		t.Fatal("GetProxy returned not ok")
	}
	if first == second {
		t.Error("GetProxy should rotate between proxies")
	}
}

func TestGetProxy_Empty(t *testing.T) {
	c := newTestCache(t)
	_, ok := c.GetProxy()
	if ok {
		t.Error("GetProxy on empty cache should return false")
	}
}

func TestRandomProxy_Empty(t *testing.T) {
	c := newTestCache(t)
	_, ok := c.RandomProxy()
	if ok {
		t.Error("RandomProxy on empty cache should return false")
	}
}

func TestPickProxy_ReturnsProxy(t *testing.T) {
	setupTestDB(t)
	c := newTestCache(t)

	c.SaveProxy(proxyinabox.Proxy{
		IP: "1.1.1.1", Port: "8080", Protocol: "http", Source: "test", LastVerify: time.Now(),
	})

	req, _ := http.NewRequest("GET", "http://example.com/path", nil)
	req.Host = "example.com"
	uri, err := c.PickProxy(req)
	if err != nil {
		t.Fatalf("PickProxy error: %v", err)
	}
	if uri != "http://1.1.1.1:8080" {
		t.Errorf("PickProxy = %q, want http://1.1.1.1:8080", uri)
	}
}

func TestPickProxy_EmptyPool(t *testing.T) {
	c := newTestCache(t)

	req, _ := http.NewRequest("GET", "http://example.com/path", nil)
	req.Host = "example.com"
	_, err := c.PickProxy(req)
	if err == nil {
		t.Error("PickProxy on empty pool should return error")
	}
}

func TestPickProxy_RotatesDomainProxies(t *testing.T) {
	setupTestDB(t)
	c := newTestCache(t)

	c.SaveProxy(proxyinabox.Proxy{
		IP: "1.1.1.1", Port: "8080", Protocol: "http", Source: "test", LastVerify: time.Now(),
	})
	c.SaveProxy(proxyinabox.Proxy{
		IP: "2.2.2.2", Port: "8080", Protocol: "http", Source: "test", LastVerify: time.Now(),
	})

	req, _ := http.NewRequest("GET", "http://example.com/path", nil)
	req.Host = "example.com"

	first, err := c.PickProxy(req)
	if err != nil {
		t.Fatalf("first PickProxy error: %v", err)
	}
	second, err := c.PickProxy(req)
	if err != nil {
		t.Fatalf("second PickProxy error: %v", err)
	}
	if first == second {
		t.Error("PickProxy should rotate to a different proxy for the same domain")
	}
}

func TestLoadExcludesBlockedIPs(t *testing.T) {
	db := setupTestDB(t)

	db.Create(&proxyinabox.Proxy{
		IP: "1.1.1.1", Port: "8080", Protocol: "http", Source: "test", LastVerify: time.Now(),
	})
	db.Create(&proxyinabox.Proxy{
		IP: "2.2.2.2", Port: "8080", Protocol: "http", Source: "test", LastVerify: time.Now(),
	})
	db.Create(&proxyinabox.BlockedIP{
		IP:                  "2.2.2.2",
		ConsecutiveFailures: 3,
		LockedUntil:         time.Now().Add(24 * time.Hour),
	})

	c := &MemCache{
		proxies: &proxyList{
			pl:    make([]*proxyEntry, 0),
			index: make(map[string]struct{}),
		},
		domains: &domainScheduling{
			dl: make(map[string][]*proxyEntry),
		},
		ips: &ipActivity{
			list: make(map[string]*ipActivityEntry),
		},
		domainLimit: &domainActivityList{
			list: make(map[string]*domainActivity),
		},
	}
	c.load()

	if c.ProxyLength() != 1 {
		t.Errorf("ProxyLength = %d, want 1 (blocked IP should be excluded)", c.ProxyLength())
	}
	if !c.HasProxy("http://1.1.1.1:8080") {
		t.Error("non-blocked proxy should be loaded")
	}
	if c.HasProxy("http://2.2.2.2:8080") {
		t.Error("blocked proxy should not be loaded")
	}
}
