package orders

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHandler(t *testing.T) {
	handler := NewHandler()

	tests := []struct {
		name        string
		method      string
		target      string
		wantStatus  int
		wantStrings []string
	}{
		{
			name:        "health",
			method:      http.MethodGet,
			target:      "/healthz",
			wantStatus:  http.StatusOK,
			wantStrings: []string{`"service":"mock-orders"`, `"status":"ok"`},
		},
		{
			name:        "list orders",
			method:      http.MethodGet,
			target:      "/",
			wantStatus:  http.StatusOK,
			wantStrings: []string{`"id":"1001"`, `"id":"1002"`, `"status":"pending"`},
		},
		{
			name:        "get order",
			method:      http.MethodGet,
			target:      "/9000",
			wantStatus:  http.StatusOK,
			wantStrings: []string{`"id":"9000"`, `"total_cents":2599`, `"status":"paid"`},
		},
		{
			name:        "mock error",
			method:      http.MethodGet,
			target:      "/_mock/error?status=502",
			wantStatus:  http.StatusBadGateway,
			wantStrings: []string{`"code":"mock_error"`, `"message":"Bad Gateway"`},
		},
		{
			name:        "invalid mock status",
			method:      http.MethodGet,
			target:      "/_mock/error?status=text",
			wantStatus:  http.StatusBadRequest,
			wantStrings: []string{`"code":"invalid_status"`},
		},
		{
			name:        "invalid delay",
			method:      http.MethodGet,
			target:      "/_mock/delay?duration=31s",
			wantStatus:  http.StatusBadRequest,
			wantStrings: []string{`"code":"invalid_delay"`},
		},
		{
			name:       "unknown nested path",
			method:     http.MethodGet,
			target:     "/unknown/path",
			wantStatus: http.StatusNotFound,
		},
		{
			name:       "method not allowed",
			method:     http.MethodDelete,
			target:     "/1001",
			wantStatus: http.StatusMethodNotAllowed,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			request := httptest.NewRequest(tt.method, tt.target, nil)
			recorder := httptest.NewRecorder()

			handler.ServeHTTP(recorder, request)

			if recorder.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d", recorder.Code, tt.wantStatus)
			}
			for _, expected := range tt.wantStrings {
				if !strings.Contains(recorder.Body.String(), expected) {
					t.Errorf("body = %q, want it to contain %q", recorder.Body.String(), expected)
				}
			}
			if tt.wantStatus != http.StatusNotFound &&
				tt.wantStatus != http.StatusMethodNotAllowed {
				if contentType := recorder.Header().Get("Content-Type"); contentType != "application/json" {
					t.Errorf("Content-Type = %q, want application/json", contentType)
				}
			}
		})
	}
}

func TestDelay(t *testing.T) {
	request := httptest.NewRequest(
		http.MethodGet,
		"/_mock/delay?duration=1ms",
		nil,
	)
	recorder := httptest.NewRecorder()

	NewHandler().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
	}

	var response map[string]string
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response["delay"] != "1ms" {
		t.Errorf("delay = %q, want 1ms", response["delay"])
	}
}

func TestDelayStopsWhenContextIsCanceled(t *testing.T) {
	request := httptest.NewRequest(
		http.MethodGet,
		"/_mock/delay?duration=30s",
		nil,
	)
	ctx, cancel := context.WithCancel(request.Context())
	cancel()
	request = request.WithContext(ctx)
	recorder := httptest.NewRecorder()

	NewHandler().ServeHTTP(recorder, request)

	if recorder.Body.Len() != 0 {
		t.Errorf("body = %q, want an empty body after cancellation", recorder.Body.String())
	}
}
