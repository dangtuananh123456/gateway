package gateway

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dangtuananh123456/gateway/internal/registry"
	"github.com/dangtuananh123456/gateway/internal/routing"
	"github.com/dangtuananh123456/gateway/pkg/constants"
)

func TestProxyForwardsRequestAndResponseWithoutBufferingBody(t *testing.T) {
	type observedRequest struct {
		method, path, query, body, customHeader, hopHeader string
	}
	observed := make(chan observedRequest, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		body, err := io.ReadAll(request.Body)
		if err != nil {
			t.Errorf("read upstream body: %v", err)
		}
		observed <- observedRequest{
			method:       request.Method,
			path:         request.URL.Path,
			query:        request.URL.RawQuery,
			body:         string(body),
			customHeader: request.Header.Get("X-Client-Header"),
			hopHeader:    request.Header.Get("X-Hop-Only"),
		}
		writer.Header().Set("X-Upstream-Header", "preserved")
		writer.Header().Set("Location", "/contexts/ctx-1")
		writer.WriteHeader(http.StatusCreated)
		_, _ = writer.Write([]byte(`{"handledBy":"pdu-1"}`))
	}))
	t.Cleanup(upstream.Close)

	candidates := registry.New()
	registerProxyBackend(t, candidates, upstream.URL, "pdu-1", 1)
	body := &observedBody{reader: strings.NewReader(`{"supi":"imsi-1"}`)}
	transport := newProxyTestTransport(t)
	checkingTransport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		body.roundTripStarted.Store(true)
		return transport.RoundTrip(request)
	})
	proxy := newProxyForTest(t, candidates, routing.NewRoundRobin(), checkingTransport, time.Second)
	request := httptest.NewRequest(
		http.MethodPost,
		"http://gateway"+constants.CreateSMContextPath+"?trace=abc",
		body,
	)
	request.Header.Set("Content-Type", constants.ContentTypeJSON)
	request.Header.Set("X-Client-Header", "preserved")
	request.Header.Set("Connection", "X-Hop-Only")
	request.Header.Set("X-Hop-Only", "removed")
	response := httptest.NewRecorder()

	proxy.ServeHTTP(response, request)

	if body.readsBeforeRoundTrip.Load() {
		t.Fatal("request body was read before transport RoundTrip")
	}
	gotRequest := <-observed
	if gotRequest.method != http.MethodPost || gotRequest.path != constants.CreateSMContextPath ||
		gotRequest.query != "trace=abc" || gotRequest.body != `{"supi":"imsi-1"}` ||
		gotRequest.customHeader != "preserved" || gotRequest.hopHeader != "" {
		t.Errorf("upstream request = %+v, want original method/path/query/body/header without hop header", gotRequest)
	}
	if response.Code != http.StatusCreated || response.Header().Get("X-Upstream-Header") != "preserved" ||
		response.Header().Get("Location") != "/contexts/ctx-1" ||
		response.Body.String() != `{"handledBy":"pdu-1"}` {
		t.Errorf("proxy response = status %d headers=%v body=%q", response.Code, response.Header(), response.Body.String())
	}
}

func TestProxySelectsBackendFromCurrentSnapshot(t *testing.T) {
	first := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte("pdu-1"))
	}))
	defer first.Close()
	second := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte("pdu-2"))
	}))
	defer second.Close()

	candidates := registry.New()
	registerProxyBackend(t, candidates, second.URL, "pdu-2", 1)
	registerProxyBackend(t, candidates, first.URL, "pdu-1", 2)
	transport := newProxyTestTransport(t)
	proxy := newProxyForTest(t, candidates, routing.NewRoundRobin(), transport, time.Second)

	for index, want := range []string{"pdu-1", "pdu-2"} {
		response := httptest.NewRecorder()
		proxy.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "http://gateway/session", strings.NewReader("body")))
		if response.Code != http.StatusOK || response.Body.String() != want {
			t.Errorf("request %d = status %d body %q, want 200 %q", index+1, response.Code, response.Body.String(), want)
		}
	}
}

func TestProxyReusesTransportAndPassesStreamingBodyDirectly(t *testing.T) {
	candidates := registry.New()
	address := netip.MustParseAddrPort("10.0.0.1:8081")
	registerProxyAddress(t, candidates, address, "pdu-1")
	var calls atomic.Int64
	var expectedBody io.ReadCloser
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		calls.Add(1)
		if request.Body != expectedBody {
			t.Errorf("outbound body = %T %p, want original %T %p", request.Body, request.Body, expectedBody, expectedBody)
		}
		if request.GetBody != nil {
			t.Error("outbound GetBody is non-nil; POST could become replayable")
		}
		body, err := io.ReadAll(request.Body)
		if err != nil {
			return nil, err
		}
		return proxyResponse(http.StatusOK, string(body)), nil
	})
	proxy := newProxyForTest(t, candidates, routing.NewRoundRobin(), transport, time.Second)
	for _, payload := range []string{"first", "second"} {
		body := &observedBody{reader: strings.NewReader(payload)}
		expectedBody = body
		response := httptest.NewRecorder()
		proxy.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "http://gateway/session", body))
		if response.Body.String() != payload {
			t.Errorf("response body = %q, want %q", response.Body.String(), payload)
		}
	}
	if got := calls.Load(); got != 2 {
		t.Errorf("shared transport calls = %d, want 2", got)
	}
}

