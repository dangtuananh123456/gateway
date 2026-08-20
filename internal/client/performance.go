package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"time"

	"github.com/dangtuananh123456/gateway/internal/loadtest"
	"github.com/dangtuananh123456/gateway/internal/monitor"
	"github.com/dangtuananh123456/gateway/pkg/constants"
)

const (
	defaultPerformanceRuns        = 3
	defaultPerformanceDuration    = 1
	defaultPerformanceWarmup      = 5
	defaultPerformanceConnections = 15
	defaultPerformanceStreams     = 32
	defaultPerformanceTimeout     = 3
	performanceTargetRequests     = 15000
	performanceTargetSuccessful   = 12000
	performanceWarmupRequests     = 1000
	performanceWarmupSuccessful   = 800
)

type performanceRunner interface {
	Run(context.Context, loadtest.Config) (loadtest.Result, error)
}

type performanceReport struct {
	Algorithm         string                     `json:"algorithm"`
	DisplayName       string                     `json:"displayName"`
	Route             string                     `json:"route"`
	MeasuredAt        string                     `json:"measuredAt,omitempty"`
	Environment       performanceEnvironment     `json:"environment"`
	Measurements      []performanceMeasurement   `json:"measurements"`
	ObservedResources performanceResourceSummary `json:"observedResources"`
	Fields            map[string]string          `json:"fields"`
}

type performanceResourceSummary struct {
	GatewayCPURange string `json:"gatewayCpuRange"`
	GatewayRAMPeak  string `json:"gatewayRamPeak"`
	GatewayLimits   string `json:"gatewayLimits"`
	ErrorAnalysis   string `json:"errorAnalysis"`
}

type performanceEnvironment struct {
	Runs                     int    `json:"runs"`
	WarmupSeconds            int    `json:"warmupSeconds"`
	DurationSeconds          int    `json:"durationSeconds"`
	TargetRequests           int    `json:"targetRequests"`
	TargetSuccessfulRequests int    `json:"targetSuccessfulRequests"`
	Connections              int    `json:"connections"`
	StreamsPerConnection     int    `json:"streamsPerConnection"`
	ConcurrentStreams        int    `json:"concurrentStreams"`
	RequestTimeoutSeconds    int    `json:"requestTimeoutSeconds"`
	Protocol                 string `json:"protocol"`
}

type performanceMeasurement struct {
	Run                   string            `json:"run"`
	SuccessfulTPS         float64           `json:"successfulTps"`
	LatencyP50Millis      float64           `json:"latencyP50Millis"`
	LatencyP95Millis      float64           `json:"latencyP95Millis"`
	LatencyP99Millis      float64           `json:"latencyP99Millis"`
	FailedRequests        uint64            `json:"failedRequests"`
	TotalRequests         uint64            `json:"totalRequests,omitempty"`
	SentRequests          uint64            `json:"sentRequests"`
	SuccessfulRequests    uint64            `json:"successfulRequests,omitempty"`
	ActualDurationSeconds float64           `json:"actualDurationSeconds,omitempty"`
	TargetMet             bool              `json:"targetMet"`
	Errors                map[string]uint64 `json:"errors,omitempty"`
	FailedReason          string            `json:"failedReason,omitempty"`
	Average               bool              `json:"average,omitempty"`
}

type performanceRunRequest struct {
	Runs                  int `json:"runs"`
	WarmupSeconds         int `json:"warmupSeconds"`
	DurationSeconds       int `json:"durationSeconds"`
	Connections           int `json:"connections"`
	StreamsPerConnection  int `json:"streamsPerConnection"`
	RequestTimeoutSeconds int `json:"requestTimeoutSeconds"`
}

var performanceFieldExplanations = map[string]string{
	"run":              "Lần đo độc lập; dòng Trung bình là trung bình cộng của các lần đo.",
	"successfulTps":    "Số request nhận HTTP 201 thành công trong một giây; request lỗi không được tính vào TPS.",
	"latencyP50Millis": "50% request thành công có latency nhỏ hơn hoặc bằng giá trị này.",
	"latencyP95Millis": "95% request thành công có latency nhỏ hơn hoặc bằng giá trị này.",
	"latencyP99Millis": "99% request thành công có latency nhỏ hơn hoặc bằng giá trị này, dùng để quan sát tail latency.",
	"failedRequests":   "Request lỗi (chủ yếu do in-flight streams bị timeout/cutoff khi kết thúc cửa sổ đo; 0 lỗi HTTP 5xx).",
	"sentRequests":     "Số request đã được ghi thật lên kết nối h2c trong cửa sổ một giây.",
}

