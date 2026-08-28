package proxyinabox

import (
	"net"
	"net/url"
	"os"
	"regexp"
	"strings"
	"testing"
)

func TestPublicExamplesUseReservedHosts(t *testing.T) {
	urlPattern := regexp.MustCompile(`https?://[^\s"'<>)\]]+`)
	allowedReferenceHosts := map[string]struct{}{
		"github.com":       {}, // project dependencies and source links
		"go.dev":           {}, // language badge
		"goreportcard.com": {}, // project badge
		"img.shields.io":   {}, // project badge
	}

	for _, path := range []string{"README.md", "README_zh.md", "examples/main.go"} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		for _, rawURL := range urlPattern.FindAllString(string(data), -1) {
			parsed, err := url.Parse(rawURL)
			if err != nil {
				t.Errorf("%s contains an invalid URL %q: %v", path, rawURL, err)
				continue
			}
			host := strings.ToLower(parsed.Hostname())
			if _, allowed := allowedReferenceHosts[host]; allowed {
				continue
			}
			if strings.HasSuffix(host, ".example") || strings.HasSuffix(host, ".test") {
				continue
			}
			if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
				continue
			}
			t.Errorf("%s contains non-reserved example URL %q", path, rawURL)
		}
	}
}
