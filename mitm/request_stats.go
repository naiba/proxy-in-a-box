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

// ProtocolRequestStats 按上游协议（HTTP 明文 / HTTPS CONNECT 隧道）分别统计请求数和流量
type ProtocolRequestStats struct {
	HTTP  RequestStats
	HTTPS RequestStats
}

// trafficCountingWriter 包装 io.Writer，将写入字节数实时累加到 GlobalRequestStats.BytesTransferred。
// 用于 io.Copy 场景（如 HTTPS 隧道透传），无需缓存整个响应即可精确计量流量。
type trafficCountingWriter struct {
	inner         io.Writer
	protocolStats *RequestStats
}

func (w *trafficCountingWriter) Write(p []byte) (int, error) {
	n, err := w.inner.Write(p)
	if n > 0 {
		GlobalRequestStats.BytesTransferred.Add(int64(n))
		if w.protocolStats != nil {
			w.protocolStats.BytesTransferred.Add(int64(n))
		}
	}
	return n, err
}

var GlobalRequestStats = &RequestStats{}
var GlobalProtocolStats = &ProtocolRequestStats{}

type RequestStatsSnapshot struct {
	TotalRequests    int64   `json:"total_requests"`
	SuccessRequests  int64   `json:"success_requests"`
	FailedRequests   int64   `json:"failed_requests"`
	SuccessRate      float64 `json:"success_rate"`
	BytesTransferred int64   `json:"bytes_transferred"`
}

type ProtocolStatsSnapshot struct {
	Total RequestStatsSnapshot `json:"total"`
	HTTP  RequestStatsSnapshot `json:"http"`
	HTTPS RequestStatsSnapshot `json:"https"`
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

func (s *ProtocolRequestStats) Snapshot() ProtocolStatsSnapshot {
	return ProtocolStatsSnapshot{
		Total: GlobalRequestStats.Snapshot(),
		HTTP:  s.HTTP.Snapshot(),
		HTTPS: s.HTTPS.Snapshot(),
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
