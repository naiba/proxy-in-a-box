package proxyinabox

import (
	"strings"
	"testing"
	"time"

	"github.com/spf13/viper"
)

func TestUpstreamConfigParsesDurations(t *testing.T) {
	v := viper.New()
	v.SetConfigType("yaml")
	if err := v.ReadConfig(strings.NewReader(`
upstream:
  max_attempts: 4
  connect_timeout: 3s
  handshake_timeout: 6s
  response_header_timeout: 9s
  request_timeout: 15s
  target_failure_ttl: 2m
verification:
  interval: 3h
  deep_check_interval: 36h
  retries: 4
  response_body_limit: 8192
  max_attempts_per_second: 40
  attempt_burst: 12
  routine_reserved_workers: 5
source_fetch:
  proxy_fallback: true
  retries: 3
  timeout: 12s
  response_body_limit: 1048576
  max_candidates_per_fetch: 150
`)); err != nil {
		t.Fatalf("read config: %v", err)
	}

	var config Conf
	if err := v.Unmarshal(&config); err != nil {
		t.Fatalf("unmarshal config: %v", err)
	}
	if config.Upstream.MaxAttempts != 4 {
		t.Errorf("max attempts = %d, want 4", config.Upstream.MaxAttempts)
	}
	durations := map[string]struct {
		got  time.Duration
		want time.Duration
	}{
		"connect":         {config.Upstream.ConnectTimeout, 3 * time.Second},
		"handshake":       {config.Upstream.HandshakeTimeout, 6 * time.Second},
		"response header": {config.Upstream.ResponseHeaderTimeout, 9 * time.Second},
		"request":         {config.Upstream.RequestTimeout, 15 * time.Second},
		"target failure":  {config.Upstream.TargetFailureTTL, 2 * time.Minute},
	}
	for name, duration := range durations {
		if duration.got != duration.want {
			t.Errorf("%s duration = %s, want %s", name, duration.got, duration.want)
		}
	}
	if config.Verification.Interval != 3*time.Hour {
		t.Errorf("verification interval = %s, want 3h", config.Verification.Interval)
	}
	if config.Verification.DeepCheckInterval != 36*time.Hour {
		t.Errorf("deep check interval = %s, want 36h", config.Verification.DeepCheckInterval)
	}
	if config.Verification.Retries != 4 {
		t.Errorf("verification retries = %d, want 4", config.Verification.Retries)
	}
	if config.Verification.ResponseBodyLimit != 8192 {
		t.Errorf("verification response body limit = %d, want 8192", config.Verification.ResponseBodyLimit)
	}
	if config.Verification.MaxAttemptsPerSecond != 40 || config.Verification.AttemptBurst != 12 || config.Verification.RoutineReservedWorkers != 5 {
		t.Errorf("verification resource limits = %d/%d/%d", config.Verification.MaxAttemptsPerSecond, config.Verification.AttemptBurst, config.Verification.RoutineReservedWorkers)
	}
	if !config.SourceFetch.ProxyFallback || config.SourceFetch.Retries != 3 {
		t.Errorf("source fetch fallback/retries = %v/%d, want true/3", config.SourceFetch.ProxyFallback, config.SourceFetch.Retries)
	}
	if config.SourceFetch.Timeout != 12*time.Second || config.SourceFetch.ResponseBodyLimit != 1048576 {
		t.Errorf("source fetch timeout/limit = %s/%d", config.SourceFetch.Timeout, config.SourceFetch.ResponseBodyLimit)
	}
	if config.SourceFetch.MaxCandidatesPerFetch != 150 {
		t.Errorf("source max candidates per fetch = %d, want 150", config.SourceFetch.MaxCandidatesPerFetch)
	}
}
