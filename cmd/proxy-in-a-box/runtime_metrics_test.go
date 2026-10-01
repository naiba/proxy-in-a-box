package main

import (
	"runtime"
	"testing"
)

func TestProcessRuntimeSnapshot(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("current RSS and file descriptor metrics use procfs")
	}
	got := runtimeSampler.snapshot()
	if got.RSSBytes == 0 {
		t.Error("RSS should be available on Linux")
	}
	if got.HeapAlloc == 0 || got.GoSysBytes == 0 {
		t.Fatalf("Go memory snapshot is empty: %+v", got)
	}
	if got.Goroutines < 1 {
		t.Fatalf("goroutines = %d, want at least one", got.Goroutines)
	}
	if got.OpenFDs < 0 {
		t.Fatalf("open FDs unavailable: %+v", got)
	}
}

func BenchmarkProcessRuntimeSnapshot(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = runtimeSampler.snapshot()
	}
}
