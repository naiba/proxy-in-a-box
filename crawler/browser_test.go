package crawler

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"slices"
	"testing"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/cdp"
	"github.com/go-rod/rod/lib/proto"
	"github.com/naiba/proxyinabox"
)

type cdpCall struct {
	sessionID string
	method    string
	params    interface{}
}

type fakeCDPClient struct {
	calls  []cdpCall
	result []byte
	err    error
	events chan *cdp.Event
}

func (c *fakeCDPClient) Call(
	_ context.Context,
	sessionID, method string,
	params interface{},
) ([]byte, error) {
	c.calls = append(c.calls, cdpCall{sessionID: sessionID, method: method, params: params})
	return c.result, c.err
}

func (c *fakeCDPClient) Event() <-chan *cdp.Event {
	return c.events
}

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

func TestBuildObscuraEnvAlignsServerAndClientTimeouts(t *testing.T) {
	env := buildObscuraEnv([]string{"KEEP=value"})
	for _, want := range []string{
		"KEEP=value",
		"OBSCURA_NAV_TIMEOUT_MS=60000",
		"OBSCURA_SCRIPT_DEADLINE_MS=60000",
		"OBSCURA_FETCH_TIMEOUT_MS=60000",
		"OBSCURA_CDP_COMMAND_TIMEOUT_MS=65000",
		"OBSCURA_MODULE_BUDGET_MS=10000",
	} {
		if !slices.Contains(env, want) {
			t.Errorf("environment missing %q: %#v", want, env)
		}
	}
}

func TestObscuraCDPClientSuppressesUnsupportedStopLoading(t *testing.T) {
	upstream := &fakeCDPClient{}
	client := &obscuraCDPClient{CDPClient: upstream}

	result, err := client.Call(context.Background(), "page-session", "Page.stopLoading", nil)
	if err != nil {
		t.Fatalf("Call() error = %v", err)
	}
	if string(result) != `{}` {
		t.Fatalf("Call() result = %q, want {}", result)
	}
	if len(upstream.calls) != 0 {
		t.Fatalf("upstream calls = %#v, want none", upstream.calls)
	}
}

func TestObscuraCDPClientForwardsSupportedCalls(t *testing.T) {
	upstream := &fakeCDPClient{result: []byte(`{"frameId":"page-1"}`)}
	client := &obscuraCDPClient{CDPClient: upstream}
	params := proto.PageNavigate{URL: "https://example.com"}

	result, err := client.Call(context.Background(), "page-session", "Page.navigate", params)
	if err != nil {
		t.Fatalf("Call() error = %v", err)
	}
	if string(result) != string(upstream.result) {
		t.Fatalf("Call() result = %q, want %q", result, upstream.result)
	}
	if len(upstream.calls) != 1 || upstream.calls[0].method != "Page.navigate" {
		t.Fatalf("upstream calls = %#v, want Page.navigate", upstream.calls)
	}
}

func TestClosePageUsesTargetCloseTarget(t *testing.T) {
	upstream := &fakeCDPClient{result: []byte(`{"success":true}`)}
	session := &BrowserSession{
		browser: rod.New().Client(upstream),
		page:    &rod.Page{TargetID: "page-1"},
	}

	if err := session.closePage(); err != nil {
		t.Fatalf("closePage() error = %v", err)
	}
	if session.page != nil {
		t.Fatal("closePage() did not clear the current page")
	}
	if len(upstream.calls) != 1 {
		t.Fatalf("upstream calls = %#v, want one", upstream.calls)
	}
	call := upstream.calls[0]
	if call.method != "Target.closeTarget" || call.sessionID != "" {
		t.Fatalf("upstream call = %#v, want browser-level Target.closeTarget", call)
	}
	params, ok := call.params.(proto.TargetCloseTarget)
	if !ok {
		t.Fatalf("params type = %T, want proto.TargetCloseTarget", call.params)
	}
	if params.TargetID != "page-1" {
		t.Fatalf("target ID = %q, want page-1", params.TargetID)
	}
}

func TestClosePageKeepsPageWhenTargetCloseFails(t *testing.T) {
	wantErr := errors.New("close failed")
	upstream := &fakeCDPClient{err: wantErr}
	page := &rod.Page{TargetID: "page-1"}
	session := &BrowserSession{browser: rod.New().Client(upstream), page: page}

	err := session.closePage()
	if !errors.Is(err, wantErr) {
		t.Fatalf("closePage() error = %v, want %v", err, wantErr)
	}
	if session.page != page {
		t.Fatal("closePage() cleared the page after a failed target close")
	}
}

func TestLatestObscuraIntegration(t *testing.T) {
	bin := os.Getenv("OBSCURA_INTEGRATION_BIN")
	if bin == "" {
		t.Skip("set OBSCURA_INTEGRATION_BIN to run against a release binary")
	}

	previous := proxyinabox.Config.Obscura.Bin
	proxyinabox.Config.Obscura.Bin = bin
	t.Cleanup(func() { proxyinabox.Config.Obscura.Bin = previous })
	t.Setenv("OBSCURA_ALLOW_PRIVATE_NETWORK", "1")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("<html><body>obscura-v0.2.1-ok</body></html>"))
	}))
	t.Cleanup(server.Close)

	session := &BrowserSession{}
	if err := session.start(); err != nil {
		t.Fatalf("start latest Obscura: %v", err)
	}
	t.Cleanup(session.stop)

	if err := session.navigate(server.URL); err != nil {
		t.Fatalf("navigate: %v", err)
	}
	result, err := session.evaluate("document.body.textContent")
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if result != "obscura-v0.2.1-ok" {
		t.Fatalf("body text = %q, want obscura-v0.2.1-ok", result)
	}
}