func TestProxyDoesNotRetryFailedPost(t *testing.T) {
	candidates := registry.New()
	registerProxyAddress(t, candidates, netip.MustParseAddrPort("10.0.0.1:8081"), "pdu-1")
	var calls atomic.Int64
	transport := roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return nil, errors.New("connect failed")
	})
	proxy := newProxyForTest(t, candidates, routing.NewRoundRobin(), transport, time.Second)
	response := httptest.NewRecorder()
	proxy.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "http://gateway/session", strings.NewReader("body")))
	if got := calls.Load(); got != 1 {
		t.Errorf("RoundTrip calls = %d, want exactly 1", got)
	}
	if response.Code != http.StatusBadGateway {
		t.Errorf("status = %d, want %d", response.Code, http.StatusBadGateway)
	}
}

func TestProxyPropagatesClientCancellationToUpstream(t *testing.T) {
	started := make(chan struct{})
	canceled := make(chan struct{})
	release := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
		_, _ = io.Copy(io.Discard, request.Body)
		close(started)
		select {
		case <-request.Context().Done():
			close(canceled)
		case <-release:
		}
	}))
	t.Cleanup(upstream.Close)
	t.Cleanup(func() { close(release) })

	candidates := registry.New()
	registerProxyBackend(t, candidates, upstream.URL, "pdu-1", 1)
	transport := newProxyTestTransport(t)
	proxy := newProxyForTest(t, candidates, routing.NewRoundRobin(), transport, time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	request := httptest.NewRequest(http.MethodPost, "http://gateway/session", strings.NewReader("body")).WithContext(ctx)
	done := make(chan struct{})
	go func() {
		proxy.ServeHTTP(httptest.NewRecorder(), request)
		close(done)
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("upstream did not receive request")
	}
	cancel()
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("upstream context was not canceled")
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("proxy did not return after client cancellation")
	}
}

func TestBoundedUpstreamContextReusesEarlierCallerDeadline(t *testing.T) {
	parent, stopParent := context.WithTimeout(context.Background(), time.Second)
	defer stopParent()

	bounded, cancel := boundedUpstreamContext(parent, 2*time.Second)
	cancel()
	if bounded != parent {
		t.Fatal("bounded context did not reuse the earlier caller deadline")
	}
	if parent.Err() != nil {
		t.Fatalf("no-op child cancellation canceled parent: %v", parent.Err())
	}

	bounded, cancel = boundedUpstreamContext(context.Background(), time.Second)
	defer cancel()
	if _, found := bounded.Deadline(); !found {
		t.Fatal("bounded context has no deadline when caller supplied none")
	}
}

func TestNewProxyValidatesDependencies(t *testing.T) {
	candidates := registry.New()
	selector := routing.NewRoundRobin()
	transport := roundTripFunc(func(*http.Request) (*http.Response, error) {
		return proxyResponse(http.StatusOK, ""), nil
	})
	tests := []struct {
		name      string
		snapshots SnapshotProvider
		selector  routing.Selector
		transport http.RoundTripper
		timeout   time.Duration
	}{
		{name: "snapshots", selector: selector, transport: transport, timeout: time.Second},
		{name: "selector", snapshots: candidates, transport: transport, timeout: time.Second},
		{name: "transport", snapshots: candidates, selector: selector, timeout: time.Second},
		{name: "timeout", snapshots: candidates, selector: selector, transport: transport},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := NewProxy(test.snapshots, test.selector, test.transport, test.timeout); err == nil {
				t.Fatal("NewProxy() error = nil, want validation error")
			}
		})
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

type observedBody struct {
	reader               io.Reader
	roundTripStarted     atomic.Bool
	readsBeforeRoundTrip atomic.Bool
}

func (body *observedBody) Read(buffer []byte) (int, error) {
	if !body.roundTripStarted.Load() {
		body.readsBeforeRoundTrip.Store(true)
	}
	return body.reader.Read(buffer)
}

func (*observedBody) Close() error { return nil }

func newProxyForTest(
	t *testing.T,
	snapshots SnapshotProvider,
	selector routing.Selector,
	transport http.RoundTripper,
	timeout time.Duration,
) *Proxy {
	t.Helper()
	proxy, err := NewProxy(snapshots, selector, transport, timeout)
	if err != nil {
		t.Fatalf("NewProxy() error = %v", err)
	}
	return proxy
}

func newProxyTestTransport(t *testing.T) *http.Transport {
	t.Helper()
	transport := &http.Transport{DisableCompression: true}
	t.Cleanup(transport.CloseIdleConnections)
	return transport
}

func registerProxyBackend(
	t *testing.T,
	candidates *registry.Registry,
	serverURL string,
	instanceID string,
	sequence int,
) {
	t.Helper()
	address := netip.MustParseAddrPort(strings.TrimPrefix(serverURL, "http://"))
	registerProxyAddressAt(t, candidates, address, instanceID, routingTestTimestamp().Add(time.Duration(sequence)*time.Second))
}

func registerProxyAddress(
	t *testing.T,
	candidates *registry.Registry,
	address netip.AddrPort,
	instanceID string,
) {
	t.Helper()
	registerProxyAddressAt(t, candidates, address, instanceID, routingTestTimestamp().Add(time.Second))
}

func registerProxyAddressAt(
	t *testing.T,
	candidates *registry.Registry,
	address netip.AddrPort,
	instanceID string,
	observedAt time.Time,
) {
	t.Helper()
	if _, err := candidates.Upsert(address, routingTestTimestamp()); err != nil {
		t.Fatalf("Upsert(%s) error = %v", address, err)
	}
	if err := candidates.MarkHealthy(address, registry.Metadata{
		InstanceID: instanceID, Weight: 1,
	}, observedAt); err != nil {
		t.Fatalf("MarkHealthy(%s) error = %v", address, err)
	}
}

func proxyResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func routingTestTimestamp() time.Time {
	return time.Date(2026, time.August, 3, 0, 0, 0, 0, time.UTC)
}
