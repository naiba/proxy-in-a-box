package service

import (
	"fmt"
	"net/http"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/naiba/proxyinabox"
	"math/rand/v2"
)

const (
	proxyFailureLockThreshold = 3
	proxyFailureLockDuration  = 15 * 24 * time.Hour
)

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
	l  sync.Mutex
	dl map[string][]*proxyEntry
}

type MemCache struct {
	proxies   *proxyList
	domains   *domainScheduling
	lockedIPs sync.Map
	failureMu sync.Mutex
}

func NewMemCache() *MemCache {
	c := &MemCache{
		proxies: &proxyList{
			pl:    make([]*proxyEntry, 0),
			index: make(map[string]struct{}),
		},
		domains: &domainScheduling{
			dl: make(map[string][]*proxyEntry),
		},
	}
	c.load()
	c.gc(time.Minute * 10)
	return c
}

func (c *MemCache) load() {
	// BUG-FIX: 启动时将旧数据中空 protocol 统一为 "http"，保证 uniqueIndex 一致性。
	// 同时删除因旧版缺少唯一约束而产生的重复 (IP, Port, Protocol) 记录，只保留最新的。
	proxyinabox.DB.Model(&proxyinabox.Proxy{}).Where("protocol = '' OR protocol IS NULL").Update("protocol", "http")
	proxyinabox.DB.Exec(`DELETE FROM proxies WHERE id NOT IN (
		SELECT MAX(id) FROM proxies WHERE deleted_at IS NULL GROUP BY ip, port, protocol
	) AND deleted_at IS NULL`)

	var ps []proxyinabox.Proxy
	err := proxyinabox.DB.Where("ip NOT IN (?)",
		proxyinabox.DB.Table("blocked_ips").Select("ip").Where("locked_until > ?", time.Now()),
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
	fmt.Println("[PIAB]", "cache", "[✅]", "load", len(ps), "items!")
}

func (c *MemCache) gc(dur time.Duration) {
	ticker := time.NewTicker(dur)
	go func() {
		for range ticker.C {
			num := 0
			now := time.Now().Unix()
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
	sort.Sort(sortedList)
	c.domains.l.Lock()
	defer c.domains.l.Unlock()
	if pl, has := c.domains.dl[domain]; has {
		// BUG-FIX: 同样使用副本排序域名列表
		sortedDomainList := make(sortableProxyList, len(pl))
		copy(sortedDomainList, pl)
		sort.Sort(sortedDomainList)
		for i := 0; i < len(sortedDomainList); i++ {
			// BUG-FIX: 使用原子操作读取 n 字段
			if now-sortedDomainList[i].getN() < 3 {
				candidate[sortedDomainList[i].p.IP] = struct{}{}
			} else {
				// 删除过期记录
				c.domains.dl[domain] = append(pl[:i], pl[i+1:]...)
			}
		}
	} else {
		c.domains.dl[domain] = make([]*proxyEntry, 0)
	}
	for i := 0; i < length; i++ {
		if _, has := candidate[c.proxies.pl[i].p.IP]; !has {
			c.domains.dl[domain] = append(c.domains.dl[domain], &proxyEntry{
				p: c.proxies.pl[i].p,
				n: now,
			})
			// BUG-FIX: 使用原子操作增加 n 字段，防止并发竞态
			c.proxies.pl[i].atomicAddN(1)
			fmt.Println("[PIAB]", "proxy scheduling", "[✅]", req.Host, "-->", c.proxies.pl[i].p.URI())
			return c.proxies.pl[i].p.URI(), nil
		}
	}
	return "", fmt.Errorf("%s:all(%d),domain(%s)", "No free agent can be used:", length, domain)
}

// --- 代理生命周期 ---

func (c *MemCache) UpsertProxy(p proxyinabox.Proxy) error {
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
	return nil
}

func (c *MemCache) MarkVerifySuccess(p proxyinabox.Proxy, delay int64, verifyTime time.Time) {
	c.proxies.l.Lock()
	defer c.proxies.l.Unlock()

	c.clearFailureLocked(p.IP)
	// BUG-FIX: 用显式 WHERE 定位 DB 记录，避免 p.ID 为零时 GORM 报 "WHERE conditions required"
	proxyinabox.DB.Model(&proxyinabox.Proxy{}).Where("id = ?", p.ID).Updates(map[string]interface{}{"delay": delay, "last_verify": verifyTime})

	// BUG-FIX: 按 URI 精确匹配内存 entry，而非按 IP 匹配后 early return。
	// 旧逻辑按 IP 匹配找到第一个就 return，同 IP 不同端口的其他 entry 的
	// LastVerify 永远不会被更新，导致 dashboard 显示超过 2h 未验证的代理。
	uri := p.URI()
	for _, e := range c.proxies.pl {
		if e.p.URI() == uri {
			e.p.Delay = delay
			e.p.LastVerify = verifyTime
			return
		}
	}
}

func (c *MemCache) MarkVerifyFailed(p proxyinabox.Proxy) {
	c.proxies.l.Lock()
	defer c.proxies.l.Unlock()

	// BUG-FIX: 只移除验证失败的特定代理（按 URI），而非同 IP 的所有端口。
	// 旧逻辑 removeFromCacheLocked(ip) 会误删同 IP 其他正常端口的代理。
	c.removeByURIFromCacheLocked(p.URI())
	// BUG-FIX: 用显式 WHERE 定位 DB 记录，避免 p.ID 为零时 GORM 报 "WHERE conditions required"
	proxyinabox.DB.Model(&proxyinabox.Proxy{}).Where("id = ?", p.ID).Update("last_verify", time.Now())
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
		// BUG-FIX: 锁定时同时从 proxies 表和内存缓存删除该 IP 的所有记录，
		// 保证 DB 和内存一致。解锁后源站重新抓取并验证成功时会重新写入。
		proxyinabox.DB.Unscoped().Where("ip = ?", ip).Delete(&proxyinabox.Proxy{})
		c.proxies.l.Lock()
		c.removeFromCacheLocked(ip)
		c.proxies.l.Unlock()
		fmt.Printf("[PIAB] IP [🔒] %s locked for 15 days after %d consecutive failures\n", ip, b.ConsecutiveFailures)
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
		c.removeFromCacheLocked(p.IP)
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
