package main

import (
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

type processRuntimeStats struct {
	CPUPercent    float64 `json:"cpu_percent"`
	CPUSeconds    float64 `json:"cpu_seconds"`
	RSSBytes      uint64  `json:"rss_bytes"`
	HeapAlloc     uint64  `json:"heap_alloc_bytes"`
	HeapInUse     uint64  `json:"heap_inuse_bytes"`
	GoSysBytes    uint64  `json:"go_sys_bytes"`
	Goroutines    int     `json:"goroutines"`
	OpenFDs       int     `json:"open_fds"`
	UptimeSeconds int64   `json:"uptime_seconds"`
}

type processRuntimeSampler struct {
	mu      sync.Mutex
	started time.Time
	lastAt  time.Time
	lastCPU float64
}

var runtimeSampler = processRuntimeSampler{started: time.Now()}

func (s *processRuntimeSampler) snapshot() processRuntimeStats {
	s.mu.Lock()
	now := time.Now()
	cpu := processCPUSeconds()
	baseAt, baseCPU := s.lastAt, s.lastCPU
	if baseAt.IsZero() {
		baseAt, baseCPU = s.started, 0
	}
	deltaWall := now.Sub(baseAt).Seconds()
	cpuPercent := 0.0
	if deltaWall > 0 && cpu >= baseCPU {
		cpuPercent = (cpu - baseCPU) / deltaWall * 100
	}
	s.lastAt, s.lastCPU = now, cpu
	s.mu.Unlock()

	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)
	return processRuntimeStats{
		CPUPercent:    cpuPercent,
		CPUSeconds:    cpu,
		RSSBytes:      currentRSSBytes(),
		HeapAlloc:     mem.HeapAlloc,
		HeapInUse:     mem.HeapInuse,
		GoSysBytes:    mem.Sys,
		Goroutines:    runtime.NumGoroutine(),
		OpenFDs:       openFDCount(),
		UptimeSeconds: int64(now.Sub(s.started) / time.Second),
	}
}

func processCPUSeconds() float64 {
	var usage syscall.Rusage
	if syscall.Getrusage(syscall.RUSAGE_SELF, &usage) != nil {
		return 0
	}
	return timevalSeconds(usage.Utime) + timevalSeconds(usage.Stime)
}

func timevalSeconds(tv syscall.Timeval) float64 {
	return float64(tv.Sec) + float64(tv.Usec)/1e6
}

func currentRSSBytes() uint64 {
	data, err := os.ReadFile("/proc/self/statm")
	if err != nil {
		return 0
	}
	fields := strings.Fields(string(data))
	if len(fields) < 2 {
		return 0
	}
	pages, err := strconv.ParseUint(fields[1], 10, 64)
	if err != nil {
		return 0
	}
	return pages * uint64(os.Getpagesize())
}

func openFDCount() int {
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		return -1
	}
	return len(entries)
}
