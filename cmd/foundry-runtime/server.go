package main

import (
	"net"
	"net/http"
	"os"
	"time"
)

// defaultPort is the port Foundry documents for hosted agents. The platform
// injects PORT, so this is only the fallback for local runs.
const defaultPort = "8088"

// readHeaderTimeout bounds slow-header clients. It is deliberately generous:
// the platform relays long-lived SSE and WebSocket sessions through this
// server, so only the header phase is bounded here.
const readHeaderTimeout = 30 * time.Second

// listenAddr resolves the address to bind. Foundry injects PORT and expects the
// container to honor it rather than hardcode the documented default.
func listenAddr(getenv func(string) string) string {
	port := getenv("PORT")
	if port == "" {
		port = defaultPort
	}
	return net.JoinHostPort("0.0.0.0", port)
}

// newMux builds the HTTP routes the container serves.
//
// /readiness is required of every hosted agent. Microsoft's Python and C#
// protocol libraries provide it for free; a Go container has to serve it
// itself, and an agent version that does not stays stuck in `creating`.
func newMux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /readiness", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	return mux
}

// newServer builds the HTTP server for the runtime.
func newServer() *http.Server {
	return &http.Server{
		Addr:              listenAddr(os.Getenv),
		Handler:           newMux(),
		ReadHeaderTimeout: readHeaderTimeout,
	}
}
