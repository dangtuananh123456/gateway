package requestlog

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAccessLogRecordsCompletedRequest(t *testing.T) {
	var output bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&output, nil))
	handler := New(logger, "gateway", http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if got := request.Header.Get(RequestIDHeader); got != "request-123" {
			t.Errorf("request ID = %q, want request-123", got)
		}
		writer.WriteHeader(http.StatusCreated)
		_, _ = writer.Write([]byte("created"))
	}))

	request := httptest.NewRequest(http.MethodPost, "http://gateway/session?ignored=true", nil)
	request.Header.Set(RequestIDHeader, "request-123")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	var entry map[string]any
	if err := json.Unmarshal(output.Bytes(), &entry); err != nil {
		t.Fatalf("decode log: %v", err)
	}
	for field, want := range map[string]any{
		"level": "INFO", "msg": "HTTP request completed", "service": "gateway",
		"request_id": "request-123", "method": http.MethodPost, "path": "/session",
		"status": float64(http.StatusCreated), "response_bytes": float64(len("created")),
		"protocol": "HTTP/1.1",
	} {
		if got := entry[field]; got != want {
			t.Errorf("log field %s = %#v, want %#v", field, got, want)
		}
	}
	if _, found := entry["duration_ms"]; !found {
		t.Error("duration_ms is missing")
	}
	if _, found := entry["ignored"]; found {
		t.Error("query parameter unexpectedly logged")
	}
}

func TestWrapDisabledBypassesAccessLogging(t *testing.T) {
	var output bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&output, nil))
	called := false
	handler := Wrap(false, logger, "gateway", http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		called = true
		if request.Header.Get(RequestIDHeader) != "" {
			t.Error("disabled access log unexpectedly generated a request ID")
		}
		writer.WriteHeader(http.StatusNoContent)
	}))

	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/health", nil))
	if !called {
		t.Fatal("next handler was not called")
	}
	if output.Len() != 0 {
		t.Errorf("disabled access log output = %q, want empty", output.String())
	}
}

func TestAccessLogGeneratesRequestIDAndWarnsForFailure(t *testing.T) {
	var output bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&output, nil))
	var generatedID string
	handler := New(logger, "pdu-session", http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		generatedID = request.Header.Get(RequestIDHeader)
		http.Error(writer, "missing", http.StatusNotFound)
	}))

	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/missing", nil))
	if generatedID == "" {
		t.Fatal("generated request ID is empty")
	}

	var entry map[string]any
	if err := json.Unmarshal(output.Bytes(), &entry); err != nil {
		t.Fatalf("decode log: %v", err)
	}
	if entry["level"] != "WARN" || entry["request_id"] != generatedID || entry["status"] != float64(http.StatusNotFound) {
		t.Errorf("log entry = %#v, want WARN with generated ID and 404", entry)
	}
}

func TestAccessLogRecordsPanicAsInternalErrorWithoutRecoveringIt(t *testing.T) {
	var output bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&output, nil))
	handler := New(logger, "client", http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("handler failed")
	}))

	func() {
		defer func() {
			if recovered := recover(); recovered != "handler failed" {
				t.Errorf("recovered = %#v, want original panic", recovered)
			}
		}()
		handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/panic", nil))
	}()

	var entry map[string]any
	if err := json.Unmarshal(output.Bytes(), &entry); err != nil {
		t.Fatalf("decode log: %v", err)
	}
	if entry["level"] != "ERROR" || entry["status"] != float64(http.StatusInternalServerError) {
		t.Errorf("log entry = %#v, want ERROR with 500", entry)
	}
}
