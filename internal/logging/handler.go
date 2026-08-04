// Package logging provides the shared human-readable service logger.
package logging

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	timestampLayout = "2006-01-02 15:04:05.000"
	ansiReset       = "\x1b[0m"
	ansiBold        = "\x1b[1m"
	ansiDim         = "\x1b[2m"
	ansiRed         = "\x1b[31m"
	ansiGreen       = "\x1b[32m"
	ansiYellow      = "\x1b[33m"
	ansiBlue        = "\x1b[34m"
	ansiMagenta     = "\x1b[35m"
	ansiCyan        = "\x1b[36m"
	ansiBrightRed   = "\x1b[91m"
	ansiBrightCyan  = "\x1b[96m"
	ansiGray        = "\x1b[90m"
)

// New creates a concurrency-safe logger with compact container-friendly output.
func New(writer io.Writer) *slog.Logger {
	return slog.New(&Handler{
		output: &sharedOutput{writer: writer},
		color:  os.Getenv("NO_COLOR") == "",
	})
}

type sharedOutput struct {
	mu     sync.Mutex
	writer io.Writer
}

// Handler renders one slog record as a single compact text line.
type Handler struct {
	output *sharedOutput
	attrs  []slog.Attr
	groups []string
	color  bool
}

func (handler *Handler) Enabled(context.Context, slog.Level) bool {
	return true
}

func (handler *Handler) Handle(_ context.Context, record slog.Record) error {
	var line strings.Builder
	line.WriteString(style(handler.color, ansiGray, "["+record.Time.Format(timestampLayout)+"]"))
	line.WriteByte(' ')
	levelColor := colorForLevel(record.Level)
	line.WriteString(style(handler.color, ansiBold+levelColor, shortLevel(record.Level)))
	if record.Message != "" {
		line.WriteByte(' ')
		line.WriteString(style(handler.color, levelColor, record.Message))
	}
	for _, attribute := range handler.attrs {
		appendAttribute(&line, handler.groups, attribute, handler.color)
	}
	record.Attrs(func(attribute slog.Attr) bool {
		appendAttribute(&line, handler.groups, attribute, handler.color)
		return true
	})
	line.WriteByte('\n')

	handler.output.mu.Lock()
	defer handler.output.mu.Unlock()
	_, err := io.WriteString(handler.output.writer, line.String())
	return err
}

func (handler *Handler) WithAttrs(attributes []slog.Attr) slog.Handler {
	clone := handler.clone()
	clone.attrs = append(clone.attrs, attributes...)
	return clone
}

func (handler *Handler) WithGroup(name string) slog.Handler {
	clone := handler.clone()
	if name != "" {
		clone.groups = append(clone.groups, name)
	}
	return clone
}

func (handler *Handler) clone() *Handler {
	return &Handler{
		output: handler.output,
		attrs:  append([]slog.Attr(nil), handler.attrs...),
		groups: append([]string(nil), handler.groups...),
		color:  handler.color,
	}
}

func appendAttribute(line *strings.Builder, groups []string, attribute slog.Attr, color bool) {
	attribute.Value = attribute.Value.Resolve()
	if attribute.Equal(slog.Attr{}) {
		return
	}
	if attribute.Value.Kind() == slog.KindGroup {
		nestedGroups := groups
		if attribute.Key != "" {
			nestedGroups = append(append([]string(nil), groups...), attribute.Key)
		}
		for _, nested := range attribute.Value.Group() {
			appendAttribute(line, nestedGroups, nested, color)
		}
		return
	}

	key := attribute.Key
	if len(groups) > 0 {
		key = strings.Join(append(append([]string(nil), groups...), key), ".")
	}
	line.WriteByte(' ')
	line.WriteString(style(color, ansiGray, key))
	line.WriteByte('=')
	line.WriteString(style(color, colorForAttribute(key, attribute.Value), formatValue(attribute.Value)))
}

