package pdu

import (
	"encoding/json"
	"net/http"
)

// NewHandler creates the minimal PDU Session HTTP handler used by Docker smoke tests.
func NewHandler(instanceID string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(writer).Encode(map[string]string{
			"instanceId": instanceID,
			"status":     "UP",
		})
	})

	return mux
}
