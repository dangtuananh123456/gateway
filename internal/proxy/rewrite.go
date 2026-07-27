package proxy

import (
	"net/http/httputil"
	"net/url"
	"strings"
)

// NewRewrite creates a request rewrite function for an immutable route.
func NewRewrite(route Route) func(*httputil.ProxyRequest) {
	target := route.Upstream()
	stripPrefix := route.StripPrefix()

	return func(request *httputil.ProxyRequest) {
		rewritePath(request.Out.URL, stripPrefix)
		request.SetURL(&target)

		clearUntrustedForwardingHeaders(request)
		request.SetXForwarded()
	}
}

func rewritePath(requestURL *url.URL, stripPrefix string) {
	originalPath := requestURL.Path
	originalRawPath := requestURL.RawPath

	requestURL.Path = stripPathPrefix(originalPath, stripPrefix)
	requestURL.RawPath = ""

	if originalRawPath == "" || stripPrefix == "" {
		if stripPrefix == "" {
			requestURL.RawPath = originalRawPath
		}
		return
	}

	escapedPrefix := (&url.URL{Path: stripPrefix}).EscapedPath()
	rawPath := stripPathPrefix(originalRawPath, escapedPrefix)
	decodedRawPath, err := url.PathUnescape(rawPath)
	if err == nil && decodedRawPath == requestURL.Path {
		requestURL.RawPath = rawPath
	}
}

func stripPathPrefix(requestPath string, prefix string) string {
	if requestPath == "" {
		return "/"
	}
	if prefix == "" {
		return requestPath
	}
	if requestPath == prefix {
		return "/"
	}
	if strings.HasPrefix(requestPath, prefix+"/") {
		return requestPath[len(prefix):]
	}

	return requestPath
}

func clearUntrustedForwardingHeaders(request *httputil.ProxyRequest) {
	for _, header := range []string{
		"Forwarded",
		"X-Forwarded",
		"X-Forwarded-For",
		"X-Forwarded-Host",
		"X-Forwarded-Proto",
		"X-Real-Ip",
	} {
		request.Out.Header.Del(header)
	}
}
