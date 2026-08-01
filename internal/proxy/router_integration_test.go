package proxy

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/dangtuananh123456/gateway/internal/config"
	"github.com/dangtuananh123456/gateway/internal/mock/orders"
	"github.com/dangtuananh123456/gateway/internal/mock/users"
)

func TestRouterProxiesEndToEndToBothMockUpstreams(t *testing.T) {
	usersUpstream := httptest.NewServer(users.NewHandler())
	defer usersUpstream.Close()

	ordersUpstream := httptest.NewServer(orders.NewHandler())
	defer ordersUpstream.Close()

	table, err := NewTable([]config.RouteConfig{
		{
			Prefix:      "/api/users",
			Upstream:    usersUpstream.URL,
			StripPrefix: "/api/users",
		},
		{
			Prefix:      "/api/orders",
			Upstream:    ordersUpstream.URL,
			StripPrefix: "/api/orders",
		},
	})
	if err != nil {
		t.Fatalf("NewTable(): %v", err)
	}

	transport, err := NewTransport(DefaultTransportConfig(time.Second))
	if err != nil {
		t.Fatalf("NewTransport(): %v", err)
	}
	t.Cleanup(transport.CloseIdleConnections)

	router, err := NewRouter(table, transport, time.Second)
	if err != nil {
		t.Fatalf("NewRouter(): %v", err)
	}

	gateway := httptest.NewServer(router)
	defer gateway.Close()

	tests := []struct {
		name        string
		path        string
		wantService string
		wantID      string
		entityKey   string
	}{
		{
			name:        "users route",
			path:        "/api/users/42",
			wantService: "mock-users",
			wantID:      "42",
			entityKey:   "user",
		},
		{
			name:        "orders route",
			path:        "/api/orders/9000",
			wantService: "mock-orders",
			wantID:      "9000",
			entityKey:   "order",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response, err := gateway.Client().Get(gateway.URL + test.path)
			if err != nil {
				t.Fatalf("gateway GET %q: %v", test.path, err)
			}
			defer response.Body.Close()

			if response.StatusCode != http.StatusOK {
				t.Fatalf("status = %d, want %d", response.StatusCode, http.StatusOK)
			}

			var body struct {
				Service string `json:"service"`
				User    struct {
					ID string `json:"id"`
				} `json:"user"`
				Order struct {
					ID string `json:"id"`
				} `json:"order"`
			}
			if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if body.Service != test.wantService {
				t.Errorf("service = %q, want %q", body.Service, test.wantService)
			}

			gotID := body.User.ID
			if test.entityKey == "order" {
				gotID = body.Order.ID
			}
			if gotID != test.wantID {
				t.Errorf("%s.id = %q, want %q", test.entityKey, gotID, test.wantID)
			}
		})
	}
}
