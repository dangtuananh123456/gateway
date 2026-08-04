//go:build ignore

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"time"

	"github.com/dangtuananh123456/gateway/internal/model"
	"github.com/dangtuananh123456/gateway/internal/requestlog"
	"github.com/dangtuananh123456/gateway/pkg/constants"
)

const (
	failureComposeFile = "docker-compose.failure.yml"
	failureGatewayURL  = "http://localhost:18084"
	failurePDUService  = "pdu-session"
	failureTimeout     = 30 * time.Second
)

type failureRunner struct {
	client  *http.Client
	counter atomic.Uint64
}

type gatewayResponse struct {
	status      int
	contentType string
	body        []byte
	session     model.CreateSMContextResponse
	failure     model.ErrorResponse
}

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	protocols := new(http.Protocols)
	protocols.SetUnencryptedHTTP2(true)
	transport := &http.Transport{Protocols: protocols}
	defer transport.CloseIdleConnections()
	runner := &failureRunner{
		client: &http.Client{Transport: transport, Timeout: 6 * time.Second},
	}

	if err := runner.compose(ctx, nil, "build"); err != nil {
		fail(err)
	}
	if err := runner.connectErrorPhase(ctx); err != nil {
		fail(fmt.Errorf("connect error phase: %w", err))
	}
	if err := runner.allDownAndRestartPhase(ctx); err != nil {
		fail(fmt.Errorf("all-down/restart phase: %w", err))
	}
	if err := runner.timeoutAndCancelPhase(ctx); err != nil {
		fail(fmt.Errorf("timeout/cancel phase: %w", err))
	}
	fmt.Println("Failure E2E passed: 502, exact 503, 504, no retry, client cancel, and PDU restart recovery")
}

func (runner *failureRunner) connectErrorPhase(ctx context.Context) error {
	fmt.Println("\n=== connect error -> 502 ===")
	environment := map[string]string{
		"FAILURE_PROBE_INTERVAL":   "30s",
		"FAILURE_STALE_TTL":        "35s",
		"FAILURE_UPSTREAM_TIMEOUT": "10s",
		"FAILURE_PROCESSING_DELAY": "5s",
	}
	if err := runner.recreate(ctx, environment); err != nil {
		return err
	}
	if err := runner.waitForHealth(ctx); err != nil {
		return err
	}
	pduContainer, err := runner.singleContainer(ctx, environment, failurePDUService)
	if err != nil {
		return err
	}
	requestDone := make(chan gatewayResponse, 1)
	requestErrors := make(chan error, 1)
	go func() {
		result, requestErr := runner.sendCreate(ctx, "failure-connect-502")
		requestDone <- result
		requestErrors <- requestErr
	}()
	if err := runner.waitForActiveRequests(ctx, 1); err != nil {
		return fmt.Errorf("connection-failure request never reached PDU: %w", err)
	}
	if err := runner.docker(ctx, "kill", pduContainer); err != nil {
		return err
	}
	result := <-requestDone
	if requestErr := <-requestErrors; requestErr != nil {
		return requestErr
	}
	if err := assertFailure(result, http.StatusBadGateway, constants.CauseBadGateway); err != nil {
		return err
	}
	fmt.Printf("connect_error status=%d cause=%s\n", result.status, result.failure.Cause)
	return nil
}

