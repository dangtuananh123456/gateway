package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dangtuananh123456/gateway/internal/loadtest"
	"github.com/dangtuananh123456/gateway/internal/requestlog"
	"github.com/dangtuananh123456/gateway/pkg/constants"
)

func TestHandlerServesEmbeddedUIAndConfiguration(t *testing.T) {
	handler := newTestHandler(t, doerFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("unexpected Gateway request")
	}))
	for _, path := range []string{"/", "/app.css", "/app.js", "/openapi.json"} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusOK || response.Body.Len() == 0 {
			t.Errorf("GET %s = status %d body bytes %d, want 200 non-empty", path, response.Code, response.Body.Len())
		}
		if response.Header().Get("Content-Security-Policy") == "" {
			t.Errorf("GET %s has no Content-Security-Policy", path)
		}
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/config", nil))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "http://gateway:8080") {
		t.Errorf("GET /api/config = status %d body %q", response.Code, response.Body.String())
	}
}

func TestHandlerForwardsControlledRequestAndReportsH2CResponse(t *testing.T) {
	var calls atomic.Int64
	handler := newTestHandler(t, doerFunc(func(request *http.Request) (*http.Response, error) {
		calls.Add(1)
		if request.Method != http.MethodPost || request.URL.String() != "http://gateway:8080/session?trace=1" {
			t.Errorf("upstream request = %s %s", request.Method, request.URL)
		}
		if request.Header.Get("X-Debug") != "yes" || request.Header.Get("Content-Type") != constants.ContentTypeJSON {
			t.Errorf("upstream headers = %v", request.Header)
		}
		body, _ := io.ReadAll(request.Body)
		if string(body) != `{"hello":"gateway"}` {
			t.Errorf("upstream body = %q", body)
		}
		return &http.Response{
			StatusCode: http.StatusCreated,
			Status:     "201 Created",
			Proto:      "HTTP/2.0",
			ProtoMajor: 2,
			Header:     http.Header{"X-Upstream": []string{"pdu-1"}},
			Body:       io.NopCloser(strings.NewReader(`{"handledBy":"pdu-1"}`)),
		}, nil
	}))
	input := executeRequest{
		Method: "post", Path: "/session?trace=1",
		Headers: map[string]string{"X-Debug": "yes", "Content-Type": constants.ContentTypeJSON},
		Body:    `{"hello":"gateway"}`,
	}
	response := executeThroughBridge(t, handler, input)
	if response.Code != http.StatusOK {
		t.Fatalf("bridge status = %d body=%q", response.Code, response.Body.String())
	}
	var result executeResponse
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		t.Fatalf("decode execute response: %v", err)
	}
	if result.StatusCode != http.StatusCreated || result.Protocol != "HTTP/2.0" ||
		result.Body != `{"handledBy":"pdu-1"}` || result.Headers.Get("X-Upstream") != "pdu-1" {
		t.Errorf("execute response = %+v", result)
	}
	if calls.Load() != 1 {
		t.Errorf("Gateway calls = %d, want 1", calls.Load())
	}
}

func TestHandlerPreservesGatewayErrorAsInspectableResult(t *testing.T) {
	handler := newTestHandler(t, doerFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusServiceUnavailable, Status: "503 Service Unavailable",
			Proto: "HTTP/2.0", ProtoMajor: 2, Header: make(http.Header),
			Body: io.NopCloser(strings.NewReader(`{"status":"ERROR","cause":"NO_BACKEND_AVAILABLE"}`)),
		}, nil
	}))
	response := executeThroughBridge(t, handler, executeRequest{Method: http.MethodGet, Path: "/health"})
	var result executeResponse
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		t.Fatalf("decode execute response: %v", err)
	}
	if response.Code != http.StatusOK || result.StatusCode != http.StatusServiceUnavailable ||
		!strings.Contains(result.Body, "NO_BACKEND_AVAILABLE") {
		t.Errorf("bridge=%d result=%+v", response.Code, result)
	}
}

