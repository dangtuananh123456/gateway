//go:build ignore

package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/dangtuananh123456/gateway/internal/model"
	"github.com/dangtuananh123456/gateway/pkg/constants"
)

const (
	weightedCycleLength = 6
	weightedCycles      = 2
	weightedWarmup      = weightedCycleLength
)

var expectedWeights = map[string]int{
	"pdu-1": 3,
	"pdu-2": 2,
	"pdu-3": 1,
}

func main() {
	baseURL := "http://localhost:18081"
	if len(os.Args) > 1 {
		baseURL = strings.TrimRight(os.Args[1], "/")
	}

	protocols := new(http.Protocols)
	protocols.SetUnencryptedHTTP2(true)
	transport := &http.Transport{Protocols: protocols}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 3 * time.Second}

	if err := waitForWeights(client, baseURL, expectedWeights, 30*time.Second); err != nil {
		fail(err)
	}
	for requestIndex := range weightedWarmup {
		if _, err := createWeightedSession(client, baseURL, requestIndex); err != nil {
			fail(fmt.Errorf("warmup request %d: %w", requestIndex+1, err))
		}
	}

	requestCount := weightedCycleLength * weightedCycles
	sequence := make([]string, 0, requestCount)
	contextReferences := make(map[string]struct{}, requestCount)
	for requestIndex := range requestCount {
		response, err := createWeightedSession(client, baseURL, weightedWarmup+requestIndex)
		if err != nil {
			fail(fmt.Errorf("measured request %d: %w", requestIndex+1, err))
		}
		if _, found := expectedWeights[response.HandledBy]; !found {
			fail(fmt.Errorf("request %d handled by unexpected instance %q", requestIndex+1, response.HandledBy))
		}
		if _, duplicate := contextReferences[response.SMContextRef]; duplicate {
			fail(fmt.Errorf("request %d returned duplicate context reference %q", requestIndex+1, response.SMContextRef))
		}
		contextReferences[response.SMContextRef] = struct{}{}
		sequence = append(sequence, response.HandledBy)
		fmt.Printf("request=%d handledBy=%s context=%s\n", requestIndex+1, response.HandledBy, response.SMContextRef)
	}

	if err := validateWeightedCycles(sequence, expectedWeights); err != nil {
		fail(err)
	}
	counts := countInstances(sequence)
	fmt.Printf(
		"Weighted E2E passed: sequence=%s, counts=%v, unique_contexts=%d\n",
		strings.Join(sequence, " -> "), counts, len(contextReferences),
	)
}

func waitForWeights(
	client *http.Client,
	baseURL string,
	expected map[string]int,
	timeout time.Duration,
) error {
	deadline := time.Now().Add(timeout)
	observed := make(map[string]int, len(expected))
	var lastError error
	for time.Now().Before(deadline) {
		metrics, err := getMetrics(client, baseURL+constants.MetricsPath)
		if err != nil {
			lastError = err
			time.Sleep(100 * time.Millisecond)
			continue
		}
		expectedWeight, found := expected[metrics.InstanceID]
		if !found {
			return fmt.Errorf("metrics returned unexpected instance %q", metrics.InstanceID)
		}
		if metrics.Weight != expectedWeight {
			return fmt.Errorf("instance %q weight=%d, want %d", metrics.InstanceID, metrics.Weight, expectedWeight)
		}
		if _, found := observed[metrics.InstanceID]; !found {
			observed[metrics.InstanceID] = metrics.Weight
			fmt.Printf("ready instance=%s weight=%d (%d/%d)\n", metrics.InstanceID, metrics.Weight, len(observed), len(expected))
		}
		if len(observed) == len(expected) {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	if lastError == nil {
		lastError = errors.New("not all weighted replicas were observed")
	}
	return fmt.Errorf("readiness timeout: observed %d/%d weighted instances: %w", len(observed), len(expected), lastError)
}

func getMetrics(client *http.Client, target string) (model.MetricsResponse, error) {
	response, err := client.Get(target)
	if err != nil {
		return model.MetricsResponse{}, err
	}
	defer response.Body.Close()
	if response.ProtoMajor != 2 || response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(response.Body, constants.CollectorMaxResponseBodyBytes))
		return model.MetricsResponse{}, fmt.Errorf("metrics response protocol=%s status=%s body=%s", response.Proto, response.Status, body)
	}
	var payload model.MetricsResponse
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		return model.MetricsResponse{}, fmt.Errorf("decode metrics response: %w", err)
	}
	if payload.InstanceID == "" || payload.Weight <= 0 || payload.ActiveRequests < 0 {
		return model.MetricsResponse{}, fmt.Errorf("invalid metrics response: %+v", payload)
	}
	return payload, nil
}

