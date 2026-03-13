package service

import (
	"testing"
	"time"

	"github.com/naiba/proxyinabox"
)

// --- MarkVerifySuccess 多端口回归测试 ---

func TestMarkVerifySuccess_MultiPort_UpdatesCorrectEntry(t *testing.T) {
	setupTestDB(t)
	c := newTestCache(t)

	baseTime := time.Now().Add(-3 * time.Hour)

	// given: 同 IP 两个端口共存于缓存
	p1 := proxyinabox.Proxy{
		IP: "1.1.1.1", Port: "8080", Protocol: "http",
		Source: "src-a", LastVerify: baseTime,
	}
	p2 := proxyinabox.Proxy{
		IP: "1.1.1.1", Port: "3128", Protocol: "http",
		Source: "src-b", LastVerify: baseTime,
	}
	c.UpsertProxy(p1)
	c.UpsertProxy(p2)
	if c.ProxyLength() != 2 {
		t.Fatalf("precondition: want 2 entries, got %d", c.ProxyLength())
	}

	// when: 对 port 8080 执行验证成功（用 DB 查出的带 ID 对象模拟真实流程）
	var dbP1 proxyinabox.Proxy
	proxyinabox.DB.Where("ip = ? AND port = ?", "1.1.1.1", "8080").First(&dbP1)
	newTime := time.Now()
	c.MarkVerifySuccess(dbP1, 5, newTime)

	// then: 只有 port 8080 的 LastVerify 被更新，port 3128 保持旧值
	for _, p := range c.GetAllProxies() {
		if p.IP == "1.1.1.1" && p.Port == "8080" {
			if !p.LastVerify.Equal(newTime) {
				t.Errorf("port 8080: LastVerify not updated, got %v", p.LastVerify)
			}
			if p.Delay != 5 {
				t.Errorf("port 8080: Delay not updated, got %d", p.Delay)
			}
		}
		if p.IP == "1.1.1.1" && p.Port == "3128" {
			if !p.LastVerify.Equal(baseTime) {
				t.Errorf("port 3128: LastVerify should remain unchanged, got %v", p.LastVerify)
			}
		}
	}
}

func TestMarkVerifySuccess_MultiPort_EachPortUpdatedIndependently(t *testing.T) {
	setupTestDB(t)
	c := newTestCache(t)

	baseTime := time.Now().Add(-3 * time.Hour)
	p1 := proxyinabox.Proxy{
		IP: "2.2.2.2", Port: "8080", Protocol: "http",
		Source: "src-a", LastVerify: baseTime,
	}
	p2 := proxyinabox.Proxy{
		IP: "2.2.2.2", Port: "3128", Protocol: "http",
		Source: "src-b", LastVerify: baseTime,
	}
	c.UpsertProxy(p1)
	c.UpsertProxy(p2)

	// when: 分别对两个端口执行验证成功
	var dbP1, dbP2 proxyinabox.Proxy
	proxyinabox.DB.Where("ip = ? AND port = ?", "2.2.2.2", "8080").First(&dbP1)
	proxyinabox.DB.Where("ip = ? AND port = ?", "2.2.2.2", "3128").First(&dbP2)

	time1 := time.Now()
	c.MarkVerifySuccess(dbP1, 3, time1)
	time2 := time.Now().Add(time.Second)
	c.MarkVerifySuccess(dbP2, 7, time2)

	// then: 两个端口各自有正确的 LastVerify 和 Delay
	for _, p := range c.GetAllProxies() {
		if p.IP == "2.2.2.2" && p.Port == "8080" {
			if !p.LastVerify.Equal(time1) {
				t.Errorf("port 8080: want LastVerify=%v, got %v", time1, p.LastVerify)
			}
			if p.Delay != 3 {
				t.Errorf("port 8080: want Delay=3, got %d", p.Delay)
			}
		}
		if p.IP == "2.2.2.2" && p.Port == "3128" {
			if !p.LastVerify.Equal(time2) {
				t.Errorf("port 3128: want LastVerify=%v, got %v", time2, p.LastVerify)
			}
			if p.Delay != 7 {
				t.Errorf("port 3128: want Delay=7, got %d", p.Delay)
			}
		}
	}
}

// --- MarkVerifyFailed 多端口回归测试 ---

