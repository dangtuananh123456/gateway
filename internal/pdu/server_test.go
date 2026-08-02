package pdu

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"go.uber.org/goleak"
)

func TestMain(testMain *testing.M) {
	goleak.VerifyTestMain(testMain)
}

func TestServerAcceptsHTTP1(t *testing.T) {
	protocol := make(chan int, 1)
	running := startPDUServer(t, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		protocol <- request.ProtoMajor
		writer.WriteHeader(http.StatusNoContent)
	}))

	response, err := running.client.Get(running.url)
	if err != nil {
		t.Fatalf("send HTTP/1 request: %v", err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusNoContent || response.ProtoMajor != 1 {
		t.Errorf("response status/protocol = %d/HTTP%d, want 204/HTTP1", response.StatusCode, response.ProtoMajor)
	}
	select {
	case got := <-protocol:
		if got != 1 {
			t.Errorf("handler protocol = HTTP%d, want HTTP1", got)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for handler")
	}
}

func TestServerMultipleStartStopCycles(t *testing.T) {
	for cycle := range 10 {
		t.Run(fmt.Sprintf("cycle_%d", cycle), func(t *testing.T) {
			running := startPDUServer(t, http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				writer.WriteHeader(http.StatusNoContent)
			}))
			response, err := running.client.Get(running.url)
			if err != nil {
				t.Fatalf("send request: %v", err)
			}
			response.Body.Close()

			running.cancel()
			if err := waitForServer(t, running.done); err != nil {
				t.Fatalf("shutdown server: %v", err)
			}
		})
	}
}

func TestServerGracefulShutdownWaitsForRequest(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	running := startPDUServer(t, http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		close(started)
		<-release
		writer.WriteHeader(http.StatusNoContent)
	}))

	responseDone := make(chan error, 1)
	go func() {
		response, err := running.client.Get(running.url)
		if err == nil {
			response.Body.Close()
		}
		responseDone <- err
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("request did not reach handler")
	}

	running.cancel()
	select {
	case err := <-running.done:
		t.Fatalf("server stopped before in-flight request completed: %v", err)
	case <-time.After(25 * time.Millisecond):
	}
	close(release)
	if err := <-responseDone; err != nil {
		t.Fatalf("in-flight request failed during graceful shutdown: %v", err)
	}
	if err := waitForServer(t, running.done); err != nil {
		t.Fatalf("shutdown server: %v", err)
	}
}

func TestClientCancellationReachesRequestContext(t *testing.T) {
	started := make(chan struct{})
	canceled := make(chan error, 1)
	running := startPDUServer(t, http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
		close(started)
		<-request.Context().Done()
		canceled <- request.Context().Err()
	}))

	ctx, cancel := context.WithCancel(context.Background())
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, running.url, nil)
	if err != nil {
		t.Fatalf("create request: %v", err)
	}
	requestDone := make(chan error, 1)
	go func() {
		response, requestErr := running.client.Do(request)
		if response != nil {
			response.Body.Close()
		}
		requestDone <- requestErr
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("request did not reach handler")
	}
	cancel()

	select {
	case err := <-canceled:
		if !strings.Contains(err.Error(), "canceled") {
			t.Errorf("handler context error = %v, want cancellation", err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancellation did not reach handler")
	}
	select {
	case err := <-requestDone:
		if err == nil {
			t.Error("client request error = nil, want cancellation")
		}
	case <-time.After(time.Second):
		t.Fatal("client request did not stop")
	}
}

func TestRunRejectsNilHandler(t *testing.T) {
	err := Run(context.Background(), testPDUConfig("127.0.0.1:0"), nil)
	if err == nil || !strings.Contains(err.Error(), "handler must not be nil") {
		t.Fatalf("Run() error = %v, want nil handler error", err)
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
		testPDUConfig(listener.Addr().String()),
		http.NotFoundHandler(),
	)
	if err == nil || !strings.Contains(err.Error(), "listen on") {
		t.Fatalf("Run() error = %v, want listen error", err)
	}
}

type runningPDUServer struct {
	cancel context.CancelFunc
	done   <-chan error
	client *http.Client
	url    string
}

func startPDUServer(t *testing.T, handler http.Handler) runningPDUServer {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	stopped := make(chan struct{})
	go func() {
		done <- serve(ctx, testPDUConfig(listener.Addr().String()), handler, listener)
		close(stopped)
	}()

	transport := &http.Transport{DisableKeepAlives: true}
	t.Cleanup(func() {
		cancel()
		transport.CloseIdleConnections()
		select {
		case <-stopped:
		case <-time.After(2 * time.Second):
			t.Error("timed out cleaning up PDU server")
		}
	})
	return runningPDUServer{
		cancel: cancel,
		done:   done,
		client: &http.Client{Transport: transport, Timeout: 2 * time.Second},
		url:    "http://" + listener.Addr().String(),
	}
}

func waitForServer(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for PDU server shutdown")
		return nil
	}
}

func testPDUConfig(address string) ServerConfig {
	return ServerConfig{
		Address:           address,
		ReadHeaderTimeout: time.Second,
		IdleTimeout:       time.Second,
		ShutdownTimeout:   time.Second,
		MaxHeaderBytes:    1 << 20,
	}
}
