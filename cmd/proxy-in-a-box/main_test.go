package main

import (
	"encoding/json"
	"errors"
	"net"
	"net/http"
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

func TestServeManagerDoesNotWaitForStartupVerification(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	started := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	var runs atomic.Int32
	job := skipIfStillRunning(func() {
		runs.Add(1)
		close(started)
		<-release
	})
	cleaned := make(chan struct{})
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	done := make(chan error, 1)
	go func() {
		done <- serveManager(listener, mux, job, func() { close(cleaned) })
	}()

	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("startup verification did not start")
	}
	client := &http.Client{Timeout: time.Second}
	resp, err := client.Get("http://" + listener.Addr().String())
	if err != nil {
		t.Fatalf("dashboard unavailable while verifying: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("dashboard returned %d", resp.StatusCode)
	}
	job.Run() // Simulate the scheduled scan while startup verification is still running.
	if runs.Load() != 1 {
		t.Fatalf("overlapping scans ran %d times, want 1", runs.Load())
	}
	select {
	case <-cleaned:
		t.Fatal("cleanup started before startup verification finished")
	default:
	}
	releaseOnce.Do(func() { close(release) })
	select {
	case <-cleaned:
	case <-time.After(time.Second):
		t.Fatal("cleanup did not run after startup verification")
	}
	listener.Close()
	select {
	case err := <-done:
		if !errors.Is(err, net.ErrClosed) {
			t.Fatalf("serveManager returned %v, want closed listener", err)
		}
	case <-time.After(time.Second):
		t.Fatal("management server did not stop")
	}
}