func TestMarkVerifyFailed_MultiPort_OnlyRemovesFailedPort(t *testing.T) {
	setupTestDB(t)
	c := newTestCache(t)

	p1 := proxyinabox.Proxy{
		IP: "3.3.3.3", Port: "8080", Protocol: "http",
		Source: "src-a", LastVerify: time.Now(),
	}
	p2 := proxyinabox.Proxy{
		IP: "3.3.3.3", Port: "3128", Protocol: "http",
		Source: "src-b", LastVerify: time.Now(),
	}
	c.UpsertProxy(p1)
	c.UpsertProxy(p2)

	// when: port 8080 验证失败
	var dbP1 proxyinabox.Proxy
	proxyinabox.DB.Where("ip = ? AND port = ?", "3.3.3.3", "8080").First(&dbP1)
	c.MarkVerifyFailed(dbP1)

	// then: 只有 port 8080 被移除，port 3128 仍在
	if c.ProxyLength() != 1 {
		t.Fatalf("want 1 entry after MarkVerifyFailed, got %d", c.ProxyLength())
	}
	if c.HasProxy("http://3.3.3.3:8080") {
		t.Error("failed port 8080 should be removed")
	}
	if !c.HasProxy("http://3.3.3.3:3128") {
		t.Error("healthy port 3128 should remain")
	}
}

func TestMarkVerifyFailed_UpdatesDBLastVerify(t *testing.T) {
	setupTestDB(t)
	c := newTestCache(t)

	oldTime := time.Now().Add(-3 * time.Hour)
	p := proxyinabox.Proxy{
		IP: "4.4.4.4", Port: "8080", Protocol: "http",
		Source: "test", LastVerify: oldTime,
	}
	c.UpsertProxy(p)

	// when: 用 DB 查出的带 ID 对象执行 MarkVerifyFailed
	var dbProxy proxyinabox.Proxy
	proxyinabox.DB.Where("ip = ?", "4.4.4.4").First(&dbProxy)
	c.MarkVerifyFailed(dbProxy)

	// then: DB 中 last_verify 被更新为当前时间
	var updated proxyinabox.Proxy
	proxyinabox.DB.Where("ip = ?", "4.4.4.4").First(&updated)
	if time.Since(updated.LastVerify) > time.Minute {
		t.Errorf("DB last_verify should be updated to now, got %v", updated.LastVerify)
	}
}

// --- DB-内存一致性测试 ---

func TestMarkVerifySuccess_DBAndCacheConsistency(t *testing.T) {
	setupTestDB(t)
	c := newTestCache(t)

	oldTime := time.Now().Add(-2 * time.Hour)
	p := proxyinabox.Proxy{
		IP: "5.5.5.5", Port: "8080", Protocol: "http",
		Source: "test", LastVerify: oldTime, Delay: 10,
	}
	c.UpsertProxy(p)

	// when: 模拟完整的 GetUnVerified → MarkVerifySuccess 流程
	ps := &ProxyService{DB: proxyinabox.DB}
	unverified, err := ps.GetUnVerified()
	if err != nil {
		t.Fatalf("GetUnVerified error: %v", err)
	}
	if len(unverified) == 0 {
		t.Fatal("should have 1 unverified proxy")
	}

	freshTime := time.Now()
	c.MarkVerifySuccess(unverified[0], 3, freshTime)

	// then: DB 和内存都反映更新
	for _, cached := range c.GetAllProxies() {
		if cached.IP == "5.5.5.5" {
			if !cached.LastVerify.Equal(freshTime) {
				t.Errorf("cache LastVerify not updated: got %v, want %v", cached.LastVerify, freshTime)
			}
			if cached.Delay != 3 {
				t.Errorf("cache Delay not updated: got %d, want 3", cached.Delay)
			}
		}
	}

	var dbProxy proxyinabox.Proxy
	proxyinabox.DB.Where("ip = ?", "5.5.5.5").First(&dbProxy)
	if !dbProxy.LastVerify.Equal(freshTime) {
		t.Errorf("DB LastVerify not updated: got %v, want %v", dbProxy.LastVerify, freshTime)
	}
}

// --- Dashboard 完整流程测试 ---

func TestDashboard_FullVerifyFlow_ReflectsUpdate(t *testing.T) {
	setupTestDB(t)
	c := newTestCache(t)

	staleTime := time.Now().Add(-30 * time.Minute)
	p := proxyinabox.Proxy{
		IP: "6.6.6.6", Port: "8080", Protocol: "http",
		Source: "test", LastVerify: staleTime, Delay: 5,
	}
	c.UpsertProxy(p)

	// when: 模拟 Verify() 流程
	ps := &ProxyService{DB: proxyinabox.DB}
	unverified, _ := ps.GetUnVerified()
	if len(unverified) == 0 {
		t.Fatal("should have 1 stale proxy")
	}
	freshTime := time.Now()
	c.MarkVerifySuccess(unverified[0], 2, freshTime)

	// then: Dashboard (GetAllProxies) 反映更新
	for _, cached := range c.GetAllProxies() {
		if cached.IP == "6.6.6.6" {
			if !cached.LastVerify.Equal(freshTime) {
				t.Errorf("dashboard LastVerify stale: got %v, want %v", cached.LastVerify, freshTime)
			}
		}
	}
}

// --- DB 唯一约束测试 ---

