package logging

import (
	"bytes"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"
)

func TestHandlerFormatsCompactStructuredLine(t *testing.T) {
	var output bytes.Buffer
	handler := &Handler{output: &sharedOutput{writer: &output}}
	record := slog.NewRecord(
		time.Date(2026, time.August, 4, 7, 6, 19, 542_000_000, time.UTC),
		slog.LevelInfo,
		"HTTP request completed",
		0,
	)
	record.AddAttrs(
		slog.String("service", "gateway"),
		slog.String("method", "GET"),
		slog.Int("status", 200),
		slog.Float64("duration_ms", 2.125),
		slog.String("path", "/api/v1/alert"),
	)
	if err := handler.Handle(t.Context(), record); err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	want := "[2026-08-04 07:06:19.542] INF HTTP request completed service=gateway method=GET status=200 duration_ms=2.125 path=/api/v1/alert\n"
	if output.String() != want {
		t.Errorf("output = %q, want %q", output.String(), want)
	}
}

func TestHandlerQuotesErrorsAndSupportsGroups(t *testing.T) {
	var output bytes.Buffer
	logger := slog.New(&Handler{output: &sharedOutput{writer: &output}}).
		With("component", "discovery").WithGroup("upstream")
	logger.Error("fetch error", "error", errors.New("unexpected status 404: page not found"))
	line := output.String()
	for _, fragment := range []string{
		"] ERR fetch error", "upstream.component=discovery",
		`upstream.error="unexpected status 404: page not found"`,
	} {
		if !strings.Contains(line, fragment) {
			t.Errorf("output %q does not contain %q", line, fragment)
		}
	}
}

func TestHandlerFormatsNamedStringWithoutDoubleQuotes(t *testing.T) {
	type routingMode string
	var output bytes.Buffer
	slog.New(&Handler{output: &sharedOutput{writer: &output}}).
		Info("gateway starting", "routing_mode", routingMode("round_robin"))
	if !strings.Contains(output.String(), " routing_mode=round_robin\n") {
		t.Errorf("output = %q, want plain named string", output.String())
	}
}

func TestNewColorsLevelsMethodsStatusAndFields(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	var output bytes.Buffer
	New(&output).Info(
		"HTTP request completed",
		"method", "DELETE",
		"status", 503,
		"duration_ms", 1200.5,
		"path", "/session",
		"request_id", "request-1",
	)
	line := output.String()
	for _, sequence := range []string{
		ansiGreen + "HTTP request completed" + ansiReset,
		ansiRed + "DELETE" + ansiReset,
		ansiBrightRed + "503" + ansiReset,
		ansiBrightRed + "1200.5" + ansiReset,
		ansiCyan + "/session" + ansiReset,
		ansiMagenta + "request-1" + ansiReset,
	} {
		if !strings.Contains(line, sequence) {
			t.Errorf("colored output %q does not contain %q", line, sequence)
		}
	}
}

func TestNewHonorsNoColor(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	var output bytes.Buffer
	New(&output).Error("failed", "status", 500)
	if strings.Contains(output.String(), "\x1b[") {
		t.Errorf("NO_COLOR output contains ANSI escape: %q", output.String())
	}
}
