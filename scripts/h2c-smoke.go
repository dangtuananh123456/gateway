//go:build ignore

package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"time"
)

func main() {
	target := "http://localhost:18080/health"
	expectedInstances := 3
	if len(os.Args) > 1 {
		target = os.Args[1]
	}
	if len(os.Args) > 2 {
		value, err := strconv.Atoi(os.Args[2])
		if err != nil || value <= 0 {
			fmt.Fprintln(os.Stderr, "expected instance count must be a positive integer")
			os.Exit(2)
		}
		expectedInstances = value
	}

	protocols := new(http.Protocols)
	protocols.SetUnencryptedHTTP2(true)
	transport := &http.Transport{Protocols: protocols}
	defer transport.CloseIdleConnections()

	client := &http.Client{Transport: transport, Timeout: 2 * time.Second}
	deadline := time.Now().Add(30 * time.Second)
	instances := make(map[string]struct{}, expectedInstances)
	var lastError error
	for time.Now().Before(deadline) {
		instanceID, err := probe(client, target)
		if err == nil {
			if _, found := instances[instanceID]; !found {
				instances[instanceID] = struct{}{}
				fmt.Printf("observed instance=%s (%d/%d)\n", instanceID, len(instances), expectedInstances)
			}
			if len(instances) >= expectedInstances {
				fmt.Printf("h2c smoke passed: %d PDU replicas discovered through Gateway\n", len(instances))
				return
			}
		} else {
			lastError = err
		}
		time.Sleep(100 * time.Millisecond)
	}
	fmt.Fprintf(os.Stderr, "h2c smoke failed: observed %d/%d PDU replicas: %v\n", len(instances), expectedInstances, lastError)
	os.Exit(1)
}

func probe(client *http.Client, target string) (string, error) {
	response, err := client.Get(target)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	if response.ProtoMajor != 2 {
		_, _ = io.Copy(io.Discard, response.Body)
		return "", fmt.Errorf("unexpected protocol %s", response.Proto)
	}
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, response.Body)
		return "", fmt.Errorf("unexpected status %s", response.Status)
	}
	var payload struct {
		InstanceID string `json:"instanceId"`
	}
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		return "", fmt.Errorf("decode health response: %w", err)
	}
	if payload.InstanceID == "" {
		return "", fmt.Errorf("health response has an empty instanceId")
	}
	return payload.InstanceID, nil
}
