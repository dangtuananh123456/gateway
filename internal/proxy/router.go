package proxy

import (
	"errors"
	"net/http"
	"net/http/httputil"
)

// Router matches inbound paths and dispatches requests to prebuilt reverse proxies.
type Router struct {
	table   *Table
	proxies map[string]*httputil.ReverseProxy
}

// NewRouter creates one reverse proxy per route, all sharing the same transport.
func NewRouter(table *Table, transport http.RoundTripper) (*Router, error) {
	if table == nil {
		return nil, errors.New("create proxy router: route table must not be nil")
	}
	if transport == nil {
		return nil, errors.New("create proxy router: transport must not be nil")
	}

	proxies := make(map[string]*httputil.ReverseProxy, len(table.routes))
	for _, route := range table.routes {
		proxies[route.prefix] = &httputil.ReverseProxy{
			Rewrite:   NewRewrite(route),
			Transport: transport,
		}
	}

	return &Router{
		table:   table,
		proxies: proxies,
	}, nil
}

// ServeHTTP forwards a request through its most specific matching route.
func (router *Router) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	route, ok := router.table.Match(request.URL.Path)
	if !ok {
		http.NotFound(writer, request)
		return
	}

	router.proxies[route.prefix].ServeHTTP(writer, request)
}
