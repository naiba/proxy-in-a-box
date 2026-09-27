package main

import (
	"encoding/json"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/naiba/proxyinabox"
	"github.com/naiba/proxyinabox/crawler"
	"github.com/naiba/proxyinabox/service"
	"gorm.io/gorm"
)

func TestMaintenanceSummary_SourceStateAndJSON(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&proxyinabox.Proxy{}, &proxyinabox.BlockedIP{}); err != nil {
		t.Fatal(err)
	}
	summary, err := maintenanceSummary(&service.ProxyService{DB: db}, []crawler.SourceStatus{
		{Name: "ok", LastFetch: time.Now()},
		{Name: "failed", LastFetch: time.Now(), Error: "unreachable"},
		{Name: "pending"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if summary.SourceTotal != 3 || summary.SourceErrors != 1 || summary.SourcePending != 1 {
		t.Fatalf("source health = %+v", summary)
	}
	data, err := json.Marshal(summary)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"healthy_due", "quarantined_ready", "quarantined_waiting", "source_errors", "checks"} {
		if !strings.Contains(string(data), `"`+field+`"`) {
			t.Errorf("missing dashboard JSON field %q: %s", field, data)
		}
	}
}

func TestDashboardDisplaysMaintenanceStats(t *testing.T) {
	for _, id := range []string{"statChecksDue", "statWaiting", "statSourceHealth", "statHealthChecks"} {
		if !strings.Contains(dashboardHTML, `id="`+id+`"`) {
			t.Errorf("dashboard missing %s", id)
		}
	}
}

func TestSkipIfStillRunningSkipsOverlappingRun(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var runs atomic.Int32
	var startedOnce sync.Once

	job := skipIfStillRunning(func() {
		runs.Add(1)
		startedOnce.Do(func() { close(started) })
		<-release
	})

	done := make(chan struct{})
	go func() {
		job.Run()
		close(done)
	}()

	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("first cron run did not start")
	}

	job.Run()
	if got := runs.Load(); got != 1 {
		t.Fatalf("overlapping cron runs = %d, want 1", got)
	}

	close(release)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("first cron run did not finish")
	}

	job.Run()
	if got := runs.Load(); got != 2 {
		t.Fatalf("cron runs after release = %d, want 2", got)
	}
}
