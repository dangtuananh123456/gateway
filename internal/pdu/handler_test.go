package pdu

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dangtuananh123456/gateway/internal/model"
	"github.com/dangtuananh123456/gateway/internal/store"
	"github.com/dangtuananh123456/gateway/pkg/constants"
)

func TestHealth(t *testing.T) {
	handler := newTestHandler(t, store.NewLocal(), 0)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(
		recorder,
		httptest.NewRequest(http.MethodGet, constants.HealthPath, nil),
	)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
	}
	var body model.HealthResponse
	if err := json.NewDecoder(recorder.Body).Decode(&body); err != nil {
		t.Fatalf("decode health response: %v", err)
	}
	if body.InstanceID != "pdu-session-1" || body.Status != constants.ServiceUp {
		t.Errorf("body = %+v, want instance pdu-session-1 with UP status", body)
	}
}

func TestCreateSMContext(t *testing.T) {
	local := store.NewLocal()
	handler := newTestHandler(t, local, 0)
	payload := validCreateSMContextRequest()
	recorder := serveCreate(t, handler, context.Background(), payload)

	if recorder.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d; body = %s", recorder.Code, http.StatusCreated, recorder.Body.String())
	}
	var response model.CreateSMContextResponse
	if err := json.NewDecoder(recorder.Body).Decode(&response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	prefix := "http://localhost:18080" + constants.CreateSMContextPath + "/"
	if !strings.HasPrefix(response.SMContextRef, prefix) {
		t.Errorf("smContextRef = %q, want prefix %q", response.SMContextRef, prefix)
	}
	if response.SUPI != payload.SUPI ||
		response.PDUSessionID != payload.PDUSessionID ||
		response.HandledBy != "pdu-session-1" ||
		response.Status != constants.SMContextActive {
		t.Errorf("response = %+v, want request identity handled by pdu-session-1 and ACTIVE", response)
	}
	if location := recorder.Header().Get("Location"); location != response.SMContextRef {
		t.Errorf("Location = %q, want %q", location, response.SMContextRef)
	}

	contextID := strings.TrimPrefix(response.SMContextRef, prefix)
	stored, err := local.Read(context.Background(), contextID)
	if err != nil {
		t.Fatalf("read created context: %v", err)
	}
	if stored.Request != payload || stored.HandledBy != "pdu-session-1" || stored.Status != constants.SMContextActive {
		t.Errorf("stored session = %+v, want original request and PDU identity", stored)
	}
	if active := handler.activeRequests.Load(); active != 0 {
		t.Errorf("active requests = %d, want 0", active)
	}
}

func TestCreateSMContextValidationDoesNotBecomeActive(t *testing.T) {
	sessions := new(stubSessionStore)
	handler := newTestHandler(t, sessions, 0)
	request := httptest.NewRequest(
		http.MethodPost,
		constants.CreateSMContextPath,
		strings.NewReader(`{}`),
	)
	request.Header.Set("Content-Type", constants.ContentTypeJSON)
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", recorder.Code, http.StatusBadRequest)
	}
	if calls := sessions.calls.Load(); calls != 0 {
		t.Errorf("store calls = %d, want 0", calls)
	}
	if active := handler.activeRequests.Load(); active != 0 {
		t.Errorf("active requests = %d, want 0", active)
	}
}

func TestCreateSMContextStoreFailure(t *testing.T) {
	sessions := &stubSessionStore{err: errors.New("diskless store failed")}
	handler := newTestHandler(t, sessions, 0)
	recorder := serveCreate(t, handler, context.Background(), validCreateSMContextRequest())

	assertErrorResponse(
		t,
		recorder,
		http.StatusInternalServerError,
		constants.CauseInternalError,
		"failed to create",
	)
	if calls := sessions.calls.Load(); calls != 1 {
		t.Errorf("store calls = %d, want 1", calls)
	}
	if active := handler.activeRequests.Load(); active != 0 {
		t.Errorf("active requests = %d, want 0", active)
	}
}

