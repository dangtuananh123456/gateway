package model

import (
	"encoding/json"
	"net/http"
	"testing"
)

func TestErrorCauseHTTPStatus(t *testing.T) {
	tests := []struct {
		cause ErrorCause
		want  int
	}{
		{CauseInvalidRequest, http.StatusBadRequest},
		{CauseNotFound, http.StatusNotFound},
		{CauseMethodNotAllowed, http.StatusMethodNotAllowed},
		{CausePayloadTooLarge, http.StatusRequestEntityTooLarge},
		{CauseUnsupportedMediaType, http.StatusUnsupportedMediaType},
		{CauseBadGateway, http.StatusBadGateway},
		{CauseNoBackendAvailable, http.StatusServiceUnavailable},
		{CauseUpstreamTimeout, http.StatusGatewayTimeout},
	}
	for _, test := range tests {
		t.Run(string(test.cause), func(t *testing.T) {
			if got := test.cause.HTTPStatus(); got != test.want {
				t.Errorf("HTTPStatus() = %d, want %d", got, test.want)
			}
		})
	}
}

func TestUnknownErrorCauseIsInternalServerError(t *testing.T) {
	if got := ErrorCause("UNKNOWN").HTTPStatus(); got != http.StatusInternalServerError {
		t.Errorf("HTTPStatus() = %d, want %d", got, http.StatusInternalServerError)
	}
}

func TestErrorResponseJSON(t *testing.T) {
	response := NewErrorResponse(CauseInvalidRequest, "supi is required")
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
	response := NewErrorResponse(CauseNoBackendAvailable, "")
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
