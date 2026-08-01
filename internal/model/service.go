package model

// ServiceStatus is the health state exposed by a PDU Session instance.
type ServiceStatus string

const (
	// ServiceUp indicates that an instance is ready to receive requests.
	ServiceUp ServiceStatus = "UP"
)

// HealthResponse is returned by a PDU Session health endpoint.
type HealthResponse struct {
	InstanceID string        `json:"instanceId"`
	Status     ServiceStatus `json:"status"`
}

// MetricsResponse is the load metadata periodically collected by the Gateway.
type MetricsResponse struct {
	InstanceID     string `json:"instanceId"`
	Weight         int    `json:"weight"`
	ActiveRequests int64  `json:"activeRequests"`
}
