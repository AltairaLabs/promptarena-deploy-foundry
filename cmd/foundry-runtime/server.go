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

// moduleName identifies this runtime in the x-platform-server header.
const moduleName = "promptarena-deploy-foundry"

// buildMux registers the routes this container serves, wrapped so every
// response carries the platform headers Foundry's own servers send.
//
// voice may be nil when the pack declares no speech bindings; the route is
// then left unregistered rather than answering with a socket that can do
// nothing.
func buildMux(turn turnFunc, stream streamFunc, voice http.Handler) *http.ServeMux {
	mux := http.NewServeMux()
	mux.Handle("GET "+routeReadiness, withPlatformHeaders(newReadinessHandler()))
	mux.Handle(routeInvocations, withPlatformHeaders(newInvocationsHandler(turn, stream)))
	if voice != nil {
		// Not wrapped: the upgrade response is the platform's own handshake.
		mux.Handle(routeInvocationsWS, voice)
	}
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

	// ctx is already canceled by the time we get here, so strip the
	// cancellation and keep its values for the drain window.
	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownTimeout)
	defer cancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shutdown: %w", err)
	}

	log.Info("shutdown complete")
	return nil
}
