package pdu

import (
	"net/http"

	"github.com/dangtuananh123456/gateway/internal/model"
	"github.com/dangtuananh123456/gateway/pkg/constants"
)

// NewHandler creates the minimal PDU Session HTTP handler used by Docker smoke tests.
func NewHandler(instanceID string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(writer http.ResponseWriter, _ *http.Request) {
		writeJSON(writer, http.StatusOK, model.HealthResponse{
			InstanceID: instanceID,
			Status:     constants.ServiceUp,
		})
	})

	return mux
}
