package main

import (
	"testing"
	"time"

	"github.com/naiba/proxyinabox/crawler"
	"github.com/naiba/proxyinabox/service"
)

func TestBacklogTrendRequiresTenMinutesAndThreeSamples(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	history := &backlogHistory{}
	old := service.MaintenanceStats{HealthyDue: 10, QuarantinedWaiting: 2}
	mid := service.MaintenanceStats{HealthyDue: 12, QuarantinedWaiting: 4}
	current := service.MaintenanceStats{HealthyDue: 15, QuarantinedWaiting: 6}
	history.record(now.Add(-10*time.Minute), old)
	if history.trend(now, current).Ready {
		t.Fatal("one sample cannot establish a trend")
	}
	history.record(now.Add(-5*time.Minute), mid)
	trend := history.trend(now, current)
	if !trend.Ready || !trend.DueStalled || !trend.DueGrowing || !trend.WaitingGrowing ||
		trend.DueDelta != 5 || trend.WaitingDelta != 4 || trend.WaitingBaseline != 2 {
		t.Fatalf("trend = %+v", trend)
	}
	if history.trend(now.Add(6*time.Minute), current).Ready {
		t.Fatal("old samples should not masquerade as a current trend")
	}
	shrinking := history.trend(now, service.MaintenanceStats{HealthyDue: 5, QuarantinedWaiting: 1})
	if !shrinking.Ready || shrinking.DueStalled || shrinking.WaitingGrowing || shrinking.DueDelta != -5 {
		t.Fatalf("shrinking backlog = %+v", shrinking)
	}
}

func TestClassifyMaintenanceSeparatesNormalAndAbnormalStates(t *testing.T) {
	m := maintenanceSnapshot{MaintenanceStats: service.MaintenanceStats{HealthyDue: 15, QuarantinedWaiting: 6},
		Trend:       backlogTrend{Ready: true, DueStalled: true, DueGrowing: true, WaitingGrowing: true, WaitingBaseline: 2},
		SourceTotal: 3, SourceErrors: 1, SourcePending: 1,
		RecentChecks: crawler.VerificationStats{RoutineSuccess: 8, RoutineFailure: 2, CandidateFailure: 100},
	}
	got := classifyMaintenance(m, 100)
	if got.Due != "yellow" || got.Waiting != "neutral" || got.Sources != "yellow" || got.Checks != "green" {
		t.Fatalf("moderate maintenance state = %+v", got)
	}
	m.QuarantinedWaiting = 12
	m.RecentChecks.RoutineSuccess = 7
	m.RecentChecks.RoutineFailure = 3
	got = classifyMaintenance(m, 100)
	if got.Waiting != "yellow" || got.Checks != "yellow" {
		t.Fatalf("rising retry queue and moderate routine failures = %+v", got)
	}

	m.HealthyDue = 25
	m.QuarantinedWaiting = 30
	m.SourceErrors = 2
	m.SourcePending = 0
	m.RecentChecks.RoutineFailure = 11
	got = classifyMaintenance(m, 40)
	if got.Due != "red" || got.Waiting != "red" || got.Sources != "red" || got.Checks != "red" {
		t.Fatalf("critical maintenance state = %+v", got)
	}

	m = maintenanceSnapshot{MaintenanceStats: service.MaintenanceStats{HealthyDue: 1, QuarantinedWaiting: 20},
		SourceTotal: 3, SourcePending: 3,
	}
	got = classifyMaintenance(m, 100)
	if got.Due != "gray" || got.Waiting != "neutral" || got.Sources != "gray" || got.Checks != "gray" {
		t.Fatalf("startup state = %+v", got)
	}
	m.HealthyDue = 0
	if got := classifyMaintenance(m, 100); got.Due != "green" {
		t.Fatalf("empty backlog should be green even without history: %+v", got)
	}
	m.Trend = backlogTrend{Ready: true, DueStalled: false}
	m.HealthyDue = 15
	if got := classifyMaintenance(m, 100); got.Due != "green" {
		t.Fatalf("shrinking backlog should remain green: %+v", got)
	}
	m.HealthyDue = 25
	m.Trend.DueGrowing = true
	if got := classifyMaintenance(m, 100); got.Due != "green" {
		t.Fatalf("new backlog without sustained prior backlog should not be red: %+v", got)
	}
}
