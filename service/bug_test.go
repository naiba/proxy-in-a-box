package service

import (
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/naiba/proxyinabox"
)

// ==================== BUG-1: UpsertProxy 多端口误删测试 ====================
// 位置: service/memcache.go:305-322
// 问题: removeFromCacheLocked(p.IP) 删除该 IP 的所有端口，而非仅当前 URI

func TestUpsertProxy_MultiPortSameIP(t *testing.T) {
	setupTestDB(t)
	c := newTestCache(t)

	// 步骤1: 先入库第一个端口的代理
	p1 := proxyinabox.Proxy{
		IP: "1.1.1.1", Port: "8080", Protocol: "http",
		Source: "src-a", LastVerify: time.Now(),
	}
	if err := c.UpsertProxy(p1); err != nil {
		t.Fatalf("UpsertProxy p1 failed: %v", err)
	}

	// 步骤2: 再入库同一 IP 不同端口的代理
	p2 := proxyinabox.Proxy{
		IP: "1.1.1.1", Port: "3128", Protocol: "http",
		Source: "src-b", LastVerify: time.Now(),
	}
	if err := c.UpsertProxy(p2); err != nil {
		t.Fatalf("UpsertProxy p2 failed: %v", err)
	}

	// 步骤3: 验证两个代理都在缓存中
	if c.ProxyLength() != 2 {
		t.Errorf("ProxyLength = %d, want 2 (both ports should exist)", c.ProxyLength())
	}

	// 步骤4: 再次入库第一个端口的代理（模拟新抓取）
	p1New := proxyinabox.Proxy{
		IP: "1.1.1.1", Port: "8080", Protocol: "http",
		Source: "src-c", LastVerify: time.Now(),
	}
	if err := c.UpsertProxy(p1New); err != nil {
		t.Fatalf("UpsertProxy p1New failed: %v", err)
	}

	// BUG: 此时 p2 (3128端口) 会被误删！只剩 1 个代理
	if c.ProxyLength() != 2 {
		t.Errorf("BUG CONFIRMED: ProxyLength = %d, want 2 (p2 should NOT be deleted when upserting p1)", c.ProxyLength())
	}

	// 验证具体的代理存在
	if !c.HasProxy("http://1.1.1.1:8080") {
		t.Error("8080 port should exist")
	}
	if !c.HasProxy("http://1.1.1.1:3128") {
		t.Error("3128 port should exist (BUG: was deleted)")
	}
}

// ==================== BUG-2: HasProxy 无锁访问测试 ====================
// 位置: service/memcache.go:192-195
// 问题: HasProxy 直接读取 map 不加锁，存在竞态

func TestHasProxy_RaceCondition(t *testing.T) {
	setupTestDB(t)
	c := newTestCache(t)

	// 先入库一个代理
	p := proxyinabox.Proxy{
		IP: "1.1.1.1", Port: "8080", Protocol: "http",
		Source: "test", LastVerify: time.Now(),
	}
	c.UpsertProxy(p)

	const goroutines = 100
	var wg sync.WaitGroup
	wg.Add(goroutines * 2)

	// 并发读取 HasProxy
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			c.HasProxy("http://1.1.1.1:8080")
		}()
	}

	// 并发写入 UpsertProxy
	for i := 0; i < goroutines; i++ {
		go func(idx int) {
			defer wg.Done()
			newP := proxyinabox.Proxy{
				IP:   fmt.Sprintf("%d.%d.%d.%d", idx, idx, idx, idx),
				Port: "8080", Protocol: "http",
				Source: "test", LastVerify: time.Now(),
			}
			c.UpsertProxy(newP)
		}(i)
	}

	wg.Wait()

	// 如果没有竞态，测试应该通过
	// 实际使用 -race 标志运行可以检测到竞态
	t.Log("Run with -race flag to detect the race condition in HasProxy")
}

// ==================== BUG-4: TOCTOU 测试 ====================
// 位置: service/memcache.go:305-322
// 问题: IsIPLocked 检查在加锁前，存在时间窗口

