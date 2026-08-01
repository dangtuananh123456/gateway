package pdu

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"
)

// ServerConfig controls one PDU Session HTTP server lifecycle.
type ServerConfig struct {
	Address           string
	ReadHeaderTimeout time.Duration
	IdleTimeout       time.Duration
	ShutdownTimeout   time.Duration
	MaxHeaderBytes    int
}

// Run serves HTTP/1.1 requests until the server fails or ctx is canceled.
func Run(ctx context.Context, cfg ServerConfig, handler http.Handler) error {
	if handler == nil {
		return errors.New("run PDU server: handler must not be nil")
	}

	protocols := new(http.Protocols)
	protocols.SetHTTP1(true)

	httpServer := &http.Server{
		Addr:              cfg.Address,
		Handler:           handler,
		ReadHeaderTimeout: cfg.ReadHeaderTimeout,
		IdleTimeout:       cfg.IdleTimeout,
		MaxHeaderBytes:    cfg.MaxHeaderBytes,
		Protocols:         protocols,
		BaseContext: func(net.Listener) context.Context {
			return context.WithoutCancel(ctx)
		},
	}

	serveErrors := make(chan error, 1)
	go func() {
		serveErrors <- httpServer.ListenAndServe()
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
			return fmt.Errorf("shutdown PDU HTTP server: %w", err)
		}

		return normalizeServeError(<-serveErrors)
	}
}

func normalizeServeError(err error) error {
	if err == nil || errors.Is(err, http.ErrServerClosed) {
		return nil
	}

	return fmt.Errorf("serve PDU HTTP: %w", err)
}
