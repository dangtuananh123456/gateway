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
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/dangtuananh123456/gateway/internal/model"
	"github.com/dangtuananh123456/gateway/pkg/constants"
)

const (
	scaleComposeFile        = "docker-compose.scale.yml"
	scaleGatewayURL         = "http://localhost:18083"
	scaleMembershipTimeout  = 45 * time.Second
	scaleRequestTimeout     = 12 * time.Second
	scaleLoadDelay          = "6s"
	scaleLoadStagger        = 250 * time.Millisecond
	scaleMetricsSettle      = 500 * time.Millisecond
	scaleTrafficInterval    = 25 * time.Millisecond
	scaleStableProbeFactor  = 2
	scaleContainerIDMinimum = 12
)

type scaleScenario struct {
	mode            constants.RoutingMode
	processingDelay string
}

type scaleRunner struct {
	client  *http.Client
	counter atomic.Uint64
}

type sessionResult struct {
	response model.CreateSMContextResponse
	err      error
}

type trafficStats struct {
	attempts atomic.Int64
	errors   atomic.Int64
}

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
	defer cancel()

	protocols := new(http.Protocols)
	protocols.SetUnencryptedHTTP2(true)
	transport := &http.Transport{Protocols: protocols}
	defer transport.CloseIdleConnections()
	runner := &scaleRunner{
		client: &http.Client{Transport: transport, Timeout: scaleRequestTimeout},
	}

	scenarios := []scaleScenario{
		{mode: constants.RoutingRoundRobin, processingDelay: "0s"},
		{mode: constants.RoutingWeighted, processingDelay: "0s"},
		{mode: constants.RoutingLoad, processingDelay: scaleLoadDelay},
	}
	if err := runner.compose(ctx, nil, "build"); err != nil {
		fail(err)
	}
	for _, scenario := range scenarios {
		if err := runner.runScenario(ctx, scenario); err != nil {
			fail(fmt.Errorf("mode %s: %w", scenario.mode, err))
		}
	}
	fmt.Println("DNS scale E2E passed for round_robin, weighted, and load")
}

