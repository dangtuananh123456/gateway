package proxy

import (
	"testing"

	"github.com/dangtuananh123456/gateway/internal/config"
)

func TestTableMatchesLongestPathPrefix(t *testing.T) {
	table, err := NewTable([]config.RouteConfig{
		{
			Prefix:      "/",
			Upstream:    "http://fallback:8080",
			StripPrefix: "",
		},
		{
			Prefix:      "/api",
			Upstream:    "http://api:8080",
			StripPrefix: "/api",
		},
		{
			Prefix:      "/api/users/admin",
			Upstream:    "http://admin:8080",
			StripPrefix: "/api/users",
		},
		{
			Prefix:      "/api/users",
			Upstream:    "http://users:8080",
			StripPrefix: "/api/users",
		},
	})
	if err != nil {
		t.Fatalf("NewTable(): %v", err)
	}

	tests := []struct {
		path           string
		wantPrefix     string
		wantUpstream   string
		wantStrip      string
		wantSuccessful bool
	}{
		{
			path:           "/api/users/admin/42",
			wantPrefix:     "/api/users/admin",
			wantUpstream:   "admin:8080",
			wantStrip:      "/api/users",
			wantSuccessful: true,
		},
		{
			path:           "/api/users/42",
			wantPrefix:     "/api/users",
			wantUpstream:   "users:8080",
			wantStrip:      "/api/users",
			wantSuccessful: true,
		},
		{
			path:           "/api/orders",
			wantPrefix:     "/api",
			wantUpstream:   "api:8080",
			wantStrip:      "/api",
			wantSuccessful: true,
		},
		{
			path:           "/other",
			wantPrefix:     "/",
			wantUpstream:   "fallback:8080",
			wantSuccessful: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			route, ok := table.Match(tt.path)
			if ok != tt.wantSuccessful {
				t.Fatalf("Match() ok = %t, want %t", ok, tt.wantSuccessful)
			}
			if route.Prefix() != tt.wantPrefix {
				t.Errorf("Prefix() = %q, want %q", route.Prefix(), tt.wantPrefix)
			}
			if route.Upstream().Host != tt.wantUpstream {
				t.Errorf("Upstream().Host = %q, want %q", route.Upstream().Host, tt.wantUpstream)
			}
			if route.StripPrefix() != tt.wantStrip {
				t.Errorf("StripPrefix() = %q, want %q", route.StripPrefix(), tt.wantStrip)
			}
		})
	}
}

func TestTableRespectsPathSegmentBoundaries(t *testing.T) {
	table, err := NewTable([]config.RouteConfig{
		{
			Prefix:   "/api/users",
			Upstream: "http://users:8080",
		},
	})
	if err != nil {
		t.Fatalf("NewTable(): %v", err)
	}

	for _, requestPath := range []string{"/api/users", "/api/users/", "/api/users/42"} {
		if _, ok := table.Match(requestPath); !ok {
			t.Errorf("Match(%q) did not match", requestPath)
		}
	}
	for _, requestPath := range []string{"", "/", "/api/user", "/api/users-v2"} {
		if route, ok := table.Match(requestPath); ok {
			t.Errorf("Match(%q) = %q, want no match", requestPath, route.Prefix())
		}
	}
}

func TestRootRouteOnlyMatchesAbsolutePaths(t *testing.T) {
	table, err := NewTable([]config.RouteConfig{
		{
			Prefix:   "/",
			Upstream: "http://fallback:8080",
		},
	})
	if err != nil {
		t.Fatalf("NewTable(): %v", err)
	}

	if _, ok := table.Match("/anything"); !ok {
		t.Error("Match() did not match an absolute path")
	}
	for _, requestPath := range []string{"", "relative"} {
		if _, ok := table.Match(requestPath); ok {
			t.Errorf("Match(%q) matched, want no match", requestPath)
		}
	}
}

func TestTableIsImmutable(t *testing.T) {
	definitions := []config.RouteConfig{
		{
			Prefix:      "/api/users",
			Upstream:    "http://users:8080",
			StripPrefix: "/api/users",
		},
	}
	table, err := NewTable(definitions)
	if err != nil {
		t.Fatalf("NewTable(): %v", err)
	}

	definitions[0].Prefix = "/changed"
	definitions[0].Upstream = "http://changed:8080"

	first, ok := table.Match("/api/users/42")
	if !ok {
		t.Fatal("Match() did not find the copied route")
	}
	target := first.Upstream()
	target.Host = "mutated:8080"

	second, ok := table.Match("/api/users/42")
	if !ok {
		t.Fatal("Match() did not find the route after mutating returned values")
	}
	if second.Prefix() != "/api/users" {
		t.Errorf("Prefix() = %q, want %q", second.Prefix(), "/api/users")
	}
	if second.Upstream().Host != "users:8080" {
		t.Errorf("Upstream().Host = %q, want %q", second.Upstream().Host, "users:8080")
	}
}

func TestNewTableRejectsInvalidDefinitions(t *testing.T) {
	tests := []struct {
		name   string
		routes []config.RouteConfig
	}{
		{
			name: "empty routes",
		},
		{
			name: "relative prefix",
			routes: []config.RouteConfig{
				{Prefix: "api", Upstream: "http://api:8080"},
			},
		},
		{
			name: "duplicate prefix",
			routes: []config.RouteConfig{
				{Prefix: "/api", Upstream: "http://one:8080"},
				{Prefix: "/api", Upstream: "http://two:8080"},
			},
		},
		{
			name: "unrelated strip prefix",
			routes: []config.RouteConfig{
				{Prefix: "/api", StripPrefix: "/other", Upstream: "http://api:8080"},
			},
		},
		{
			name: "unsupported upstream scheme",
			routes: []config.RouteConfig{
				{Prefix: "/api", Upstream: "ftp://api:21"},
			},
		},
		{
			name: "upstream without host",
			routes: []config.RouteConfig{
				{Prefix: "/api", Upstream: "http:///api"},
			},
		},
		{
			name: "upstream with query",
			routes: []config.RouteConfig{
				{Prefix: "/api", Upstream: "http://api:8080?debug=true"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := NewTable(tt.routes); err == nil {
				t.Fatal("NewTable() error = nil, want error")
			}
		})
	}
}

func TestNilTableDoesNotMatch(t *testing.T) {
	var table *Table

	if _, ok := table.Match("/api/users"); ok {
		t.Fatal("nil Table.Match() matched, want no match")
	}
}