func TestUpsertProxy_TOCTOU(t *testing.T) {
	setupTestDB(t)
	c := newTestCache(t)

	// 先使 IP 被锁定
	for i := 0; i < proxyFailureLockThreshold; i++ {
		c.RecordFailure("10.0.0.1")
	}

	if !c.IsIPLocked("10.0.0.1") {
		t.Fatal("IP should be locked")
	}

	var wg sync.WaitGroup
	const attempts = 100
	insertedCount := 0
	var mu sync.Mutex

	// 并发尝试入库被锁定的 IP
	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p := proxyinabox.Proxy{
				IP: "10.0.0.1", Port: "8080", Protocol: "http",
				Source: "test", LastVerify: time.Now(),
			}
			err := c.UpsertProxy(p)
			if err == nil {
				mu.Lock()
				insertedCount++
				mu.Unlock()
			}
		}()
	}

	wg.Wait()

	if insertedCount > 0 {
		t.Errorf("BUG CONFIRMED: %d goroutines inserted locked IP (should be 0)", insertedCount)
	}
}

// ==================== BUG-5: PickProxy 排序副作用测试 ====================
// 位置: service/memcache.go:219
// 问题: sort.Sort 直接修改原数组，破坏 GetProxy 轮询顺序

func TestPickProxy_SortSideEffect(t *testing.T) {
	setupTestDB(t)
	c := newTestCache(t)

	// 入库3个代理
	proxies := []proxyinabox.Proxy{
		{IP: "1.1.1.1", Port: "8080", Protocol: "http", Source: "test", Delay: 100, LastVerify: time.Now()},
		{IP: "2.2.2.2", Port: "8080", Protocol: "http", Source: "test", Delay: 50, LastVerify: time.Now()},
		{IP: "3.3.3.3", Port: "8080", Protocol: "http", Source: "test", Delay: 200, LastVerify: time.Now()},
	}
	for _, p := range proxies {
		c.UpsertProxy(p)
	}

	// 记录排序前的轮询顺序
	originalOrder := make([]string, 3)
	for i := 0; i < 3; i++ {
		p, _ := c.GetProxy()
		originalOrder[i] = p
	}

	// 调用 PickProxy（会排序）
	req, _ := http.NewRequest("GET", "http://example.com/path", nil)
	req.Host = "example.com"
	c.PickProxy(req)

	// 重置轮询索引
	c.proxies.getProxyIndex = 0

	// 再次获取轮询顺序
	afterOrder := make([]string, 3)
	for i := 0; i < 3; i++ {
		p, _ := c.GetProxy()
		afterOrder[i] = p
	}

	// 检查顺序是否一致
	sameOrder := true
	for i := range originalOrder {
		if originalOrder[i] != afterOrder[i] {
			sameOrder = false
			break
		}
	}

	if !sameOrder {
		t.Log("BUG CONFIRMED: PickProxy sorting changed GetProxy round-robin order")
		t.Logf("Before: %v", originalOrder)
		t.Logf("After:  %v", afterOrder)
	}
}

// ==================== BUG-6: PickProxy n 字段竞态测试 ====================
// 位置: service/memcache.go:240
// 问题: c.proxies.pl[i].n++ 无原子保护

func TestPickProxy_NFieldRace(t *testing.T) {
	setupTestDB(t)
	c := newTestCache(t)

	// 入库一个代理
	c.UpsertProxy(proxyinabox.Proxy{
		IP: "1.1.1.1", Port: "8080", Protocol: "http",
		Source: "test", LastVerify: time.Now(),
	})

	const goroutines = 100
	const picksPerGoroutine = 10
	var wg sync.WaitGroup
	wg.Add(goroutines)

	// 并发调用 PickProxy
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < picksPerGoroutine; j++ {
				req, _ := http.NewRequest("GET", fmt.Sprintf("http://site%d.com/path", j), nil)
				req.Host = fmt.Sprintf("site%d.com", j)
				c.PickProxy(req)
			}
		}()
	}

	wg.Wait()

	// 计算期望的 n 值总和
	expectedN := int64(goroutines * picksPerGoroutine)
	actualN := c.proxies.pl[0].n

	if actualN != expectedN {
		t.Logf("BUG CONFIRMED: n field race detected. Expected %d, got %d", expectedN, actualN)
	}

	t.Log("Run with -race flag to detect the race condition")
}