func formatValue(value slog.Value) string {
	switch value.Kind() {
	case slog.KindString:
		return quoteWhenNeeded(value.String())
	case slog.KindInt64:
		if value.Any() != nil {
			if duration, ok := value.Any().(time.Duration); ok {
				return duration.String()
			}
		}
		return strconv.FormatInt(value.Int64(), 10)
	case slog.KindUint64:
		return strconv.FormatUint(value.Uint64(), 10)
	case slog.KindFloat64:
		return strconv.FormatFloat(value.Float64(), 'f', -1, 64)
	case slog.KindBool:
		return strconv.FormatBool(value.Bool())
	case slog.KindDuration:
		return value.Duration().String()
	case slog.KindTime:
		return quoteWhenNeeded(value.Time().Format(time.RFC3339Nano))
	case slog.KindAny:
		if err, ok := value.Any().(error); ok {
			return quoteWhenNeeded(err.Error())
		}
		return quoteWhenNeeded(fmt.Sprint(value.Any()))
	default:
		return quoteWhenNeeded(value.String())
	}
}

func quoteWhenNeeded(value string) string {
	if value == "" || strings.ContainsAny(value, " \t\r\n\"=") {
		return strconv.Quote(value)
	}
	return value
}

func colorForLevel(level slog.Level) string {
	switch {
	case level < slog.LevelInfo:
		return ansiYellow
	case level < slog.LevelWarn:
		return ansiGreen
	case level < slog.LevelError:
		return ansiYellow
	default:
		return ansiBrightRed
	}
}

func colorForAttribute(key string, value slog.Value) string {
	leafKey := key
	if separator := strings.LastIndexByte(leafKey, '.'); separator >= 0 {
		leafKey = leafKey[separator+1:]
	}
	switch leafKey {
	case "method":
		return colorForMethod(fmt.Sprint(value.Any()))
	case "status":
		return colorForStatus(value)
	case "duration_ms", "latency_ms", "latency":
		return colorForLatency(value)
	case "path":
		return ansiCyan
	case "request_id":
		return ansiMagenta
	case "protocol":
		return ansiBlue
	case "service", "component", "instance_id", "handled_by":
		return ansiBrightCyan
	case "remote_address", "address", "discovery_hostname":
		return ansiGray
	case "error":
		return ansiBrightRed
	default:
		return ansiReset
	}
}

func colorForMethod(method string) string {
	switch strings.ToUpper(method) {
	case httpMethodGet, httpMethodHead:
		return ansiGreen
	case httpMethodPost:
		return ansiBlue
	case httpMethodPut:
		return ansiYellow
	case httpMethodPatch:
		return ansiMagenta
	case httpMethodDelete:
		return ansiRed
	case httpMethodOptions:
		return ansiCyan
	default:
		return ansiReset
	}
}

const (
	httpMethodGet     = "GET"
	httpMethodHead    = "HEAD"
	httpMethodPost    = "POST"
	httpMethodPut     = "PUT"
	httpMethodPatch   = "PATCH"
	httpMethodDelete  = "DELETE"
	httpMethodOptions = "OPTIONS"
)

func colorForStatus(value slog.Value) string {
	status, err := strconv.Atoi(fmt.Sprint(value.Any()))
	if err != nil {
		return ansiReset
	}
	switch {
	case status >= 500:
		return ansiBrightRed
	case status >= 400:
		return ansiYellow
	case status >= 300:
		return ansiCyan
	case status >= 200:
		return ansiGreen
	default:
		return ansiReset
	}
}

func colorForLatency(value slog.Value) string {
	latency, err := strconv.ParseFloat(fmt.Sprint(value.Any()), 64)
	if err != nil {
		return ansiCyan
	}
	switch {
	case latency >= 1000:
		return ansiBrightRed
	case latency >= 250:
		return ansiYellow
	default:
		return ansiCyan
	}
}

func style(enabled bool, code, value string) string {
	if !enabled || code == "" || code == ansiReset {
		return value
	}
	return code + value + ansiReset
}

func shortLevel(level slog.Level) string {
	switch {
	case level < slog.LevelInfo:
		return "DBG"
	case level < slog.LevelWarn:
		return "INF"
	case level < slog.LevelError:
		return "WRN"
	default:
		return "ERR"
	}
}
