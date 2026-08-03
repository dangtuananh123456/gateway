package client

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
)

// NewH2CTransport creates the client-side transport required by the Gateway.
func NewH2CTransport() *http.Transport {
	protocols := new(http.Protocols)
	protocols.SetUnencryptedHTTP2(true)
	return &http.Transport{
		DisableCompression: true,
		Protocols:          protocols,
	}
}

// Run serves the browser UI until ctx is canceled.
func Run(ctx context.Context, cfg Config, handler http.Handler) error {
	if ctx == nil {
		return errors.New("run test client: context must not be nil")
	}
	if handler == nil {
		return errors.New("run test client: handler must not be nil")
	}
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("run test client: %w", err)
	}
	listener, err := net.Listen("tcp", cfg.Address)
	if err != nil {
		return fmt.Errorf("run test client: listen on %s: %w", cfg.Address, err)
	}
	server := &http.Server{
		Addr:              cfg.Address,
		Handler:           handler,
		ReadHeaderTimeout: cfg.ReadHeaderTimeout,
		IdleTimeout:       cfg.IdleTimeout,
		MaxHeaderBytes:    cfg.MaxHeaderBytes,
		BaseContext: func(net.Listener) context.Context {
			return context.WithoutCancel(ctx)
		},
	}
	serveErrors := make(chan error, 1)
	go func() { serveErrors <- server.Serve(listener) }()
	select {
	case err := <-serveErrors:
		return normalizeServerError(err)
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cfg.ShutdownTimeout)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			_ = server.Close()
			<-serveErrors
			return fmt.Errorf("shutdown test client: %w", err)
		}
		return normalizeServerError(<-serveErrors)
	}
}

func normalizeServerError(err error) error {
	if err == nil || errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return fmt.Errorf("serve test client: %w", err)
}