func TestHandlerServesPerformanceReportsByAlgorithm(t *testing.T) {
	handler := newTestHandler(t, doerFunc(func(request *http.Request) (*http.Response, error) {
		switch request.URL.Path {
		case constants.GatewayBackendsPath:
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(`{"routingMode":"round_robin"}`)),
				Header:     make(http.Header),
			}, nil
		case constants.GatewayStatsPath:
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(`{"cpuPercent":95.5,"ramUsageBytes":31457280,"ramUsageMiB":30.0,"ramPeakMiB":30.0,"totalCpuTimeMillis":5000}`)),
				Header:     make(http.Header),
			}, nil
		default:
			t.Errorf("unexpected gateway request path = %q", request.URL.Path)
			return &http.Response{
				StatusCode: http.StatusNotFound,
				Body:       io.NopCloser(strings.NewReader(`{}`)),
				Header:     make(http.Header),
			}, nil
		}
	}))
	var runnerCalls atomic.Int64
	handler.performanceRunner = performanceRunnerFunc(func(_ context.Context, cfg loadtest.Config) (loadtest.Result, error) {
		call := runnerCalls.Add(1)
		if cfg.Target != "http://gateway:8080"+constants.CreateSMContextPath {
			t.Errorf("load target = %q", cfg.Target)
		}
		wantMinimum := uint64(performanceTargetSuccessful)
		if call == 1 {
			wantMinimum = performanceWarmupSuccessful
		}
		if cfg.MinimumSuccessfulRequests != wantMinimum {
			t.Errorf("minimum successful requests on call %d = %d, want %d", call, cfg.MinimumSuccessfulRequests, wantMinimum)
		}
		return loadtest.Result{
			SuccessfulTPS:    float64(call * 100),
			LatencyP50Millis: float64(call * 10),
			LatencyP95Millis: float64(call * 20),
			LatencyP99Millis: float64(call * 30),
			FailedRequests:   uint64(call),
		}, nil
	})

	metadataResponse := httptest.NewRecorder()
	handler.ServeHTTP(metadataResponse, httptest.NewRequest(http.MethodGet, constants.ClientPerformanceRoundRobinPath, nil))
	var metadata performanceReport
	if err := json.NewDecoder(metadataResponse.Body).Decode(&metadata); err != nil {
		t.Fatalf("decode metadata: %v", err)
	}
	if metadataResponse.Code != http.StatusOK || len(metadata.Measurements) != 0 ||
		metadata.Environment.Runs != 3 || metadata.Environment.ConcurrentStreams != 480 ||
		metadata.Environment.TargetRequests != 15000 || metadata.Environment.TargetSuccessfulRequests != 12000 {
		t.Errorf("metadata status=%d report=%+v", metadataResponse.Code, metadata)
	}

	input := `{"runs":2,"warmupSeconds":1,"durationSeconds":1,"connections":15,"streamsPerConnection":1000,"requestTimeoutSeconds":1}`
	runResponse := httptest.NewRecorder()
	handler.ServeHTTP(runResponse, httptest.NewRequest(
		http.MethodPost,
		constants.ClientPerformanceRoundRobinPath,
		strings.NewReader(input),
	))
	var report performanceReport
	if err := json.NewDecoder(runResponse.Body).Decode(&report); err != nil {
		t.Fatalf("decode live report: %v body=%q", err, runResponse.Body.String())
	}
	if runResponse.Code != http.StatusOK || report.Algorithm != "round_robin" ||
		len(report.Measurements) != 3 || report.MeasuredAt == "" || runnerCalls.Load() != 3 {
		t.Fatalf("live status=%d calls=%d report=%+v", runResponse.Code, runnerCalls.Load(), report)
	}
	average := report.Measurements[2]
	if !average.Average || average.SuccessfulTPS != 250 || average.FailedRequests != 3 {
		t.Errorf("average = %+v", average)
	}

	mismatch := httptest.NewRecorder()
	handler.ServeHTTP(mismatch, httptest.NewRequest(
		http.MethodPost,
		constants.ClientPerformanceWeightedPath,
		strings.NewReader(input),
	))
	if mismatch.Code != http.StatusConflict || !strings.Contains(mismatch.Body.String(), "round_robin") {
		t.Errorf("mode mismatch status=%d body=%q", mismatch.Code, mismatch.Body.String())
	}
}