func TestCreateSMContextCancellationStopsDelay(t *testing.T) {
	sessions := new(stubSessionStore)
	handler := newTestHandler(t, sessions, time.Hour)
	ctx, cancel := context.WithCancel(context.Background())
	request := createRequest(t, ctx, validCreateSMContextRequest())
	recorder := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		handler.ServeHTTP(recorder, request)
		close(done)
	}()

	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for handler.activeRequests.Load() != 1 {
		select {
		case <-deadline.C:
			t.Fatal("request did not enter processing state")
		case <-ticker.C:
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("handler did not stop after context cancellation")
	}

	if calls := sessions.calls.Load(); calls != 0 {
		t.Errorf("store calls = %d, want 0", calls)
	}
	if active := handler.activeRequests.Load(); active != 0 {
		t.Errorf("active requests = %d, want 0", active)
	}
}

func TestHandlerFailureContract(t *testing.T) {
	handler := newTestHandler(t, store.NewLocal(), 0)
	tests := []struct {
		name        string
		method      string
		path        string
		wantStatus  int
		wantCause   constants.ErrorCause
		wantAllowed string
	}{
		{name: "create method", method: http.MethodGet, path: constants.CreateSMContextPath, wantStatus: http.StatusMethodNotAllowed, wantCause: constants.CauseMethodNotAllowed, wantAllowed: http.MethodPost},
		{name: "health method", method: http.MethodPost, path: constants.HealthPath, wantStatus: http.StatusMethodNotAllowed, wantCause: constants.CauseMethodNotAllowed, wantAllowed: http.MethodGet},
		{name: "not found", method: http.MethodGet, path: "/missing", wantStatus: http.StatusNotFound, wantCause: constants.CauseNotFound},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, httptest.NewRequest(test.method, test.path, nil))
			assertErrorResponse(t, recorder, test.wantStatus, test.wantCause, "")
			if allowed := recorder.Header().Get("Allow"); allowed != test.wantAllowed {
				t.Errorf("Allow = %q, want %q", allowed, test.wantAllowed)
			}
		})
	}
}

func TestNewHandlerValidation(t *testing.T) {
	tests := []struct {
		name     string
		config   HandlerConfig
		sessions sessionCreator
	}{
		{name: "nil store", config: validHandlerConfig()},
		{name: "empty instance", config: HandlerConfig{PublicGatewayURL: "http://gateway"}, sessions: store.NewLocal()},
		{name: "empty public URL", config: HandlerConfig{InstanceID: "pdu-1"}, sessions: store.NewLocal()},
		{name: "negative delay", config: HandlerConfig{InstanceID: "pdu-1", PublicGatewayURL: "http://gateway", ProcessingDelay: -time.Second}, sessions: store.NewLocal()},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := NewHandler(test.config, test.sessions); err == nil {
				t.Fatal("NewHandler() error = nil, want validation error")
			}
		})
	}
}

type stubSessionStore struct {
	calls atomic.Int64
	err   error
}

func (sessions *stubSessionStore) Create(
	_ context.Context,
	_ store.CreateParams,
) (store.Session, error) {
	sessions.calls.Add(1)
	return store.Session{}, sessions.err
}

func newTestHandler(t *testing.T, sessions sessionCreator, delay time.Duration) *Handler {
	t.Helper()
	config := validHandlerConfig()
	config.ProcessingDelay = delay
	handler, err := NewHandler(config, sessions)
	if err != nil {
		t.Fatalf("NewHandler() error = %v", err)
	}
	return handler
}

func validHandlerConfig() HandlerConfig {
	return HandlerConfig{
		InstanceID:       "pdu-session-1",
		PublicGatewayURL: "http://localhost:18080",
	}
}

func serveCreate(
	t *testing.T,
	handler http.Handler,
	ctx context.Context,
	payload model.CreateSMContextRequest,
) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, createRequest(t, ctx, payload))
	return recorder
}

func createRequest(
	t *testing.T,
	ctx context.Context,
	payload model.CreateSMContextRequest,
) *http.Request {
	t.Helper()
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	request := httptest.NewRequest(
		http.MethodPost,
		constants.CreateSMContextPath,
		bytes.NewReader(body),
	).WithContext(ctx)
	request.Header.Set("Content-Type", constants.ContentTypeJSON)
	return request
}
