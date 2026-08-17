package gateway

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
	"time"

	"github.com/dangtuananh123456/gateway/internal/registry"
	"github.com/dangtuananh123456/gateway/pkg/constants"
)

func TestAPIHandlerShowsNewHealthyBackendWithoutRestart(t *testing.T) {
	candidates := registry.New()
	for index := range 3 {
		addAPIBackend(t, candidates, index)
	}
	proxyCalls := 0
	handler, err := NewAPIHandler(candidates, constants.RoutingRoundRobin, http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		proxyCalls++
		writer.WriteHeader(http.StatusTeapot)
	}))
	if err != nil {
		t.Fatalf("NewAPIHandler() error = %v", err)
	}

	first := getBackends(t, handler)
	if first.Count != 3 || len(first.Instances) != 3 {
		t.Fatalf("initial response = %+v, want 3 instances", first)
	}
	if first.RoutingMode != constants.RoutingRoundRobin {
		t.Errorf("routing mode = %q, want %q", first.RoutingMode, constants.RoutingRoundRobin)
	}

	addAPIBackend(t, candidates, 3)
	second := getBackends(t, handler)
	if second.Count != 4 || len(second.Instances) != 4 {
		t.Fatalf("response after scale up = %+v, want 4 instances", second)
	}
	if second.Instances[3].InstanceID != "pdu-04" {
		t.Errorf("fourth instance = %+v, want pdu-04", second.Instances[3])
	}
	if proxyCalls != 0 {
		t.Errorf("backend endpoint was forwarded %d times, want 0", proxyCalls)
	}
}

func TestAPIHandlerDelegatesOtherPathsAndRejectsWrongMethod(t *testing.T) {
	candidates := registry.New()
	proxyCalls := 0
	handler, err := NewAPIHandler(candidates, constants.RoutingRoundRobin, http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		proxyCalls++
		writer.WriteHeader(http.StatusCreated)
	}))
	if err != nil {
		t.Fatalf("NewAPIHandler() error = %v", err)
	}

	delegated := httptest.NewRecorder()
	handler.ServeHTTP(delegated, httptest.NewRequest(http.MethodPost, constants.CreateSMContextPath, nil))
	if delegated.Code != http.StatusCreated || proxyCalls != 1 {
		t.Errorf("delegated status=%d calls=%d, want 201 and 1", delegated.Code, proxyCalls)
	}

	rejected := httptest.NewRecorder()
	handler.ServeHTTP(rejected, httptest.NewRequest(http.MethodPost, constants.GatewayBackendsPath, nil))
	if rejected.Code != http.StatusMethodNotAllowed || rejected.Header().Get("Allow") != http.MethodGet {
		t.Errorf("wrong method status=%d Allow=%q", rejected.Code, rejected.Header().Get("Allow"))
	}
}

func TestAPIHandlerServesStats(t *testing.T) {
	candidates := registry.New()
	handler, err := NewAPIHandler(candidates, constants.RoutingRoundRobin, http.NotFoundHandler())
	if err != nil {
		t.Fatalf("NewAPIHandler() error = %v", err)
	}

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, constants.GatewayStatsPath, nil))
	if response.Code != http.StatusOK {
		t.Fatalf("GET stats status=%d body=%q", response.Code, response.Body.String())
	}
	var stats map[string]any
	if err := json.NewDecoder(response.Body).Decode(&stats); err != nil {
		t.Fatalf("decode stats: %v", err)
	}
	if _, found := stats["ramUsageBytes"]; !found {
		t.Errorf("stats missing ramUsageBytes: %+v", stats)
	}
}

func TestNewAPIHandlerValidatesDependencies(t *testing.T) {
	if _, err := NewAPIHandler(nil, constants.RoutingRoundRobin, http.NotFoundHandler()); err == nil {
		t.Fatal("NewAPIHandler(nil snapshots) error = nil")
	}
	if _, err := NewAPIHandler(registry.New(), constants.RoutingRoundRobin, nil); err == nil {
		t.Fatal("NewAPIHandler(nil proxy) error = nil")
	}
	if _, err := NewAPIHandler(registry.New(), constants.RoutingMode("random"), http.NotFoundHandler()); err == nil {
		t.Fatal("NewAPIHandler(invalid routing mode) error = nil")
	}
}

func addAPIBackend(t *testing.T, candidates *registry.Registry, index int) {
	t.Helper()
	address := netip.AddrPortFrom(netip.AddrFrom4([4]byte{10, 0, 0, byte(index + 1)}), 8081)
	base := time.Date(2026, time.August, 16, 0, 0, 0, 0, time.UTC)
	if _, err := candidates.Upsert(address, base); err != nil {
		t.Fatalf("Upsert(%s) error = %v", address, err)
	}
	if err := candidates.MarkHealthy(address, registry.Metadata{
		InstanceID:     fmt.Sprintf("pdu-%02d", index+1),
		Weight:         index%3 + 1,
		ActiveRequests: int64(index),
	}, base.Add(time.Duration(index+1)*time.Second)); err != nil {
		t.Fatalf("MarkHealthy(%s) error = %v", address, err)
	}
}

func getBackends(t *testing.T, handler http.Handler) backendListResponse {
	t.Helper()
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, constants.GatewayBackendsPath, nil))
	if response.Code != http.StatusOK {
		t.Fatalf("GET backends status=%d body=%q", response.Code, response.Body.String())
	}
	var result backendListResponse
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		t.Fatalf("decode backends response: %v", err)
	}
	return result
}