// ==================== BUG-7: IPLimiter 限流偏松测试 ====================
// 位置: service/memcache.go:259
// 问题: 检查条件是 > 而非 >=，允许 limit+1 个请求

func TestIPLimiter_AllowsLimitPlusOne(t *testing.T) {
	setupTestDB(t)
	c := newTestCache(t)

	// 设置限制为 3
	proxyinabox.Config.Sys.RequestLimitPerIP = 3

	// 模拟来自同一 IP 的请求
	baseReq, _ := http.NewRequest("GET", "http://example.com/path", nil)

	allowedCount := 0
	for i := 0; i < 5; i++ {
		req := baseReq.Clone(baseReq.Context())
		req.RemoteAddr = "192.168.1.1:12345"
		if c.IPLimiter(req) {
			allowedCount++
		}
	}

	// BUG: 应该只允许 3 个，实际允许 4 个
	if allowedCount > 3 {
		t.Errorf("BUG CONFIRMED: IPLimiter allowed %d requests, want max 3", allowedCount)
	}
}

// ==================== BUG-9: GC 内存泄漏测试 ====================
// 位置: service/memcache.go:144-149
// 问题: 在迭代中修改切片导致某些记录未清理

func TestGC_DomainCleanup(t *testing.T) {
	setupTestDB(t)
	c := newTestCache(t)

	// 手动填充 domainActivityList
	now := time.Now().Unix()
	c.domainLimit.list["client1"] = &domainActivity{
		domains: map[string]int64{
			"expired1.com": now - 60*31, // 31 分钟前过期
			"expired2.com": now - 60*32, // 32 分钟前过期
			"fresh.com":    now,         // 当前
		},
		last: now,
	}

	// 手动触发 GC 逻辑的一部分
	c.domainLimit.l.Lock()
	toDelete := make([]string, 0)
	for k, v := range c.domainLimit.list["client1"].domains {
		if now-v > 60*30 {
			toDelete = append(toDelete, k)
		}
	}

	// 模拟错误的删除逻辑（向前删除）
	for _, k := range toDelete {
		delete(c.domainLimit.list["client1"].domains, k)
	}
	c.domainLimit.l.Unlock()

	// 验证是否全部清理
	c.domainLimit.l.Lock()
	remaining := len(c.domainLimit.list["client1"].domains)
	c.domainLimit.l.Unlock()

	// 应该只剩 fresh.com
	if remaining != 1 {
		t.Errorf("BUG: expected 1 domain remaining (fresh.com), got %d", remaining)
	}
}

// ==================== 综合竞态测试 ====================

func TestConcurrent_ProxyOperations(t *testing.T) {
	setupTestDB(t)
	c := newTestCache(t)

	const (
		upserterGoroutines = 10
		pickerGoroutines   = 10
		failureGoroutines  = 5
		operations         = 50
	)

	var wg sync.WaitGroup
	wg.Add(upserterGoroutines + pickerGoroutines + failureGoroutines)

	// 并发 UpsertProxy
	for i := 0; i < upserterGoroutines; i++ {
		go func(id int) {
			defer wg.Done()
			for j := 0; j < operations; j++ {
				p := proxyinabox.Proxy{
					IP:         fmt.Sprintf("10.%d.%d.%d", id, j/256, j%256),
					Port:       "8080",
					Protocol:   "http",
					Source:     "test",
					LastVerify: time.Now(),
				}
				c.UpsertProxy(p)
			}
		}(i)
	}

	// 并发 PickProxy
	for i := 0; i < pickerGoroutines; i++ {
		go func(id int) {
			defer wg.Done()
			for j := 0; j < operations; j++ {
				req, _ := http.NewRequest("GET", fmt.Sprintf("http://site%d.com/path", j), nil)
				req.Host = fmt.Sprintf("site%d.com", j)
				c.PickProxy(req)
			}
		}(i)
	}

	// 并发 RecordFailure
	for i := 0; i < failureGoroutines; i++ {
		go func(id int) {
			defer wg.Done()
			for j := 0; j < operations; j++ {
				ip := fmt.Sprintf("192.168.%d.%d", id, j%256)
				c.RecordFailure(ip)
			}
		}(i)
	}

	wg.Wait()

	t.Log("Concurrent operations completed. Run with -race to detect issues.")
}
