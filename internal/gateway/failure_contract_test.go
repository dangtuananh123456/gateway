package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dangtuananh123456/gateway/internal/model"
	"github.com/dangtuananh123456/gateway/internal/registry"
	"github.com/dangtuananh123456/gateway/internal/routing"
	"github.com/dangtuananh123456/gateway/pkg/constants"
)

func TestProxyNoBackendExactContract(t *testing.T) {
	var transportCalls atomic.Int64
	transport := roundTripFunc(func(*http.Request) (*http.Response, error) {
		transportCalls.Add(1)
		return proxyResponse(http.StatusOK, "unexpected"), nil
	})
	proxy := newProxyForTest(t, registry.New(), routing.NewRoundRobin(), transport, time.Second)
	response := httptest.NewRecorder()

	proxy.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "http://gateway/session", nil))

	const wantBody = `{"status":"ERROR","cause":"NO_BACKEND_AVAILABLE"}`
	if response.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want %d", response.Code, http.StatusServiceUnavailable)
	}
	if got := response.Body.String(); got != wantBody {
		t.Errorf("body = %q, want exact %q", got, wantBody)
	}
	if got := response.Header().Get("Content-Type"); got != constants.ContentTypeJSON {
		t.Errorf("Content-Type = %q, want %q", got, constants.ContentTypeJSON)
	}
	if got := transportCalls.Load(); got != 0 {
		t.Errorf("transport calls = %d, want 0", got)
	}
}

func TestProxyConnectErrorContractAndNoRetry(t *testing.T) {
	var transportCalls atomic.Int64
	transport := roundTripFunc(func(*http.Request) (*http.Response, error) {
		transportCalls.Add(1)
		return nil, errors.New("connection refused")
	})
	proxy := proxyWithOneBackend(t, transport, time.Second)
	writer := newContractWriter()

	proxy.ServeHTTP(writer, httptest.NewRequest(http.MethodPost, "http://gateway/session", bytes.NewReader([]byte("body"))))

	assertGatewayErrorContract(t, writer, http.StatusBadGateway, constants.CauseBadGateway)
	if got := transportCalls.Load(); got != 1 {
		t.Errorf("POST transport calls = %d, want exactly 1", got)
	}
}

func TestProxyUpstreamTimeoutContractAndNoRetry(t *testing.T) {
	var transportCalls atomic.Int64
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		transportCalls.Add(1)
		<-request.Context().Done()
		return nil, request.Context().Err()
	})
	proxy := proxyWithOneBackend(t, transport, 10*time.Millisecond)
	writer := newContractWriter()

	proxy.ServeHTTP(writer, httptest.NewRequest(http.MethodPost, "http://gateway/session", bytes.NewReader([]byte("body"))))

	assertGatewayErrorContract(t, writer, http.StatusGatewayTimeout, constants.CauseUpstreamTimeout)
	if got := transportCalls.Load(); got != 1 {
		t.Errorf("POST transport calls = %d, want exactly 1", got)
	}
}

func TestProxyPassesThroughSuccessAndPDUClientErrors(t *testing.T) {
	tests := []struct {
		name        string
		status      int
		body        string
		location    string
		contentType string
	}{
		{
			name: "created", status: http.StatusCreated,
			body:     `{"smContextRef":"ctx-1","handledBy":"pdu-1"}`,
			location: "/contexts/ctx-1", contentType: constants.ContentTypeJSON,
		},
		{
			name: "bad request", status: http.StatusBadRequest,
			body:        `{"status":"ERROR","cause":"INVALID_REQUEST"}`,
			contentType: constants.ContentTypeJSON,
		},
		{
			name: "method not allowed", status: http.StatusMethodNotAllowed,
			body:        `{"status":"ERROR","cause":"METHOD_NOT_ALLOWED"}`,
			contentType: constants.ContentTypeJSON,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			transport := roundTripFunc(func(*http.Request) (*http.Response, error) {
				response := proxyResponse(test.status, test.body)
				response.Header.Set("Content-Type", test.contentType)
				if test.location != "" {
					response.Header.Set("Location", test.location)
				}
				return response, nil
			})
			proxy := proxyWithOneBackend(t, transport, time.Second)
			writer := newContractWriter()

			proxy.ServeHTTP(writer, httptest.NewRequest(http.MethodPost, "http://gateway/session", nil))

			if len(writer.statusCodes) != 1 || writer.statusCodes[0] != test.status {
				t.Errorf("status writes = %v, want exactly [%d]", writer.statusCodes, test.status)
			}
			if got := writer.body.String(); got != test.body {
				t.Errorf("body = %q, want %q", got, test.body)
			}
			if got := writer.header.Get("Content-Type"); got != test.contentType {
				t.Errorf("Content-Type = %q, want %q", got, test.contentType)
			}
			if got := writer.header.Get("Location"); got != test.location {
				t.Errorf("Location = %q, want %q", got, test.location)
			}
		})
	}
}

