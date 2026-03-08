package mitm

import (
	"io"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
)

// RequestStats 代理请求的全局运行时统计（atomic，无锁）
type RequestStats struct {
	TotalRequests    atomic.Int64
	SuccessRequests  atomic.Int64
	FailedRequests   atomic.Int64
	BytesTransferred atomic.Int64
}

// UpstreamProtocolStats 按上游代理协议（http/https/socks4/socks5）分别统计请求数和流量
type UpstreamProtocolStats struct {
	mu    sync.RWMutex
	stats map[string]*RequestStats
}

var GlobalUpstreamStats = &UpstreamProtocolStats{
	stats: make(map[string]*RequestStats),
}

func (u *UpstreamProtocolStats) Get(protocol string) *RequestStats {
	protocol = strings.ToLower(protocol)
	u.mu.RLock()
	s, ok := u.stats[protocol]
	u.mu.RUnlock()
	if ok {
		return s
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	s, ok = u.stats[protocol]
	if !ok {
		s = &RequestStats{}
		u.stats[protocol] = s
	}
	return s
}

func (u *UpstreamProtocolStats) Snapshot() map[string]RequestStatsSnapshot {
	u.mu.RLock()
	defer u.mu.RUnlock()
	result := make(map[string]RequestStatsSnapshot, len(u.stats))
	for proto, s := range u.stats {
		result[proto] = s.Snapshot()
	}
	return result
}

// trafficCountingWriter 包装 io.Writer，将写入字节数实时累加到 GlobalRequestStats.BytesTransferred。
// 用于 io.Copy 场景（如 HTTPS 隧道透传），无需缓存整个响应即可精确计量流量。
type trafficCountingWriter struct {
	inner            io.Writer
	upstreamProtocol string
}

func (w *trafficCountingWriter) Write(p []byte) (int, error) {
	n, err := w.inner.Write(p)
	if n > 0 {
		GlobalRequestStats.BytesTransferred.Add(int64(n))
		if w.upstreamProtocol != "" {
			GlobalUpstreamStats.Get(w.upstreamProtocol).BytesTransferred.Add(int64(n))
		}
	}
	return n, err
}

var GlobalRequestStats = &RequestStats{}

type RequestStatsSnapshot struct {
	TotalRequests    int64   `json:"total_requests"`
	SuccessRequests  int64   `json:"success_requests"`
	FailedRequests   int64   `json:"failed_requests"`
	SuccessRate      float64 `json:"success_rate"`
	BytesTransferred int64   `json:"bytes_transferred"`
}

func (s *RequestStats) Snapshot() RequestStatsSnapshot {
	total := s.TotalRequests.Load()
	success := s.SuccessRequests.Load()
	failed := s.FailedRequests.Load()
	bytes := s.BytesTransferred.Load()

	var rate float64
	if total > 0 {
		rate = float64(success) / float64(total)
	}
	return RequestStatsSnapshot{
		TotalRequests:    total,
		SuccessRequests:  success,
		FailedRequests:   failed,
		SuccessRate:      rate,
		BytesTransferred: bytes,
	}
}

type ProcessCounts struct {
	Chromium int `json:"chromium"`
	Pinchtab int `json:"pinchtab"`
}

// pgrep 未找到进程时返回退出码 1，此时返回 0（非错误）
func countProcessByName(name string) int {
	out, err := exec.Command("pgrep", "-c", "-f", name).Output()
	if err != nil {
		return 0
	}
	n, _ := strconv.Atoi(strings.TrimSpace(string(out)))
	return n
}

var processCountsMu sync.Mutex

func GetProcessCounts() ProcessCounts {
	processCountsMu.Lock()
	defer processCountsMu.Unlock()
	return ProcessCounts{
		Chromium: countProcessByName("chromium"),
		Pinchtab: countProcessByName("pinchtab"),
	}
}