func TestHandlerPropagatesInboundRequestIDToGateway(t *testing.T) {
	handler := newTestHandler(t, doerFunc(func(request *http.Request) (*http.Response, error) {
		if got := request.Header.Get(requestlog.RequestIDHeader); got != "browser-request-123" {
			t.Errorf("Gateway request ID = %q, want browser-request-123", got)
		}
		return &http.Response{
			StatusCode: http.StatusOK, Status: "200 OK", Proto: "HTTP/2.0", ProtoMajor: 2,
			Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{}`)),
		}, nil
	}))
	input, err := json.Marshal(executeRequest{Method: http.MethodGet, Path: "/health"})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/request", bytes.NewReader(input))
	request.Header.Set(requestlog.RequestIDHeader, "browser-request-123")
	handler.ServeHTTP(httptest.NewRecorder(), request)
}

func TestHandlerRejectsUnsafeOrMalformedBridgeRequests(t *testing.T) {
	handler := newTestHandler(t, doerFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("must not be called")
	}))
	tests := []struct {
		name  string
		input executeRequest
		raw   string
	}{
		{name: "method", input: executeRequest{Method: http.MethodConnect, Path: "/health"}},
		{name: "absolute URL", input: executeRequest{Method: http.MethodGet, Path: "http://evil.test/"}},
		{name: "scheme relative", input: executeRequest{Method: http.MethodGet, Path: "//evil.test/"}},
		{name: "managed header", input: executeRequest{Method: http.MethodGet, Path: "/health", Headers: map[string]string{"Host": "evil.test"}}},
		{name: "header line break", input: executeRequest{Method: http.MethodGet, Path: "/health", Headers: map[string]string{"X-Debug": "safe\r\ninjected"}}},
		{name: "unknown field", raw: `{"method":"GET","path":"/health","extra":true}`},
		{name: "multiple JSON", raw: `{"method":"GET","path":"/health"}{}`},
		{name: "malformed JSON", raw: `{`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var body []byte
			if test.raw != "" {
				body = []byte(test.raw)
			} else {
				body, _ = json.Marshal(test.input)
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/request", bytes.NewReader(body)))
			if response.Code != http.StatusBadRequest {
				t.Errorf("status = %d body=%q, want 400", response.Code, response.Body.String())
			}
		})
	}
}

func TestHandlerMapsTransportAndOversizedResponseErrors(t *testing.T) {
	t.Run("transport", func(t *testing.T) {
		handler := newTestHandler(t, doerFunc(func(*http.Request) (*http.Response, error) {
			return nil, errors.New("connection refused")
		}))
		response := executeThroughBridge(t, handler, executeRequest{Method: http.MethodGet, Path: "/health"})
		if response.Code != http.StatusBadGateway || !strings.Contains(response.Body.String(), "connection refused") {
			t.Errorf("status=%d body=%q", response.Code, response.Body.String())
		}
	})
	t.Run("oversized response", func(t *testing.T) {
		handler := newTestHandler(t, doerFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: 200, Status: "200 OK", Proto: "HTTP/2.0", Header: make(http.Header),
				Body: io.NopCloser(io.LimitReader(zeroReader{}, constants.ClientMaxResponseBodyBytes+1)),
			}, nil
		}))
		response := executeThroughBridge(t, handler, executeRequest{Method: http.MethodGet, Path: "/health"})
		if response.Code != http.StatusBadGateway || !strings.Contains(response.Body.String(), "exceeds") {
			t.Errorf("status=%d body=%q", response.Code, response.Body.String())
		}
	})
}

func TestHandlerPropagatesBrowserCancellation(t *testing.T) {
	started := make(chan struct{})
	handler := newTestHandler(t, doerFunc(func(request *http.Request) (*http.Response, error) {
		close(started)
		<-request.Context().Done()
		return nil, request.Context().Err()
	}))
	input, _ := json.Marshal(executeRequest{Method: http.MethodGet, Path: "/health"})
	ctx, cancel := context.WithCancel(context.Background())
	request := httptest.NewRequest(http.MethodPost, "/api/request", bytes.NewReader(input)).WithContext(ctx)
	response := httptest.NewRecorder()
	done := make(chan struct{})
	go func() { handler.ServeHTTP(response, request); close(done) }()
	<-started
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("handler did not return after browser cancellation")
	}
	if response.Body.Len() != 0 {
		t.Errorf("canceled request body = %q, want empty", response.Body.String())
	}
}

func TestNewHandlerAndMethodsValidateInputs(t *testing.T) {
	if _, err := NewHandler("http://gateway:8080", nil); err == nil {
		t.Fatal("NewHandler(nil client) error = nil")
	}
	for _, target := range []string{"", "https://gateway", "http://gateway/path", "http://user@gateway"} {
		if _, err := NewHandler(target, doerFunc(nil)); err == nil {
			t.Errorf("NewHandler(%q) error = nil", target)
		}
	}
	handler := newTestHandler(t, doerFunc(func(*http.Request) (*http.Response, error) { return nil, nil }))
	for _, test := range []struct{ method, path, allow string }{
		{http.MethodPost, "/api/config", http.MethodGet},
		{http.MethodGet, "/api/request", http.MethodPost},
		{http.MethodDelete, constants.ClientPerformanceRoundRobinPath, "GET, POST"},
		{http.MethodPost, "/", http.MethodGet},
	} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(test.method, test.path, nil))
		if response.Code != http.StatusMethodNotAllowed || response.Header().Get("Allow") != test.allow {
			t.Errorf("%s %s = %d Allow=%q", test.method, test.path, response.Code, response.Header().Get("Allow"))
		}
	}
}

type doerFunc func(*http.Request) (*http.Response, error)

func (function doerFunc) Do(request *http.Request) (*http.Response, error) {
	return function(request)
}

type performanceRunnerFunc func(context.Context, loadtest.Config) (loadtest.Result, error)

func (function performanceRunnerFunc) Run(ctx context.Context, cfg loadtest.Config) (loadtest.Result, error) {
	return function(ctx, cfg)
}

type zeroReader struct{}

func (zeroReader) Read(buffer []byte) (int, error) {
	for index := range buffer {
		buffer[index] = 0
	}
	return len(buffer), nil
}

func newTestHandler(t *testing.T, doer HTTPDoer) *Handler {
	t.Helper()
	handler, err := NewHandler("http://gateway:8080", doer)
	if err != nil {
		t.Fatalf("NewHandler() error = %v", err)
	}
	return handler
}

func executeThroughBridge(t *testing.T, handler *Handler, input executeRequest) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(input)
	if err != nil {
		t.Fatalf("marshal execute request: %v", err)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/request", bytes.NewReader(body)))
	return response
}
