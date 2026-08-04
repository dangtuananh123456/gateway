package client

import (
	"bytes"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/dangtuananh123456/gateway/internal/requestlog"
	"github.com/dangtuananh123456/gateway/pkg/constants"
)

//go:embed static/*
var staticAssets embed.FS

// HTTPDoer is implemented by http.Client and test doubles.
type HTTPDoer interface {
	Do(*http.Request) (*http.Response, error)
}

// Handler serves the UI and forwards controlled requests to one fixed Gateway.
type Handler struct {
	gatewayURL *url.URL
	client     HTTPDoer
	static     http.Handler
}

type executeRequest struct {
	Method  string            `json:"method"`
	Path    string            `json:"path"`
	Headers map[string]string `json:"headers,omitempty"`
	Body    string            `json:"body,omitempty"`
}

type executeResponse struct {
	StatusCode int         `json:"statusCode"`
	Status     string      `json:"status"`
	Protocol   string      `json:"protocol"`
	Headers    http.Header `json:"headers"`
	Body       string      `json:"body"`
	LatencyMS  float64     `json:"latencyMs"`
}

type clientError struct {
	Error string `json:"error"`
}

// NewHandler creates the same-origin browser UI and h2c request bridge.
func NewHandler(gatewayURL string, HTTPClient HTTPDoer) (*Handler, error) {
	if HTTPClient == nil {
		return nil, errors.New("create test client handler: HTTP client must not be nil")
	}
	parsedGateway, err := url.Parse(gatewayURL)
	if err != nil || parsedGateway.Scheme != "http" || parsedGateway.Host == "" ||
		parsedGateway.User != nil || parsedGateway.RawQuery != "" || parsedGateway.Fragment != "" ||
		(parsedGateway.Path != "" && parsedGateway.Path != "/") {
		return nil, errors.New("create test client handler: Gateway URL must be an absolute http origin")
	}
	assets, err := fs.Sub(staticAssets, "static")
	if err != nil {
		return nil, fmt.Errorf("create test client handler: load static assets: %w", err)
	}
	return &Handler{
		gatewayURL: parsedGateway,
		client:     HTTPClient,
		static:     http.FileServer(http.FS(assets)),
	}, nil
}

// ServeHTTP exposes the UI, target configuration, and generic request bridge.
func (handler *Handler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	setSecurityHeaders(writer.Header())
	switch request.URL.Path {
	case "/api/config":
		if request.Method != http.MethodGet {
			methodNotAllowed(writer, http.MethodGet)
			return
		}
		writeJSON(writer, http.StatusOK, map[string]string{"gatewayUrl": handler.gatewayURL.String()})
	case "/api/request":
		if request.Method != http.MethodPost {
			methodNotAllowed(writer, http.MethodPost)
			return
		}
		handler.execute(writer, request)
	default:
		if request.Method != http.MethodGet && request.Method != http.MethodHead {
			methodNotAllowed(writer, http.MethodGet)
			return
		}
		handler.static.ServeHTTP(writer, request)
	}
}

