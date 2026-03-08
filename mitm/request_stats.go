package mitm

import (
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
)

// RequestStats 代理请求的全局运行时统计（atomic，无锁）
type RequestStats struct {
	TotalRequests   atomic.Int64
	SuccessRequests atomic.Int64
	FailedRequests  atomic.Int64
	// HTTPS 隧道模式下流量在内核态透传，无法在用户态精确计量，仅统计 HTTP 明文代理的响应体
	BytesTransferred atomic.Int64
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
