package service

import (
	"fmt"
	"math/rand/v2"
	"net/http"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/naiba/proxyinabox"
)

const (
	proxyFailureLockThreshold = 3
	// A 15-day lock made transient public-proxy failures effectively permanent.
	// Six hours still stops hot retry loops while allowing automatic recovery.
	proxyFailureLockDuration = 6 * time.Hour
	defaultTargetFailureTTL  = 10 * time.Minute
)

var proxyRetryBackoff = [...]time.Duration{
	30 * time.Minute,
	2 * time.Hour,
	6 * time.Hour,
	24 * time.Hour,
}

func retryBackoffForFailure(failures int) time.Duration {
	if failures <= 0 {
		failures = 1
	}
	if failures > len(proxyRetryBackoff) {
		failures = len(proxyRetryBackoff)
	}
	return proxyRetryBackoff[failures-1]
}

type proxyEntry struct {
	p *proxyinabox.Proxy
	n int64
}

// atomicAddN 原子增加 n 字段，防止并发竞态
func (e *proxyEntry) atomicAddN(delta int64) int64 {
	return atomic.AddInt64(&e.n, delta)
}

// getN 原子读取 n 字段
func (e *proxyEntry) getN() int64 {
	return atomic.LoadInt64(&e.n)
}

type sortableProxyList []*proxyEntry

func (p sortableProxyList) Len() int           { return len(p) }
func (p sortableProxyList) Swap(i, j int)      { p[i], p[j] = p[j], p[i] }
func (p sortableProxyList) Less(i, j int) bool { return p[i].n < p[j].n }

type proxyList struct {
	l             sync.Mutex
	pl            []*proxyEntry
	getProxyIndex int
	index         map[string]struct{}
}

type domainScheduling struct {
	l              sync.Mutex
	dl             map[string][]*proxyEntry
	targetFailures map[string]map[string]time.Time
	failureTTL     time.Duration
}

type MemCache struct {
	proxies         *proxyList
	domains         *domainScheduling
	lockedIPs       sync.Map
	proxyRetryAfter sync.Map
	failureMu       sync.Mutex
}

func NewMemCache() *MemCache {
	c := &MemCache{
		proxies: &proxyList{
			pl:    make([]*proxyEntry, 0),
			index: make(map[string]struct{}),
		},
		domains: &domainScheduling{
			dl:             make(map[string][]*proxyEntry),
			targetFailures: make(map[string]map[string]time.Time),
			failureTTL:     configuredTargetFailureTTL(),
		},
	}
	c.load()
	c.gc(time.Minute * 10)
	return c
}

func configuredTargetFailureTTL() time.Duration {
	if proxyinabox.Config.Upstream.TargetFailureTTL > 0 {
		return proxyinabox.Config.Upstream.TargetFailureTTL
	}
	return defaultTargetFailureTTL
}

func (c *MemCache) load() {
	now := time.Now()
	var ps []proxyinabox.Proxy
	err := proxyinabox.DB.Where("available = ?", true).Where("ip NOT IN (?)",
		proxyinabox.DB.Table("blocked_ips").Select("ip").Where("locked_until > ?", now),
	).Find(&ps).Error
	if err != nil {
		panic(err)
	}
	c.proxies.l.Lock()
	defer c.proxies.l.Unlock()
	for i := 0; i < len(ps); i++ {
		c.proxies.pl = append(c.proxies.pl, &proxyEntry{p: &ps[i]})
		c.proxies.index[ps[i].URI()] = struct{}{}
	}

	var delayed []proxyinabox.Proxy
	if err := proxyinabox.DB.Select("ip", "port", "protocol", "next_verify_at").
		Where("available = ? AND next_verify_at > ?", false, now).
		Find(&delayed).Error; err != nil {
		panic(err)
	}
	for i := range delayed {
		c.proxyRetryAfter.Store(delayed[i].URI(), delayed[i].NextVerifyAt)
	}
	fmt.Println("[PIAB]", "cache", "[✅]", "load", len(ps), "items!")
}

