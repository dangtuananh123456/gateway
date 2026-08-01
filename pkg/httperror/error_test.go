package httperror

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestWrite(t *testing.T) {
	recorder := httptest.NewRecorder()

	Write(
		recorder,
		http.StatusBadGateway,
		CodeBadGateway,
		"upstream service unavailable",
		"request-123",
	)

	if recorder.Code != http.StatusBadGateway {
		t.Errorf("status = %d, want %d", recorder.Code, http.StatusBadGateway)
	}
	if contentType := recorder.Header().Get("Content-Type"); contentType != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", contentType)
	}
	if cacheControl := recorder.Header().Get("Cache-Control"); cacheControl != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", cacheControl)
	}

	var response Response
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.Error.Code != CodeBadGateway {
		t.Errorf("error.code = %q, want %q", response.Error.Code, CodeBadGateway)
	}
	if response.Error.Message != "upstream service unavailable" {
		t.Errorf(
			"error.message = %q, want %q",
			response.Error.Message,
			"upstream service unavailable",
		)
	}
	if response.Error.RequestID != "request-123" {
		t.Errorf("error.request_id = %q, want request-123", response.Error.RequestID)
	}
}

func TestWriteOmitsEmptyRequestID(t *testing.T) {
	recorder := httptest.NewRecorder()

	Write(
		recorder,
		http.StatusNotFound,
		CodeRouteNotFound,
		"route not found",
		"",
	)

	var body map[string]map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if _, exists := body["error"]["request_id"]; exists {
		t.Error("request_id exists, want it omitted")
	}
}
