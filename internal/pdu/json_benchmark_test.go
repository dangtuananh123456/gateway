package pdu

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/bytedance/sonic"
	"github.com/dangtuananh123456/gateway/internal/model"
	"github.com/dangtuananh123456/gateway/internal/store"
	"github.com/dangtuananh123456/gateway/pkg/constants"
)

var benchmarkCreateBody = []byte(`{"supi":"imsi-452040000000001","gpsi":"msisdn-84900000001","pduSessionId":1,"dnn":"v-internet","sNssai":{"sst":1,"sd":"000001"},"servingNfId":"2ab2b5a9-68e8-4ee6-b939-024c109b520c","anType":"3GPP_ACCESS"}`)

// BenchmarkJSONDecoderCreateSMContext preserves the previous streaming
// implementation as a comparison baseline for the hot request path.
func BenchmarkJSONDecoderCreateSMContext(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		decoder := json.NewDecoder(bytes.NewReader(benchmarkCreateBody))
		var payload model.CreateSMContextRequest
		if err := decoder.Decode(&payload); err != nil {
			b.Fatal(err)
		}
		if err := decoder.Decode(&struct{}{}); err != io.EOF {
			b.Fatalf("trailing JSON check: %v", err)
		}
	}
}

func BenchmarkJSONUnmarshalCreateSMContext(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		var payload model.CreateSMContextRequest
		if err := json.Unmarshal(benchmarkCreateBody, &payload); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkSonicUnmarshalCreateSMContext(b *testing.B) {
	// Exclude one-time JIT compilation from steady-state request measurements.
	var warmup model.CreateSMContextRequest
	if err := sonic.ConfigStd.Unmarshal(benchmarkCreateBody, &warmup); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		var payload model.CreateSMContextRequest
		if err := sonic.ConfigStd.Unmarshal(benchmarkCreateBody, &payload); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkJSONEncodeCreateSMContext(b *testing.B) {
	response := model.CreateSMContextResponse{
		SMContextRef: "http://gateway/nsmf-pdusession/v1/sm-contexts/ctx-0001",
		SUPI:         "imsi-452040000000001",
		PDUSessionID: 1,
		HandledBy:    "pdu-session-1",
		Status:       constants.SMContextActive,
	}
	b.ReportAllocs()
	for b.Loop() {
		if _, err := json.Marshal(response); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkJSONEncoderCreateSMContext(b *testing.B) {
	response := benchmarkCreateResponse()
	b.ReportAllocs()
	for b.Loop() {
		if err := json.NewEncoder(io.Discard).Encode(response); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkSonicEncoderCreateSMContext(b *testing.B) {
	response := benchmarkCreateResponse()
	if err := sonic.ConfigStd.NewEncoder(io.Discard).Encode(response); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		if err := sonic.ConfigStd.NewEncoder(io.Discard).Encode(response); err != nil {
			b.Fatal(err)
		}
	}
}

func benchmarkCreateResponse() model.CreateSMContextResponse {
	return model.CreateSMContextResponse{
		SMContextRef: "http://gateway/nsmf-pdusession/v1/sm-contexts/ctx-0001",
		SUPI:         "imsi-452040000000001",
		PDUSessionID: 1,
		HandledBy:    "pdu-session-1",
		Status:       constants.SMContextActive,
	}
}

func BenchmarkCreateSMContextHandler(b *testing.B) {
	handler, err := NewHandler(HandlerConfig{
		InstanceID:       "pdu-session-1",
		Weight:           1,
		PublicGatewayURL: "http://gateway",
	}, benchmarkSessionCreator{})
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	for b.Loop() {
		request := httptest.NewRequest(http.MethodPost, constants.CreateSMContextPath, bytes.NewReader(benchmarkCreateBody))
		request.Header.Set("Content-Type", constants.ContentTypeJSON)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusCreated {
			b.Fatalf("status = %d", response.Code)
		}
	}
}

type benchmarkSessionCreator struct{}

func (benchmarkSessionCreator) Create(context.Context, store.CreateParams) (store.Session, error) {
	return store.Session{ContextID: "ctx-0001"}, nil
}
