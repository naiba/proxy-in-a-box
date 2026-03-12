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

// --- UpsertProxy ---

func TestUpsertProxy(t *testing.T) {
	setupTestDB(t)
	c := newTestCache(t)

	p := proxyinabox.Proxy{
		IP: "1.2.3.4", Port: "8080", Protocol: "http",
		Source: "test", LastVerify: time.Now(),
	}
	if err := c.UpsertProxy(p); err != nil {
		t.Fatalf("UpsertProxy failed: %v", err)
	}

	if c.ProxyLength() != 1 {
		t.Errorf("ProxyLength = %d, want 1", c.ProxyLength())
	}
	if !c.HasProxy("http://1.2.3.4:8080") {
		t.Error("HasProxy should return true")
	}

	var dbProxy proxyinabox.Proxy
	proxyinabox.DB.First(&dbProxy, "ip = ?", "1.2.3.4")
	if dbProxy.IP != "1.2.3.4" {
		t.Errorf("DB proxy IP = %q, want 1.2.3.4", dbProxy.IP)
	}
}

func TestUpsertProxy_DuplicateIP_NoGhostEntry(t *testing.T) {
	setupTestDB(t)
	c := newTestCache(t)

	first := proxyinabox.Proxy{
		IP: "1.2.3.4", Port: "8080", Protocol: "http",
		Source: "src-a", LastVerify: time.Now().Add(-2 * time.Hour),
	}
	c.UpsertProxy(first)

	second := proxyinabox.Proxy{
		IP: "1.2.3.4", Port: "8080", Protocol: "http",
		Source: "src-b", LastVerify: time.Now(),
	}
	c.UpsertProxy(second)

	if c.ProxyLength() != 1 {
		t.Errorf("ProxyLength = %d, want 1 (duplicate IP should replace, not append)", c.ProxyLength())
	}

	all := c.GetAllProxies()
	if len(all) != 1 {
		t.Fatalf("GetAllProxies returned %d, want 1", len(all))
	}
	if all[0].Source != "src-b" {
		t.Errorf("Source = %q, want src-b (latest should win)", all[0].Source)
	}
}

func TestUpsertProxy_ClearsOldFailureRecord(t *testing.T) {
	setupTestDB(t)
	c := newTestCache(t)

	proxyinabox.DB.Create(&proxyinabox.BlockedIP{
		IP:                  "5.6.7.8",
		ConsecutiveFailures: 5,
		LockedUntil:         time.Now().Add(-1 * time.Hour),
	})

	p := proxyinabox.Proxy{
		IP: "5.6.7.8", Port: "1080", Protocol: "socks5",
		Source: "test", LastVerify: time.Now(),
	}
	if err := c.UpsertProxy(p); err != nil {
		t.Fatalf("UpsertProxy failed: %v", err)
	}

	var count int64
	proxyinabox.DB.Model(&proxyinabox.BlockedIP{}).Where("ip = ?", "5.6.7.8").Count(&count)
	if count != 0 {
		t.Errorf("blocked_ips count = %d, want 0 (UpsertProxy should clear old failures)", count)
	}

	if c.ProxyLength() != 1 {
		t.Errorf("ProxyLength = %d, want 1", c.ProxyLength())
	}
	if !c.HasProxy("socks5://5.6.7.8:1080") {
		t.Error("proxy should be in cache after UpsertProxy")
	}
}

// --- MarkVerifySuccess ---