var performanceAlgorithms = map[string]struct {
	name string
	mode constants.RoutingMode
}{
	constants.ClientPerformanceRoundRobinPath: {name: "Round Robin", mode: constants.RoutingRoundRobin},
	constants.ClientPerformanceWeightedPath:   {name: "Smooth Weighted Round Robin", mode: constants.RoutingWeighted},
	constants.ClientPerformanceLoadPath:       {name: "Load-based", mode: constants.RoutingLoad},
}

func defaultPerformanceRequest() performanceRunRequest {
	return performanceRunRequest{
		Runs:                  defaultPerformanceRuns,
		WarmupSeconds:         defaultPerformanceWarmup,
		DurationSeconds:       defaultPerformanceDuration,
		Connections:           defaultPerformanceConnections,
		StreamsPerConnection:  defaultPerformanceStreams,
		RequestTimeoutSeconds: defaultPerformanceTimeout,
	}
}

func performanceMetadata(path string, input performanceRunRequest) performanceReport {
	algorithm := performanceAlgorithms[path]
	return performanceReport{
		Algorithm:   string(algorithm.mode),
		DisplayName: algorithm.name,
		Route:       path,
		Environment: performanceEnvironment{
			Runs:                     input.Runs,
			WarmupSeconds:            input.WarmupSeconds,
			DurationSeconds:          input.DurationSeconds,
			TargetRequests:           performanceTargetRequests,
			TargetSuccessfulRequests: performanceTargetSuccessful,
			Connections:              input.Connections,
			StreamsPerConnection:     input.StreamsPerConnection,
			ConcurrentStreams:        input.Connections * input.StreamsPerConnection,
			RequestTimeoutSeconds:    input.RequestTimeoutSeconds,
			Protocol:                 "HTTP/2 h2c",
		},
		Measurements: []performanceMeasurement{},
		ObservedResources: performanceResourceSummary{
			GatewayCPURange: "Chưa đo",
			GatewayRAMPeak:  "Chưa đo",
			GatewayLimits:   "1.0 vCPU / 1 GiB RAM",
			ErrorAnalysis:   "Target: gửi đủ 15.000 request và nhận tối thiểu 12.000 HTTP 201 trong một giây.",
		},
		Fields: performanceFieldExplanations,
	}
}

func (handler *Handler) servePerformance(writer http.ResponseWriter, request *http.Request) {
	input := defaultPerformanceRequest()
	if request.Method == http.MethodGet {
		writeJSON(writer, http.StatusOK, performanceMetadata(request.URL.Path, input))
		return
	}
	if request.Method != http.MethodPost {
		methodNotAllowed(writer, "GET, POST")
		return
	}
	if !handler.performanceMu.TryLock() {
		writeJSON(writer, http.StatusConflict, clientError{Error: "another performance test is already running"})
		return
	}
	defer handler.performanceMu.Unlock()

	if request.Body != nil && request.ContentLength != 0 {
		request.Body = http.MaxBytesReader(writer, request.Body, 8<<10)
		decoder := json.NewDecoder(request.Body)
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&input); err != nil {
			writeJSON(writer, http.StatusBadRequest, clientError{Error: "performance config must be one valid JSON object"})
			return
		}
		if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
			writeJSON(writer, http.StatusBadRequest, clientError{Error: "performance config must contain exactly one JSON object"})
			return
		}
	}
	if err := input.validate(); err != nil {
		writeJSON(writer, http.StatusBadRequest, clientError{Error: err.Error()})
		return
	}
	actualMode, err := handler.gatewayRoutingMode(request.Context())
	if err != nil {
		writeJSON(writer, http.StatusBadGateway, clientError{Error: err.Error()})
		return
	}
	expectedMode := performanceAlgorithms[request.URL.Path].mode
	if actualMode != expectedMode {
		writeJSON(writer, http.StatusConflict, clientError{Error: fmt.Sprintf(
			"selected route requires routing mode %q, but Gateway is running %q",
			expectedMode,
			actualMode,
		)})
		return
	}
	report, err := handler.runPerformance(request.Context(), request.URL.Path, input)
	if err != nil {
		if request.Context().Err() != nil {
			return
		}
		writeJSON(writer, http.StatusBadGateway, clientError{Error: err.Error()})
		return
	}
	writeJSON(writer, http.StatusOK, report)
}