func (c *MemCache) gc(dur time.Duration) {
	ticker := time.NewTicker(dur)
	go func() {
		for range ticker.C {
			num := 0
			nowTime := time.Now()
			now := nowTime.Unix()
			c.domains.l.Lock()
			// BUG-FIX: 使用倒序遍历删除切片元素，避免索引错位导致某些记录未清理
			for k, v := range c.domains.dl {
				toDelete := make([]int, 0)
				for i, v1 := range v {
					if now-v1.n > 3 {
						toDelete = append(toDelete, i)
					}
				}
				// 倒序删除，避免索引问题
				for i := len(toDelete) - 1; i >= 0; i-- {
					idx := toDelete[i]
					c.domains.dl[k] = append(v[:idx], v[idx+1:]...)
					num++
				}
				if len(c.domains.dl[k]) == 0 {
					delete(c.domains.dl, k)
					num++
				}
			}
			for target, failures := range c.domains.targetFailures {
				for proxyURI, expiresAt := range failures {
					if !nowTime.Before(expiresAt) {
						delete(failures, proxyURI)
						num++
					}
				}
				if len(failures) == 0 {
					delete(c.domains.targetFailures, target)
				}
			}
			c.domains.l.Unlock()
			fmt.Println("[PIAB]", "cache GC", "[🚮]", "clean", num, "items.")
		}
	}()
}

// --- 代理池读取 ---

func (c *MemCache) RandomProxy() (string, bool) {
	c.proxies.l.Lock()
	defer c.proxies.l.Unlock()
	if len(c.proxies.pl) == 0 {
		return "", false
	}
	return c.proxies.pl[rand.IntN(len(c.proxies.pl))].p.URI(), true
}

func (c *MemCache) GetProxy() (string, bool) {
	c.proxies.l.Lock()
	defer c.proxies.l.Unlock()
	if len(c.proxies.pl) == 0 {
		return "", false
	}
	if c.proxies.getProxyIndex >= len(c.proxies.pl) {
		c.proxies.getProxyIndex = 0
	}
	p := c.proxies.pl[c.proxies.getProxyIndex].p.URI()
	c.proxies.getProxyIndex++
	return p, true
}

func (c *MemCache) ProxyLength() int {
	c.proxies.l.Lock()
	defer c.proxies.l.Unlock()
	return len(c.proxies.pl)
}

func (c *MemCache) HasProxy(proxy string) bool {
	c.proxies.l.Lock()
	defer c.proxies.l.Unlock()
	_, has := c.proxies.index[proxy]
	return has
}

func (c *MemCache) IsProxyValidationDue(proxyURI string) bool {
	retryAt, ok := c.proxyRetryAfter.Load(proxyURI)
	if !ok {
		return true
	}
	if time.Now().Before(retryAt.(time.Time)) {
		return false
	}
	c.proxyRetryAfter.Delete(proxyURI)
	return true
}

func (c *MemCache) GetAllProxies() []proxyinabox.Proxy {
	c.proxies.l.Lock()
	defer c.proxies.l.Unlock()
	result := make([]proxyinabox.Proxy, len(c.proxies.pl))
	for i, e := range c.proxies.pl {
		result[i] = *e.p
	}
	return result
}

