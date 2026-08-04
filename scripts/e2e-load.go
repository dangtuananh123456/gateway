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
	loadInstanceCount    = 3
	loadObservationDelay = 600 * time.Millisecond
	loadReadinessSettle  = time.Second
	loadRequestTimeout   = 6 * time.Second
)

type loadResult struct {
	index    int
	response model.CreateSMContextResponse
	err      error
}

func main() {
	baseURL := "http://localhost:18082"
	if len(os.Args) > 1 {
		baseURL = strings.TrimRight(os.Args[1], "/")
	}

	protocols := new(http.Protocols)
	protocols.SetUnencryptedHTTP2(true)
	transport := &http.Transport{Protocols: protocols}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: loadRequestTimeout}

	if err := waitForLoadGateway(client, baseURL, 30*time.Second); err != nil {
		fail(err)
	}
	// Give the immediate DNS/health rounds time to publish all three backends.
	time.Sleep(loadReadinessSettle)

	results := make(chan loadResult, loadInstanceCount)
	for requestIndex := range loadInstanceCount {
		go func(index int) {
			response, err := createLoadSession(client, baseURL, index)
			results <- loadResult{index: index, response: response, err: err}
		}(requestIndex)
		if requestIndex+1 < loadInstanceCount {
			time.Sleep(loadObservationDelay)
		}
	}

	responses := make([]model.CreateSMContextResponse, loadInstanceCount)
	contextReferences := make(map[string]struct{}, loadInstanceCount+1)
	for range loadInstanceCount {
		result := <-results
		if result.err != nil {
			fail(fmt.Errorf("busy request %d: %w", result.index+1, result.err))
		}
		responses[result.index] = result.response
		if _, duplicate := contextReferences[result.response.SMContextRef]; duplicate {
			fail(fmt.Errorf("busy request %d returned duplicate context reference %q", result.index+1, result.response.SMContextRef))
		}
		contextReferences[result.response.SMContextRef] = struct{}{}
	}

	expectedSequence := []string{"pdu-1", "pdu-2", "pdu-3"}
	for index, expectedInstance := range expectedSequence {
		if responses[index].HandledBy != expectedInstance {
			fail(fmt.Errorf(
				"busy request %d handled by %q, want %q; Gateway did not avoid the cached busy backends",
				index+1, responses[index].HandledBy, expectedInstance,
			))
		}
		fmt.Printf("busy_request=%d handledBy=%s context=%s\n", index+1, responses[index].HandledBy, responses[index].SMContextRef)
	}

	// All three delayed requests have finished. Wait for the next metrics polls;
	// with equal load 0, deterministic tie-break must select pdu-1 again.
	time.Sleep(loadObservationDelay)
	recovered, err := createLoadSession(client, baseURL, loadInstanceCount)
	if err != nil {
		fail(fmt.Errorf("recovery request: %w", err))
	}
	if recovered.HandledBy != "pdu-1" {
		fail(fmt.Errorf("recovery request handled by %q, want pdu-1 after load returned to zero", recovered.HandledBy))
	}
	if _, duplicate := contextReferences[recovered.SMContextRef]; duplicate {
		fail(fmt.Errorf("recovery request returned duplicate context reference %q", recovered.SMContextRef))
	}
	contextReferences[recovered.SMContextRef] = struct{}{}

	fmt.Printf("recovery_request handledBy=%s context=%s\n", recovered.HandledBy, recovered.SMContextRef)
	fmt.Printf(
		"Load-based E2E passed: busy_sequence=%s, recovered_to=%s, unique_contexts=%d\n",
		strings.Join(expectedSequence, " -> "), recovered.HandledBy, len(contextReferences),
	)
}

func waitForLoadGateway(client *http.Client, baseURL string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var lastError error
	for time.Now().Before(deadline) {
		response, err := client.Get(baseURL + constants.HealthPath)
		if err != nil {
			lastError = err
			time.Sleep(100 * time.Millisecond)
			continue
		}
		var payload model.HealthResponse
		decodeErr := json.NewDecoder(response.Body).Decode(&payload)
		response.Body.Close()
		if response.ProtoMajor == 2 && response.StatusCode == http.StatusOK &&
			decodeErr == nil && payload.Status == constants.ServiceUp && payload.InstanceID == "pdu-1" {
			fmt.Printf("ready instance=%s protocol=%s\n", payload.InstanceID, response.Proto)
			return nil
		}
		lastError = fmt.Errorf(
			"health response protocol=%s status=%s payload=%+v decode_error=%v",
			response.Proto, response.Status, payload, decodeErr,
		)
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("load Gateway readiness timeout: %w", lastError)
}

func createLoadSession(
	client *http.Client,
	baseURL string,
	requestIndex int,
) (model.CreateSMContextResponse, error) {
	payload := model.CreateSMContextRequest{
		SUPI:         fmt.Sprintf("imsi-452060000%06d", requestIndex+1),
		GPSI:         fmt.Sprintf("msisdn-84920%06d", requestIndex+1),
		PDUSessionID: requestIndex + 1,
		DNN:          "v-internet",
		SNSSAI:       model.SNSSAI{SST: 1, SD: "000001"},
		ServingNFID:  fmt.Sprintf("load-e2e-amf-%d", requestIndex+1),
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

func fail(err error) {
	if err == nil {
		err = errors.New("unknown E2E failure")
	}
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
