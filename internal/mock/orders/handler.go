// Package orders provides the HTTP handler for the mock orders upstream.
package orders

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"
)

const (
	serviceName  = "mock-orders"
	defaultDelay = 100 * time.Millisecond
	maxDelay     = 30 * time.Second
)

// NewHandler creates the mock orders HTTP handler.
func NewHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", health)
	mux.HandleFunc("GET /_mock/delay", delay)
	mux.HandleFunc("GET /_mock/error", mockError)
	mux.HandleFunc("GET /{$}", list)
	mux.HandleFunc("GET /{id}", get)

	return mux
}

func health(writer http.ResponseWriter, _ *http.Request) {
	writeJSON(writer, http.StatusOK, map[string]string{
		"service": serviceName,
		"status":  "ok",
	})
}

func list(writer http.ResponseWriter, _ *http.Request) {
	writeJSON(writer, http.StatusOK, struct {
		Service string          `json:"service"`
		Orders  []orderResponse `json:"orders"`
	}{
		Service: serviceName,
		Orders: []orderResponse{
			{ID: "1001", UserID: "1", TotalCents: 1299, Status: "paid"},
			{ID: "1002", UserID: "2", TotalCents: 4999, Status: "pending"},
		},
	})
}

func get(writer http.ResponseWriter, request *http.Request) {
	id := request.PathValue("id")
	writeJSON(writer, http.StatusOK, struct {
		Service string        `json:"service"`
		Order   orderResponse `json:"order"`
	}{
		Service: serviceName,
		Order: orderResponse{
			ID:         id,
			UserID:     "1",
			TotalCents: 2599,
			Status:     "paid",
		},
	})
}

func delay(writer http.ResponseWriter, request *http.Request) {
	duration, err := requestedDelay(request)
	if err != nil {
		writeError(writer, http.StatusBadRequest, "invalid_delay", err.Error())
		return
	}

	timer := time.NewTimer(duration)
	defer timer.Stop()

	select {
	case <-request.Context().Done():
		return
	case <-timer.C:
		writeJSON(writer, http.StatusOK, map[string]string{
			"delay":   duration.String(),
			"service": serviceName,
		})
	}
}

func requestedDelay(request *http.Request) (time.Duration, error) {
	value := request.URL.Query().Get("duration")
	if value == "" {
		return defaultDelay, nil
	}

	duration, err := time.ParseDuration(value)
	if err != nil {
		return 0, fmt.Errorf("duration must use Go duration syntax: %w", err)
	}
	if duration <= 0 || duration > maxDelay {
		return 0, fmt.Errorf("duration must be greater than zero and at most %s", maxDelay)
	}

	return duration, nil
}

func mockError(writer http.ResponseWriter, request *http.Request) {
	status := http.StatusInternalServerError
	if value := request.URL.Query().Get("status"); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 400 || parsed > 599 {
			writeError(
				writer,
				http.StatusBadRequest,
				"invalid_status",
				"status must be an integer between 400 and 599",
			)
			return
		}
		status = parsed
	}

	writeError(writer, status, "mock_error", http.StatusText(status))
}

func writeError(writer http.ResponseWriter, status int, code string, message string) {
	writeJSON(writer, status, struct {
		Error errorResponse `json:"error"`
	}{
		Error: errorResponse{
			Code:    code,
			Message: message,
		},
	})
}

func writeJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}

type orderResponse struct {
	ID         string `json:"id"`
	UserID     string `json:"user_id"`
	TotalCents int64  `json:"total_cents"`
	Status     string `json:"status"`
}

type errorResponse struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}
