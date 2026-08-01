package pdu

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/dangtuananh123456/gateway/internal/model"
	"github.com/dangtuananh123456/gateway/pkg/constants"
)

func TestHealth(t *testing.T) {
	recorder := httptest.NewRecorder()
	NewHandler("pdu-1").ServeHTTP(
		recorder,
		httptest.NewRequest(http.MethodGet, "/health", nil),
	)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
	}

	var body model.HealthResponse
	if err := json.NewDecoder(recorder.Body).Decode(&body); err != nil {
		t.Fatalf("decode health response: %v", err)
	}
	if body.InstanceID != "pdu-1" || body.Status != constants.ServiceUp {
		t.Errorf("body = %+v, want instance pdu-1 with UP status", body)
	}
}