func (runner *failureRunner) allDownAndRestartPhase(ctx context.Context) error {
	fmt.Println("\n=== all down -> exact 503 -> recovery/restart ===")
	environment := map[string]string{
		"FAILURE_PROBE_INTERVAL":   "250ms",
		"FAILURE_STALE_TTL":        "35s",
		"FAILURE_UPSTREAM_TIMEOUT": "3s",
		"FAILURE_PROCESSING_DELAY": "0s",
	}
	if err := runner.recreate(ctx, environment); err != nil {
		return err
	}
	beforeRestart, err := runner.waitForCreated(ctx, "failure-before-stop")
	if err != nil {
		return err
	}
	gatewayContainer, err := runner.singleContainer(ctx, environment, "gateway")
	if err != nil {
		return err
	}
	pduContainer, err := runner.singleContainer(ctx, environment, failurePDUService)
	if err != nil {
		return err
	}
	if err := runner.docker(ctx, "stop", pduContainer); err != nil {
		return err
	}
	if err := runner.waitForExactNoBackend(ctx); err != nil {
		return err
	}
	if err := runner.docker(ctx, "start", pduContainer); err != nil {
		return err
	}
	afterRecovery, err := runner.waitForCreated(ctx, "failure-after-start")
	if err != nil {
		return err
	}
	if beforeRestart.session.SMContextRef == afterRecovery.session.SMContextRef {
		return errors.New("recovery returned a duplicate context reference")
	}
	if err := runner.docker(ctx, "restart", pduContainer); err != nil {
		return err
	}
	afterRestart, err := runner.waitForCreated(ctx, "failure-after-restart")
	if err != nil {
		return err
	}
	if afterRestart.session.HandledBy != beforeRestart.session.HandledBy ||
		afterRestart.session.SMContextRef == beforeRestart.session.SMContextRef {
		return fmt.Errorf("unexpected restart response: before=%+v after=%+v", beforeRestart.session, afterRestart.session)
	}
	actualGateway, err := runner.singleContainer(ctx, environment, "gateway")
	if err != nil {
		return err
	}
	if actualGateway != gatewayContainer {
		return errors.New("Gateway restarted during PDU stop/start/restart")
	}
	fmt.Printf(
		"all_down status=503 exact_body=true recovery_instance=%s gateway_unchanged=true\n",
		afterRestart.session.HandledBy,
	)
	return nil
}

func (runner *failureRunner) timeoutAndCancelPhase(ctx context.Context) error {
	fmt.Println("\n=== upstream timeout -> 504; client cancel propagation ===")
	environment := map[string]string{
		"FAILURE_PROBE_INTERVAL":   "250ms",
		"FAILURE_STALE_TTL":        "35s",
		"FAILURE_UPSTREAM_TIMEOUT": "1500ms",
		"FAILURE_PROCESSING_DELAY": "3s",
	}
	if err := runner.recreate(ctx, environment); err != nil {
		return err
	}
	if err := runner.waitForHealth(ctx); err != nil {
		return err
	}

	timeoutRequestID := "failure-timeout-single-attempt"
	timedOut, err := runner.sendCreate(ctx, timeoutRequestID)
	if err != nil {
		return err
	}
	if err := assertFailure(timedOut, http.StatusGatewayTimeout, constants.CauseUpstreamTimeout); err != nil {
		return err
	}
	if err := runner.waitForActiveRequests(ctx, 0); err != nil {
		return fmt.Errorf("active requests after timeout: %w", err)
	}
	if err := runner.assertPDURequestLogCount(ctx, environment, timeoutRequestID, 1); err != nil {
		return fmt.Errorf("timeout retry check: %w", err)
	}

	cancelRequestID := "failure-client-cancel-single-attempt"
	requestCtx, cancel := context.WithCancel(ctx)
	requestDone := make(chan error, 1)
	go func() {
		_, requestErr := runner.sendCreate(requestCtx, cancelRequestID)
		requestDone <- requestErr
	}()
	if err := runner.waitForActiveRequests(ctx, 1); err != nil {
		cancel()
		<-requestDone
		return fmt.Errorf("request never became active before cancel: %w", err)
	}
	cancel()
	requestErr := <-requestDone
	if !errors.Is(requestErr, context.Canceled) {
		return fmt.Errorf("canceled client error = %v, want context.Canceled", requestErr)
	}
	if err := runner.waitForActiveRequests(ctx, 0); err != nil {
		return fmt.Errorf("active requests after client cancel: %w", err)
	}
	if err := runner.assertPDURequestLogCount(ctx, environment, cancelRequestID, 1); err != nil {
		return fmt.Errorf("cancel retry check: %w", err)
	}
	fmt.Printf(
		"timeout status=%d cause=%s attempts=1; client_cancel propagated=true attempts=1 active_requests=0\n",
		timedOut.status, timedOut.failure.Cause,
	)
	return nil
}