func TestMarkVerifySuccess_UpdatesDBAndCache(t *testing.T) {
	setupTestDB(t)
	c := newTestCache(t)

	oldTime := time.Now().Add(-1 * time.Hour)
	p := proxyinabox.Proxy{
		IP: "1.1.1.1", Port: "8080", Protocol: "http",
		Source: "test", Delay: 99, LastVerify: oldTime,
	}
	c.UpsertProxy(p)

	proxyinabox.DB.Create(&proxyinabox.BlockedIP{
		IP: "1.1.1.1", ConsecutiveFailures: 2,
	})

	var dbProxy proxyinabox.Proxy
	proxyinabox.DB.First(&dbProxy, "ip = ?", "1.1.1.1")

	newTime := time.Now()
	c.MarkVerifySuccess(dbProxy, 42, newTime)

	all := c.GetAllProxies()
	if len(all) != 1 {
		t.Fatalf("GetAllProxies = %d, want 1", len(all))
	}
	if all[0].Delay != 42 {
		t.Errorf("cache Delay = %d, want 42", all[0].Delay)
	}
	if all[0].LastVerify.Before(newTime.Add(-time.Second)) {
		t.Errorf("cache LastVerify not updated: %v", all[0].LastVerify)
	}

	var updated proxyinabox.Proxy
	proxyinabox.DB.First(&updated, "ip = ?", "1.1.1.1")
	if updated.Delay != 42 {
		t.Errorf("DB Delay = %d, want 42", updated.Delay)
	}

	var blockedCount int64
	proxyinabox.DB.Model(&proxyinabox.BlockedIP{}).Where("ip = ?", "1.1.1.1").Count(&blockedCount)
	if blockedCount != 0 {
		t.Errorf("blocked_ips count = %d, want 0 after MarkVerifySuccess", blockedCount)
	}
}

// --- MarkVerifyFailed ---

func TestMarkVerifyFailed_RemovesFromCacheKeepsDB(t *testing.T) {
	setupTestDB(t)
	c := newTestCache(t)

	oldTime := time.Now().Add(-1 * time.Hour)
	p := proxyinabox.Proxy{
		IP: "3.3.3.3", Port: "8080", Protocol: "http",
		Source: "test", LastVerify: oldTime,
	}
	c.UpsertProxy(p)

	var dbProxy proxyinabox.Proxy
	proxyinabox.DB.First(&dbProxy, "ip = ?", "3.3.3.3")
	c.MarkVerifyFailed(dbProxy)

	if c.ProxyLength() != 0 {
		t.Errorf("ProxyLength = %d, want 0 after MarkVerifyFailed", c.ProxyLength())
	}

	var dbCount int64
	proxyinabox.DB.Model(&proxyinabox.Proxy{}).Where("ip = ?", "3.3.3.3").Count(&dbCount)
	if dbCount != 1 {
		t.Errorf("DB proxy count = %d, want 1 (MarkVerifyFailed should not delete from DB)", dbCount)
	}

	var updated proxyinabox.Proxy
	proxyinabox.DB.First(&updated, "ip = ?", "3.3.3.3")
	if !updated.LastVerify.After(oldTime) {
		t.Error("DB last_verify should be updated to prevent re-selection")
	}
}

// --- RecordFailure ---

func TestRecordFailure_BelowThreshold(t *testing.T) {
	setupTestDB(t)
	c := newTestCache(t)

	c.UpsertProxy(proxyinabox.Proxy{
		IP: "1.2.3.4", Port: "8080", Protocol: "http",
		Source: "test", LastVerify: time.Now(),
	})

	c.RecordFailure("1.2.3.4")
	c.RecordFailure("1.2.3.4")

	if c.ProxyLength() != 1 {
		t.Error("proxy should still be in cache below threshold")
	}

	var b proxyinabox.BlockedIP
	proxyinabox.DB.First(&b, "ip = ?", "1.2.3.4")
	if b.ConsecutiveFailures != 2 {
		t.Errorf("ConsecutiveFailures = %d, want 2", b.ConsecutiveFailures)
	}
	if c.IsIPLocked("1.2.3.4") {
		t.Error("IP should not be locked with only 2 failures")
	}
}

