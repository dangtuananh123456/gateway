// Package gateway provides the public SMF Gateway HTTP server.
package gateway

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"
)

// ServerConfig controls the public h2c server lifecycle.
type ServerConfig struct {
	Address           string
	ReadHeaderTimeout time.Duration
	IdleTimeout       time.Duration
	ShutdownTimeout   time.Duration
	MaxHeaderBytes    int
}

// Run serves unencrypted HTTP/2 requests until the server fails or ctx is canceled.
func Run(ctx context.Context, cfg ServerConfig, handler http.Handler) error {
	if handler == nil {
		return errors.New("run gateway server: handler must not be nil")
	}

	listener, err := net.Listen("tcp", cfg.Address)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", cfg.Address, err)
	}

	return serve(ctx, cfg, handler, listener)
}

func serve(
	ctx context.Context,
	cfg ServerConfig,
	handler http.Handler,
	listener net.Listener,
) error {
	httpServer := &http.Server{
		Addr:              cfg.Address,
		Handler:           handler,
		ReadHeaderTimeout: cfg.ReadHeaderTimeout,
		IdleTimeout:       cfg.IdleTimeout,
		MaxHeaderBytes:    cfg.MaxHeaderBytes,
		Protocols:         h2cOnlyProtocols(),
		BaseContext: func(net.Listener) context.Context {
			return context.WithoutCancel(ctx)
		},
	}

	serveErrors := make(chan error, 1)
	go func() {
		serveErrors <- httpServer.Serve(listener)
	}()

	select {
	case err := <-serveErrors:
		return normalizeServeError(err)
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(
			context.WithoutCancel(ctx),
			cfg.ShutdownTimeout,
		)
		defer cancel()

		if err := httpServer.Shutdown(shutdownCtx); err != nil {
			_ = httpServer.Close()
			<-serveErrors
			return fmt.Errorf("shutdown gateway HTTP server: %w", err)
		}

		return normalizeServeError(<-serveErrors)
	}
}

func h2cOnlyProtocols() *http.Protocols {
	protocols := new(http.Protocols)
	protocols.SetUnencryptedHTTP2(true)
	return protocols
}

func normalizeServeError(err error) error {
	if err == nil || errors.Is(err, http.ErrServerClosed) {
		return nil
	}

	return fmt.Errorf("serve gateway HTTP: %w", err)
}
