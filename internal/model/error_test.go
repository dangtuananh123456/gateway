package model

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/dangtuananh123456/gateway/pkg/constants"
)

func TestErrorCauseHTTPStatus(t *testing.T) {
	tests := []struct {
		cause constants.ErrorCause
		want  int
	}{
		{constants.CauseInvalidRequest, http.StatusBadRequest},
		{constants.CauseNotFound, http.StatusNotFound},
		{constants.CauseMethodNotAllowed, http.StatusMethodNotAllowed},
		{constants.CausePayloadTooLarge, http.StatusRequestEntityTooLarge},
		{constants.CauseUnsupportedMediaType, http.StatusUnsupportedMediaType},
		{constants.CauseInternalError, http.StatusInternalServerError},
		{constants.CauseBadGateway, http.StatusBadGateway},
		{constants.CauseNoBackendAvailable, http.StatusServiceUnavailable},
		{constants.CauseUpstreamTimeout, http.StatusGatewayTimeout},
	}
	for _, test := range tests {
		t.Run(string(test.cause), func(t *testing.T) {
			if got := HTTPStatus(test.cause); got != test.want {
				t.Errorf("HTTPStatus() = %d, want %d", got, test.want)
			}
		})
	}
}

func TestUnknownErrorCauseIsInternalServerError(t *testing.T) {
	if got := HTTPStatus(constants.ErrorCause("UNKNOWN")); got != http.StatusInternalServerError {
		t.Errorf("HTTPStatus() = %d, want %d", got, http.StatusInternalServerError)
	}
}

func TestErrorResponseJSON(t *testing.T) {
	response := NewErrorResponse(constants.CauseInvalidRequest, "supi is required")
	encoded, err := json.Marshal(response)
	if err != nil {
		t.Fatalf("encode error response: %v", err)
	}
	assertJSONEqual(
		t,
		encoded,
		[]byte(`{"status":"ERROR","cause":"INVALID_REQUEST","detail":"supi is required"}`),
	)
}

func TestNoBackendErrorMatchesRequiredContract(t *testing.T) {
	response := NewErrorResponse(constants.CauseNoBackendAvailable, "")
	encoded, err := json.Marshal(response)
	if err != nil {
		t.Fatalf("encode no-backend response: %v", err)
	}
	assertJSONEqual(
		t,
		encoded,
		[]byte(`{"status":"ERROR","cause":"NO_BACKEND_AVAILABLE"}`),
	)
}
