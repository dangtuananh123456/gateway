package server

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"go.uber.org/goleak"
)

func TestMain(testMain *testing.M) {
	goleak.VerifyTestMain(testMain)
}

func TestGracefulShutdownWaitsForInflightRequest(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var signalStarted sync.Once

	handler := http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		signalStarted.Do(func() {
			close(started)
		})
		<-release
		writer.WriteHeader(http.StatusOK)
		_, _ = writer.Write([]byte("completed"))
	})

	running := startServer(t, handler, time.Second)
	responseResult := request(running.client, running.url)

	waitForSignal(t, started, "request to start")
	running.cancel()

	select {
	case err := <-running.done:
		t.Fatalf("server stopped before the inflight request completed: %v", err)
	case <-time.After(50 * time.Millisecond):
	}

	close(release)
	response := waitForResponse(t, responseResult)
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want %d", response.StatusCode, http.StatusOK)
	}
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read response body: %v", err)
	}
	if string(body) != "completed" {
		t.Errorf("body = %q, want %q", body, "completed")
	}

	if err := waitForServer(t, running.done); err != nil {
		t.Fatalf("server shutdown: %v", err)
	}
}

func TestShutdownTimeoutCancelsSlowRequest(t *testing.T) {
	started := make(chan struct{})
	requestCanceled := make(chan struct{})
	var signalStarted sync.Once
	var signalCanceled sync.Once

	handler := http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
		signalStarted.Do(func() {
			close(started)
		})
		<-request.Context().Done()
		signalCanceled.Do(func() {
			close(requestCanceled)
		})
	})

	running := startServer(t, handler, 50*time.Millisecond)
	responseResult := request(running.client, running.url)

	waitForSignal(t, started, "request to start")
	running.cancel()

	err := waitForServer(t, running.done)
	if err == nil {
		t.Fatal("server shutdown error = nil, want deadline error")
	}
	if !strings.Contains(err.Error(), "shutdown HTTP server") ||
		!errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("server shutdown error = %v, want wrapped context deadline", err)
	}

	waitForSignal(t, requestCanceled, "request context cancellation")

	result := <-responseResult
	if result.err == nil {
		result.response.Body.Close()
		t.Fatal("request error = nil, want connection close error")
	}
}

func TestRunReturnsListenError(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve address: %v", err)
	}
	defer listener.Close()

	err = Run(
		context.Background(),
		testConfig(listener.Addr().String(), time.Second),
		http.NotFoundHandler(),
	)
	if err == nil {
		t.Fatal("Run() error = nil, want listen error")
	}
	if !strings.Contains(err.Error(), "listen on") {
		t.Errorf("Run() error = %q, want it to contain %q", err, "listen on")
	}
}

type runningServer struct {
	cancel context.CancelFunc
	client *http.Client
	done   <-chan error
	url    string
}

func startServer(
	t *testing.T,
	handler http.Handler,
	shutdownTimeout time.Duration,
) runningServer {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- serve(
			ctx,
			testConfig(listener.Addr().String(), shutdownTimeout),
			handler,
			listener,
		)
	}()

	transport := &http.Transport{
		DisableKeepAlives: true,
	}
	t.Cleanup(func() {
		cancel()
		transport.CloseIdleConnections()
	})

	return runningServer{
		cancel: cancel,
		client: &http.Client{
			Transport: transport,
			Timeout:   2 * time.Second,
		},
		done: done,
		url:  "http://" + listener.Addr().String(),
	}
}

func testConfig(address string, shutdownTimeout time.Duration) Config {
	return Config{
		Address:           address,
		ReadHeaderTimeout: time.Second,
		IdleTimeout:       time.Second,
		ShutdownTimeout:   shutdownTimeout,
		MaxHeaderBytes:    1 << 20,
	}
}

type responseResult struct {
	response *http.Response
	err      error
}

func request(client *http.Client, url string) <-chan responseResult {
	result := make(chan responseResult, 1)
	go func() {
		response, err := client.Get(url)
		result <- responseResult{
			response: response,
			err:      err,
		}
	}()

	return result
}

func waitForResponse(t *testing.T, result <-chan responseResult) *http.Response {
	t.Helper()

	select {
	case response := <-result:
		if response.err != nil {
			t.Fatalf("request: %v", response.err)
		}
		return response.response
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for response")
		return nil
	}
}

func waitForServer(t *testing.T, done <-chan error) error {
	t.Helper()

	select {
	case err := <-done:
		return err
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for server shutdown")
		return nil
	}
}

func waitForSignal(t *testing.T, signal <-chan struct{}, description string) {
	t.Helper()

	select {
	case <-signal:
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for %s", description)
	}
}
