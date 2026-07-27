package httperror

import (
	"encoding/json"
	"net/http"
)

// Code identifies a stable machine-readable gateway error.
type Code string

const (
	// CodeRouteNotFound indicates that no configured route matched the request path.
	CodeRouteNotFound Code = "route_not_found"
	// CodeBadGateway indicates that the selected upstream could not serve the request.
	CodeBadGateway Code = "bad_gateway"
	// CodeUpstreamTimeout indicates that the upstream exceeded its request deadline.
	CodeUpstreamTimeout Code = "upstream_timeout"
)

// Response is the public JSON error envelope.
type Response struct {
	Error Detail `json:"error"`
}

// Detail contains safe client-facing error information.
type Detail struct {
	Code      Code   `json:"code"`
	Message   string `json:"message"`
	RequestID string `json:"request_id,omitempty"`
}

// Write sends one gateway error using the public JSON contract.
func Write(
	writer http.ResponseWriter,
	status int,
	code Code,
	message string,
	requestID string,
) {
	writer.Header().Set("Content-Type", "application/json")
	writer.Header().Set("Cache-Control", "no-store")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(Response{
		Error: Detail{
			Code:      code,
			Message:   message,
			RequestID: requestID,
		},
	})
}