func (c *MemCache) PickProxy(req *http.Request) (string, error) {
	c.proxies.l.Lock()
	defer c.proxies.l.Unlock()

	length := len(c.proxies.pl)
	domain := req.Host
	now := time.Now().Unix()
	if length == 0 {
		return "", fmt.Errorf("%s", "There is no proxy in the proxy pool.")
	}

	candidate := make(map[string]struct{})
	// BUG-FIX: 使用副本排序，不修改原数组，避免破坏 GetProxy 的轮询顺序
	sortedList := make(sortableProxyList, len(c.proxies.pl))
	copy(sortedList, c.proxies.pl)
	sort.Stable(sortedList)
	c.domains.l.Lock()
	defer c.domains.l.Unlock()

	// Rebuild instead of deleting from one slice using indexes from a sorted
	// copy. The old code could remove the wrong entry and repeatedly return the
	// first proxy after the three-second window elapsed.
	recent := make([]*proxyEntry, 0, len(c.domains.dl[domain]))
	for _, entry := range c.domains.dl[domain] {
		if now-entry.getN() < 3 {
			candidate[entry.p.IP] = struct{}{}
			recent = append(recent, entry)
		}
	}
	c.domains.dl[domain] = recent

	failedForTarget := c.domains.targetFailures[domain]
	for proxyURI, expiresAt := range failedForTarget {
		if !time.Now().Before(expiresAt) {
			delete(failedForTarget, proxyURI)
		}
	}
	if len(failedForTarget) == 0 {
		delete(c.domains.targetFailures, domain)
	}

	for _, entry := range sortedList {
		if _, failed := failedForTarget[entry.p.URI()]; failed {
			continue
		}
		if _, has := candidate[entry.p.IP]; !has {
			c.domains.dl[domain] = append(c.domains.dl[domain], &proxyEntry{
				p: entry.p,
				n: now,
			})
			entry.atomicAddN(1)
			fmt.Println("[PIAB]", "proxy scheduling", "[✅]", req.Host, "-->", entry.p.URI())
			return entry.p.URI(), nil
		}
	}
	return "", fmt.Errorf("%s:all(%d),domain(%s)", "No free agent can be used:", length, domain)
}

func (c *MemCache) MarkProxyTargetFailure(proxyURI string, target string) {
	if proxyURI == "" || target == "" {
		return
	}
	c.domains.l.Lock()
	defer c.domains.l.Unlock()

	if c.domains.targetFailures == nil {
		c.domains.targetFailures = make(map[string]map[string]time.Time)
	}
	if c.domains.failureTTL <= 0 {
		c.domains.failureTTL = configuredTargetFailureTTL()
	}
	failures := c.domains.targetFailures[target]
	if failures == nil {
		failures = make(map[string]time.Time)
		c.domains.targetFailures[target] = failures
	}
	failures[proxyURI] = time.Now().Add(c.domains.failureTTL)
	fmt.Printf("[PIAB] proxy [⚠️] circuit opened for %s -> %s for %s\n", proxyURI, target, c.domains.failureTTL)
}

// --- 代理生命周期 ---

func (c *MemCache) UpsertProxy(p proxyinabox.Proxy) error {
	c.failureMu.Lock()
	defer c.failureMu.Unlock()

	c.proxies.l.Lock()
	defer c.proxies.l.Unlock()

	// BUG-FIX: 持锁下检查锁定状态，防止 TOCTOU 竞态。必须在锁内检查，
	// 确保 IsIPLocked 和 DB.Save 之间的时间窗口内不会被其他 goroutine 锁定。
	if c.IsIPLocked(p.IP) {
		return fmt.Errorf("ip %s is locked, rejecting upsert", p.IP)
	}

	// BUG-FIX: 空 protocol 统一为 "http"，否则 uniqueIndex 会将 "" 和 "http"
	// 视为不同值，导致同一 endpoint 在 DB 中产生重复记录。
	if p.Protocol == "" {
		p.Protocol = "http"
	}
	p.Available = true
	p.ConsecutiveFailures = 0
	p.NextVerifyAt = time.Time{}

	// BUG-FIX: 先查 DB 中是否已有相同 (IP, Port, Protocol) 的记录。
	// 若有则复用其主键以触发 UPDATE 而非 INSERT，避免 uniqueIndex 冲突。
	var existing proxyinabox.Proxy
	if err := proxyinabox.DB.Where("ip = ? AND port = ? AND protocol = ?", p.IP, p.Port, p.Protocol).First(&existing).Error; err == nil {
		p.Model = existing.Model
	}
	if e := proxyinabox.DB.Save(&p).Error; e != nil {
		return e
	}
	// BUG-FIX: 只删除相同 URI 的代理，不删除同 IP 的其他端口代理，
	// 避免同一 IP 的多端口代理被误删。
	uri := p.URI()
	for i := len(c.proxies.pl) - 1; i >= 0; i-- {
		if c.proxies.pl[i].p.URI() == uri {
			delete(c.proxies.index, uri)
			c.proxies.pl = append(c.proxies.pl[:i], c.proxies.pl[i+1:]...)
		}
	}
	c.proxies.pl = append(c.proxies.pl, &proxyEntry{p: &p, n: 0})
	c.proxies.index[uri] = struct{}{}
	c.proxyRetryAfter.Delete(uri)
	// A successful source validation is a genuine health success. Clear any
	// sub-threshold failures so they cannot accumulate across successful runs.
	c.clearFailureLocked(p.IP)
	return nil
}

