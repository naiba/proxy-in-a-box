package proxyinabox

import (
	"testing"
)

func TestProxyURI(t *testing.T) {
	tests := []struct {
		name     string
		proxy    Proxy
		expected string
	}{
		{
			name:     "http protocol",
			proxy:    Proxy{IP: "1.2.3.4", Port: "8080", Protocol: "http"},
			expected: "http://1.2.3.4:8080",
		},
		{
			name:     "socks5 protocol",
			proxy:    Proxy{IP: "5.6.7.8", Port: "1080", Protocol: "socks5"},
			expected: "socks5://5.6.7.8:1080",
		},
		{
			name:     "empty protocol defaults to http",
			proxy:    Proxy{IP: "10.0.0.1", Port: "3128", Protocol: ""},
			expected: "http://10.0.0.1:3128",
		},
		{
			name:     "https protocol",
			proxy:    Proxy{IP: "192.168.1.1", Port: "443", Protocol: "https"},
			expected: "https://192.168.1.1:443",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.proxy.URI()
			if got != tt.expected {
				t.Errorf("URI() = %q, want %q", got, tt.expected)
			}
		})
	}
}

func TestProxyString(t *testing.T) {
	p := Proxy{IP: "1.2.3.4", Port: "8080", Protocol: "http", Country: "US", Source: "test"}
	s := p.String()
	if s == "" {
		t.Error("String() returned empty")
	}
	for _, substr := range []string{"1.2.3.4", "8080", "http", "US", "test"} {
		if !contains(s, substr) {
			t.Errorf("String() missing %q in %q", substr, s)
		}
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && searchString(s, substr)
}

func searchString(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
