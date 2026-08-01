package crawler

import (
	"errors"
	"net"
	"testing"
	"time"
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

func TestDeadlineDialer_SetsConnectionDeadline(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer listener.Close()

	accepted := make(chan struct{})
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		close(accepted)
		<-time.After(time.Second)
	}()

	conn, err := (deadlineDialer{timeout: 50 * time.Millisecond}).Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	select {
	case <-accepted:
	case <-time.After(time.Second):
		t.Fatal("server did not accept connection")
	}
	_, err = conn.Read(make([]byte, 1))
	netErr, ok := err.(net.Error)
	if !ok || !netErr.Timeout() {
		t.Errorf("read error = %v, want timeout", err)
	}
}

func TestProbeTLSHandshakeTimesOut(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()

	started := time.Now()
	err := probeTLSHandshake(client, "example.com", 25*time.Millisecond)
	if err == nil {
		t.Fatal("probeTLSHandshake should time out when the peer does not respond")
	}
	var netErr net.Error
	if !errors.As(err, &netErr) || !netErr.Timeout() {
		t.Fatalf("probeTLSHandshake error = %v, want timeout", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("probeTLSHandshake took %s, want bounded timeout", elapsed)
	}
}
