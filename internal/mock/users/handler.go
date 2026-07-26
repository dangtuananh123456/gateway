// Package users provides the HTTP handler for the mock users upstream.
package users

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"
)

const (
	serviceName  = "mock-users"
	defaultDelay = 100 * time.Millisecond
	maxDelay     = 30 * time.Second
)

// NewHandler creates the mock users HTTP handler.
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
		Service string         `json:"service"`
		Users   []userResponse `json:"users"`
	}{
		Service: serviceName,
		Users: []userResponse{
			{ID: "1", Name: "Ada"},
			{ID: "2", Name: "Linus"},
		},
	})
}

func get(writer http.ResponseWriter, request *http.Request) {
	id := request.PathValue("id")
	writeJSON(writer, http.StatusOK, struct {
		Service string       `json:"service"`
		User    userResponse `json:"user"`
	}{
		Service: serviceName,
		User: userResponse{
			ID:   id,
			Name: "User " + id,
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

type userResponse struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type errorResponse struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}
