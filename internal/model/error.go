package model

import (
	"net/http"

	"github.com/dangtuananh123456/gateway/pkg/constants"
)

// ErrorResponse is the common JSON error envelope used by both services.
type ErrorResponse struct {
	Status string               `json:"status"`
	Cause  constants.ErrorCause `json:"cause"`
	Detail string               `json:"detail,omitempty"`
}

// NewErrorResponse constructs the standard error envelope.
func NewErrorResponse(cause constants.ErrorCause, detail string) ErrorResponse {
	return ErrorResponse{
		Status: constants.ErrorStatus,
		Cause:  cause,
		Detail: detail,
	}
}

// HTTPStatus returns the status code assigned to a machine-readable cause.
// An unknown cause is treated as an internal error instead of being exposed as
// a successful response.
func HTTPStatus(cause constants.ErrorCause) int {
	switch cause {
	case constants.CauseInvalidRequest:
		return http.StatusBadRequest
	case constants.CauseNotFound:
		return http.StatusNotFound
	case constants.CauseMethodNotAllowed:
		return http.StatusMethodNotAllowed
	case constants.CausePayloadTooLarge:
		return http.StatusRequestEntityTooLarge
	case constants.CauseUnsupportedMediaType:
		return http.StatusUnsupportedMediaType
	case constants.CauseBadGateway:
		return http.StatusBadGateway
	case constants.CauseNoBackendAvailable:
		return http.StatusServiceUnavailable
	case constants.CauseUpstreamTimeout:
		return http.StatusGatewayTimeout
	default:
		return http.StatusInternalServerError
	}
}
