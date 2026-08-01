package proxy

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httputil"
	"time"

	"github.com/dangtuananh123456/gateway/pkg/httperror"
)

// Router matches inbound paths and dispatches requests to prebuilt reverse proxies.
type Router struct {
	table           *Table
	proxies         map[string]*httputil.ReverseProxy
	upstreamTimeout time.Duration
}

// NewRouter creates one reverse proxy per route, all sharing the same transport.
func NewRouter(
	table *Table,
	transport http.RoundTripper,
	upstreamTimeout time.Duration,
) (*Router, error) {
	if table == nil {
		return nil, errors.New("create proxy router: route table must not be nil")
	}
	if transport == nil {
		return nil, errors.New("create proxy router: transport must not be nil")
	}
	if upstreamTimeout <= 0 {
		return nil, errors.New("create proxy router: upstream timeout must be greater than zero")
	}

	proxies := make(map[string]*httputil.ReverseProxy, len(table.routes))
	for _, route := range table.routes {
		proxies[route.prefix] = &httputil.ReverseProxy{
			Rewrite:      NewRewrite(route),
			Transport:    transport,
			ErrorHandler: handleProxyError,
		}
	}

	return &Router{
		table:           table,
		proxies:         proxies,
		upstreamTimeout: upstreamTimeout,
	}, nil
}

// ServeHTTP forwards a request through its most specific matching route.
func (router *Router) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	route, ok := router.table.Match(request.URL.Path)
	if !ok {
		httperror.Write(
			writer,
			http.StatusNotFound,
			httperror.CodeRouteNotFound,
			"route not found",
			"",
		)
		return
	}

	ctx, cancel := context.WithTimeout(request.Context(), router.upstreamTimeout)
	defer cancel()

	router.proxies[route.prefix].ServeHTTP(writer, request.WithContext(ctx))
}

func handleProxyError(
	writer http.ResponseWriter,
	request *http.Request,
	err error,
) {
	if errors.Is(request.Context().Err(), context.DeadlineExceeded) ||
		isTimeout(err) {
		httperror.Write(
			writer,
			http.StatusGatewayTimeout,
			httperror.CodeUpstreamTimeout,
			"upstream request timed out",
			"",
		)
		return
	}
	if errors.Is(err, context.Canceled) ||
		errors.Is(request.Context().Err(), context.Canceled) {
		return
	}

	httperror.Write(
		writer,
		http.StatusBadGateway,
		httperror.CodeBadGateway,
		"upstream service unavailable",
		"",
	)
}

func isTimeout(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}

	var networkError net.Error
	return errors.As(err, &networkError) && networkError.Timeout()
}
