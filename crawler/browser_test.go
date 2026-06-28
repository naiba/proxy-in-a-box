package crawler

import (
	"reflect"
	"slices"
	"testing"
)

func TestBuildObscuraServeArgsDefaults(t *testing.T) {
	args := buildObscuraServeArgs(9222, "")
	want := []string{"serve", "--host", "127.0.0.1", "--port", "9222", "--stealth"}

	if !reflect.DeepEqual(args, want) {
		t.Fatalf("args = %#v, want %#v", args, want)
	}
	if slices.Contains(args, "--proxy") {
		t.Fatalf("args unexpectedly include proxy flag: %#v", args)
	}
}

func TestBuildObscuraServeArgsProxy(t *testing.T) {
	proxyAddr := "http://127.0.0.1:8080"
	args := buildObscuraServeArgs(9333, proxyAddr)
	want := []string{"serve", "--host", "127.0.0.1", "--port", "9333", "--stealth", "--proxy", proxyAddr}

	if !reflect.DeepEqual(args, want) {
		t.Fatalf("args = %#v, want %#v", args, want)
	}
	if slices.Contains(args, "--http_proxy") {
		t.Fatalf("args use old proxy flag: %#v", args)
	}
}
