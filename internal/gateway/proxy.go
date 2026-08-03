package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"time"

	"github.com/dangtuananh123456/gateway/internal/model"
	"github.com/dangtuananh123456/gateway/internal/registry"
	"github.com/dangtuananh123456/gateway/internal/routing"
	"github.com/dangtuananh123456/gateway/pkg/constants"
)

// SnapshotProvider publishes the latest immutable healthy backend view.
type SnapshotProvider interface {
	HealthySnapshot() registry.Snapshot
}

// Proxy streams each request to a backend selected from the current snapshot.
type Proxy struct {
	snapshots       SnapshotProvider
	selector        routing.Selector
	transport       http.RoundTripper
	upstreamTimeout time.Duration
}

var proxyCopyBufferPool = sync.Pool{New: func() any {
	buffer := make([]byte, constants.ProxyCopyBufferBytes)
	return &buffer
}}

// NewProxy creates a dynamic reverse proxy with injected shared dependencies.
// The caller owns the transport and must close its idle connections at shutdown.
func NewProxy(
	snapshots SnapshotProvider,
	selector routing.Selector,
	transport http.RoundTripper,
	upstreamTimeout time.Duration,
) (*Proxy, error) {
	if snapshots == nil {
		return nil, errors.New("create Gateway proxy: snapshot provider must not be nil")
	}
	if selector == nil {
		return nil, errors.New("create Gateway proxy: selector must not be nil")
	}
	if transport == nil {
		return nil, errors.New("create Gateway proxy: transport must not be nil")
	}
	if upstreamTimeout <= 0 {
		return nil, errors.New("create Gateway proxy: upstream timeout must be greater than zero")
	}
	return &Proxy{
		snapshots:       snapshots,
		selector:        selector,
		transport:       transport,
		upstreamTimeout: upstreamTimeout,
	}, nil
}

// ServeHTTP selects exactly one backend and performs exactly one RoundTrip.
func (proxy *Proxy) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	instance, err := proxy.selector.Select(proxy.snapshots.HealthySnapshot())
	if err != nil {
		proxy.writeSelectionError(writer, err)
		return
	}

	upstreamCtx, cancel := context.WithTimeout(request.Context(), proxy.upstreamTimeout)
	defer cancel()
	outbound := cloneForUpstream(request, upstreamCtx, instance.Address)
	response, err := proxy.transport.RoundTrip(outbound)
	if err != nil {
		proxy.writeTransportError(writer, request.Context(), upstreamCtx, err)
		return
	}
	defer response.Body.Close()
	proxy.copyResponse(writer, response)
}

func cloneForUpstream(
	request *http.Request,
	ctx context.Context,
	address netip.AddrPort,
) *http.Request {
	outbound := request.Clone(ctx)
	upstreamURL := *request.URL
	upstreamURL.Scheme = "http"
	upstreamURL.Host = address.String()
	outbound.URL = &upstreamURL
	outbound.Host = address.String()
	outbound.RequestURI = ""
	outbound.GetBody = nil
	removeHopByHopHeaders(outbound.Header)
	return outbound
}

func (proxy *Proxy) copyResponse(writer http.ResponseWriter, response *http.Response) {
	removeHopByHopHeaders(response.Header)
	copyHeaders(writer.Header(), response.Header)
	for trailerName := range response.Trailer {
		writer.Header().Add("Trailer", trailerName)
	}
	writer.WriteHeader(response.StatusCode)

	buffer := proxyCopyBufferPool.Get().(*[]byte)
	_, copyErr := io.CopyBuffer(writer, response.Body, *buffer)
	proxyCopyBufferPool.Put(buffer)
	if copyErr != nil {
		return
	}
	for trailerName, values := range response.Trailer {
		writer.Header()[http.TrailerPrefix+trailerName] = values
	}
}

func (proxy *Proxy) writeSelectionError(writer http.ResponseWriter, err error) {
	if errors.Is(err, routing.ErrNoBackend) {
		writeGatewayJSON(writer, http.StatusServiceUnavailable, model.NewErrorResponse(
			constants.CauseNoBackendAvailable,
			"",
		))
		return
	}
	writeGatewayJSON(writer, http.StatusInternalServerError, model.NewErrorResponse(
		constants.CauseInternalError,
		"backend selection failed",
	))
}

func (proxy *Proxy) writeTransportError(
	writer http.ResponseWriter,
	requestCtx context.Context,
	upstreamCtx context.Context,
	transportErr error,
) {
	if requestCtx.Err() != nil {
		return
	}
	if errors.Is(upstreamCtx.Err(), context.DeadlineExceeded) || errors.Is(transportErr, context.DeadlineExceeded) {
		writeGatewayJSON(writer, http.StatusGatewayTimeout, model.NewErrorResponse(
			constants.CauseUpstreamTimeout,
			"upstream request timed out",
		))
		return
	}
	writeGatewayJSON(writer, http.StatusBadGateway, model.NewErrorResponse(
		constants.CauseBadGateway,
		"upstream request failed",
	))
}

func writeGatewayJSON(writer http.ResponseWriter, status int, payload model.ErrorResponse) {
	body, _ := json.Marshal(payload)
	writer.Header().Set("Content-Type", constants.ContentTypeJSON)
	writer.WriteHeader(status)
	_, _ = writer.Write(body)
}

func copyHeaders(destination, source http.Header) {
	for name, values := range source {
		for _, value := range values {
			destination.Add(name, value)
		}
	}
}

func removeHopByHopHeaders(header http.Header) {
	for _, connectionValue := range header.Values("Connection") {
		for token := range strings.SplitSeq(connectionValue, ",") {
			header.Del(strings.TrimSpace(token))
		}
	}
	for _, name := range []string{
		"Connection",
		"Keep-Alive",
		"Proxy-Authenticate",
		"Proxy-Authorization",
		"Te",
		"Trailer",
		"Transfer-Encoding",
		"Upgrade",
	} {
		header.Del(name)
	}
}
