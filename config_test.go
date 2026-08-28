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
}
