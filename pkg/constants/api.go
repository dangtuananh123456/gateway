// Package constants contains stable values shared across Project 2 packages.
package constants

// SMContextStatus is the lifecycle state returned for an SM context.
type SMContextStatus string

const (
	SMContextActive SMContextStatus = "ACTIVE"
)

// ServiceStatus is the health state exposed by a PDU Session instance.
type ServiceStatus string

const (
	ServiceUp ServiceStatus = "UP"
)

// ErrorCause is a stable, machine-readable failure identifier.
type ErrorCause string

const (
	ErrorStatus                          = "ERROR"
	CauseInvalidRequest       ErrorCause = "INVALID_REQUEST"
	CauseNotFound             ErrorCause = "NOT_FOUND"
	CauseMethodNotAllowed     ErrorCause = "METHOD_NOT_ALLOWED"
	CausePayloadTooLarge      ErrorCause = "PAYLOAD_TOO_LARGE"
	CauseUnsupportedMediaType ErrorCause = "UNSUPPORTED_MEDIA_TYPE"
	CauseInternalError        ErrorCause = "INTERNAL_ERROR"
	CauseBadGateway           ErrorCause = "BAD_GATEWAY"
	CauseNoBackendAvailable   ErrorCause = "NO_BACKEND_AVAILABLE"
	CauseUpstreamTimeout      ErrorCause = "UPSTREAM_TIMEOUT"
)