func (c *MemCache) MarkVerifySuccess(p proxyinabox.Proxy, delay int64, verifyTime time.Time, deepVerified bool) {
	c.failureMu.Lock()
	defer c.failureMu.Unlock()

	if c.IsIPLocked(p.IP) {
		return
	}

	c.proxies.l.Lock()
	defer c.proxies.l.Unlock()

	// BUG-FIX: 用显式 WHERE 定位 DB 记录，避免 p.ID 为零时 GORM 报 "WHERE conditions required"
	updates := map[string]interface{}{
		"available":            true,
		"consecutive_failures": 0,
		"delay":                delay,
		"last_verify":          verifyTime,
		"next_verify_at":       time.Time{},
	}
	if deepVerified {
		updates["last_deep_verify"] = verifyTime
	}
	result := proxyinabox.DB.Model(&proxyinabox.Proxy{}).Where("id = ?", p.ID).Updates(updates)
	if result.Error != nil {
		fmt.Printf("[PIAB] verify [❎] update proxy %s: %v\n", p.URI(), result.Error)
		return
	}
	if result.RowsAffected != 1 {
		fmt.Printf("[PIAB] verify [⚠️] proxy %s no longer exists in database\n", p.URI())
		return
	}

	// BUG-FIX: 按 URI 精确匹配内存 entry，而非按 IP 匹配后 early return。
	// 旧逻辑按 IP 匹配找到第一个就 return，同 IP 不同端口的其他 entry 的
	// LastVerify 永远不会被更新，导致 dashboard 显示超过 2h 未验证的代理。
	uri := p.URI()
	c.proxyRetryAfter.Delete(uri)
	for _, e := range c.proxies.pl {
		if e.p.URI() == uri {
			c.clearFailureLocked(p.IP)
			e.p.Available = true
			e.p.ConsecutiveFailures = 0
			e.p.Delay = delay
			e.p.LastVerify = verifyTime
			e.p.NextVerifyAt = time.Time{}
			if deepVerified {
				e.p.LastDeepVerify = verifyTime
			}
			return
		}
	}

	// 验证失败会暂时从内存移除，但仍保留 SQLite 记录。定期验证随后成功时，
	// 从 DB 重新加载完整行，以保留来源、地区等元数据后恢复到缓存。
	var stored proxyinabox.Proxy
	if err := proxyinabox.DB.Where("id = ?", p.ID).First(&stored).Error; err != nil {
		fmt.Printf("[PIAB] verify [❎] reload proxy %s: %v\n", p.URI(), err)
		return
	}
	c.clearFailureLocked(p.IP)
	c.proxies.pl = append(c.proxies.pl, &proxyEntry{p: &stored})
	c.proxies.index[uri] = struct{}{}
}

