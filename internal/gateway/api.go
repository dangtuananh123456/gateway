package gateway

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/dangtuananh123456/gateway/internal/model"
	"github.com/dangtuananh123456/gateway/internal/monitor"
	"github.com/dangtuananh123456/gateway/internal/registry"
	"github.com/dangtuananh123456/gateway/pkg/constants"
)

// StatsProvider publishes process resource metrics.
type StatsProvider interface {
	Snapshot() monitor.Stats
}

// APIHandler serves Gateway-owned endpoints and delegates business traffic.
type APIHandler struct {
	snapshots   SnapshotProvider
	proxy       http.Handler
	routingMode constants.RoutingMode
	stats       StatsProvider
}

type backendListResponse struct {
	Count       int                   `json:"count"`
	RoutingMode constants.RoutingMode `json:"routingMode"`
	Instances   []backendResponse     `json:"instances"`
}

type backendResponse struct {
	InstanceID     string `json:"instanceId"`
	Address        string `json:"address"`
	Weight         int    `json:"weight"`
	ActiveRequests int64  `json:"activeRequests"`
}

// NewAPIHandler creates the public Gateway handler with default background sampler.
func NewAPIHandler(snapshots SnapshotProvider, routingMode constants.RoutingMode, proxy http.Handler) (*APIHandler, error) {
	return NewAPIHandlerWithStats(snapshots, routingMode, proxy, monitor.NewSampler())
}

// NewAPIHandlerWithStats creates the Gateway handler with injected stats provider.
func NewAPIHandlerWithStats(
	snapshots SnapshotProvider,
	routingMode constants.RoutingMode,
	proxy http.Handler,
	stats StatsProvider,
) (*APIHandler, error) {
	if snapshots == nil {
		return nil, errors.New("create Gateway API handler: snapshot provider must not be nil")
	}
	if proxy == nil {
		return nil, errors.New("create Gateway API handler: proxy must not be nil")
	}
	if routingMode != constants.RoutingRoundRobin && routingMode != constants.RoutingWeighted && routingMode != constants.RoutingLoad {
		return nil, errors.New("create Gateway API handler: invalid routing mode")
	}
	if stats == nil {
		stats = monitor.NewSampler()
	}
	return &APIHandler{snapshots: snapshots, proxy: proxy, routingMode: routingMode, stats: stats}, nil
}

// ServeHTTP handles Gateway diagnostics without forwarding them to a PDU.
func (handler *APIHandler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	switch request.URL.Path {
	case constants.GatewayBackendsPath:
		handler.serveBackends(writer, request)
	case constants.GatewayStatsPath:
		handler.serveStats(writer, request)
	default:
		handler.proxy.ServeHTTP(writer, request)
	}
}

func (handler *APIHandler) serveBackends(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		writer.Header().Set("Allow", http.MethodGet)
		writeGatewayJSON(writer, http.StatusMethodNotAllowed, model.NewErrorResponse(
			constants.CauseMethodNotAllowed,
			"method not allowed",
		))
		return
	}

	snapshot := handler.snapshots.HealthySnapshot()
	instances := snapshot.All()
	response := backendListResponse{
		Count:       len(instances),
		RoutingMode: handler.routingMode,
		Instances:   make([]backendResponse, 0, len(instances)),
	}
	for _, instance := range instances {
		response.Instances = append(response.Instances, backendFrom(instance))
	}
	writer.Header().Set("Content-Type", constants.ContentTypeJSON)
	writer.Header().Set("Cache-Control", "no-store")
	writer.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(writer).Encode(response)
}

func (handler *APIHandler) serveStats(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		writer.Header().Set("Allow", http.MethodGet)
		writeGatewayJSON(writer, http.StatusMethodNotAllowed, model.NewErrorResponse(
			constants.CauseMethodNotAllowed,
			"method not allowed",
		))
		return
	}

	stats := handler.stats.Snapshot()
	writer.Header().Set("Content-Type", constants.ContentTypeJSON)
	writer.Header().Set("Cache-Control", "no-store")
	writer.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(writer).Encode(stats)
}

func backendFrom(instance registry.Instance) backendResponse {
	return backendResponse{
		InstanceID:     instance.InstanceID,
		Address:        instance.Address.String(),
		Weight:         instance.Weight,
		ActiveRequests: instance.ActiveRequests,
	}
}