func TestRecordFailure_AtThreshold_DeletesDBAndCache(t *testing.T) {
	setupTestDB(t)
	c := newTestCache(t)

	c.UpsertProxy(proxyinabox.Proxy{
		IP: "5.6.7.8", Port: "8080", Protocol: "http",
		Source: "test", LastVerify: time.Now(),
	})

	for i := 0; i < proxyFailureLockThreshold; i++ {
		c.RecordFailure("5.6.7.8")
	}

	if c.ProxyLength() != 0 {
		t.Errorf("ProxyLength = %d, want 0 after lock threshold", c.ProxyLength())
	}
	if !c.IsIPLocked("5.6.7.8") {
		t.Error("IP should be locked after reaching threshold")
	}

	var proxyCount int64
	proxyinabox.DB.Model(&proxyinabox.Proxy{}).Where("ip = ?", "5.6.7.8").Count(&proxyCount)
	if proxyCount != 0 {
		t.Errorf("DB proxy count = %d, want 0 (RecordFailure should delete proxies on lock)", proxyCount)
	}

	var b proxyinabox.BlockedIP
	proxyinabox.DB.First(&b, "ip = ?", "5.6.7.8")
	if b.LockedUntil.Before(time.Now()) {
		t.Error("LockedUntil should be in the future")
	}
}

func TestRecordFailure_ClearedByUpsertProxy(t *testing.T) {
	setupTestDB(t)
	c := newTestCache(t)

	for i := 0; i < proxyFailureLockThreshold; i++ {
		c.RecordFailure("10.0.0.1")
	}
	if !c.IsIPLocked("10.0.0.1") {
		t.Fatal("IP should be locked before UpsertProxy")
	}

	c.UpsertProxy(proxyinabox.Proxy{
		IP: "10.0.0.1", Port: "8080", Protocol: "http",
		Source: "test", LastVerify: time.Now(),
	})

	if c.IsIPLocked("10.0.0.1") {
		t.Error("IP should not be locked after UpsertProxy")
	}
	var count int64
	proxyinabox.DB.Model(&proxyinabox.BlockedIP{}).Where("ip = ?", "10.0.0.1").Count(&count)
	if count != 0 {
		t.Errorf("blocked_ips count = %d, want 0 after UpsertProxy clears failure", count)
	}
}

// --- IsIPLocked ---

func TestIsIPLocked_Expired(t *testing.T) {
	c := newTestCache(t)
	c.lockedIPs.Store("expired.ip", time.Now().Add(-1*time.Hour))

	if c.IsIPLocked("expired.ip") {
		t.Error("expired lock should return false")
	}
	if _, ok := c.lockedIPs.Load("expired.ip"); ok {
		t.Error("expired entry should be removed from cache")
	}
}

func TestIsIPLocked_NotLocked(t *testing.T) {
	c := newTestCache(t)
	if c.IsIPLocked("unknown.ip") {
		t.Error("unknown IP should not be locked")
	}
}

// --- LoadLockedIPs ---

func TestLoadLockedIPs(t *testing.T) {
	setupTestDB(t)
	c := newTestCache(t)

	proxyinabox.DB.Create(&proxyinabox.BlockedIP{
		IP: "active.lock", ConsecutiveFailures: 3,
		LockedUntil: time.Now().Add(24 * time.Hour),
	})
	proxyinabox.DB.Create(&proxyinabox.BlockedIP{
		IP: "expired.lock", ConsecutiveFailures: 3,
		LockedUntil: time.Now().Add(-24 * time.Hour),
	})

	c.LoadLockedIPs()

	if !c.IsIPLocked("active.lock") {
		t.Error("active lock should be loaded")
	}
	if c.IsIPLocked("expired.lock") {
		t.Error("expired lock should not be loaded")
	}
}

// --- CleanupStaleProxies ---

