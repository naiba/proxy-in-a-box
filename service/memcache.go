package service

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
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

type ipActivityEntry struct {
	lastActive int64
	num        int64
}
type ipActivity struct {
	l    sync.Mutex
	list map[string]*ipActivityEntry
}

type domainActivity struct {
	domains map[string]int64
	last    int64
}
type domainActivityList struct {
	l    sync.Mutex
	list map[string]*domainActivity
}

type MemCache struct {
	proxies     *proxyList
	domains     *domainScheduling
	ips         *ipActivity
	domainLimit *domainActivityList
	lockedIPs   sync.Map
	failureMu   sync.Mutex
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
		ips: &ipActivity{
			list: make(map[string]*ipActivityEntry),
		},
		domainLimit: &domainActivityList{
			list: make(map[string]*domainActivity),
		},
	}
	c.load()
	c.gc(time.Minute * 10)
	return c
}

func (c *MemCache) load() {
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
			c.domainLimit.l.Lock()
			for k, v := range c.domainLimit.list {
				if now-v.last > 60*30 {
					delete(c.domainLimit.list, k)
					num++
				} else {
					for k1, v1 := range v.domains {
						if now-v1 > 60*30 {
							delete(v.domains, k1)
							num++
						}
					}
					if len(v.domains) == 0 {
						delete(c.domainLimit.list, k)
						num++
					}
				}
			}
			c.domainLimit.l.Unlock()
			now = time.Now().Unix()
			c.ips.l.Lock()
			for k, v := range c.ips.list {
				if v.lastActive != now {
					delete(c.ips.list, k)
					num++
				}
			}
			c.ips.l.Unlock()
			now = time.Now().Unix()
			c.domains.l.Lock()
			for k, v := range c.domains.dl {
				for i, v1 := range v {
					if now-v1.n > 3 {
						c.domains.dl[k] = append(v[:i], v[i+1:]...)
					}
				}
				if len(v) == 0 {
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
	sort.Sort(sortableProxyList(c.proxies.pl))
	c.domains.l.Lock()
	defer c.domains.l.Unlock()
	if pl, has := c.domains.dl[domain]; has {
		sort.Sort(sortableProxyList(pl))
		for i := 0; i < len(pl); i++ {
			if now-pl[i].n < 3 {
				candidate[pl[i].p.IP] = struct{}{}
			} else {
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
			c.proxies.pl[i].n++
			fmt.Println("[PIAB]", "proxy scheduling", "[✅]", req.Host, "-->", c.proxies.pl[i].p.URI())
			return c.proxies.pl[i].p.URI(), nil
		}
	}
	return "", fmt.Errorf("%s:all(%d),domain(%s)", "No free agent can be used:", length, domain)
}

// --- 限流 ---

func (c *MemCache) IPLimiter(req *http.Request) bool {
	c.ips.l.Lock()
	defer c.ips.l.Unlock()
	now := time.Now().Unix()
	ip := getIP(req.RemoteAddr)

	entry, has := c.ips.list[ip]
	if has {
		if now == entry.lastActive {
			if entry.num > proxyinabox.Config.Sys.RequestLimitPerIP {
				return false
			}
			entry.num++
		} else {
			entry.num = 1
		}
		entry.lastActive = now
	} else {
		c.ips.list[ip] = &ipActivityEntry{num: 1, lastActive: now}
	}
	return true
}

func (c *MemCache) HostLimiter(req *http.Request) bool {
	c.domainLimit.l.Lock()
	defer c.domainLimit.l.Unlock()
	ip := getIP(req.RemoteAddr)
	domain := req.Host
	now := time.Now().Unix()
	ds, has := c.domainLimit.list[ip]
	if !has {
		c.domainLimit.list[ip] = &domainActivity{
			domains: make(map[string]int64),
		}
		c.domainLimit.list[ip].domains[domain] = now
		return true
	}
	if now-ds.last > 60*30 {
		ds.domains = make(map[string]int64)
		ds.domains[domain] = now
		ds.last = now
		return true
	}
	ds.domains[domain] = now
	ds.last = now
	for k, v := range ds.domains {
		if now-v > 60*30 {
			delete(ds.domains, k)
		}
	}
	return len(ds.domains) < proxyinabox.Config.Sys.DomainsPerIP
}

// --- 代理生命周期 ---

func (c *MemCache) UpsertProxy(p proxyinabox.Proxy) error {
	// BUG-FIX: 新抓取的代理入库前必须检查锁定状态。被锁定的 IP 说明近期连续验证失败，
	// 即使源站重新抓到也不应入库，更不应清除锁定记录，必须等锁定自然过期。
	if c.IsIPLocked(p.IP) {
		return fmt.Errorf("ip %s is locked, rejecting upsert", p.IP)
	}

	c.proxies.l.Lock()
	defer c.proxies.l.Unlock()

	if e := proxyinabox.DB.Save(&p).Error; e != nil {
		return e
	}
	c.removeFromCacheLocked(p.IP)
	c.proxies.pl = append(c.proxies.pl, &proxyEntry{p: &p, n: 0})
	c.proxies.index[p.URI()] = struct{}{}
	return nil
}

func (c *MemCache) MarkVerifySuccess(p proxyinabox.Proxy, delay int64, verifyTime time.Time) {
	c.proxies.l.Lock()
	defer c.proxies.l.Unlock()

	c.clearFailureLocked(p.IP)
	proxyinabox.DB.Model(&p).Updates(map[string]interface{}{"delay": delay, "last_verify": verifyTime})

	for _, e := range c.proxies.pl {
		if e.p.IP == p.IP {
			e.p.Delay = delay
			e.p.LastVerify = verifyTime
			return
		}
	}
}

func (c *MemCache) MarkVerifyFailed(p proxyinabox.Proxy) {
	c.proxies.l.Lock()
	defer c.proxies.l.Unlock()

	c.removeFromCacheLocked(p.IP)
	proxyinabox.DB.Model(&p).Update("last_verify", time.Now())
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

// removeFromCacheLocked 调用方必须已持有 c.proxies.l 锁
func (c *MemCache) removeFromCacheLocked(ip string) {
	for i, e := range c.proxies.pl {
		if e.p.IP == ip {
			delete(c.proxies.index, e.p.URI())
			c.proxies.pl = append(c.proxies.pl[:i], c.proxies.pl[i+1:]...)
			return
		}
	}
}

func (c *MemCache) clearFailureLocked(ip string) {
	c.lockedIPs.Delete(ip)
	proxyinabox.DB.Where("ip = ?", ip).Delete(&proxyinabox.BlockedIP{})
}

func getIP(str string) string {
	return strings.Split(str, ":")[0]
}