func (runner *scaleRunner) runScenario(ctx context.Context, scenario scaleScenario) error {
	environment := map[string]string{
		"ROUTING_MODE":         string(scenario.mode),
		"PDU_PROCESSING_DELAY": scenario.processingDelay,
	}
	fmt.Printf("\n=== mode=%s ===\n", scenario.mode)
	if err := runner.compose(ctx, environment, "down", "--remove-orphans"); err != nil {
		return err
	}
	if err := runner.compose(ctx, environment, "up", "-d", "--scale", "pdu-session=1"); err != nil {
		return err
	}
	gatewayID, err := runner.singleServiceContainer(ctx, environment, "gateway")
	if err != nil {
		return err
	}

	if _, err := runner.verifyScale(ctx, environment, scenario.mode, 1); err != nil {
		return err
	}
	if err := runner.assertGatewayUnchanged(ctx, environment, gatewayID); err != nil {
		return err
	}
	if err := runner.scalePDU(ctx, environment, 3); err != nil {
		return err
	}
	threeInstances, err := runner.verifyScale(ctx, environment, scenario.mode, 3)
	if err != nil {
		return err
	}
	if err := runner.assertGatewayUnchanged(ctx, environment, gatewayID); err != nil {
		return err
	}
	if err := runner.scalePDU(ctx, environment, 20); err != nil {
		return err
	}
	twentyInstances, err := runner.verifyScale(ctx, environment, scenario.mode, 20)
	if err != nil {
		return err
	}
	if err := runner.assertGatewayUnchanged(ctx, environment, gatewayID); err != nil {
		return err
	}

	if err := runner.scalePDU(ctx, environment, 3); err != nil {
		return err
	}
	remainingInstances, err := runner.verifyScale(ctx, environment, scenario.mode, 3)
	if err != nil {
		return err
	}
	removedCount := differenceCount(twentyInstances, remainingInstances)
	if removedCount != 17 {
		return fmt.Errorf("scale down removed %d instances, want 17", removedCount)
	}
	if err := runner.assertGatewayUnchanged(ctx, environment, gatewayID); err != nil {
		return err
	}

	// Kill the lexical-first replica while health traffic is flowing. This is
	// also the deterministic load-based winner when all counters are zero.
	killedInstance := remainingInstances[0]
	killedContainer, err := runner.containerForHostname(ctx, environment, killedInstance)
	if err != nil {
		return err
	}
	trafficCtx, stopTraffic := context.WithCancel(ctx)
	stats, trafficDone := runner.startHealthTraffic(trafficCtx)
	time.Sleep(200 * time.Millisecond)
	if err := runner.docker(ctx, nil, "kill", killedContainer); err != nil {
		stopTraffic()
		<-trafficDone
		return err
	}
	healthyAfterKill := without(remainingInstances, killedInstance)
	if err := runner.waitMembership(ctx, scenario.mode, healthyAfterKill); err != nil {
		stopTraffic()
		<-trafficDone
		return fmt.Errorf("detect killed instance %s: %w", killedInstance, err)
	}
	if err := runner.assertOnlyHealthy(ctx, scenario.mode, healthyAfterKill); err != nil {
		stopTraffic()
		<-trafficDone
		return err
	}
	if err := runner.docker(ctx, nil, "start", killedContainer); err != nil {
		stopTraffic()
		<-trafficDone
		return err
	}
	if err := runner.waitMembership(ctx, scenario.mode, remainingInstances); err != nil {
		stopTraffic()
		<-trafficDone
		return fmt.Errorf("recover restarted instance %s: %w", killedInstance, err)
	}
	stopTraffic()
	<-trafficDone
	if stats.attempts.Load() == 0 {
		return errors.New("kill/restart traffic loop made no requests")
	}
	fmt.Printf(
		"kill/restart instance=%s traffic_attempts=%d transient_errors=%d\n",
		killedInstance, stats.attempts.Load(), stats.errors.Load(),
	)
	if err := runner.assertGatewayUnchanged(ctx, environment, gatewayID); err != nil {
		return err
	}

	if err := runner.scalePDU(ctx, environment, 1); err != nil {
		return err
	}
	if _, err := runner.verifyScale(ctx, environment, scenario.mode, 1); err != nil {
		return err
	}
	if err := runner.assertGatewayUnchanged(ctx, environment, gatewayID); err != nil {
		return err
	}
	fmt.Printf(
		"mode=%s passed scales=1->3->20->3->1 initial_three=%d gateway=%s\n",
		scenario.mode, len(threeInstances), shortID(gatewayID),
	)
	return nil
}

func (runner *scaleRunner) verifyScale(
	ctx context.Context,
	environment map[string]string,
	mode constants.RoutingMode,
	expectedCount int,
) ([]string, error) {
	instances, err := runner.waitForPDUContainers(ctx, environment, expectedCount)
	if err != nil {
		return nil, err
	}
	if err := runner.waitMembership(ctx, mode, instances); err != nil {
		return nil, fmt.Errorf("verify %d PDU membership: %w", expectedCount, err)
	}
	fmt.Printf("scale=%d discovered=%d instances\n", expectedCount, len(instances))
	return instances, nil
}

func (runner *scaleRunner) waitMembership(
	ctx context.Context,
	mode constants.RoutingMode,
	expected []string,
) error {
	deadline := time.Now().Add(scaleMembershipTimeout)
	var lastError error
	for time.Now().Before(deadline) {
		var observed []string
		var err error
		if mode == constants.RoutingLoad && len(expected) > 1 {
			time.Sleep(scaleMetricsSettle)
			observed, err = runner.loadCascade(ctx, len(expected))
		} else {
			observed, err = runner.sequentialMembership(ctx, len(expected))
		}
		if err == nil && sameStrings(observed, expected) {
			return nil
		}
		if err != nil {
			lastError = err
		} else {
			lastError = fmt.Errorf("observed instances %v, want %v", observed, expected)
		}
		time.Sleep(scaleMetricsSettle)
	}
	return fmt.Errorf("membership timeout: %w", lastError)
}

