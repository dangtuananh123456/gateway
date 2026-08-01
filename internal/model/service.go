package model

import "github.com/dangtuananh123456/gateway/pkg/constants"

// HealthResponse is returned by a PDU Session health endpoint.
type HealthResponse struct {
	InstanceID string                  `json:"instanceId"`
	Status     constants.ServiceStatus `json:"status"`
}

// MetricsResponse is the load metadata periodically collected by the Gateway.
type MetricsResponse struct {
	InstanceID     string `json:"instanceId"`
	Weight         int    `json:"weight"`
	ActiveRequests int64  `json:"activeRequests"`
}
