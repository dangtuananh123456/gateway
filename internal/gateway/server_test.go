package gateway

import (
	"context"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dangtuananh123456/gateway/pkg/constants"
	"go.uber.org/goleak"
)

func TestMain(testMain *testing.M) {
	goleak.VerifyTestMain(testMain)
}

func TestServerAcceptsUnencryptedHTTP2(t *testing.T) {
	requestProtocol := make(chan int, 1)
	handler := http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requestProtocol <- request.ProtoMajor
		writer.WriteHeader(http.StatusNoContent)
	})
	running := startServer(t, handler)

	response, err := running.h2cClient.Get(running.url + constants.CreateSMContextPath)
	if err != nil {
		t.Fatalf("send h2c request: %v", err)
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusNoContent {
		t.Errorf("status = %d, want %d", response.StatusCode, http.StatusNoContent)
	}
	if response.ProtoMajor != 2 {
		t.Errorf("response protocol major = %d, want 2", response.ProtoMajor)
	}

	select {
	case protocol := <-requestProtocol:
		if protocol != 2 {
			t.Errorf("request protocol major = %d, want 2", protocol)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for handler protocol")
	}
}

func TestServerRejectsHTTP1BeforeHandler(t *testing.T) {
	var handlerCalls atomic.Int64
	running := startServer(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		handlerCalls.Add(1)
	}))

	http1Protocols := new(http.Protocols)
	http1Protocols.SetHTTP1(true)
	http1Transport := &http.Transport{
		DisableKeepAlives: true,
		Protocols:         http1Protocols,
	}
	t.Cleanup(http1Transport.CloseIdleConnections)

	client := &http.Client{
		Transport: http1Transport,
		Timeout:   time.Second,
	}
	response, err := client.Get(running.url + constants.CreateSMContextPath)
	if err == nil {
		response.Body.Close()
		t.Fatal("HTTP/1.1 request error = nil, want protocol rejection")
	}
	if calls := handlerCalls.Load(); calls != 0 {
		t.Errorf("handler calls = %d, want 0", calls)
	}
}

func TestGatewayProtocolsEnableOnlyUnencryptedHTTP2(t *testing.T) {
	protocols := h2cOnlyProtocols()
	if protocols.HTTP1() {
		t.Error("HTTP/1.0 and HTTP/1.1 are enabled, want disabled")
	}
	if protocols.HTTP2() {
		t.Error("HTTP/2 over TLS is enabled, want disabled")
	}
	if !protocols.UnencryptedHTTP2() {
		t.Error("unencrypted HTTP/2 is disabled, want enabled")
	}
}

func TestServerGracefullyShutsDown(t *testing.T) {
	running := startServer(t, http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusNoContent)
	}))

	response, err := running.h2cClient.Get(running.url)
	if err != nil {
		t.Fatalf("send h2c request: %v", err)
	}
	response.Body.Close()

	running.h2cClient.CloseIdleConnections()
	running.cancel()
	select {
	case err := <-running.done:
		if err != nil {
			t.Fatalf("server shutdown: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for server shutdown")
	}
}

func TestRunRejectsNilHandler(t *testing.T) {
	err := Run(context.Background(), testConfig("127.0.0.1:0"), nil)
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
		testConfig(listener.Addr().String()),
		http.NotFoundHandler(),
	)
	if err == nil || !strings.Contains(err.Error(), "listen on") {
		t.Fatalf("Run() error = %v, want listen error", err)
	}
}

type runningServer struct {
	cancel    context.CancelFunc
	done      <-chan error
	h2cClient *http.Client
	url       string
}

func startServer(t *testing.T, handler http.Handler) runningServer {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	stopped := make(chan struct{})
	go func() {
		done <- serve(ctx, testConfig(listener.Addr().String()), handler, listener)
		close(stopped)
	}()

	h2cTransport := &http.Transport{
		Protocols: h2cOnlyProtocols(),
	}
	t.Cleanup(func() {
		cancel()
		h2cTransport.CloseIdleConnections()
		select {
		case <-stopped:
		case <-time.After(2 * time.Second):
			t.Error("timed out cleaning up gateway server")
		}
	})

	return runningServer{
		cancel: cancel,
		done:   done,
		h2cClient: &http.Client{
			Transport: h2cTransport,
			Timeout:   2 * time.Second,
		},
		url: "http://" + listener.Addr().String(),
	}
}

func testConfig(address string) ServerConfig {
	return ServerConfig{
		Address:           address,
		ReadHeaderTimeout: time.Second,
		IdleTimeout:       time.Second,
		ShutdownTimeout:   time.Second,
		MaxHeaderBytes:    1 << 20,
	}
}
