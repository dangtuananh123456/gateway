package pdu

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dangtuananh123456/gateway/internal/model"
	"github.com/dangtuananh123456/gateway/pkg/constants"
)

func TestDecodeCreateSMContext(t *testing.T) {
	want := validCreateSMContextRequest()
	body, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	request := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json; charset=utf-8")

	got, requestErr := decodeCreateSMContext(httptest.NewRecorder(), request)
	if requestErr != nil {
		t.Fatalf("decodeCreateSMContext() error = %v", requestErr)
	}
	if got != want {
		t.Errorf("decodeCreateSMContext() = %+v, want %+v", got, want)
	}
}

func TestDecodeCreateSMContextRejectsMediaType(t *testing.T) {
	tests := []struct {
		name        string
		contentType string
	}{
		{name: "missing"},
		{name: "wrong", contentType: "text/plain"},
		{name: "malformed", contentType: `application/json; charset="`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assertValidationError(
				t,
				[]byte(`{}`),
				test.contentType,
				constants.CauseUnsupportedMediaType,
				http.StatusUnsupportedMediaType,
				false,
			)
		})
	}
}

func TestDecodeCreateSMContextRejectsInvalidJSON(t *testing.T) {
	validBody, err := json.Marshal(validCreateSMContextRequest())
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	tests := []struct {
		name string
		body []byte
	}{
		{name: "empty"},
		{name: "malformed", body: []byte(`{"supi":`)},
		{name: "wrong field type", body: []byte(`{"pduSessionId":"one"}`)},
		{name: "multiple documents", body: append(append([]byte{}, validBody...), validBody...)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assertValidationError(
				t,
				test.body,
				constants.ContentTypeJSON,
				constants.CauseInvalidRequest,
				http.StatusBadRequest,
				false,
			)
		})
	}
}

func TestDecodeCreateSMContextRejectsLargeBody(t *testing.T) {
	body := []byte(`{"padding":"` +
		strings.Repeat("x", int(constants.CreateSMContextMaxBodyBytes)) +
		`"}`)
	for _, streamed := range []bool{false, true} {
		name := "known content length"
		if streamed {
			name = "streamed body"
		}
		t.Run(name, func(t *testing.T) {
			assertValidationError(
				t,
				body,
				constants.ContentTypeJSON,
				constants.CausePayloadTooLarge,
				http.StatusRequestEntityTooLarge,
				streamed,
			)
		})
	}
}

func TestValidateCreateSMContextRequiredFields(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*model.CreateSMContextRequest)
		detail string
	}{
		{name: "missing supi", mutate: func(value *model.CreateSMContextRequest) { value.SUPI = "" }, detail: "supi"},
		{name: "invalid supi", mutate: func(value *model.CreateSMContextRequest) { value.SUPI = "   " }, detail: "supi"},
		{name: "missing PDU session ID", mutate: func(value *model.CreateSMContextRequest) { value.PDUSessionID = 0 }, detail: "pduSessionId"},
		{name: "invalid PDU session ID", mutate: func(value *model.CreateSMContextRequest) { value.PDUSessionID = -1 }, detail: "pduSessionId"},
		{name: "missing DNN", mutate: func(value *model.CreateSMContextRequest) { value.DNN = "" }, detail: "dnn"},
		{name: "invalid DNN", mutate: func(value *model.CreateSMContextRequest) { value.DNN = "\t" }, detail: "dnn"},
		{name: "missing SST", mutate: func(value *model.CreateSMContextRequest) { value.SNSSAI.SST = 0 }, detail: "sNssai.sst"},
		{name: "invalid SST", mutate: func(value *model.CreateSMContextRequest) { value.SNSSAI.SST = -1 }, detail: "sNssai.sst"},
		{name: "missing SD", mutate: func(value *model.CreateSMContextRequest) { value.SNSSAI.SD = "" }, detail: "sNssai.sd"},
		{name: "invalid SD", mutate: func(value *model.CreateSMContextRequest) { value.SNSSAI.SD = "\n" }, detail: "sNssai.sd"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			payload := validCreateSMContextRequest()
			test.mutate(&payload)
			body, err := json.Marshal(payload)
			if err != nil {
				t.Fatalf("marshal request: %v", err)
			}
			recorder := serveValidation(body, constants.ContentTypeJSON, false)
			assertErrorResponse(
				t,
				recorder,
				http.StatusBadRequest,
				constants.CauseInvalidRequest,
				test.detail,
			)
		})
	}
}

func assertValidationError(
	t *testing.T,
	body []byte,
	contentType string,
	cause constants.ErrorCause,
	status int,
	streamed bool,
) {
	t.Helper()
	recorder := serveValidation(body, contentType, streamed)
	assertErrorResponse(t, recorder, status, cause, "")
}

func serveValidation(body []byte, contentType string, streamed bool) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
	request.Header.Set("Content-Type", contentType)
	if streamed {
		request.ContentLength = -1
	}
	recorder := httptest.NewRecorder()
	_, requestErr := decodeCreateSMContext(recorder, request)
	if requestErr != nil {
		writeRequestError(recorder, requestErr)
	}
	return recorder
}

func assertErrorResponse(
	t *testing.T,
	recorder *httptest.ResponseRecorder,
	status int,
	cause constants.ErrorCause,
	detailContains string,
) {
	t.Helper()
	if recorder.Code != status {
		t.Fatalf("status = %d, want %d; body = %s", recorder.Code, status, recorder.Body.String())
	}
	if contentType := recorder.Header().Get("Content-Type"); contentType != constants.ContentTypeJSON {
		t.Errorf("Content-Type = %q, want %q", contentType, constants.ContentTypeJSON)
	}
	var response model.ErrorResponse
	if err := json.NewDecoder(recorder.Body).Decode(&response); err != nil {
		t.Fatalf("decode error response: %v", err)
	}
	if response.Status != constants.ErrorStatus || response.Cause != cause {
		t.Errorf("error response = %+v, want status ERROR and cause %s", response, cause)
	}
	if detailContains != "" && !strings.Contains(response.Detail, detailContains) {
		t.Errorf("detail = %q, want it to contain %q", response.Detail, detailContains)
	}
}

func validCreateSMContextRequest() model.CreateSMContextRequest {
	return model.CreateSMContextRequest{
		SUPI:         "imsi-452040000000001",
		GPSI:         "msisdn-84900000001",
		PDUSessionID: 1,
		DNN:          "v-internet",
		SNSSAI:       model.SNSSAI{SST: 1, SD: "000001"},
		ServingNFID:  "2ab2b5a9-68e8-4ee6-b939-024c109b520c",
		ANType:       "3GPP_ACCESS",
	}
}
