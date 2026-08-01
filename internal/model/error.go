package model

import "net/http"

// ErrorCause is a stable, machine-readable failure identifier.
type ErrorCause string

const (
	CauseInvalidRequest       ErrorCause = "INVALID_REQUEST"
	CauseNotFound             ErrorCause = "NOT_FOUND"
	CauseMethodNotAllowed     ErrorCause = "METHOD_NOT_ALLOWED"
	CausePayloadTooLarge      ErrorCause = "PAYLOAD_TOO_LARGE"
	CauseUnsupportedMediaType ErrorCause = "UNSUPPORTED_MEDIA_TYPE"
	CauseBadGateway           ErrorCause = "BAD_GATEWAY"
	CauseNoBackendAvailable   ErrorCause = "NO_BACKEND_AVAILABLE"
	CauseUpstreamTimeout      ErrorCause = "UPSTREAM_TIMEOUT"
)

// ErrorResponse is the common JSON error envelope used by both services.
type ErrorResponse struct {
	Status string     `json:"status"`
	Cause  ErrorCause `json:"cause"`
	Detail string     `json:"detail,omitempty"`
}

// NewErrorResponse constructs the standard error envelope.
func NewErrorResponse(cause ErrorCause, detail string) ErrorResponse {
	return ErrorResponse{
		Status: "ERROR",
		Cause:  cause,
		Detail: detail,
	}
}

// HTTPStatus returns the status code assigned to a machine-readable cause.
// An unknown cause is treated as an internal error instead of being exposed as
// a successful response.
func (cause ErrorCause) HTTPStatus() int {
	switch cause {
	case CauseInvalidRequest:
		return http.StatusBadRequest
	case CauseNotFound:
		return http.StatusNotFound
	case CauseMethodNotAllowed:
		return http.StatusMethodNotAllowed
	case CausePayloadTooLarge:
		return http.StatusRequestEntityTooLarge
	case CauseUnsupportedMediaType:
		return http.StatusUnsupportedMediaType
	case CauseBadGateway:
		return http.StatusBadGateway
	case CauseNoBackendAvailable:
		return http.StatusServiceUnavailable
	case CauseUpstreamTimeout:
		return http.StatusGatewayTimeout
	default:
		return http.StatusInternalServerError
	}
}
