package crawler

import (
	"testing"
)

func TestParseCloudflareTrace(t *testing.T) {
	tests := []struct {
		name      string
		body      string
		wantIP    string
		wantLoc   string
		wantError bool
	}{
		{
			name:    "valid response",
			body:    "fl=1f1\nh=blog.cloudflare.com\nip=1.2.3.4\nts=1234567890\nvisit_scheme=https\nloc=US\n",
			wantIP:  "1.2.3.4",
			wantLoc: "US",
		},
		{
			name:    "ipv6 address",
			body:    "ip=2001:db8::1\nloc=DE\n",
			wantIP:  "2001:db8::1",
			wantLoc: "DE",
		},
		{
			name:      "missing ip field",
			body:      "fl=1f1\nloc=JP\n",
			wantError: true,
		},
		{
			name:    "extra whitespace",
			body:    "  ip=5.6.7.8  \n  loc=CN  \n",
			wantIP:  "5.6.7.8",
			wantLoc: "CN",
		},
		{
			name:      "empty body",
			body:      "",
			wantError: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := parseCloudflareTrace([]byte(tt.body))
			if tt.wantError {
				if err == nil {
					t.Error("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if result.IP != tt.wantIP {
				t.Errorf("IP = %q, want %q", result.IP, tt.wantIP)
			}
			if result.Loc != tt.wantLoc {
				t.Errorf("Loc = %q, want %q", result.Loc, tt.wantLoc)
			}
		})
	}
}
