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
	expectedRoundRobinInstances = 3
	roundRobinCycles            = 2
)

func main() {
	baseURL := "http://localhost:18080"
	if len(os.Args) > 1 {
		baseURL = strings.TrimRight(os.Args[1], "/")
	}

	protocols := new(http.Protocols)
	protocols.SetUnencryptedHTTP2(true)
	transport := &http.Transport{Protocols: protocols}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 3 * time.Second}

	instances, err := waitForInstances(client, baseURL, expectedRoundRobinInstances, 30*time.Second)
	if err != nil {
		fail(err)
	}
	sequence := make([]string, 0, expectedRoundRobinInstances*roundRobinCycles)
	contextReferences := make(map[string]struct{}, cap(sequence))
	for requestIndex := range cap(sequence) {
		response, err := createSession(client, baseURL, requestIndex)
		if err != nil {
			fail(fmt.Errorf("request %d: %w", requestIndex+1, err))
		}
		if _, found := instances[response.HandledBy]; !found {
			fail(fmt.Errorf("request %d handled by undiscovered instance %q", requestIndex+1, response.HandledBy))
		}
		if _, duplicate := contextReferences[response.SMContextRef]; duplicate {
			fail(fmt.Errorf("request %d returned duplicate context reference %q", requestIndex+1, response.SMContextRef))
		}
		contextReferences[response.SMContextRef] = struct{}{}
		sequence = append(sequence, response.HandledBy)
		fmt.Printf("request=%d handledBy=%s context=%s\n", requestIndex+1, response.HandledBy, response.SMContextRef)
	}
	if err := validateRoundRobin(sequence, expectedRoundRobinInstances); err != nil {
		fail(err)
	}
	fmt.Printf("Round Robin E2E passed: sequence=%s, unique_contexts=%d\n",
		strings.Join(sequence, " -> "), len(contextReferences))
}

func waitForInstances(
	client *http.Client,
	baseURL string,
	expected int,
	timeout time.Duration,
) (map[string]struct{}, error) {
	deadline := time.Now().Add(timeout)
	instances := make(map[string]struct{}, expected)
	var lastError error
	for time.Now().Before(deadline) {
		instanceID, err := getHealth(client, baseURL+constants.HealthPath)
		if err == nil {
			if _, found := instances[instanceID]; !found {
				instances[instanceID] = struct{}{}
				fmt.Printf("ready instance=%s (%d/%d)\n", instanceID, len(instances), expected)
			}
			if len(instances) == expected {
				return instances, nil
			}
		} else {
			lastError = err
		}
		time.Sleep(100 * time.Millisecond)
	}
	if lastError == nil {
		lastError = errors.New("not all replicas were observed")
	}
	return nil, fmt.Errorf("readiness timeout: observed %d/%d instances: %w", len(instances), expected, lastError)
}

func getHealth(client *http.Client, target string) (string, error) {
	response, err := client.Get(target)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	if response.ProtoMajor != 2 || response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, response.Body)
		return "", fmt.Errorf("health response protocol=%s status=%s", response.Proto, response.Status)
	}
	var payload model.HealthResponse
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		return "", fmt.Errorf("decode health response: %w", err)
	}
	if payload.InstanceID == "" || payload.Status != constants.ServiceUp {
		return "", fmt.Errorf("invalid health response: %+v", payload)
	}
	return payload.InstanceID, nil
}

func createSession(
	client *http.Client,
	baseURL string,
	requestIndex int,
) (model.CreateSMContextResponse, error) {
	payload := model.CreateSMContextRequest{
		SUPI:         fmt.Sprintf("imsi-452040000%06d", requestIndex+1),
		GPSI:         fmt.Sprintf("msisdn-84900%06d", requestIndex+1),
		PDUSessionID: requestIndex + 1,
		DNN:          "v-internet",
		SNSSAI:       model.SNSSAI{SST: 1, SD: "000001"},
		ServingNFID:  fmt.Sprintf("e2e-amf-%d", requestIndex+1),
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
	decoder := json.NewDecoder(response.Body)
	if err := decoder.Decode(&result); err != nil {
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

func validateRoundRobin(sequence []string, instanceCount int) error {
	if len(sequence) != instanceCount*roundRobinCycles {
		return fmt.Errorf("sequence length=%d, want %d", len(sequence), instanceCount*roundRobinCycles)
	}
	firstCycle := make(map[string]struct{}, instanceCount)
	for index := range instanceCount {
		firstCycle[sequence[index]] = struct{}{}
		if sequence[index+instanceCount] != sequence[index] {
			return fmt.Errorf("sequence does not repeat at position %d: %q != %q", index, sequence[index+instanceCount], sequence[index])
		}
	}
	if len(firstCycle) != instanceCount {
		return fmt.Errorf("first cycle contains %d unique instances, want %d: %v", len(firstCycle), instanceCount, sequence[:instanceCount])
	}
	return nil
}

func fail(err error) {
	if err == nil {
		err = errors.New("unknown E2E failure")
	}
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
