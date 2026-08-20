package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os/signal"
	"syscall"
	"time"
)

const (
	shutdownTimeout = 10 * time.Second
	// readHeaderTimeout bounds slow-header clients. It is deliberately generous:
	// the platform relays long-lived SSE through this server, so only the header
	// phase is bounded here.
	readHeaderTimeout = 30 * time.Second
)

// buildMux registers the routes this container serves.
func buildMux(turn turnFunc, stream streamFunc) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+routeReadiness, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.Handle(routeInvocations, newInvocationsHandler(turn, stream))
	return mux
}

// listenAddr resolves the address to bind. Foundry injects PORT and expects the
// container to honor it rather than hardcode the documented default.
func listenAddr(cfg *runtimeConfig) string {
	return net.JoinHostPort("0.0.0.0", cfg.Port)
}

// runServer listens on addr and serves until the context is canceled or a
// termination signal arrives.
func runServer(ctx context.Context, log *slog.Logger, addr string, mux *http.ServeMux) error {
	ctx, stop := signal.NotifyContext(ctx, syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", addr, err)
	}

	srv := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: readHeaderTimeout,
	}

	errCh := make(chan error, 1)
	go func() {
		if serveErr := srv.Serve(ln); serveErr != nil &&
			!errors.Is(serveErr, http.ErrServerClosed) {
			errCh <- serveErr
		}
	}()

	log.Info("foundry-runtime listening", "addr", ln.Addr().String(), "version", Version)

	select {
	case <-ctx.Done():
		log.Info("shutdown signal received")
	case serveErr := <-errCh:
		return fmt.Errorf("serve: %w", serveErr)
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shutdown: %w", err)
	}

	log.Info("shutdown complete")
	return nil
}
