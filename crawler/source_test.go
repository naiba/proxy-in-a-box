package crawler

import (
	"testing"

	"github.com/naiba/proxyinabox"
)

func TestParseTextResponse(t *testing.T) {
	tests := []struct {
		name      string
		body      string
		src       Source
		wantLen   int
		checkIdx  int
		wantIP    string
		wantPort  string
		wantProto string
	}{
		{
			name:      "basic ip:port list",
			body:      "1.2.3.4:8080\n5.6.7.8:3128\n",
			src:       Source{Name: "test", Protocol: "http"},
			wantLen:   2,
			checkIdx:  0,
			wantIP:    "1.2.3.4",
			wantPort:  "8080",
			wantProto: "http",
		},
		{
			name:      "spys.me format with extra fields",
			body:      "1.2.3.4:8080 US-H-S+\n5.6.7.8:3128 CN-N!-\n",
			src:       Source{Name: "spys", Protocol: "http"},
			wantLen:   2,
			checkIdx:  1,
			wantIP:    "5.6.7.8",
			wantPort:  "3128",
			wantProto: "http",
		},
		{
			name:      "protocol://ip:port format",
			body:      "socks5://10.0.0.1:1080\nhttp://10.0.0.2:8080\n",
			src:       Source{Name: "trio", Protocol: "http"},
			wantLen:   2,
			checkIdx:  0,
			wantIP:    "10.0.0.1",
			wantPort:  "1080",
			wantProto: "socks5",
		},
		{
			name:    "empty lines and whitespace",
			body:    "\n  \n1.2.3.4:8080\n  \n",
			src:     Source{Name: "test", Protocol: "http"},
			wantLen: 1,
		},
		{
			name:    "invalid lines skipped",
			body:    "not-a-proxy\n1.2.3.4:8080\njunk\n",
			src:     Source{Name: "test", Protocol: "http"},
			wantLen: 1,
		},
		{
			name:    "empty body",
			body:    "",
			src:     Source{Name: "test", Protocol: "http"},
			wantLen: 0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			proxies := parseTextResponse(tt.body, tt.src)
			if len(proxies) != tt.wantLen {
				t.Fatalf("got %d proxies, want %d", len(proxies), tt.wantLen)
			}
			if tt.wantLen > 0 && tt.wantIP != "" {
				p := proxies[tt.checkIdx]
				if p.IP != tt.wantIP {
					t.Errorf("IP = %q, want %q", p.IP, tt.wantIP)
				}
				if p.Port != tt.wantPort {
					t.Errorf("Port = %q, want %q", p.Port, tt.wantPort)
				}
				if p.Protocol != tt.wantProto {
					t.Errorf("Protocol = %q, want %q", p.Protocol, tt.wantProto)
				}
			}
		})
	}
}

func TestParseJSONResponse(t *testing.T) {
	tests := []struct {
		name     string
		body     string
		src      Source
		wantLen  int
		wantIP   string
		wantPort string
	}{
		{
			name: "nested array with field paths",
			body: `{"proxies":[{"ip":"1.2.3.4","port":8080},{"ip":"5.6.7.8","port":3128}]}`,
			src: Source{
				Name:      "test",
				IPField:   "proxies.*.ip",
				PortField: "proxies.*.port",
				Protocol:  "http",
			},
			wantLen:  2,
			wantIP:   "1.2.3.4",
			wantPort: "8080",
		},
		{
			name: "root array",
			body: `[{"host":"10.0.0.1","port":"1080"}]`,
			src: Source{
				Name:      "test",
				IPField:   "*.host",
				PortField: "*.port",
				Protocol:  "socks5",
			},
			wantLen:  1,
			wantIP:   "10.0.0.1",
			wantPort: "1080",
		},
		{
			name: "with protocol field",
			body: `{"proxies":[{"ip":"1.2.3.4","port":8080,"protocol":"socks4"}]}`,
			src: Source{
				Name:          "test",
				IPField:       "proxies.*.ip",
				PortField:     "proxies.*.port",
				ProtocolField: "proxies.*.protocol",
				Protocol:      "http",
			},
			wantLen: 1,
		},
		{
			name: "invalid json",
			body: `not json at all`,
			src: Source{
				Name:      "test",
				IPField:   "*.ip",
				PortField: "*.port",
			},
			wantLen: 0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			proxies := parseJSONResponse(tt.body, tt.src)
			if len(proxies) != tt.wantLen {
				t.Fatalf("got %d proxies, want %d", len(proxies), tt.wantLen)
			}
			if tt.wantLen > 0 && tt.wantIP != "" {
				if proxies[0].IP != tt.wantIP {
					t.Errorf("IP = %q, want %q", proxies[0].IP, tt.wantIP)
				}
				if proxies[0].Port != tt.wantPort {
					t.Errorf("Port = %q, want %q", proxies[0].Port, tt.wantPort)
				}
			}
		})
	}
}

func TestUpdateSourceAvailableCounts(t *testing.T) {
	sourceStatusesMu.Lock()
	sourceStatuses = []SourceStatus{
		{Name: "src-a"},
		{Name: "src-b"},
		{Name: "src-c"},
	}
	sourceStatusesMu.Unlock()

	proxies := []proxyinabox.Proxy{
		{IP: "1.1.1.1", Source: "src-a"},
		{IP: "2.2.2.2", Source: "src-a"},
		{IP: "3.3.3.3", Source: "src-b"},
	}

	UpdateSourceAvailableCounts(proxies)

	statuses := GetSourceStatuses()
	expected := map[string]int{"src-a": 2, "src-b": 1, "src-c": 0}
	for _, s := range statuses {
		if s.AvailableCount != expected[s.Name] {
			t.Errorf("source %s: AvailableCount = %d, want %d", s.Name, s.AvailableCount, expected[s.Name])
		}
	}
}

func TestSourceIntervalDuration(t *testing.T) {
	tests := []struct {
		interval string
		wantMins float64
	}{
		{"5m", 5},
		{"10m", 10},
		{"1h", 60},
		{"", 5},
		{"invalid", 5},
	}
	for _, tt := range tests {
		s := Source{Interval: tt.interval}
		got := s.intervalDuration().Minutes()
		if got != tt.wantMins {
			t.Errorf("interval %q: got %.0f min, want %.0f min", tt.interval, got, tt.wantMins)
		}
	}
}