func (runner *scaleRunner) sequentialMembership(ctx context.Context, expectedCount int) ([]string, error) {
	observed := make(map[string]struct{}, expectedCount)
	probeCount := max(expectedCount*scaleStableProbeFactor, 2)
	for range probeCount {
		response, err := runner.createSession(ctx)
		if err != nil {
			return sortedSet(observed), err
		}
		observed[response.HandledBy] = struct{}{}
	}
	return sortedSet(observed), nil
}

func (runner *scaleRunner) loadCascade(ctx context.Context, requestCount int) ([]string, error) {
	results := make(chan sessionResult, requestCount)
	for requestIndex := range requestCount {
		go func() {
			response, err := runner.createSession(ctx)
			results <- sessionResult{response: response, err: err}
		}()
		if requestIndex+1 < requestCount {
			time.Sleep(scaleLoadStagger)
		}
	}
	observed := make(map[string]struct{}, requestCount)
	var resultErrors []error
	for range requestCount {
		result := <-results
		if result.err != nil {
			resultErrors = append(resultErrors, result.err)
			continue
		}
		observed[result.response.HandledBy] = struct{}{}
	}
	return sortedSet(observed), errors.Join(resultErrors...)
}

func (runner *scaleRunner) assertOnlyHealthy(
	ctx context.Context,
	mode constants.RoutingMode,
	expected []string,
) error {
	if mode == constants.RoutingLoad && len(expected) > 1 {
		observed, err := runner.loadCascade(ctx, len(expected))
		if err != nil || !sameStrings(observed, expected) {
			return fmt.Errorf("post-detection load routing observed=%v want=%v: %w", observed, expected, err)
		}
		return nil
	}
	allowed := stringSet(expected)
	for range max(len(expected)*scaleStableProbeFactor, 10) {
		response, err := runner.createSession(ctx)
		if err != nil {
			return fmt.Errorf("post-detection request: %w", err)
		}
		if _, found := allowed[response.HandledBy]; !found {
			return fmt.Errorf("post-detection routed to unhealthy instance %q", response.HandledBy)
		}
	}
	return nil
}

func (runner *scaleRunner) createSession(ctx context.Context) (model.CreateSMContextResponse, error) {
	requestNumber := runner.counter.Add(1)
	payload := model.CreateSMContextRequest{
		SUPI:         fmt.Sprintf("imsi-452070%08d", requestNumber),
		GPSI:         fmt.Sprintf("msisdn-84930%08d", requestNumber),
		PDUSessionID: int(requestNumber%255) + 1,
		DNN:          "v-internet",
		SNSSAI:       model.SNSSAI{SST: 1, SD: "000001"},
		ServingNFID:  fmt.Sprintf("scale-e2e-amf-%d", requestNumber),
		ANType:       "3GPP_ACCESS",
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return model.CreateSMContextResponse{}, err
	}
	request, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		scaleGatewayURL+constants.CreateSMContextPath,
		bytes.NewReader(body),
	)
	if err != nil {
		return model.CreateSMContextResponse{}, err
	}
	request.Header.Set("Content-Type", constants.ContentTypeJSON)
	response, err := runner.client.Do(request)
	if err != nil {
		return model.CreateSMContextResponse{}, err
	}
	defer response.Body.Close()
	if response.ProtoMajor != 2 || response.StatusCode != http.StatusCreated {
		errorBody, _ := io.ReadAll(io.LimitReader(response.Body, constants.CreateSMContextMaxBodyBytes))
		return model.CreateSMContextResponse{}, fmt.Errorf("protocol=%s status=%s body=%s", response.Proto, response.Status, errorBody)
	}
	var result model.CreateSMContextResponse
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		return model.CreateSMContextResponse{}, err
	}
	if result.HandledBy == "" || result.SMContextRef == "" || result.Status != constants.SMContextActive {
		return model.CreateSMContextResponse{}, fmt.Errorf("invalid session response: %+v", result)
	}
	return result, nil
}

