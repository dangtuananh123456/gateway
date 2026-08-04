//go:build ignore

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

type bridgeResponse struct {
	StatusCode int    `json:"statusCode"`
	Protocol   string `json:"protocol"`
	Body       string `json:"body"`
}

func main() {
	baseURL := "http://localhost:18090"
	if len(os.Args) > 1 {
		baseURL = strings.TrimRight(os.Args[1], "/")
	}
	client := &http.Client{Timeout: 3 * time.Second}
	deadline := time.Now().Add(30 * time.Second)
	var lastError error
	for time.Now().Before(deadline) {
		if err := verifyUI(client, baseURL); err == nil {
			result, err := callHealthBridge(client, baseURL)
			if err == nil && result.StatusCode == http.StatusOK && result.Protocol == "HTTP/2.0" &&
				strings.Contains(result.Body, `"instanceId"`) {
				fmt.Printf("UI smoke passed: Gateway status=%d protocol=%s body=%s\n", result.StatusCode, result.Protocol, result.Body)
				return
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

func callHealthBridge(client *http.Client, baseURL string) (bridgeResponse, error) {
	payload, _ := json.Marshal(map[string]any{
		"method": "GET",
		"path":   "/health",
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