func TestDB_UniqueIndex_PreventsExactDuplicate(t *testing.T) {
	setupTestDB(t)

	p1 := proxyinabox.Proxy{
		IP: "7.7.7.7", Port: "8080", Protocol: "http",
		Source: "src-a", LastVerify: time.Now(),
	}
	if err := proxyinabox.DB.Create(&p1).Error; err != nil {
		t.Fatalf("first insert should succeed: %v", err)
	}

	// 完全相同的 (IP, Port, Protocol) 应被 uniqueIndex 阻止
	p2 := proxyinabox.Proxy{
		IP: "7.7.7.7", Port: "8080", Protocol: "http",
		Source: "src-b", LastVerify: time.Now(),
	}
	if err := proxyinabox.DB.Create(&p2).Error; err == nil {
		t.Error("duplicate (IP, Port, Protocol) should be rejected by uniqueIndex")
	}
}

func TestDB_UniqueIndex_AllowsDifferentPort(t *testing.T) {
	setupTestDB(t)

	p1 := proxyinabox.Proxy{
		IP: "8.8.8.8", Port: "8080", Protocol: "http",
		Source: "src-a", LastVerify: time.Now(),
	}
	p2 := proxyinabox.Proxy{
		IP: "8.8.8.8", Port: "3128", Protocol: "http",
		Source: "src-b", LastVerify: time.Now(),
	}
	if err := proxyinabox.DB.Create(&p1).Error; err != nil {
		t.Fatalf("first insert failed: %v", err)
	}
	if err := proxyinabox.DB.Create(&p2).Error; err != nil {
		t.Fatalf("different port should be allowed: %v", err)
	}

	var count int64
	proxyinabox.DB.Model(&proxyinabox.Proxy{}).Where("ip = ?", "8.8.8.8").Count(&count)
	if count != 2 {
		t.Errorf("want 2 records for same IP different ports, got %d", count)
	}
}

func TestDB_UniqueIndex_AllowsDifferentProtocol(t *testing.T) {
	setupTestDB(t)

	p1 := proxyinabox.Proxy{
		IP: "9.9.9.9", Port: "8080", Protocol: "http",
		Source: "src-a", LastVerify: time.Now(),
	}
	p2 := proxyinabox.Proxy{
		IP: "9.9.9.9", Port: "8080", Protocol: "socks5",
		Source: "src-b", LastVerify: time.Now(),
	}
	if err := proxyinabox.DB.Create(&p1).Error; err != nil {
		t.Fatalf("first insert failed: %v", err)
	}
	if err := proxyinabox.DB.Create(&p2).Error; err != nil {
		t.Fatalf("different protocol should be allowed: %v", err)
	}
}

// --- MarkVerifyFailed → re-UpsertProxy 周期测试 ---

func TestVerifyFailedThenReUpsert_LastVerifyFresh(t *testing.T) {
	setupTestDB(t)
	c := newTestCache(t)

	oldTime := time.Now().Add(-3 * time.Hour)
	p := proxyinabox.Proxy{
		IP: "10.10.10.10", Port: "8080", Protocol: "http",
		Source: "test", LastVerify: oldTime,
	}
	c.UpsertProxy(p)

	var dbProxy proxyinabox.Proxy
	proxyinabox.DB.Where("ip = ? AND port = ?", "10.10.10.10", "8080").First(&dbProxy)
	c.MarkVerifyFailed(dbProxy)
	if c.ProxyLength() != 0 {
		t.Fatal("cache should be empty after MarkVerifyFailed")
	}

	// 模拟 source 重新抓取 → 验证成功 → UpsertProxy
	reAdded := proxyinabox.Proxy{
		IP: "10.10.10.10", Port: "8080", Protocol: "http",
		Source: "test", LastVerify: time.Now(), Delay: 8,
	}
	c.UpsertProxy(reAdded)

	for _, cached := range c.GetAllProxies() {
		if cached.IP == "10.10.10.10" {
			if time.Since(cached.LastVerify) > time.Minute {
				t.Errorf("re-upserted proxy should have fresh LastVerify, got %v", cached.LastVerify)
			}
		}
	}
}

// --- UpsertProxy 同 endpoint 行为测试 ---

func TestUpsertProxy_SameEndpoint_MemoryDeduplicates(t *testing.T) {
	setupTestDB(t)
	c := newTestCache(t)

	p1 := proxyinabox.Proxy{
		IP: "11.11.11.11", Port: "8080", Protocol: "http",
		Source: "src-a", LastVerify: time.Now(), Delay: 5,
	}
	c.UpsertProxy(p1)

	p2 := proxyinabox.Proxy{
		IP: "11.11.11.11", Port: "8080", Protocol: "http",
		Source: "src-b", LastVerify: time.Now(), Delay: 10,
	}
	c.UpsertProxy(p2)

	if c.ProxyLength() != 1 {
		t.Errorf("want 1 entry for same endpoint, got %d", c.ProxyLength())
	}
}