func (c *MemCache) MarkVerifyFailed(p proxyinabox.Proxy) int {
	c.failureMu.Lock()
	defer c.failureMu.Unlock()

	var stored proxyinabox.Proxy
	if err := proxyinabox.DB.Select("id", "consecutive_failures").Where("id = ?", p.ID).First(&stored).Error; err != nil {
		fmt.Printf("[PIAB] verify [❎] load failed proxy %s: %v\n", p.URI(), err)
		return 0
	}
	failures := stored.ConsecutiveFailures + 1
	verifyTime := time.Now()
	retryAt := verifyTime.Add(retryBackoffForFailure(failures))
	result := proxyinabox.DB.Model(&proxyinabox.Proxy{}).Where("id = ?", p.ID).Updates(map[string]interface{}{
		"available":            false,
		"consecutive_failures": failures,
		"last_verify":          verifyTime,
		"next_verify_at":       retryAt,
	})
	if result.Error != nil {
		fmt.Printf("[PIAB] verify [❎] persist failed proxy %s: %v\n", p.URI(), result.Error)
		return 0
	}
	if result.RowsAffected != 1 {
		fmt.Printf("[PIAB] verify [⚠️] failed proxy %s no longer exists in database\n", p.URI())
		return 0
	}
	c.proxyRetryAfter.Store(p.URI(), retryAt)

	c.proxies.l.Lock()
	defer c.proxies.l.Unlock()

	// BUG-FIX: 只移除验证失败的特定代理（按 URI），而非同 IP 的所有端口。
	// 旧逻辑 removeFromCacheLocked(ip) 会误删同 IP 其他正常端口的代理。
	c.removeByURIFromCacheLocked(p.URI())
	return failures
}

func (c *MemCache) MarkProxyUnavailable(proxyURI string) {
	c.failureMu.Lock()
	defer c.failureMu.Unlock()

	c.proxies.l.Lock()
	defer c.proxies.l.Unlock()

	var proxyID uint
	for _, entry := range c.proxies.pl {
		if entry.p.URI() == proxyURI {
			proxyID = entry.p.ID
			break
		}
	}
	if proxyID == 0 {
		return
	}

	var stored proxyinabox.Proxy
	if err := proxyinabox.DB.Select("id", "consecutive_failures").Where("id = ?", proxyID).First(&stored).Error; err != nil {
		fmt.Printf("[PIAB] proxy [❎] load %s for quarantine: %v\n", proxyURI, err)
		return
	}
	failures := stored.ConsecutiveFailures + 1
	verifyTime := time.Now()
	retryAt := verifyTime.Add(retryBackoffForFailure(failures))
	result := proxyinabox.DB.Model(&proxyinabox.Proxy{}).Where("id = ?", proxyID).Updates(map[string]interface{}{
		"available":            false,
		"consecutive_failures": failures,
		"last_verify":          verifyTime,
		"next_verify_at":       retryAt,
	})
	if result.Error != nil {
		fmt.Printf("[PIAB] proxy [❎] quarantine %s: %v\n", proxyURI, result.Error)
		return
	}
	if result.RowsAffected != 1 {
		fmt.Printf("[PIAB] proxy [⚠️] cannot quarantine missing proxy %s\n", proxyURI)
		return
	}

	c.proxyRetryAfter.Store(proxyURI, retryAt)
	c.removeByURIFromCacheLocked(proxyURI)
	fmt.Printf("[PIAB] proxy [⚠️] quarantined %s until %s after an upstream authentication/forbidden response\n", proxyURI, retryAt.Format(time.RFC3339))
}

