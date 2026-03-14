package crawler

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"fmt"
	"math/big"
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

func generateSelfSignedCert(cn string, notBefore, notAfter time.Time) *x509.Certificate {
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: cn},
		NotBefore:    notBefore,
		NotAfter:     notAfter,
		KeyUsage:     x509.KeyUsageDigitalSignature,
		DNSNames:     []string{cn},
	}
	certDER, _ := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	cert, _ := x509.ParseCertificate(certDER)
	return cert
}

func TestVerifyPeerCertificates(t *testing.T) {
	t.Run("empty certificate chain", func(t *testing.T) {
		err := verifyPeerCertificates(nil, "example.com")
		if err == nil {
			t.Fatal("expected error for empty chain")
		}
	})

	t.Run("expired self-signed certificate", func(t *testing.T) {
		cert := generateSelfSignedCert("example.com",
			time.Now().Add(-2*time.Hour),
			time.Now().Add(-1*time.Hour))
		err := verifyPeerCertificates([]*x509.Certificate{cert}, "example.com")
		if err == nil {
			t.Fatal("expected error for expired cert")
		}
	})

	t.Run("self-signed certificate not in system roots", func(t *testing.T) {
		cert := generateSelfSignedCert("example.com",
			time.Now().Add(-1*time.Hour),
			time.Now().Add(24*time.Hour))
		err := verifyPeerCertificates([]*x509.Certificate{cert}, "example.com")
		if err == nil {
			t.Fatal("expected error for untrusted self-signed cert")
		}
	})

	t.Run("server name mismatch", func(t *testing.T) {
		cert := generateSelfSignedCert("wrong.com",
			time.Now().Add(-1*time.Hour),
			time.Now().Add(24*time.Hour))
		err := verifyPeerCertificates([]*x509.Certificate{cert}, "example.com")
		if err == nil {
			t.Fatal("expected error for name mismatch")
		}
	})
}

func TestIsTLSHijack(t *testing.T) {
	t.Run("TLSHijackError returns true", func(t *testing.T) {
		err := &TLSHijackError{Err: fmt.Errorf("cert expired")}
		if !isTLSHijack(err) {
			t.Fatal("expected true for TLSHijackError")
		}
	})

	t.Run("wrapped TLSHijackError returns true", func(t *testing.T) {
		inner := &TLSHijackError{Err: fmt.Errorf("cert expired")}
		err := fmt.Errorf("connection failed: %w", inner)
		if !isTLSHijack(err) {
			t.Fatal("expected true for wrapped TLSHijackError")
		}
	})

	t.Run("non-TLS error returns false", func(t *testing.T) {
		err := fmt.Errorf("connection refused")
		if isTLSHijack(err) {
			t.Fatal("expected false for non-TLS error")
		}
	})

	t.Run("nil error returns false", func(t *testing.T) {
		if isTLSHijack(nil) {
			t.Fatal("expected false for nil error")
		}
	})
}

func TestTLSHijackError(t *testing.T) {
	inner := fmt.Errorf("x509: certificate has expired")
	err := &TLSHijackError{Err: inner}

	if err.Error() != "tls hijack detected: x509: certificate has expired" {
		t.Errorf("unexpected error message: %s", err.Error())
	}

	if !errors.Is(err, inner) {
		t.Error("Unwrap should return inner error")
	}
}
