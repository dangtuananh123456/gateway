//go:build ignore

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

type bridgeResponse struct {
	StatusCode int    `json:"statusCode"`
	Protocol   string `json:"protocol"`
	Body       string `json:"body"`
}

type backendList struct {
	Count     int `json:"count"`
	Instances []struct {
		InstanceID string `json:"instanceId"`
	} `json:"instances"`
}

type performanceReport struct {
	Algorithm    string `json:"algorithm"`
	Measurements []struct {
		Average bool `json:"average"`
	} `json:"measurements"`
}

func main() {
	baseURL := "http://localhost:18090"
	if len(os.Args) > 1 {
		baseURL = strings.TrimRight(os.Args[1], "/")
	}
	expectedBackends := 3
	if len(os.Args) > 2 {
		parsed, err := strconv.Atoi(os.Args[2])
		if err != nil || parsed <= 0 {
			fmt.Fprintln(os.Stderr, "expected backend count must be a positive integer")
			os.Exit(2)
		}
		expectedBackends = parsed
	}
	client := &http.Client{Timeout: 3 * time.Second}
	deadline := time.Now().Add(30 * time.Second)
	var lastError error
	for time.Now().Before(deadline) {
		if err := verifyUI(client, baseURL); err == nil {
			result, err := callBridge(client, baseURL, "/health")
			if err == nil && result.StatusCode == http.StatusOK && result.Protocol == "HTTP/2.0" &&
				strings.Contains(result.Body, `"instanceId"`) {
				backends, backendErr := callBridge(client, baseURL, "/gateway/backends")
				if backendErr == nil && backends.StatusCode == http.StatusOK && backends.Protocol == "HTTP/2.0" {
					var list backendList
					backendErr = json.Unmarshal([]byte(backends.Body), &list)
					if backendErr == nil && list.Count == expectedBackends && len(list.Instances) == expectedBackends {
						backendErr = verifyPerformanceRoutes(client, baseURL)
						if backendErr == nil {
							fmt.Printf("UI smoke passed: protocol=%s healthy_backends=%d performance_routes=3\n", backends.Protocol, list.Count)
							return
						}
					}
					if backendErr == nil {
						backendErr = fmt.Errorf("healthy backend count=%d instances=%d, want %d", list.Count, len(list.Instances), expectedBackends)
					}
				}
				lastError = backendErr
				time.Sleep(100 * time.Millisecond)
				continue
			}
			if err == nil {
				err = fmt.Errorf("unexpected bridge response: %+v", result)
			}
			lastError = err
		} else {
			lastError = err
		}
		time.Sleep(100 * time.Millisecond)
	}
	fmt.Fprintf(os.Stderr, "UI smoke failed: %v\n", lastError)
	os.Exit(1)
}

func verifyPerformanceRoutes(client *http.Client, baseURL string) error {
	routes := map[string]string{
		"/api/performance/round-robin": "round_robin",
		"/api/performance/weighted":    "weighted",
		"/api/performance/load":        "load",
	}
	for route, expectedAlgorithm := range routes {
		response, err := client.Get(baseURL + route)
		if err != nil {
			return fmt.Errorf("GET %s: %w", route, err)
		}
		var report performanceReport
		decodeErr := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&report)
		response.Body.Close()
		if response.StatusCode != http.StatusOK || decodeErr != nil {
			return fmt.Errorf("GET %s status=%s decode_error=%v", route, response.Status, decodeErr)
		}
		if report.Algorithm != expectedAlgorithm || len(report.Measurements) != 0 {
			return fmt.Errorf("GET %s returned invalid report", route)
		}
	}
	return nil
}

func verifyUI(client *http.Client, baseURL string) error {
	response, err := client.Get(baseURL + "/")
	if err != nil {
		return err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return err
	}
	if response.StatusCode != http.StatusOK || !bytes.Contains(body, []byte("SMF Gateway Test Console")) {
		return fmt.Errorf("UI response status=%s", response.Status)
	}
	return nil
}

func callBridge(client *http.Client, baseURL, path string) (bridgeResponse, error) {
	payload, _ := json.Marshal(map[string]any{
		"method": "GET",
		"path":   path,
	})
	response, err := client.Post(baseURL+"/api/request", "application/json", bytes.NewReader(payload))
	if err != nil {
		return bridgeResponse{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 1<<20))
		return bridgeResponse{}, fmt.Errorf("bridge status=%s body=%s", response.Status, body)
	}
	var result bridgeResponse
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		return bridgeResponse{}, err
	}
	return result, nil
}
