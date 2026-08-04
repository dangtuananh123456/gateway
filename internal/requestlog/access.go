// Package requestlog provides structured HTTP access logging shared by all services.
package requestlog

import (
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"net/http"
	"strings"
	"sync/atomic"
	"time"
)

// RequestIDHeader carries one correlation ID through the client, Gateway, and PDU services.
const RequestIDHeader = "X-Request-ID"

var fallbackSequence atomic.Uint64

// New wraps next with one structured completion log for every HTTP request.
func New(logger *slog.Logger, service string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		startedAt := time.Now()
		requestID := ensureRequestID(request)
		response := &responseRecorder{ResponseWriter: writer}
		completed := false
		defer func() {
			status := response.status
			if !completed {
				status = http.StatusInternalServerError
			} else if status == 0 {
				status = http.StatusOK
			}
			level := slog.LevelInfo
			if status >= http.StatusInternalServerError {
				level = slog.LevelError
			} else if status >= http.StatusBadRequest || request.Context().Err() != nil {
				level = slog.LevelWarn
			}
			logger.Log(
				request.Context(),
				level,
				"HTTP request completed",
				"service", service,
				"request_id", requestID,
				"method", request.Method,
				"path", request.URL.Path,
				"status", status,
				"response_bytes", response.bytes,
				"duration_ms", float64(time.Since(startedAt).Microseconds())/1000,
				"protocol", request.Proto,
				"remote_address", request.RemoteAddr,
			)
		}()

		next.ServeHTTP(response, request)
		completed = true
	})
}

func ensureRequestID(request *http.Request) string {
	if requestID := strings.TrimSpace(request.Header.Get(RequestIDHeader)); requestID != "" {
		return requestID
	}

	var raw [16]byte
	if _, err := rand.Read(raw[:]); err == nil {
		requestID := hex.EncodeToString(raw[:])
		request.Header.Set(RequestIDHeader, requestID)
		return requestID
	}

	requestID := "fallback-" + time.Now().UTC().Format("20060102T150405.000000000") + "-" +
		stringID(fallbackSequence.Add(1))
	request.Header.Set(RequestIDHeader, requestID)
	return requestID
}

func stringID(value uint64) string {
	const digits = "0123456789abcdef"
	var encoded [16]byte
	for index := len(encoded) - 1; index >= 0; index-- {
		encoded[index] = digits[value&0x0f]
		value >>= 4
	}
	return string(encoded[:])
}

type responseRecorder struct {
	http.ResponseWriter
	status int
	bytes  int64
}

func (response *responseRecorder) WriteHeader(status int) {
	if response.status != 0 {
		return
	}
	response.status = status
	response.ResponseWriter.WriteHeader(status)
}

func (response *responseRecorder) Write(body []byte) (int, error) {
	if response.status == 0 {
		response.WriteHeader(http.StatusOK)
	}
	written, err := response.ResponseWriter.Write(body)
	response.bytes += int64(written)
	return written, err
}

// Unwrap lets net/http.ResponseController reach optional interfaces on the original writer.
func (response *responseRecorder) Unwrap() http.ResponseWriter {
	return response.ResponseWriter
}
