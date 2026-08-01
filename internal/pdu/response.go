package pdu

import (
	"encoding/json"
	"net/http"

	"github.com/dangtuananh123456/gateway/internal/model"
	"github.com/dangtuananh123456/gateway/pkg/constants"
)

func writeJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", constants.ContentTypeJSON)
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}

func writeRequestError(writer http.ResponseWriter, requestErr *requestError) {
	writeJSON(
		writer,
		model.HTTPStatus(requestErr.cause),
		model.NewErrorResponse(requestErr.cause, requestErr.detail),
	)
}