func createWeightedSession(
	client *http.Client,
	baseURL string,
	requestIndex int,
) (model.CreateSMContextResponse, error) {
	payload := model.CreateSMContextRequest{
		SUPI:         fmt.Sprintf("imsi-452050000%06d", requestIndex+1),
		GPSI:         fmt.Sprintf("msisdn-84910%06d", requestIndex+1),
		PDUSessionID: requestIndex + 1,
		DNN:          "v-internet",
		SNSSAI:       model.SNSSAI{SST: 1, SD: "000001"},
		ServingNFID:  fmt.Sprintf("weighted-e2e-amf-%d", requestIndex+1),
		ANType:       "3GPP_ACCESS",
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return model.CreateSMContextResponse{}, fmt.Errorf("encode request: %w", err)
	}
	request, err := http.NewRequest(
		http.MethodPost,
		baseURL+constants.CreateSMContextPath,
		bytes.NewReader(body),
	)
	if err != nil {
		return model.CreateSMContextResponse{}, fmt.Errorf("create request: %w", err)
	}
	request.Header.Set("Content-Type", constants.ContentTypeJSON)
	response, err := client.Do(request)
	if err != nil {
		return model.CreateSMContextResponse{}, fmt.Errorf("send request: %w", err)
	}
	defer response.Body.Close()
	if response.ProtoMajor != 2 {
		_, _ = io.Copy(io.Discard, response.Body)
		return model.CreateSMContextResponse{}, fmt.Errorf("unexpected protocol %s", response.Proto)
	}
	if response.StatusCode != http.StatusCreated {
		errorBody, _ := io.ReadAll(io.LimitReader(response.Body, constants.CreateSMContextMaxBodyBytes))
		return model.CreateSMContextResponse{}, fmt.Errorf("unexpected status %s: %s", response.Status, errorBody)
	}
	var result model.CreateSMContextResponse
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		return model.CreateSMContextResponse{}, fmt.Errorf("decode response: %w", err)
	}
	if result.SUPI != payload.SUPI || result.PDUSessionID != payload.PDUSessionID ||
		result.HandledBy == "" || result.SMContextRef == "" || result.Status != constants.SMContextActive {
		return model.CreateSMContextResponse{}, fmt.Errorf("invalid response: %+v", result)
	}
	if location := response.Header.Get("Location"); location != result.SMContextRef {
		return model.CreateSMContextResponse{}, fmt.Errorf("Location %q does not match smContextRef %q", location, result.SMContextRef)
	}
	return result, nil
}

func validateWeightedCycles(sequence []string, expected map[string]int) error {
	if len(sequence) != weightedCycleLength*weightedCycles {
		return fmt.Errorf("sequence length=%d, want %d", len(sequence), weightedCycleLength*weightedCycles)
	}
	for cycle := range weightedCycles {
		start := cycle * weightedCycleLength
		counts := countInstances(sequence[start : start+weightedCycleLength])
		for instanceID, want := range expected {
			if counts[instanceID] != want {
				return fmt.Errorf("cycle %d instance %q count=%d, want %d: %v", cycle+1, instanceID, counts[instanceID], want, sequence[start:start+weightedCycleLength])
			}
		}
	}
	for index := range weightedCycleLength {
		if sequence[index] != sequence[index+weightedCycleLength] {
			return fmt.Errorf("weighted sequence is unstable at position %d: %q != %q", index, sequence[index], sequence[index+weightedCycleLength])
		}
	}
	return nil
}

func countInstances(sequence []string) map[string]int {
	counts := make(map[string]int)
	for _, instanceID := range sequence {
		counts[instanceID]++
	}
	return counts
}

func fail(err error) {
	if err == nil {
		err = errors.New("unknown E2E failure")
	}
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