func (c *MemCache) RecordFailure(ip string) bool {
	// BUG-FIX: 必须对同一 IP 的 read-modify-write 操作加锁，防止多个 goroutine
	// 并发读取相同的 ConsecutiveFailures 值各自 +1 写回，导致计数丢失或重复触发锁定。
	c.failureMu.Lock()
	defer c.failureMu.Unlock()

	var b proxyinabox.BlockedIP
	if err := proxyinabox.DB.Where("ip = ?", ip).First(&b).Error; err != nil {
		b = proxyinabox.BlockedIP{IP: ip}
	}
	b.ConsecutiveFailures++
	locked := b.ConsecutiveFailures >= proxyFailureLockThreshold
	if locked {
		b.LockedUntil = time.Now().Add(proxyFailureLockDuration)
		c.lockedIPs.Store(ip, b.LockedUntil)
		// Persist quarantine instead of deleting the proxy rows. After the lock
		// expires, periodic verification can recover endpoints that became healthy.
		proxyinabox.DB.Model(&proxyinabox.Proxy{}).Where("ip = ?", ip).Update("available", false)
		c.proxies.l.Lock()
		c.removeFromCacheLocked(ip)
		c.proxies.l.Unlock()
		fmt.Printf("[PIAB] IP [🔒] %s quarantined for %s after %d consecutive health-check failures\n", ip, proxyFailureLockDuration, b.ConsecutiveFailures)
	}
	proxyinabox.DB.Save(&b)
	return locked
}

func (c *MemCache) IsIPLocked(ip string) bool {
	if v, ok := c.lockedIPs.Load(ip); ok {
		if time.Now().Before(v.(time.Time)) {
			return true
		}
		c.lockedIPs.Delete(ip)
	}
	return false
}

func (c *MemCache) LoadLockedIPs() {
	var blocked []proxyinabox.BlockedIP
	proxyinabox.DB.Where("locked_until > ?", time.Now()).Find(&blocked)
	for _, b := range blocked {
		c.lockedIPs.Store(b.IP, b.LockedUntil)
	}
	if len(blocked) > 0 {
		fmt.Printf("[PIAB] loaded %d locked IPs from database\n", len(blocked))
	}
}

func (c *MemCache) CleanupStaleProxies(threshold time.Duration) {
	c.failureMu.Lock()
	defer c.failureMu.Unlock()

	cutoff := time.Now().Add(-threshold)

	var staleProxies []proxyinabox.Proxy
	proxyinabox.DB.Where("last_verify < ? AND last_verify > ?", cutoff, time.Time{}).Find(&staleProxies)

	if len(staleProxies) == 0 {
		return
	}

	result := proxyinabox.DB.Unscoped().Where("last_verify < ? AND last_verify > ?", cutoff, time.Time{}).Delete(&proxyinabox.Proxy{})
	if result.Error != nil {
		fmt.Printf("[PIAB] stale cleanup [❎] error: %v\n", result.Error)
		return
	}

	c.proxies.l.Lock()
	for _, p := range staleProxies {
		c.removeByURIFromCacheLocked(p.URI())
	}
	c.proxies.l.Unlock()

	fmt.Printf("[PIAB] stale cleanup [🧹] removed %d proxies inactive for 6+ months\n", result.RowsAffected)
}

// --- 内部方法 ---

// removeFromCacheLocked 删除该 IP 的所有代理，调用方必须已持有 c.proxies.l 锁
func (c *MemCache) removeFromCacheLocked(ip string) {
	for i := len(c.proxies.pl) - 1; i >= 0; i-- {
		e := c.proxies.pl[i]
		if e.p.IP == ip {
			delete(c.proxies.index, e.p.URI())
			c.proxies.pl = append(c.proxies.pl[:i], c.proxies.pl[i+1:]...)
		}
	}
}

// removeByURIFromCacheLocked 只删除特定 URI 的代理，调用方必须已持有 c.proxies.l 锁
func (c *MemCache) removeByURIFromCacheLocked(uri string) {
	for i := len(c.proxies.pl) - 1; i >= 0; i-- {
		if c.proxies.pl[i].p.URI() == uri {
			delete(c.proxies.index, uri)
			c.proxies.pl = append(c.proxies.pl[:i], c.proxies.pl[i+1:]...)
		}
	}
}

func (c *MemCache) clearFailureLocked(ip string) {
	c.lockedIPs.Delete(ip)
	proxyinabox.DB.Where("ip = ?", ip).Delete(&proxyinabox.BlockedIP{})
}
