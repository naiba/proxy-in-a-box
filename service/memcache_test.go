package service

import (
	"fmt"
	"net/http"
	"sync"
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
			dl:             make(map[string][]*proxyEntry),
			targetFailures: make(map[string]map[string]time.Time),
			failureTTL:     defaultTargetFailureTTL,
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

func TestUpsertProxy_RejectsLockedIP(t *testing.T) {
	setupTestDB(t)
	c := newTestCache(t)

	proxyinabox.DB.Create(&proxyinabox.BlockedIP{
		IP:                  "5.6.7.8",
		ConsecutiveFailures: 5,
		LockedUntil:         time.Now().Add(24 * time.Hour),
	})
	c.LoadLockedIPs()

	p := proxyinabox.Proxy{
		IP: "5.6.7.8", Port: "1080", Protocol: "socks5",
		Source: "test", LastVerify: time.Now(),
	}
	if err := c.UpsertProxy(p); err == nil {
		t.Fatal("UpsertProxy should reject locked IP")
	}

	if c.ProxyLength() != 0 {
		t.Errorf("ProxyLength = %d, want 0 (locked IP should not be added)", c.ProxyLength())
	}

	var count int64
	proxyinabox.DB.Model(&proxyinabox.BlockedIP{}).Where("ip = ?", "5.6.7.8").Count(&count)
	if count != 1 {
		t.Errorf("blocked_ips count = %d, want 1 (UpsertProxy should NOT clear lock)", count)
	}
}

func TestUpsertProxy_AllowsExpiredLockIP(t *testing.T) {
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

	if c.ProxyLength() != 1 {
		t.Errorf("ProxyLength = %d, want 1", c.ProxyLength())
	}
	if !c.HasProxy("socks5://5.6.7.8:1080") {
		t.Error("proxy should be in cache after UpsertProxy with expired lock")
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
	c.MarkVerifySuccess(dbProxy, 42, newTime, false)

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

func TestMarkVerifySuccess_RestoresRemovedProxyWithStoredMetadata(t *testing.T) {
	setupTestDB(t)
	c := newTestCache(t)

	oldTime := time.Now().Add(-time.Hour)
	p := proxyinabox.Proxy{
		IP: "1.1.1.2", Port: "8080", Protocol: "http",
		Country: "US", Provence: "CA", Source: "source-a", Delay: 99, LastVerify: oldTime,
	}
	if err := c.UpsertProxy(p); err != nil {
		t.Fatalf("UpsertProxy failed: %v", err)
	}

	var stored proxyinabox.Proxy
	if err := proxyinabox.DB.Where("ip = ?", p.IP).First(&stored).Error; err != nil {
		t.Fatalf("load stored proxy: %v", err)
	}
	c.MarkVerifyFailed(stored)
	if c.ProxyLength() != 0 {
		t.Fatalf("ProxyLength = %d after failure, want 0", c.ProxyLength())
	}

	verifiedAt := time.Now().Round(0)
	c.MarkVerifySuccess(stored, 12, verifiedAt, false)

	all := c.GetAllProxies()
	if len(all) != 1 {
		t.Fatalf("GetAllProxies = %d after successful re-verification, want 1", len(all))
	}
	restored := all[0]
	if restored.ID != stored.ID {
		t.Errorf("restored ID = %d, want %d", restored.ID, stored.ID)
	}
	if restored.Country != stored.Country || restored.Provence != stored.Provence || restored.Source != stored.Source {
		t.Errorf("restored metadata = {%q %q %q}, want {%q %q %q}", restored.Country, restored.Provence, restored.Source, stored.Country, stored.Provence, stored.Source)
	}
	if restored.Delay != 12 || !restored.LastVerify.Equal(verifiedAt) {
		t.Errorf("restored verification = {delay:%d time:%v}, want {delay:12 time:%v}", restored.Delay, restored.LastVerify, verifiedAt)
	}
}

func TestMarkVerifySuccess_DoesNotUndoActiveIPLock(t *testing.T) {
	setupTestDB(t)
	c := newTestCache(t)
	p := proxyinabox.Proxy{
		IP: "1.1.1.3", Port: "8080", Protocol: "http",
		Source: "source-a", LastVerify: time.Now().Add(-time.Hour),
	}
	if err := c.UpsertProxy(p); err != nil {
		t.Fatalf("UpsertProxy failed: %v", err)
	}

	var stored proxyinabox.Proxy
	if err := proxyinabox.DB.Where("ip = ?", p.IP).First(&stored).Error; err != nil {
		t.Fatalf("load stored proxy: %v", err)
	}
	for range proxyFailureLockThreshold {
		c.RecordFailure(p.IP)
	}
	if !c.IsIPLocked(p.IP) {
		t.Fatal("precondition: IP should be locked")
	}

	c.MarkVerifySuccess(stored, 10, time.Now(), false)

	if !c.IsIPLocked(p.IP) {
		t.Fatal("successful stale verification must not clear an active IP lock")
	}
	if c.ProxyLength() != 0 {
		t.Fatalf("ProxyLength = %d, want 0 for locked IP", c.ProxyLength())
	}
	var blockedCount int64
	proxyinabox.DB.Model(&proxyinabox.BlockedIP{}).Where("ip = ?", p.IP).Count(&blockedCount)
	if blockedCount != 1 {
		t.Fatalf("blocked_ips count = %d, want 1", blockedCount)
	}
	var proxyCount int64
	proxyinabox.DB.Model(&proxyinabox.Proxy{}).Where("ip = ?", p.IP).Count(&proxyCount)
	if proxyCount != 1 {
		t.Fatalf("proxy DB count = %d, want 1 quarantined row after lock", proxyCount)
	}
	var quarantined proxyinabox.Proxy
	proxyinabox.DB.Where("ip = ?", p.IP).First(&quarantined)
	if quarantined.Available {
		t.Fatal("proxy should remain unavailable while its IP lock is active")
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
	if updated.Available {
		t.Error("failed proxy should be persisted as unavailable")
	}
	if updated.ConsecutiveFailures != 1 {
		t.Errorf("ConsecutiveFailures = %d, want 1", updated.ConsecutiveFailures)
	}
	remaining := time.Until(updated.NextVerifyAt)
	if remaining < 29*time.Minute || remaining > 31*time.Minute {
		t.Errorf("first retry delay = %s, want about 30m", remaining)
	}
	if c.IsProxyValidationDue(dbProxy.URI()) {
		t.Error("failed proxy should not be eligible before NextVerifyAt")
	}
}

func TestMarkVerifyFailed_UsesEscalatingEndpointBackoff(t *testing.T) {
	setupTestDB(t)
	c := newTestCache(t)
	p := proxyinabox.Proxy{
		IP: "3.3.3.8", Port: "8080", Protocol: "http",
		Source: "test", LastVerify: time.Now(),
	}
	if err := c.UpsertProxy(p); err != nil {
		t.Fatalf("UpsertProxy: %v", err)
	}

	for i, want := range proxyRetryBackoff {
		var stored proxyinabox.Proxy
		if err := proxyinabox.DB.Where("ip = ?", p.IP).First(&stored).Error; err != nil {
			t.Fatalf("load proxy before failure %d: %v", i+1, err)
		}
		before := time.Now()
		if got := c.MarkVerifyFailed(stored); got != i+1 {
			t.Fatalf("failure count = %d, want %d", got, i+1)
		}
		if err := proxyinabox.DB.Where("id = ?", stored.ID).First(&stored).Error; err != nil {
			t.Fatalf("reload proxy after failure %d: %v", i+1, err)
		}
		gotBackoff := stored.NextVerifyAt.Sub(before)
		if gotBackoff < want-time.Second || gotBackoff > want+time.Second {
			t.Fatalf("failure %d backoff = %s, want %s", i+1, gotBackoff, want)
		}
	}
}

func TestMarkVerifySuccessClearsBackoffAndRecordsDeepCheck(t *testing.T) {
	setupTestDB(t)
	c := newTestCache(t)
	p := proxyinabox.Proxy{
		IP: "3.3.3.9", Port: "8080", Protocol: "http",
		Source: "test", LastVerify: time.Now().Add(-time.Hour),
	}
	if err := c.UpsertProxy(p); err != nil {
		t.Fatalf("UpsertProxy: %v", err)
	}
	var stored proxyinabox.Proxy
	if err := proxyinabox.DB.Where("ip = ?", p.IP).First(&stored).Error; err != nil {
		t.Fatalf("load proxy: %v", err)
	}
	c.MarkVerifyFailed(stored)

	verifiedAt := time.Now().Round(0)
	c.MarkVerifySuccess(stored, 3, verifiedAt, true)
	if err := proxyinabox.DB.Where("id = ?", stored.ID).First(&stored).Error; err != nil {
		t.Fatalf("reload proxy: %v", err)
	}
	if !stored.Available || stored.ConsecutiveFailures != 0 || !stored.NextVerifyAt.IsZero() {
		t.Fatalf("recovered state = {available:%t failures:%d next:%v}", stored.Available, stored.ConsecutiveFailures, stored.NextVerifyAt)
	}
	if !stored.LastDeepVerify.Equal(verifiedAt) {
		t.Fatalf("LastDeepVerify = %v, want %v", stored.LastDeepVerify, verifiedAt)
	}
	if !c.IsProxyValidationDue(p.URI()) {
		t.Fatal("recovered proxy should clear its retry delay")
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

func TestRecordFailure_AtThreshold_QuarantinesDBAndCache(t *testing.T) {
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
	if proxyCount != 1 {
		t.Errorf("DB proxy count = %d, want 1 quarantined row", proxyCount)
	}
	var quarantined proxyinabox.Proxy
	proxyinabox.DB.Where("ip = ?", "5.6.7.8").First(&quarantined)
	if quarantined.Available {
		t.Error("proxy should be marked unavailable at the lock threshold")
	}

	var b proxyinabox.BlockedIP
	proxyinabox.DB.First(&b, "ip = ?", "5.6.7.8")
	if b.LockedUntil.Before(time.Now()) {
		t.Error("LockedUntil should be in the future")
	}
}

func TestQuarantinedProxyRecoversAfterLockExpires(t *testing.T) {
	setupTestDB(t)
	c := newTestCache(t)
	p := proxyinabox.Proxy{
		IP: "5.6.7.9", Port: "8080", Protocol: "http",
		Source: "test", LastVerify: time.Now().Add(-time.Hour),
	}
	if err := c.UpsertProxy(p); err != nil {
		t.Fatalf("UpsertProxy: %v", err)
	}
	for range proxyFailureLockThreshold {
		c.RecordFailure(p.IP)
	}

	var stored proxyinabox.Proxy
	if err := proxyinabox.DB.Where("ip = ?", p.IP).First(&stored).Error; err != nil {
		t.Fatalf("load quarantined proxy: %v", err)
	}
	if stored.Available {
		t.Fatal("precondition: proxy should be quarantined")
	}

	expired := time.Now().Add(-time.Minute)
	c.lockedIPs.Store(p.IP, expired)
	if err := proxyinabox.DB.Model(&proxyinabox.BlockedIP{}).Where("ip = ?", p.IP).Update("locked_until", expired).Error; err != nil {
		t.Fatalf("expire lock: %v", err)
	}
	c.MarkVerifySuccess(stored, 4, time.Now(), false)

	if !c.HasProxy(p.URI()) {
		t.Fatal("successful health check should restore the proxy after lock expiry")
	}
	if err := proxyinabox.DB.Where("id = ?", stored.ID).First(&stored).Error; err != nil {
		t.Fatalf("reload recovered proxy: %v", err)
	}
	if !stored.Available {
		t.Fatal("recovered proxy should be persisted as available")
	}
	var failures int64
	proxyinabox.DB.Model(&proxyinabox.BlockedIP{}).Where("ip = ?", p.IP).Count(&failures)
	if failures != 0 {
		t.Fatalf("failure rows = %d, want 0 after recovery", failures)
	}
}

func TestRecordFailure_NotClearedByUpsertProxy(t *testing.T) {
	setupTestDB(t)
	c := newTestCache(t)

	for i := 0; i < proxyFailureLockThreshold; i++ {
		c.RecordFailure("10.0.0.1")
	}
	if !c.IsIPLocked("10.0.0.1") {
		t.Fatal("IP should be locked after reaching threshold")
	}

	err := c.UpsertProxy(proxyinabox.Proxy{
		IP: "10.0.0.1", Port: "8080", Protocol: "http",
		Source: "test", LastVerify: time.Now(),
	})

	if err == nil {
		t.Error("UpsertProxy should reject locked IP")
	}
	if !c.IsIPLocked("10.0.0.1") {
		t.Error("IP should still be locked after rejected UpsertProxy")
	}
	var count int64
	proxyinabox.DB.Model(&proxyinabox.BlockedIP{}).Where("ip = ?", "10.0.0.1").Count(&count)
	if count != 1 {
		t.Errorf("blocked_ips count = %d, want 1 (lock should not be cleared)", count)
	}
}

func TestUpsertProxy_ClearsSubThresholdFailures(t *testing.T) {
	setupTestDB(t)
	c := newTestCache(t)
	p := proxyinabox.Proxy{
		IP: "10.0.0.2", Port: "8080", Protocol: "http",
		Source: "test", LastVerify: time.Now(),
	}
	if err := c.UpsertProxy(p); err != nil {
		t.Fatalf("initial UpsertProxy: %v", err)
	}
	c.RecordFailure(p.IP)
	c.RecordFailure(p.IP)

	p.LastVerify = time.Now()
	if err := c.UpsertProxy(p); err != nil {
		t.Fatalf("successful revalidation UpsertProxy: %v", err)
	}

	var count int64
	proxyinabox.DB.Model(&proxyinabox.BlockedIP{}).Where("ip = ?", p.IP).Count(&count)
	if count != 0 {
		t.Fatalf("failure rows = %d, want 0 after successful revalidation", count)
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

func TestCleanupStaleProxies_OnlyRemovesStaleURIForSharedIP(t *testing.T) {
	setupTestDB(t)
	c := newTestCache(t)
	c.UpsertProxy(proxyinabox.Proxy{
		IP: "3.3.3.4", Port: "8080", Protocol: "http",
		Source: "test", LastVerify: time.Now().Add(-7 * 30 * 24 * time.Hour),
	})
	c.UpsertProxy(proxyinabox.Proxy{
		IP: "3.3.3.4", Port: "3128", Protocol: "http",
		Source: "test", LastVerify: time.Now(),
	})

	c.CleanupStaleProxies(6 * 30 * 24 * time.Hour)

	if c.HasProxy("http://3.3.3.4:8080") {
		t.Fatal("stale endpoint should be removed")
	}
	if !c.HasProxy("http://3.3.3.4:3128") {
		t.Fatal("fresh endpoint sharing the same IP should remain")
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
		domains: &domainScheduling{dl: make(map[string][]*proxyEntry)},
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

func TestLoadExcludesPersistedUnavailableProxies(t *testing.T) {
	db := setupTestDB(t)
	p := proxyinabox.Proxy{
		IP: "3.3.3.5", Port: "8080", Protocol: "http",
		Source: "test", LastVerify: time.Now(),
	}
	if err := db.Create(&p).Error; err != nil {
		t.Fatalf("create proxy: %v", err)
	}
	if err := db.Model(&p).Update("available", false).Error; err != nil {
		t.Fatalf("mark unavailable: %v", err)
	}

	c := newTestCache(t)
	c.load()
	if c.ProxyLength() != 0 {
		t.Fatalf("ProxyLength = %d, want 0 for persisted unavailable proxy", c.ProxyLength())
	}
}

func TestMarkProxyUnavailablePersistsAcrossReload(t *testing.T) {
	setupTestDB(t)
	c := newTestCache(t)
	p := proxyinabox.Proxy{
		IP: "3.3.3.6", Port: "8080", Protocol: "http",
		Source: "test", LastVerify: time.Now(),
	}
	if err := c.UpsertProxy(p); err != nil {
		t.Fatalf("UpsertProxy: %v", err)
	}

	c.MarkProxyUnavailable(p.URI())
	if c.ProxyLength() != 0 {
		t.Fatalf("ProxyLength = %d after quarantine, want 0", c.ProxyLength())
	}
	var stored proxyinabox.Proxy
	if err := proxyinabox.DB.Where("ip = ?", p.IP).First(&stored).Error; err != nil {
		t.Fatalf("load stored proxy: %v", err)
	}
	if stored.Available {
		t.Fatal("quarantine was not persisted")
	}

	reloaded := newTestCache(t)
	reloaded.load()
	if reloaded.ProxyLength() != 0 {
		t.Fatalf("reloaded ProxyLength = %d, unavailable proxy must not return after restart", reloaded.ProxyLength())
	}
	if reloaded.IsProxyValidationDue(p.URI()) {
		t.Fatal("persisted retry delay should survive cache reload")
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

func TestPickProxy_UsesLeastUsedProxyAcrossTargets(t *testing.T) {
	setupTestDB(t)
	c := newTestCache(t)

	firstProxy := proxyinabox.Proxy{
		IP: "1.1.1.1", Port: "8080", Protocol: "http", Source: "test", LastVerify: time.Now(),
	}
	secondProxy := proxyinabox.Proxy{
		IP: "2.2.2.2", Port: "8080", Protocol: "http", Source: "test", LastVerify: time.Now(),
	}
	c.UpsertProxy(firstProxy)
	c.UpsertProxy(secondProxy)

	firstRequest, _ := http.NewRequest(http.MethodGet, "http://first.example/resource", nil)
	selected, err := c.PickProxy(firstRequest)
	if err != nil {
		t.Fatalf("first PickProxy: %v", err)
	}
	if selected != firstProxy.URI() {
		t.Fatalf("first PickProxy = %q, want %q", selected, firstProxy.URI())
	}

	secondRequest, _ := http.NewRequest(http.MethodGet, "http://second.example/resource", nil)
	selected, err = c.PickProxy(secondRequest)
	if err != nil {
		t.Fatalf("second PickProxy: %v", err)
	}
	if selected != secondProxy.URI() {
		t.Errorf("second PickProxy = %q, want least-used %q", selected, secondProxy.URI())
	}
}

func TestPickProxy_SkipsTargetCircuitUntilExpiry(t *testing.T) {
	setupTestDB(t)
	c := newTestCache(t)

	failedProxy := proxyinabox.Proxy{
		IP: "1.1.1.1", Port: "8080", Protocol: "http", Source: "test", LastVerify: time.Now(),
	}
	healthyProxy := proxyinabox.Proxy{
		IP: "2.2.2.2", Port: "8080", Protocol: "http", Source: "test", LastVerify: time.Now(),
	}
	c.UpsertProxy(failedProxy)
	c.UpsertProxy(healthyProxy)

	target := "target.example"
	c.MarkProxyTargetFailure(failedProxy.URI(), target)
	request, _ := http.NewRequest(http.MethodGet, "http://"+target+"/resource", nil)
	selected, err := c.PickProxy(request)
	if err != nil {
		t.Fatalf("PickProxy with circuit open: %v", err)
	}
	if selected != healthyProxy.URI() {
		t.Fatalf("PickProxy with circuit open = %q, want %q", selected, healthyProxy.URI())
	}

	c.domains.l.Lock()
	c.domains.targetFailures[target][failedProxy.URI()] = time.Now().Add(-time.Second)
	c.domains.l.Unlock()

	selected, err = c.PickProxy(request)
	if err != nil {
		t.Fatalf("PickProxy after circuit expiry: %v", err)
	}
	if selected != failedProxy.URI() {
		t.Errorf("PickProxy after circuit expiry = %q, want %q", selected, failedProxy.URI())
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

// --- 并发竞态测试 ---

func TestRecordFailure_ConcurrentSameIP(t *testing.T) {
	setupTestDB(t)
	c := newTestCache(t)

	c.UpsertProxy(proxyinabox.Proxy{
		IP: "race.ip", Port: "8080", Protocol: "http",
		Source: "test", LastVerify: time.Now(),
	})

	const goroutines = 20
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			c.RecordFailure("race.ip")
		}()
	}
	wg.Wait()

	var b proxyinabox.BlockedIP
	proxyinabox.DB.First(&b, "ip = ?", "race.ip")
	if b.ConsecutiveFailures != goroutines {
		t.Errorf("ConsecutiveFailures = %d, want %d (race condition detected)", b.ConsecutiveFailures, goroutines)
	}
}

func TestRecordFailure_ConcurrentDifferentIPs(t *testing.T) {
	setupTestDB(t)
	c := newTestCache(t)

	const numIPs = 10
	for i := 0; i < numIPs; i++ {
		c.UpsertProxy(proxyinabox.Proxy{
			IP: fmt.Sprintf("10.0.0.%d", i), Port: "8080", Protocol: "http",
			Source: "test", LastVerify: time.Now(),
		})
	}

	var wg sync.WaitGroup
	for i := 0; i < numIPs; i++ {
		ip := fmt.Sprintf("10.0.0.%d", i)
		for j := 0; j < proxyFailureLockThreshold; j++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				c.RecordFailure(ip)
			}()
		}
	}
	wg.Wait()

	for i := 0; i < numIPs; i++ {
		ip := fmt.Sprintf("10.0.0.%d", i)
		var b proxyinabox.BlockedIP
		proxyinabox.DB.First(&b, "ip = ?", ip)
		if b.ConsecutiveFailures != proxyFailureLockThreshold {
			t.Errorf("IP %s: ConsecutiveFailures = %d, want %d", ip, b.ConsecutiveFailures, proxyFailureLockThreshold)
		}
		if !c.IsIPLocked(ip) {
			t.Errorf("IP %s should be locked after %d failures", ip, proxyFailureLockThreshold)
		}
	}

	if c.ProxyLength() != 0 {
		t.Errorf("ProxyLength = %d, want 0 (all should be locked)", c.ProxyLength())
	}
}

func TestUpsertProxy_ConcurrentWithRecordFailure(t *testing.T) {
	setupTestDB(t)
	c := newTestCache(t)

	for i := 0; i < proxyFailureLockThreshold; i++ {
		c.RecordFailure("locked.ip")
	}
	if !c.IsIPLocked("locked.ip") {
		t.Fatal("IP should be locked")
	}

	const goroutines = 10
	var wg sync.WaitGroup
	wg.Add(goroutines)
	rejectedCount := 0
	var mu sync.Mutex
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			err := c.UpsertProxy(proxyinabox.Proxy{
				IP: "locked.ip", Port: "8080", Protocol: "http",
				Source: "test", LastVerify: time.Now(),
			})
			if err != nil {
				mu.Lock()
				rejectedCount++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	if rejectedCount != goroutines {
		t.Errorf("rejected = %d, want %d (all should be rejected for locked IP)", rejectedCount, goroutines)
	}
	if c.ProxyLength() != 0 {
		t.Errorf("ProxyLength = %d, want 0", c.ProxyLength())
	}
}