func (handler *Handler) execute(writer http.ResponseWriter, request *http.Request) {
	request.Body = http.MaxBytesReader(writer, request.Body, constants.ClientMaxRequestBodyBytes)
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	var input executeRequest
	if err := decoder.Decode(&input); err != nil {
		writeJSON(writer, http.StatusBadRequest, clientError{Error: "request must be one valid JSON object"})
		return
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writeJSON(writer, http.StatusBadRequest, clientError{Error: "request must contain exactly one JSON object"})
		return
	}
	method, err := validateMethod(input.Method)
	if err != nil {
		writeJSON(writer, http.StatusBadRequest, clientError{Error: err.Error()})
		return
	}
	path, err := validatePath(input.Path)
	if err != nil {
		writeJSON(writer, http.StatusBadRequest, clientError{Error: err.Error()})
		return
	}
	if err := validateHeaders(input.Headers); err != nil {
		writeJSON(writer, http.StatusBadRequest, clientError{Error: err.Error()})
		return
	}

	target := *handler.gatewayURL
	target.Path = path.Path
	target.RawPath = path.RawPath
	target.RawQuery = path.RawQuery
	var body io.Reader
	if input.Body != "" {
		body = bytes.NewBufferString(input.Body)
	}
	upstreamRequest, err := http.NewRequestWithContext(request.Context(), method, target.String(), body)
	if err != nil {
		writeJSON(writer, http.StatusBadRequest, clientError{Error: err.Error()})
		return
	}
	for name, value := range input.Headers {
		upstreamRequest.Header.Set(name, value)
	}
	if upstreamRequest.Header.Get(requestlog.RequestIDHeader) == "" {
		upstreamRequest.Header.Set(
			requestlog.RequestIDHeader,
			request.Header.Get(requestlog.RequestIDHeader),
		)
	}
	if input.Body != "" && upstreamRequest.Header.Get("Content-Type") == "" {
		upstreamRequest.Header.Set("Content-Type", constants.ContentTypeJSON)
	}

	startedAt := time.Now()
	response, err := handler.client.Do(upstreamRequest)
	latency := time.Since(startedAt)
	if err != nil {
		if request.Context().Err() != nil {
			return
		}
		writeJSON(writer, http.StatusBadGateway, clientError{Error: fmt.Sprintf("Gateway request failed: %v", err)})
		return
	}
	defer response.Body.Close()
	responseBody, err := io.ReadAll(io.LimitReader(response.Body, constants.ClientMaxResponseBodyBytes+1))
	if err != nil {
		writeJSON(writer, http.StatusBadGateway, clientError{Error: fmt.Sprintf("read Gateway response: %v", err)})
		return
	}
	if int64(len(responseBody)) > constants.ClientMaxResponseBodyBytes {
		writeJSON(writer, http.StatusBadGateway, clientError{Error: "Gateway response exceeds UI limit"})
		return
	}
	writeJSON(writer, http.StatusOK, executeResponse{
		StatusCode: response.StatusCode,
		Status:     response.Status,
		Protocol:   response.Proto,
		Headers:    response.Header,
		Body:       string(responseBody),
		LatencyMS:  float64(latency.Microseconds()) / 1000,
	})
}

func validateMethod(method string) (string, error) {
	method = strings.ToUpper(strings.TrimSpace(method))
	switch method {
	case http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch,
		http.MethodDelete, http.MethodOptions:
		return method, nil
	default:
		return "", fmt.Errorf("unsupported HTTP method %q", method)
	}
}

func validatePath(rawPath string) (*url.URL, error) {
	if rawPath == "" || !strings.HasPrefix(rawPath, "/") || strings.HasPrefix(rawPath, "//") {
		return nil, errors.New("path must start with one /")
	}
	path, err := url.ParseRequestURI(rawPath)
	if err != nil || path.IsAbs() || path.Host != "" {
		return nil, errors.New("path must be a valid relative request URI")
	}
	return path, nil
}

func validateHeaders(headers map[string]string) error {
	for name, value := range headers {
		canonicalName := http.CanonicalHeaderKey(strings.TrimSpace(name))
		if canonicalName == "" {
			return errors.New("header name must not be empty")
		}
		if strings.ContainsAny(value, "\r\n") {
			return fmt.Errorf("header %q contains a line break", canonicalName)
		}
		switch canonicalName {
		case "Connection", "Content-Length", "Host", "Proxy-Authorization",
			"Te", "Trailer", "Transfer-Encoding", "Upgrade":
			return fmt.Errorf("header %q is managed by the h2c bridge", canonicalName)
		}
	}
	return nil
}

func setSecurityHeaders(header http.Header) {
	header.Set("Content-Security-Policy", "default-src 'self'; style-src 'self' 'unsafe-inline'; script-src 'self'; connect-src 'self'; img-src 'self' data:")
	header.Set("Referrer-Policy", "no-referrer")
	header.Set("X-Content-Type-Options", "nosniff")
	header.Set("X-Frame-Options", "DENY")
}

func methodNotAllowed(writer http.ResponseWriter, allowed string) {
	writer.Header().Set("Allow", allowed)
	writeJSON(writer, http.StatusMethodNotAllowed, clientError{Error: "method not allowed"})
}

func writeJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", constants.ContentTypeJSON)
	writer.Header().Set("Cache-Control", "no-store")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}
