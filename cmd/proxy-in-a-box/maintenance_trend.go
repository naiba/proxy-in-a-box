package main

import (
	"fmt"
	"sync"
	"time"

	"github.com/naiba/proxyinabox/service"
)

const (
	maintenanceSampleInterval = 5 * time.Minute
	maintenanceTrendWindow    = 10 * time.Minute
)

type backlogSample struct {
	at      time.Time
	due     int64
	waiting int64
}

type backlogHistory struct {
	mu      sync.Mutex
	samples []backlogSample
}

type backlogTrend struct {
	Ready           bool  `json:"ready"`
	DueDelta        int64 `json:"due_delta"`
	WaitingDelta    int64 `json:"waiting_delta"`
	DueStalled      bool  `json:"due_stalled"`
	DueGrowing      bool  `json:"due_growing"`
	WaitingGrowing  bool  `json:"waiting_growing"`
	WaitingBaseline int64 `json:"waiting_baseline"`
}

func dueCount(stats service.MaintenanceStats) int64 {
	return stats.HealthyDue + stats.QuarantinedReady
}

func (h *backlogHistory) record(at time.Time, stats service.MaintenanceStats) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.samples = append(h.samples, backlogSample{at: at, due: dueCount(stats), waiting: stats.QuarantinedWaiting})
	cutoff := at.Add(-2 * maintenanceTrendWindow)
	for len(h.samples) > 0 && h.samples[0].at.Before(cutoff) {
		h.samples = h.samples[1:]
	}
}

func (h *backlogHistory) trend(now time.Time, current service.MaintenanceStats) backlogTrend {
	h.mu.Lock()
	defer h.mu.Unlock()
	var old, mid *backlogSample
	for i := range h.samples {
		sample := &h.samples[i]
		age := now.Sub(sample.at)
		if age >= maintenanceTrendWindow && age <= maintenanceTrendWindow+maintenanceSampleInterval {
			old = sample
		}
		if age >= maintenanceSampleInterval && age < maintenanceTrendWindow {
			mid = sample
		}
	}
	if old == nil || mid == nil {
		return backlogTrend{}
	}
	due := dueCount(current)
	return backlogTrend{
		Ready:           true,
		DueDelta:        due - old.due,
		WaitingDelta:    current.QuarantinedWaiting - old.waiting,
		DueStalled:      due > 0 && old.due > 0 && mid.due >= old.due && due >= mid.due,
		DueGrowing:      due > old.due && mid.due >= old.due && due >= mid.due,
		WaitingGrowing:  mid.waiting > old.waiting && current.QuarantinedWaiting > mid.waiting,
		WaitingBaseline: old.waiting,
	}
}

// Sampling is independent of dashboard traffic, so a newly opened dashboard
// can still display the preceding ten minutes' trend.
func startBacklogSampler(ps *service.ProxyService, history *backlogHistory) {
	go func() {
		ticker := time.NewTicker(maintenanceSampleInterval)
		defer ticker.Stop()
		for {
			stats, err := ps.GetMaintenanceStats()
			if err != nil {
				fmt.Printf("[PIAB] maintenance [❎] sample backlog: %v\n", err)
			} else {
				history.record(time.Now(), stats)
			}
			<-ticker.C
		}
	}()
}

type maintenanceLevels struct {
	Due     string `json:"due"`
	Waiting string `json:"waiting"`
	Sources string `json:"sources"`
	Checks  string `json:"checks"`
}

func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

func classifyMaintenance(m maintenanceSnapshot, available int64) maintenanceLevels {
	levels := maintenanceLevels{Due: "gray", Waiting: "neutral", Sources: "gray", Checks: "gray"}
	due := dueCount(m.MaintenanceStats)
	switch {
	case due == 0:
		levels.Due = "green"
	case !m.Trend.Ready:
		levels.Due = "gray"
	case m.Trend.DueStalled && m.Trend.DueGrowing && due >= maxInt64(20, available/5):
		levels.Due = "red"
	case m.Trend.DueStalled:
		levels.Due = "yellow"
	default:
		levels.Due = "green"
	}
	waiting := m.QuarantinedWaiting
	baseline := maxInt64(1, m.Trend.WaitingBaseline)
	if m.Trend.Ready && m.Trend.WaitingGrowing && waiting >= maxInt64(10, available/10) && waiting >= 2*baseline {
		levels.Waiting = "yellow"
		if waiting >= maxInt64(20, available/2) && waiting >= 3*baseline {
			levels.Waiting = "red"
		}
	}
	switch {
	case m.SourceTotal == 0:
	case m.SourceErrors*2 >= m.SourceTotal:
		levels.Sources = "red"
	case m.SourceErrors > 0:
		levels.Sources = "yellow"
	case m.SourcePending > 0:
	default:
		levels.Sources = "green"
	}
	routineTotal := m.RecentChecks.RoutineSuccess + m.RecentChecks.RoutineFailure
	if routineTotal >= 10 {
		// Public proxies naturally churn at a high rate. A high failure ratio by
		// itself is therefore informational, not a service-critical condition.
		// Escalate only when no check succeeds or failures coincide with a large,
		// sustained backlog that the workers cannot drain.
		switch {
		case m.RecentChecks.RoutineSuccess == 0:
			levels.Checks = "red"
		case levels.Due == "red" && m.RecentChecks.RoutineFailure*2 > routineTotal:
			levels.Checks = "red"
		case m.RecentChecks.RoutineFailure*2 > routineTotal:
			levels.Checks = "neutral"
		default:
			levels.Checks = "green"
		}
	}
	return levels
}