func TestProxyClientCancelWritesNoResponse(t *testing.T) {
	started := make(chan struct{})
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		close(started)
		<-request.Context().Done()
		return nil, request.Context().Err()
	})
	proxy := proxyWithOneBackend(t, transport, time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	request := httptest.NewRequest(http.MethodPost, "http://gateway/session", nil).WithContext(ctx)
	writer := newContractWriter()
	done := make(chan struct{})
	go func() {
		proxy.ServeHTTP(writer, request)
		close(done)
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("transport was not called")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("proxy did not return after client cancellation")
	}
	if len(writer.statusCodes) != 0 || writer.body.Len() != 0 {
		t.Errorf("client cancellation wrote status=%v body=%q, want no response", writer.statusCodes, writer.body.String())
	}
}

func TestProxyResponseCopyErrorDoesNotDoubleWrite(t *testing.T) {
	copyFailure := errors.New("upstream body interrupted")
	transport := roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusCreated,
			Header:     make(http.Header),
			Body: &failingReadCloser{
				reader: io.MultiReader(bytes.NewBufferString("partial"), errorReader{err: copyFailure}),
			},
		}, nil
	})
	proxy := proxyWithOneBackend(t, transport, time.Second)
	writer := newContractWriter()

	proxy.ServeHTTP(writer, httptest.NewRequest(http.MethodPost, "http://gateway/session", nil))

	if len(writer.statusCodes) != 1 || writer.statusCodes[0] != http.StatusCreated {
		t.Errorf("status writes = %v, want exactly [201]", writer.statusCodes)
	}
	if got := writer.body.String(); got != "partial" {
		t.Errorf("body = %q, want partial response without a second error body", got)
	}
}

func TestProxyUnexpectedSelectorErrorReturnsInternalError(t *testing.T) {
	selector := selectorFunc(func(registry.Snapshot) (registry.Instance, error) {
		return registry.Instance{}, errors.New("selector failed")
	})
	transport := roundTripFunc(func(*http.Request) (*http.Response, error) {
		return proxyResponse(http.StatusOK, "unexpected"), nil
	})
	proxy := newProxyForTest(t, registry.New(), selector, transport, time.Second)
	writer := newContractWriter()

	proxy.ServeHTTP(writer, httptest.NewRequest(http.MethodPost, "http://gateway/session", nil))

	assertGatewayErrorContract(t, writer, http.StatusInternalServerError, constants.CauseInternalError)
}

func proxyWithOneBackend(t *testing.T, transport http.RoundTripper, timeout time.Duration) *Proxy {
	t.Helper()
	candidates := registry.New()
	registerProxyAddress(t, candidates, netip.MustParseAddrPort("10.0.0.1:8081"), "pdu-1")
	return newProxyForTest(t, candidates, routing.NewRoundRobin(), transport, timeout)
}

func assertGatewayErrorContract(
	t *testing.T,
	writer *contractWriter,
	status int,
	cause constants.ErrorCause,
) {
	t.Helper()
	if len(writer.statusCodes) != 1 || writer.statusCodes[0] != status {
		t.Errorf("status writes = %v, want exactly [%d]", writer.statusCodes, status)
	}
	if got := writer.header.Get("Content-Type"); got != constants.ContentTypeJSON {
		t.Errorf("Content-Type = %q, want %q", got, constants.ContentTypeJSON)
	}
	var response model.ErrorResponse
	if err := json.Unmarshal(writer.body.Bytes(), &response); err != nil {
		t.Fatalf("decode error response %q: %v", writer.body.String(), err)
	}
	if response.Status != constants.ErrorStatus || response.Cause != cause {
		t.Errorf("error response = %+v, want status ERROR cause %s", response, cause)
	}
}

type contractWriter struct {
	header      http.Header
	statusCodes []int
	body        bytes.Buffer
}

func newContractWriter() *contractWriter {
	return &contractWriter{header: make(http.Header)}
}

func (writer *contractWriter) Header() http.Header { return writer.header }

func (writer *contractWriter) WriteHeader(status int) {
	writer.statusCodes = append(writer.statusCodes, status)
}

func (writer *contractWriter) Write(body []byte) (int, error) {
	if len(writer.statusCodes) == 0 {
		writer.WriteHeader(http.StatusOK)
	}
	return writer.body.Write(body)
}

type selectorFunc func(registry.Snapshot) (registry.Instance, error)

func (function selectorFunc) Select(snapshot registry.Snapshot) (registry.Instance, error) {
	return function(snapshot)
}

type failingReadCloser struct {
	reader io.Reader
}

func (reader *failingReadCloser) Read(buffer []byte) (int, error) {
	return reader.reader.Read(buffer)
}

func (*failingReadCloser) Close() error { return nil }

type errorReader struct {
	err error
}

func (reader errorReader) Read([]byte) (int, error) { return 0, reader.err }