func (runner *failureRunner) waitForExactNoBackend(ctx context.Context) error {
	deadline := time.Now().Add(failureTimeout)
	const exactBody = `{"status":"ERROR","cause":"NO_BACKEND_AVAILABLE"}`
	var last string
	for time.Now().Before(deadline) {
		result, err := runner.sendCreate(ctx, "failure-all-down")
		if err == nil {
			last = fmt.Sprintf("status=%d body=%s", result.status, result.body)
			if result.status == http.StatusServiceUnavailable && string(result.body) == exactBody &&
				result.contentType == constants.ContentTypeJSON {
				return nil
			}
		} else {
			last = err.Error()
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("exact no-backend response not observed: %s", last)
}

func (runner *failureRunner) waitForCreated(ctx context.Context, requestID string) (gatewayResponse, error) {
	deadline := time.Now().Add(failureTimeout)
	var lastError error
	for time.Now().Before(deadline) {
		result, err := runner.sendCreate(ctx, requestID)
		if err == nil && result.status == http.StatusCreated && result.session.HandledBy == "failure-pdu-1" {
			return result, nil
		}
		if err != nil {
			lastError = err
		} else {
			lastError = fmt.Errorf("status=%d body=%s", result.status, result.body)
		}
		time.Sleep(100 * time.Millisecond)
	}
	return gatewayResponse{}, fmt.Errorf("Gateway did not recover: %w", lastError)
}

func (runner *failureRunner) waitForHealth(ctx context.Context) error {
	deadline := time.Now().Add(failureTimeout)
	for time.Now().Before(deadline) {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, failureGatewayURL+constants.HealthPath, nil)
		if err != nil {
			return err
		}
		response, err := runner.client.Do(request)
		if err == nil {
			_, _ = io.Copy(io.Discard, response.Body)
			response.Body.Close()
			if response.ProtoMajor == 2 && response.StatusCode == http.StatusOK {
				return nil
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	return errors.New("Gateway health readiness timeout")
}

func (runner *failureRunner) waitForActiveRequests(ctx context.Context, expected int64) error {
	deadline := time.Now().Add(2 * time.Second)
	var last int64 = -1
	for time.Now().Before(deadline) {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, failureGatewayURL+constants.MetricsPath, nil)
		if err != nil {
			return err
		}
		response, err := runner.client.Do(request)
		if err == nil {
			var metrics model.MetricsResponse
			decodeErr := json.NewDecoder(response.Body).Decode(&metrics)
			response.Body.Close()
			if response.StatusCode == http.StatusOK && decodeErr == nil {
				last = metrics.ActiveRequests
				if last == expected {
					return nil
				}
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	return fmt.Errorf("activeRequests=%d, want %d", last, expected)
}

func (runner *failureRunner) sendCreate(ctx context.Context, requestID string) (gatewayResponse, error) {
	requestNumber := runner.counter.Add(1)
	payload := model.CreateSMContextRequest{
		SUPI:         fmt.Sprintf("imsi-452080%08d", requestNumber),
		GPSI:         fmt.Sprintf("msisdn-84940%08d", requestNumber),
		PDUSessionID: int(requestNumber%255) + 1,
		DNN:          "v-internet",
		SNSSAI:       model.SNSSAI{SST: 1, SD: "000001"},
		ServingNFID:  fmt.Sprintf("failure-e2e-amf-%d", requestNumber),
		ANType:       "3GPP_ACCESS",
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return gatewayResponse{}, err
	}
	request, err := http.NewRequestWithContext(
		ctx, http.MethodPost, failureGatewayURL+constants.CreateSMContextPath, bytes.NewReader(body),
	)
	if err != nil {
		return gatewayResponse{}, err
	}
	request.Header.Set("Content-Type", constants.ContentTypeJSON)
	request.Header.Set(requestlog.RequestIDHeader, requestID)
	response, err := runner.client.Do(request)
	if err != nil {
		return gatewayResponse{}, err
	}
	defer response.Body.Close()
	responseBody, err := io.ReadAll(io.LimitReader(response.Body, constants.CreateSMContextMaxBodyBytes))
	if err != nil {
		return gatewayResponse{}, err
	}
	result := gatewayResponse{
		status: response.StatusCode, contentType: response.Header.Get("Content-Type"), body: responseBody,
	}
	if response.StatusCode == http.StatusCreated {
		if err := json.Unmarshal(responseBody, &result.session); err != nil {
			return gatewayResponse{}, err
		}
	} else if err := json.Unmarshal(responseBody, &result.failure); err != nil {
		return gatewayResponse{}, fmt.Errorf("decode failure response status=%d body=%s: %w", response.StatusCode, responseBody, err)
	}
	return result, nil
}

func (runner *failureRunner) assertPDURequestLogCount(
	ctx context.Context,
	environment map[string]string,
	requestID string,
	expected int,
) error {
	time.Sleep(300 * time.Millisecond)
	output, err := runner.composeOutput(ctx, environment, "logs", "--no-color", failurePDUService)
	if err != nil {
		return err
	}
	if count := strings.Count(output, requestID); count != expected {
		return fmt.Errorf("PDU log count for request_id=%s is %d, want %d", requestID, count, expected)
	}
	return nil
}

func assertFailure(result gatewayResponse, expectedStatus int, expectedCause constants.ErrorCause) error {
	if result.status != expectedStatus || result.failure.Status != constants.ErrorStatus ||
		result.failure.Cause != expectedCause || result.contentType != constants.ContentTypeJSON {
		return fmt.Errorf(
			"failure response status=%d content_type=%q payload=%+v, want status=%d cause=%s",
			result.status, result.contentType, result.failure, expectedStatus, expectedCause,
		)
	}
	return nil
}

func (runner *failureRunner) recreate(ctx context.Context, environment map[string]string) error {
	if err := runner.compose(ctx, environment, "down", "--remove-orphans"); err != nil {
		return err
	}
	return runner.compose(ctx, environment, "up", "-d")
}

func (runner *failureRunner) singleContainer(
	ctx context.Context,
	environment map[string]string,
	service string,
) (string, error) {
	output, err := runner.composeOutput(ctx, environment, "ps", "-q", service)
	if err != nil {
		return "", err
	}
	containers := strings.Fields(output)
	if len(containers) != 1 {
		return "", fmt.Errorf("service %s has %d running containers, want 1", service, len(containers))
	}
	return containers[0], nil
}

func (runner *failureRunner) compose(
	ctx context.Context,
	environment map[string]string,
	arguments ...string,
) error {
	_, err := runFailureCommand(ctx, environment, true, "docker", append(
		[]string{"compose", "-f", failureComposeFile}, arguments...,
	)...)
	return err
}

func (runner *failureRunner) composeOutput(
	ctx context.Context,
	environment map[string]string,
	arguments ...string,
) (string, error) {
	return runFailureCommand(ctx, environment, false, "docker", append(
		[]string{"compose", "-f", failureComposeFile}, arguments...,
	)...)
}

func (runner *failureRunner) docker(ctx context.Context, arguments ...string) error {
	_, err := runFailureCommand(ctx, nil, true, "docker", arguments...)
	return err
}

func runFailureCommand(
	ctx context.Context,
	overrides map[string]string,
	stream bool,
	name string,
	arguments ...string,
) (string, error) {
	command := exec.CommandContext(ctx, name, arguments...)
	command.Env = failureEnvironment(os.Environ(), overrides)
	if stream {
		command.Stdout = os.Stdout
		command.Stderr = os.Stderr
		if err := command.Run(); err != nil {
			return "", fmt.Errorf("run %s %s: %w", name, strings.Join(arguments, " "), err)
		}
		return "", nil
	}
	output, err := command.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("run %s %s: %w: %s", name, strings.Join(arguments, " "), err, bytes.TrimSpace(output))
	}
	return string(output), nil
}

func failureEnvironment(base []string, overrides map[string]string) []string {
	if len(overrides) == 0 {
		return base
	}
	result := make([]string, 0, len(base)+len(overrides))
	for _, entry := range base {
		key, _, found := strings.Cut(entry, "=")
		if found && hasFailureKey(overrides, key) {
			continue
		}
		result = append(result, entry)
	}
	for key, value := range overrides {
		result = append(result, key+"="+value)
	}
	return result
}

func hasFailureKey(values map[string]string, key string) bool {
	for candidate := range values {
		if strings.EqualFold(candidate, key) {
			return true
		}
	}
	return false
}

func fail(err error) {
	if err == nil {
		err = errors.New("unknown failure E2E error")
	}
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
