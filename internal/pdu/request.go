package pdu

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strings"

	"github.com/dangtuananh123456/gateway/internal/model"
	"github.com/dangtuananh123456/gateway/pkg/constants"
)

type requestError struct {
	cause  constants.ErrorCause
	detail string
}

func (requestErr *requestError) Error() string {
	return requestErr.detail
}

func decodeCreateSMContext(
	writer http.ResponseWriter,
	request *http.Request,
) (model.CreateSMContextRequest, *requestError) {
	if err := validateJSONContentType(request.Header.Get("Content-Type")); err != nil {
		return model.CreateSMContextRequest{}, err
	}
	if request.ContentLength > constants.CreateSMContextMaxBodyBytes {
		return model.CreateSMContextRequest{}, newRequestError(
			constants.CausePayloadTooLarge,
			fmt.Sprintf("request body must not exceed %d bytes", constants.CreateSMContextMaxBodyBytes),
		)
	}

	request.Body = http.MaxBytesReader(
		writer,
		request.Body,
		constants.CreateSMContextMaxBodyBytes,
	)
	decoder := json.NewDecoder(request.Body)

	var payload model.CreateSMContextRequest
	if err := decoder.Decode(&payload); err != nil {
		return model.CreateSMContextRequest{}, decodeJSONError(err)
	}
	if err := ensureJSONDocumentEnds(decoder); err != nil {
		return model.CreateSMContextRequest{}, err
	}
	if err := validateCreateSMContext(payload); err != nil {
		return model.CreateSMContextRequest{}, err
	}
	return payload, nil
}

func validateJSONContentType(contentType string) *requestError {
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil || !strings.EqualFold(mediaType, constants.ContentTypeJSON) {
		return newRequestError(
			constants.CauseUnsupportedMediaType,
			"Content-Type must be application/json",
		)
	}
	return nil
}

func ensureJSONDocumentEnds(decoder *json.Decoder) *requestError {
	var trailing any
	err := decoder.Decode(&trailing)
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err != nil {
		return decodeJSONError(err)
	}
	return newRequestError(
		constants.CauseInvalidRequest,
		"request body must contain exactly one JSON object",
	)
}

func decodeJSONError(err error) *requestError {
	var maxBytesError *http.MaxBytesError
	if errors.As(err, &maxBytesError) {
		return newRequestError(
			constants.CausePayloadTooLarge,
			fmt.Sprintf("request body must not exceed %d bytes", maxBytesError.Limit),
		)
	}
	return newRequestError(
		constants.CauseInvalidRequest,
		"request body must contain exactly one valid JSON object",
	)
}

func validateCreateSMContext(payload model.CreateSMContextRequest) *requestError {
	switch {
	case strings.TrimSpace(payload.SUPI) == "":
		return invalidField("supi", "must be a non-empty string")
	case payload.PDUSessionID <= 0:
		return invalidField("pduSessionId", "must be greater than zero")
	case strings.TrimSpace(payload.DNN) == "":
		return invalidField("dnn", "must be a non-empty string")
	case payload.SNSSAI.SST <= 0:
		return invalidField("sNssai.sst", "must be greater than zero")
	case strings.TrimSpace(payload.SNSSAI.SD) == "":
		return invalidField("sNssai.sd", "must be a non-empty string")
	default:
		return nil
	}
}

func invalidField(field, reason string) *requestError {
	return newRequestError(
		constants.CauseInvalidRequest,
		fmt.Sprintf("%s %s", field, reason),
	)
}

func newRequestError(cause constants.ErrorCause, detail string) *requestError {
	return &requestError{cause: cause, detail: detail}
}