func (runner *scaleRunner) startHealthTraffic(ctx context.Context) (*trafficStats, <-chan struct{}) {
	stats := new(trafficStats)
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(scaleTrafficInterval)
		defer ticker.Stop()
		for {
			request, err := http.NewRequestWithContext(ctx, http.MethodGet, scaleGatewayURL+constants.HealthPath, nil)
			if err != nil {
				stats.errors.Add(1)
				return
			}
			stats.attempts.Add(1)
			response, err := runner.client.Do(request)
			if err != nil {
				if ctx.Err() == nil {
					stats.errors.Add(1)
				}
			} else {
				_, _ = io.Copy(io.Discard, response.Body)
				response.Body.Close()
				if response.StatusCode != http.StatusOK {
					stats.errors.Add(1)
				}
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	return stats, done
}

func (runner *scaleRunner) scalePDU(ctx context.Context, environment map[string]string, count int) error {
	fmt.Printf("scaling pdu-session to %d\n", count)
	return runner.compose(
		ctx, environment, "up", "-d", "--no-recreate", "--scale",
		"pdu-session="+strconv.Itoa(count), "pdu-session",
	)
}

func (runner *scaleRunner) waitForPDUContainers(
	ctx context.Context,
	environment map[string]string,
	expectedCount int,
) ([]string, error) {
	deadline := time.Now().Add(scaleMembershipTimeout)
	for time.Now().Before(deadline) {
		containerIDs, err := runner.serviceContainers(ctx, environment, "pdu-session")
		if err == nil && len(containerIDs) == expectedCount {
			hostnames := make([]string, 0, expectedCount)
			for _, containerID := range containerIDs {
				hostname, inspectErr := runner.inspect(ctx, containerID, "{{.Config.Hostname}}")
				if inspectErr != nil {
					return nil, inspectErr
				}
				hostnames = append(hostnames, hostname)
			}
			slices.Sort(hostnames)
			return hostnames, nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	return nil, fmt.Errorf("Docker did not reach %d running PDU containers", expectedCount)
}

func (runner *scaleRunner) containerForHostname(
	ctx context.Context,
	environment map[string]string,
	hostname string,
) (string, error) {
	containerIDs, err := runner.serviceContainers(ctx, environment, "pdu-session")
	if err != nil {
		return "", err
	}
	for _, containerID := range containerIDs {
		candidate, inspectErr := runner.inspect(ctx, containerID, "{{.Config.Hostname}}")
		if inspectErr != nil {
			return "", inspectErr
		}
		if candidate == hostname {
			return containerID, nil
		}
	}
	return "", fmt.Errorf("no running PDU container has hostname %q", hostname)
}

func (runner *scaleRunner) assertGatewayUnchanged(
	ctx context.Context,
	environment map[string]string,
	expectedID string,
) error {
	actualID, err := runner.singleServiceContainer(ctx, environment, "gateway")
	if err != nil {
		return err
	}
	if actualID != expectedID {
		return fmt.Errorf("Gateway restarted: container changed from %s to %s", shortID(expectedID), shortID(actualID))
	}
	return nil
}

func (runner *scaleRunner) singleServiceContainer(
	ctx context.Context,
	environment map[string]string,
	service string,
) (string, error) {
	containerIDs, err := runner.serviceContainers(ctx, environment, service)
	if err != nil {
		return "", err
	}
	if len(containerIDs) != 1 {
		return "", fmt.Errorf("service %s has %d running containers, want 1", service, len(containerIDs))
	}
	return containerIDs[0], nil
}

func (runner *scaleRunner) serviceContainers(
	ctx context.Context,
	environment map[string]string,
	service string,
) ([]string, error) {
	output, err := runner.composeOutput(ctx, environment, "ps", "-q", service)
	if err != nil {
		return nil, err
	}
	return strings.Fields(output), nil
}

func (runner *scaleRunner) inspect(ctx context.Context, containerID, format string) (string, error) {
	output, err := runner.dockerOutput(ctx, nil, "inspect", "--format", format, containerID)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(output), nil
}

func (runner *scaleRunner) compose(
	ctx context.Context,
	environment map[string]string,
	arguments ...string,
) error {
	_, err := runner.composeCommand(ctx, environment, true, arguments...)
	return err
}

func (runner *scaleRunner) composeOutput(
	ctx context.Context,
	environment map[string]string,
	arguments ...string,
) (string, error) {
	return runner.composeCommand(ctx, environment, false, arguments...)
}

func (runner *scaleRunner) composeCommand(
	ctx context.Context,
	environment map[string]string,
	stream bool,
	arguments ...string,
) (string, error) {
	composeArguments := append([]string{"compose", "-f", scaleComposeFile}, arguments...)
	return runCommand(ctx, environment, stream, "docker", composeArguments...)
}

func (runner *scaleRunner) docker(
	ctx context.Context,
	environment map[string]string,
	arguments ...string,
) error {
	_, err := runCommand(ctx, environment, true, "docker", arguments...)
	return err
}

func (runner *scaleRunner) dockerOutput(
	ctx context.Context,
	environment map[string]string,
	arguments ...string,
) (string, error) {
	return runCommand(ctx, environment, false, "docker", arguments...)
}

func runCommand(
	ctx context.Context,
	environment map[string]string,
	stream bool,
	name string,
	arguments ...string,
) (string, error) {
	command := exec.CommandContext(ctx, name, arguments...)
	command.Env = mergeEnvironment(os.Environ(), environment)
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

func mergeEnvironment(base []string, overrides map[string]string) []string {
	if len(overrides) == 0 {
		return base
	}
	result := make([]string, 0, len(base)+len(overrides))
	for _, entry := range base {
		key, _, found := strings.Cut(entry, "=")
		if found {
			if _, overridden := lookupFold(overrides, key); overridden {
				continue
			}
		}
		result = append(result, entry)
	}
	for key, value := range overrides {
		result = append(result, key+"="+value)
	}
	return result
}

func lookupFold(values map[string]string, key string) (string, bool) {
	for candidate, value := range values {
		if strings.EqualFold(candidate, key) {
			return value, true
		}
	}
	return "", false
}

func sameStrings(first, second []string) bool {
	return slices.Equal(first, second)
}

func sortedSet(values map[string]struct{}) []string {
	result := make([]string, 0, len(values))
	for value := range values {
		result = append(result, value)
	}
	slices.Sort(result)
	return result
}

func stringSet(values []string) map[string]struct{} {
	result := make(map[string]struct{}, len(values))
	for _, value := range values {
		result[value] = struct{}{}
	}
	return result
}

func without(values []string, removed string) []string {
	result := make([]string, 0, len(values)-1)
	for _, value := range values {
		if value != removed {
			result = append(result, value)
		}
	}
	return result
}

func differenceCount(first, second []string) int {
	remaining := stringSet(second)
	count := 0
	for _, value := range first {
		if _, found := remaining[value]; !found {
			count++
		}
	}
	return count
}

func shortID(containerID string) string {
	if len(containerID) <= scaleContainerIDMinimum {
		return containerID
	}
	return containerID[:scaleContainerIDMinimum]
}

func fail(err error) {
	if err == nil {
		err = errors.New("unknown scale E2E failure")
	}
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
