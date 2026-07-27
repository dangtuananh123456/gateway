package proxy

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/dangtuananh123456/gateway/internal/config"
	"github.com/dangtuananh123456/gateway/pkg/httperror"
)

func TestRouterWritesBadGatewayForUpstreamFailure(t *testing.T) {
	router := newErrorTestRouter(
		t,
		roundTripperFunc(func(*http.Request) (*http.Response, error) {
			return nil, errors.New("dial failed with internal details")
		}),
		time.Second,
	)
	recorder := httptest.NewRecorder()

	router.ServeHTTP(
		recorder,
		httptest.NewRequest(http.MethodGet, "http://gateway.local/api/users/42", nil),
	)

	assertGatewayError(
		t,
		recorder,
		http.StatusBadGateway,
		httperror.CodeBadGateway,
		"upstream service unavailable",
	)
	if body := recorder.Body.String(); strings.Contains(body, "dial failed") {
		t.Errorf("response leaked internal transport error: %q", body)
	}
}

func TestRouterWritesGatewayTimeoutWhenUpstreamExceedsDeadline(t *testing.T) {
	router := newErrorTestRouter(
		t,
		roundTripperFunc(func(request *http.Request) (*http.Response, error) {
			<-request.Context().Done()
			return nil, request.Context().Err()
		}),
		20*time.Millisecond,
	)
	recorder := httptest.NewRecorder()

	router.ServeHTTP(
		recorder,
		httptest.NewRequest(http.MethodGet, "http://gateway.local/api/users/42", nil),
	)

	assertGatewayError(
		t,
		recorder,
		http.StatusGatewayTimeout,
		httperror.CodeUpstreamTimeout,
		"upstream request timed out",
	)
}

func TestRouterPropagatesClientCancellation(t *testing.T) {
	started := make(chan struct{})
	canceled := make(chan struct{})
	router := newErrorTestRouter(
		t,
		roundTripperFunc(func(request *http.Request) (*http.Response, error) {
			close(started)
			<-request.Context().Done()
			close(canceled)
			return nil, request.Context().Err()
		}),
		time.Second,
	)

	ctx, cancel := context.WithCancel(context.Background())
	request := httptest.NewRequest(
		http.MethodGet,
		"http://gateway.local/api/users/42",
		nil,
	).WithContext(ctx)
	recorder := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		router.ServeHTTP(recorder, request)
		close(done)
	}()

	waitForChannel(t, started, "transport request")
	cancel()
	waitForChannel(t, canceled, "transport context cancellation")
	waitForChannel(t, done, "router completion")

	if recorder.Body.Len() != 0 {
		t.Errorf("body = %q, want no response after client cancellation", recorder.Body.String())
	}
}

func newErrorTestRouter(
	t *testing.T,
	transport http.RoundTripper,
	timeout time.Duration,
) *Router {
	t.Helper()

	table, err := NewTable([]config.RouteConfig{
		{
			Prefix:      "/api/users",
			Upstream:    "http://users:8080",
			StripPrefix: "/api/users",
		},
	})
	if err != nil {
		t.Fatalf("NewTable(): %v", err)
	}
	router, err := NewRouter(table, transport, timeout)
	if err != nil {
		t.Fatalf("NewRouter(): %v", err)
	}

	return router
}

func assertGatewayError(
	t *testing.T,
	recorder *httptest.ResponseRecorder,
	wantStatus int,
	wantCode httperror.Code,
	wantMessage string,
) {
	t.Helper()

	if recorder.Code != wantStatus {
		t.Fatalf("status = %d, want %d", recorder.Code, wantStatus)
	}
	if contentType := recorder.Header().Get("Content-Type"); contentType != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", contentType)
	}

	var response httperror.Response
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode error response: %v", err)
	}
	if response.Error.Code != wantCode {
		t.Errorf("error.code = %q, want %q", response.Error.Code, wantCode)
	}
	if response.Error.Message != wantMessage {
		t.Errorf("error.message = %q, want %q", response.Error.Message, wantMessage)
	}
}

func waitForChannel(t *testing.T, channel <-chan struct{}, description string) {
	t.Helper()

	select {
	case <-channel:
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for %s", description)
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (function roundTripperFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}
