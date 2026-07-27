package proxy

import (
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"testing"

	"github.com/dangtuananh123456/gateway/internal/config"
)

func TestRewriteSetsTargetPathQueryAndForwardingHeaders(t *testing.T) {
	route := newTestRoute(t, config.RouteConfig{
		Prefix:      "/api/users",
		Upstream:    "http://users:8080",
		StripPrefix: "/api/users",
	})
	request := newProxyRequest(
		t,
		"https://gateway.example/api/users/42?include=roles&active=true",
	)
	request.In.RemoteAddr = "203.0.113.10:54321"
	request.Out.Header.Set("Forwarded", "for=spoofed")
	request.Out.Header.Set("X-Forwarded-For", "198.51.100.1")
	request.Out.Header.Set("X-Forwarded-Host", "spoofed.example")
	request.Out.Header.Set("X-Forwarded-Proto", "ftp")
	request.Out.Header.Set("X-Real-IP", "198.51.100.2")
	request.Out.Header.Set("X-Request-ID", "request-123")

	NewRewrite(route)(request)

	if request.Out.URL.Scheme != "http" {
		t.Errorf("URL.Scheme = %q, want http", request.Out.URL.Scheme)
	}
	if request.Out.URL.Host != "users:8080" {
		t.Errorf("URL.Host = %q, want users:8080", request.Out.URL.Host)
	}
	if request.Out.URL.Path != "/42" {
		t.Errorf("URL.Path = %q, want /42", request.Out.URL.Path)
	}
	if request.Out.URL.RawQuery != "include=roles&active=true" {
		t.Errorf(
			"URL.RawQuery = %q, want %q",
			request.Out.URL.RawQuery,
			"include=roles&active=true",
		)
	}
	if request.Out.Host != "" {
		t.Errorf("Host = %q, want empty so transport uses target host", request.Out.Host)
	}
	if got := request.Out.Header.Get("X-Forwarded-For"); got != "203.0.113.10" {
		t.Errorf("X-Forwarded-For = %q, want %q", got, "203.0.113.10")
	}
	if got := request.Out.Header.Get("X-Forwarded-Host"); got != "gateway.example" {
		t.Errorf("X-Forwarded-Host = %q, want %q", got, "gateway.example")
	}
	if got := request.Out.Header.Get("X-Forwarded-Proto"); got != "https" {
		t.Errorf("X-Forwarded-Proto = %q, want https", got)
	}
	if got := request.Out.Header.Get("Forwarded"); got != "" {
		t.Errorf("Forwarded = %q, want empty", got)
	}
	if got := request.Out.Header.Get("X-Real-IP"); got != "" {
		t.Errorf("X-Real-IP = %q, want empty", got)
	}
	if got := request.Out.Header.Get("X-Request-ID"); got != "request-123" {
		t.Errorf("X-Request-ID = %q, want preserved request ID", got)
	}
}

func TestRewriteJoinsUpstreamBasePath(t *testing.T) {
	route := newTestRoute(t, config.RouteConfig{
		Prefix:      "/api/users",
		Upstream:    "http://users:8080/internal/v1",
		StripPrefix: "/api/users",
	})
	request := newProxyRequest(t, "http://gateway.example/api/users/42")

	NewRewrite(route)(request)

	if request.Out.URL.Path != "/internal/v1/42" {
		t.Errorf("URL.Path = %q, want %q", request.Out.URL.Path, "/internal/v1/42")
	}
}

func TestRewriteExactPrefixBecomesRoot(t *testing.T) {
	route := newTestRoute(t, config.RouteConfig{
		Prefix:      "/api/users",
		Upstream:    "http://users:8080",
		StripPrefix: "/api/users",
	})
	request := newProxyRequest(t, "http://gateway.example/api/users")

	NewRewrite(route)(request)

	if request.Out.URL.Path != "/" {
		t.Errorf("URL.Path = %q, want /", request.Out.URL.Path)
	}
}

func TestRewritePreservesPathWhenStripPrefixIsEmpty(t *testing.T) {
	route := newTestRoute(t, config.RouteConfig{
		Prefix:   "/api/users",
		Upstream: "http://users:8080",
	})
	request := newProxyRequest(t, "http://gateway.example/api/users/42")

	NewRewrite(route)(request)

	if request.Out.URL.Path != "/api/users/42" {
		t.Errorf("URL.Path = %q, want %q", request.Out.URL.Path, "/api/users/42")
	}
}

func TestRewritePreservesEscapedPath(t *testing.T) {
	route := newTestRoute(t, config.RouteConfig{
		Prefix:      "/api/users",
		Upstream:    "http://users:8080",
		StripPrefix: "/api/users",
	})
	request := newProxyRequest(t, "http://gateway.example/api/users/a%2Fb")

	NewRewrite(route)(request)

	if request.Out.URL.Path != "/a/b" {
		t.Errorf("URL.Path = %q, want /a/b", request.Out.URL.Path)
	}
	if request.Out.URL.RawPath != "/a%2Fb" {
		t.Errorf("URL.RawPath = %q, want /a%%2Fb", request.Out.URL.RawPath)
	}
	if request.Out.URL.EscapedPath() != "/a%2Fb" {
		t.Errorf("URL.EscapedPath() = %q, want /a%%2Fb", request.Out.URL.EscapedPath())
	}
}

func TestStripPathPrefixDoesNotStripPartialSegment(t *testing.T) {
	if got := stripPathPrefix("/api/users-v2/42", "/api/users"); got != "/api/users-v2/42" {
		t.Errorf("stripPathPrefix() = %q, want unchanged path", got)
	}
}

func newTestRoute(t *testing.T, definition config.RouteConfig) Route {
	t.Helper()

	table, err := NewTable([]config.RouteConfig{definition})
	if err != nil {
		t.Fatalf("NewTable(): %v", err)
	}
	route, ok := table.Match(definition.Prefix)
	if !ok {
		t.Fatalf("Match(%q) did not find route", definition.Prefix)
	}

	return route
}

func newProxyRequest(t *testing.T, target string) *httputil.ProxyRequest {
	t.Helper()

	inbound := httptest.NewRequest(http.MethodGet, target, nil)
	outbound := inbound.Clone(inbound.Context())
	outbound.URL = cloneURL(inbound.URL)
	outbound.Header = inbound.Header.Clone()

	return &httputil.ProxyRequest{
		In:  inbound,
		Out: outbound,
	}
}

func cloneURL(source *url.URL) *url.URL {
	cloned := *source
	return &cloned
}
