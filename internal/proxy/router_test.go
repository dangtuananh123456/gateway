package proxy

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/dangtuananh123456/gateway/internal/config"
)

func TestRouterPrebuildsProxiesWithOneSharedTransport(t *testing.T) {
	table, err := NewTable([]config.RouteConfig{
		{
			Prefix:      "/api/users",
			Upstream:    "http://users:8080",
			StripPrefix: "/api/users",
		},
		{
			Prefix:      "/api/orders",
			Upstream:    "http://orders:8080",
			StripPrefix: "/api/orders",
		},
	})
	if err != nil {
		t.Fatalf("NewTable(): %v", err)
	}
	transport := &recordingTransport{}

	router, err := NewRouter(table, transport)
	if err != nil {
		t.Fatalf("NewRouter(): %v", err)
	}

	if len(router.proxies) != 2 {
		t.Fatalf("len(proxies) = %d, want 2", len(router.proxies))
	}
	for prefix, reverseProxy := range router.proxies {
		if reverseProxy.Transport != transport {
			t.Errorf("proxy %q does not use the shared transport", prefix)
		}
	}

	requests := []struct {
		target   string
		wantBody string
	}{
		{
			target:   "http://gateway.local/api/users/42?expand=roles",
			wantBody: "users:8080/42?expand=roles",
		},
		{
			target:   "http://gateway.local/api/orders/9000",
			wantBody: "orders:8080/9000",
		},
	}
	for _, tt := range requests {
		request := httptest.NewRequest(http.MethodGet, tt.target, nil)
		request.RemoteAddr = "203.0.113.10:1234"
		recorder := httptest.NewRecorder()

		router.ServeHTTP(recorder, request)

		if recorder.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
		}
		if body := strings.TrimSpace(recorder.Body.String()); body != tt.wantBody {
			t.Errorf("body = %q, want %q", body, tt.wantBody)
		}
	}

	if calls := transport.CallCount(); calls != 2 {
		t.Errorf("transport call count = %d, want 2", calls)
	}
}

func TestRouterReturnsNotFoundWhenNoRouteMatches(t *testing.T) {
	table, err := NewTable([]config.RouteConfig{
		{
			Prefix:   "/api/users",
			Upstream: "http://users:8080",
		},
	})
	if err != nil {
		t.Fatalf("NewTable(): %v", err)
	}
	router, err := NewRouter(table, &recordingTransport{})
	if err != nil {
		t.Fatalf("NewRouter(): %v", err)
	}

	recorder := httptest.NewRecorder()
	router.ServeHTTP(
		recorder,
		httptest.NewRequest(http.MethodGet, "http://gateway.local/not-found", nil),
	)

	if recorder.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d", recorder.Code, http.StatusNotFound)
	}
}

func TestNewRouterRejectsMissingDependencies(t *testing.T) {
	table, err := NewTable([]config.RouteConfig{
		{
			Prefix:   "/",
			Upstream: "http://fallback:8080",
		},
	})
	if err != nil {
		t.Fatalf("NewTable(): %v", err)
	}

	if _, err := NewRouter(nil, &recordingTransport{}); err == nil {
		t.Error("NewRouter(nil, transport) error = nil, want error")
	}
	if _, err := NewRouter(table, nil); err == nil {
		t.Error("NewRouter(table, nil) error = nil, want error")
	}
}

type recordingTransport struct {
	mu       sync.Mutex
	requests []*http.Request
}

func (transport *recordingTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	transport.mu.Lock()
	transport.requests = append(transport.requests, request.Clone(request.Context()))
	transport.mu.Unlock()

	body := request.URL.Host + request.URL.Path
	if request.URL.RawQuery != "" {
		body += "?" + request.URL.RawQuery
	}

	return &http.Response{
		StatusCode:    http.StatusOK,
		Status:        fmt.Sprintf("%d %s", http.StatusOK, http.StatusText(http.StatusOK)),
		Header:        make(http.Header),
		Body:          io.NopCloser(strings.NewReader(body)),
		ContentLength: int64(len(body)),
		Request:       request,
	}, nil
}

func (transport *recordingTransport) CallCount() int {
	transport.mu.Lock()
	defer transport.mu.Unlock()

	return len(transport.requests)
}
