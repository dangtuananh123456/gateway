package client

import (
	"context"
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

func TestNewH2CTransportEnablesOnlyUnencryptedHTTP2(t *testing.T) {
	transport := NewH2CTransport()
	t.Cleanup(transport.CloseIdleConnections)
	if transport.Protocols.HTTP1() || transport.Protocols.HTTP2() || !transport.Protocols.UnencryptedHTTP2() {
		t.Errorf("transport protocols = %s, want only unencrypted HTTP/2", transport.Protocols)
	}
	if !transport.DisableCompression {
		t.Error("DisableCompression = false, want response preserved for debugging")
	}
}

func TestServerStartsAndGracefullyStops(t *testing.T) {
	address := reserveClientAddress(t)
	cfg := validClientConfig()
	cfg.Address = address
	handler := http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusNoContent)
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Run(ctx, cfg, handler) }()

	deadline := time.Now().Add(time.Second)
	for {
		response, err := http.Get("http://" + address)
		if err == nil {
			response.Body.Close()
			if response.StatusCode != http.StatusNoContent {
				t.Errorf("status = %d, want 204", response.StatusCode)
			}
			break
		}
		if time.Now().After(deadline) {
			cancel()
			t.Fatalf("client server did not start: %v", err)
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Run() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("client server did not stop")
	}
}

func TestRunValidatesInputsAndListenFailure(t *testing.T) {
	cfg := validClientConfig()
	if err := Run(nil, cfg, http.NotFoundHandler()); err == nil {
		t.Fatal("Run(nil context) error = nil")
	}
	if err := Run(context.Background(), cfg, nil); err == nil {
		t.Fatal("Run(nil handler) error = nil")
	}
	if err := Run(context.Background(), Config{}, http.NotFoundHandler()); err == nil {
		t.Fatal("Run(invalid config) error = nil")
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve address: %v", err)
	}
	defer listener.Close()
	cfg.Address = listener.Addr().String()
	err = Run(context.Background(), cfg, http.NotFoundHandler())
	if err == nil || !strings.Contains(err.Error(), "listen") {
		t.Errorf("Run(occupied address) error = %v, want listen error", err)
	}
}

func validClientConfig() Config {
	return Config{
		Address: "127.0.0.1:18090", GatewayURL: "http://localhost:18080",
		RequestTimeout: time.Second, ShutdownTimeout: time.Second,
		ReadHeaderTimeout: time.Second, IdleTimeout: time.Second, MaxHeaderBytes: 1 << 20,
	}
}

func reserveClientAddress(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve address: %v", err)
	}
	address := listener.Addr().String()
	listener.Close()
	return address
}
