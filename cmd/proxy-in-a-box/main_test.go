package main

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

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