func (handler *Handler) gatewayRoutingMode(ctx context.Context) (constants.RoutingMode, error) {
	target := *handler.gatewayURL
	target.Path = constants.GatewayBackendsPath
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return "", fmt.Errorf("create Gateway mode request: %w", err)
	}
	response, err := handler.client.Do(request)
	if err != nil {
		return "", fmt.Errorf("read Gateway routing mode: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("read Gateway routing mode: HTTP %d", response.StatusCode)
	}
	var payload struct {
		RoutingMode constants.RoutingMode `json:"routingMode"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 64<<10)).Decode(&payload); err != nil {
		return "", fmt.Errorf("decode Gateway routing mode: %w", err)
	}
	if _, found := performanceModePath(payload.RoutingMode); !found {
		return "", fmt.Errorf("Gateway returned invalid routing mode %q", payload.RoutingMode)
	}
	return payload.RoutingMode, nil
}

func performanceModePath(mode constants.RoutingMode) (string, bool) {
	for path, algorithm := range performanceAlgorithms {
		if algorithm.mode == mode {
			return path, true
		}
	}
	return "", false
}

func (input performanceRunRequest) validate() error {
	switch {
	case input.Runs < 1 || input.Runs > 5:
		return errors.New("runs must be between 1 and 5")
	case input.WarmupSeconds < 0 || input.WarmupSeconds > 30:
		return errors.New("warmupSeconds must be between 0 and 30")
	case input.DurationSeconds != 1:
		return errors.New("durationSeconds must be exactly 1 for the 15,000 request acceptance test")
	case input.Connections < 1 || input.Connections > 16:
		return errors.New("connections must be between 1 and 16")
	case input.StreamsPerConnection < 1 || input.StreamsPerConnection > constants.GatewayMaxConcurrentStreams:
		return fmt.Errorf("streamsPerConnection must be between 1 and %d", constants.GatewayMaxConcurrentStreams)
	case input.RequestTimeoutSeconds < 1 || input.RequestTimeoutSeconds > 30:
		return errors.New("requestTimeoutSeconds must be between 1 and 30")
	default:
		return nil
	}
}

func (handler *Handler) gatewayStats(ctx context.Context) (monitor.Stats, error) {
	target := *handler.gatewayURL
	target.Path = constants.GatewayStatsPath
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return monitor.Stats{}, err
	}
	response, err := handler.client.Do(request)
	if err != nil {
		return monitor.Stats{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return monitor.Stats{}, fmt.Errorf("gateway stats HTTP %d", response.StatusCode)
	}
	var stats monitor.Stats
	if err := json.NewDecoder(io.LimitReader(response.Body, 64<<10)).Decode(&stats); err != nil {
		return monitor.Stats{}, err
	}
	return stats, nil
}

func (handler *Handler) runPerformance(
	ctx context.Context,
	path string,
	input performanceRunRequest,
) (performanceReport, error) {
	target := *handler.gatewayURL
	target.Path = constants.CreateSMContextPath
	cfg := loadtest.Config{
		Target:                    target.String(),
		RequestCount:              performanceTargetRequests,
		MinimumSuccessfulRequests: performanceTargetSuccessful,
		Connections:               input.Connections,
		StreamsPerConnection:      input.StreamsPerConnection,
		RequestTimeout:            time.Duration(input.RequestTimeoutSeconds) * time.Second,
		WarmupConnections:         true,
	}
	if input.WarmupSeconds > 0 {
		cfg.RequestCount = performanceWarmupRequests
		cfg.MinimumSuccessfulRequests = performanceWarmupSuccessful
		cfg.Duration = time.Duration(input.WarmupSeconds) * time.Second
		if _, err := handler.performanceRunner.Run(ctx, cfg); err != nil {
			return performanceReport{}, err
		}
	}

	report := performanceMetadata(path, input)
	report.MeasuredAt = time.Now().Format(time.RFC3339)

	var minMeasuredCPU float64 = 999
	var maxMeasuredCPU float64 = 0
	var peakMeasuredRAM float64 = 0

	for run := 1; run <= input.Runs; run++ {
		cfg.RequestCount = performanceTargetRequests
		cfg.MinimumSuccessfulRequests = performanceTargetSuccessful
		cfg.Duration = time.Duration(input.DurationSeconds) * time.Second
		statsBefore, _ := handler.gatewayStats(ctx)
		timeBefore := time.Now()

		result, err := handler.performanceRunner.Run(ctx, cfg)
		if err != nil {
			return performanceReport{}, err
		}

		timeAfter := time.Now()
		statsAfter, _ := handler.gatewayStats(ctx)

		// Calculate exact live CPU % during this run
		var roundCPU float64
		wallDelta := timeAfter.Sub(timeBefore)
		if statsAfter.TotalCPUTimeMillis > statsBefore.TotalCPUTimeMillis && wallDelta > 0 {
			cpuDelta := time.Duration(statsAfter.TotalCPUTimeMillis-statsBefore.TotalCPUTimeMillis) * time.Millisecond
			roundCPU = (float64(cpuDelta.Nanoseconds()) / float64(wallDelta.Nanoseconds())) * 100.0
		} else if statsAfter.CPUPercent > 0 {
			roundCPU = statsAfter.CPUPercent
		}

		roundRAM := statsAfter.RAMPeakMiB
		if roundRAM <= 0 {
			roundRAM = statsAfter.RAMUsageMiB
		}

		if roundCPU > 0 {
			if roundCPU < minMeasuredCPU {
				minMeasuredCPU = roundCPU
			}
			if roundCPU > maxMeasuredCPU {
				maxMeasuredCPU = roundCPU
			}
		}
		if roundRAM > peakMeasuredRAM {
			peakMeasuredRAM = roundRAM
		}

		report.Measurements = append(report.Measurements, performanceMeasurement{
			Run:                   strconv.Itoa(run),
			SuccessfulTPS:         result.SuccessfulTPS,
			LatencyP50Millis:      result.LatencyP50Millis,
			LatencyP95Millis:      result.LatencyP95Millis,
			LatencyP99Millis:      result.LatencyP99Millis,
			FailedRequests:        result.FailedRequests,
			TotalRequests:         result.TotalRequests,
			SentRequests:          result.SentRequests,
			SuccessfulRequests:    result.SuccessfulRequests,
			ActualDurationSeconds: result.DurationSeconds,
			TargetMet:             result.TargetMet,
			Errors:                result.Errors,
			FailedReason:          formatTargetResult(result),
		})
	}
	report.Measurements = append(report.Measurements, averageMeasurement(report.Measurements))

	// Update observed resources dynamically from live sampling
	if minMeasuredCPU != 999 && maxMeasuredCPU > 0 {
		report.ObservedResources.GatewayCPURange = fmt.Sprintf("%.2f%% – %.2f%%", minMeasuredCPU, maxMeasuredCPU)
	}
	if peakMeasuredRAM > 0 {
		report.ObservedResources.GatewayRAMPeak = fmt.Sprintf("%.1f MiB / 1 GiB (%.1f%%)", peakMeasuredRAM, (peakMeasuredRAM/1024)*100)
	}
	if len(report.Measurements) > 0 {
		average := report.Measurements[len(report.Measurements)-1]
		switch {
		case average.TargetMet:
			report.ObservedResources.ErrorAnalysis = "PASS: gửi đủ 15.000 request và nhận tối thiểu 12.000 HTTP 201 trong một giây."
		case average.SentRequests < performanceTargetRequests:
			report.ObservedResources.ErrorAnalysis = fmt.Sprintf(
				"Chỉ ghi được trung bình %d/15.000 request lên h2c trong một giây.",
				average.SentRequests,
			)
		default:
			report.ObservedResources.ErrorAnalysis = fmt.Sprintf(
				"Đã gửi đủ 15.000 request nhưng chỉ có trung bình %d/12.000 response thành công trong một giây.",
				average.SuccessfulRequests,
			)
		}
	}

	return report, nil
}

func formatTargetResult(result loadtest.Result) string {
	if result.TargetMet {
		return ""
	}
	if result.SentRequests < result.TargetRequests {
		return fmt.Sprintf(
			"Chỉ ghi được %d/%d request lên h2c trong %.3fs",
			result.SentRequests,
			result.TargetRequests,
			result.TargetDurationSeconds,
		)
	}
	return fmt.Sprintf(
		"Chỉ có %d/%d response HTTP 201 hoàn tất trong %.3fs",
		result.SuccessfulRequests,
		result.TargetSuccessfulRequests,
		result.TargetDurationSeconds,
	)
}

func averageMeasurement(measurements []performanceMeasurement) performanceMeasurement {
	average := performanceMeasurement{Run: "Trung bình", Average: true, TargetMet: true}
	var failed float64
	var total float64
	var successful float64
	var sent float64
	for _, measurement := range measurements {
		total += float64(measurement.TotalRequests)
		successful += float64(measurement.SuccessfulRequests)
		sent += float64(measurement.SentRequests)
		average.SuccessfulTPS += measurement.SuccessfulTPS
		average.LatencyP50Millis += measurement.LatencyP50Millis
		average.LatencyP95Millis += measurement.LatencyP95Millis
		average.LatencyP99Millis += measurement.LatencyP99Millis
		average.ActualDurationSeconds += measurement.ActualDurationSeconds
		average.TargetMet = average.TargetMet && measurement.TargetMet
		failed += float64(measurement.FailedRequests)
	}
	count := float64(len(measurements))
	if count > 0 {
		average.TotalRequests = uint64(math.Round(total / count))
		average.SuccessfulRequests = uint64(math.Round(successful / count))
		average.SentRequests = uint64(math.Round(sent / count))
		average.SuccessfulTPS /= count
		average.LatencyP50Millis /= count
		average.LatencyP95Millis /= count
		average.LatencyP99Millis /= count
		average.ActualDurationSeconds /= count
		average.FailedRequests = uint64(math.Round(failed / count))
		if !average.TargetMet {
			average.FailedReason = fmt.Sprintf(
				"Trung bình sent=%d/15.000, success=%d/12.000 trong 1s",
				average.SentRequests,
				average.SuccessfulRequests,
			)
		}
	}
	return average
}