func TestCleanupStaleProxies_RemovesFromDBAndCache(t *testing.T) {
	setupTestDB(t)
	c := newTestCache(t)

	c.UpsertProxy(proxyinabox.Proxy{
		IP: "1.1.1.1", Port: "8080", Protocol: "http",
		Source: "test", LastVerify: time.Now().Add(-24 * time.Hour),
	})
	c.UpsertProxy(proxyinabox.Proxy{
		IP: "2.2.2.2", Port: "8080", Protocol: "http",
		Source: "test", LastVerify: time.Now().Add(-7 * 30 * 24 * time.Hour),
	})

	if c.ProxyLength() != 2 {
		t.Fatalf("ProxyLength = %d, want 2 before cleanup", c.ProxyLength())
	}

	c.CleanupStaleProxies(6 * 30 * 24 * time.Hour)

	if c.ProxyLength() != 1 {
		t.Errorf("ProxyLength = %d, want 1 after cleanup", c.ProxyLength())
	}
	if !c.HasProxy("http://1.1.1.1:8080") {
		t.Error("fresh proxy should remain")
	}
	if c.HasProxy("http://2.2.2.2:8080") {
		t.Error("stale proxy should be removed from cache")
	}

	var dbCount int64
	proxyinabox.DB.Model(&proxyinabox.Proxy{}).Count(&dbCount)
	if dbCount != 1 {
		t.Errorf("DB proxy count = %d, want 1", dbCount)
	}
}

func TestCleanupStaleProxies_NoStale(t *testing.T) {
	setupTestDB(t)
	c := newTestCache(t)

	c.UpsertProxy(proxyinabox.Proxy{
		IP: "3.3.3.3", Port: "8080", Protocol: "http",
		Source: "test", LastVerify: time.Now(),
	})

	c.CleanupStaleProxies(6 * 30 * 24 * time.Hour)

	if c.ProxyLength() != 1 {
		t.Errorf("ProxyLength = %d, want 1 (nothing stale)", c.ProxyLength())
	}
}

// --- Load ---

func TestLoadExcludesBlockedIPs(t *testing.T) {
	db := setupTestDB(t)

	db.Create(&proxyinabox.Proxy{
		IP: "1.1.1.1", Port: "8080", Protocol: "http", Source: "test", LastVerify: time.Now(),
	})
	db.Create(&proxyinabox.Proxy{
		IP: "2.2.2.2", Port: "8080", Protocol: "http", Source: "test", LastVerify: time.Now(),
	})
	db.Create(&proxyinabox.BlockedIP{
		IP: "2.2.2.2", ConsecutiveFailures: 3,
		LockedUntil: time.Now().Add(24 * time.Hour),
	})

	c := &MemCache{
		proxies: &proxyList{
			pl:    make([]*proxyEntry, 0),
			index: make(map[string]struct{}),
		},
		domains:     &domainScheduling{dl: make(map[string][]*proxyEntry)},
		ips:         &ipActivity{list: make(map[string]*ipActivityEntry)},
		domainLimit: &domainActivityList{list: make(map[string]*domainActivity)},
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

// --- 代理池读取 ---

func TestGetProxy_RoundRobin(t *testing.T) {
	setupTestDB(t)
	c := newTestCache(t)

	for _, p := range []proxyinabox.Proxy{
		{IP: "1.1.1.1", Port: "8080", Protocol: "http", Source: "test", LastVerify: time.Now()},
		{IP: "2.2.2.2", Port: "8080", Protocol: "http", Source: "test", LastVerify: time.Now()},
	} {
		c.UpsertProxy(p)
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

	c.UpsertProxy(proxyinabox.Proxy{
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

	c.UpsertProxy(proxyinabox.Proxy{
		IP: "1.1.1.1", Port: "8080", Protocol: "http", Source: "test", LastVerify: time.Now(),
	})
	c.UpsertProxy(proxyinabox.Proxy{
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

func TestGetAllProxies(t *testing.T) {
	setupTestDB(t)
	c := newTestCache(t)

	proxies := []proxyinabox.Proxy{
		{IP: "1.1.1.1", Port: "8080", Protocol: "http", Source: "a", LastVerify: time.Now()},
		{IP: "2.2.2.2", Port: "3128", Protocol: "https", Source: "b", LastVerify: time.Now()},
	}
	for _, p := range proxies {
		c.UpsertProxy(p)
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
