//go:build ignore

package main

import (
	"fmt"
	"net/http"
	"os"
	"time"
)

func main() {
	target := "http://localhost:18080/health"
	if len(os.Args) > 1 {
		target = os.Args[1]
	}

	protocols := new(http.Protocols)
	protocols.SetUnencryptedHTTP2(true)
	transport := &http.Transport{Protocols: protocols}
	defer transport.CloseIdleConnections()

	client := &http.Client{
		Transport: transport,
		Timeout:   5 * time.Second,
	}
	response, err := client.Get(target)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer response.Body.Close()

	fmt.Printf("protocol=%s status=%s\n", response.Proto, response.Status)
	if response.ProtoMajor != 2 || response.StatusCode != http.StatusOK {
		os.Exit(1)
	}
}
