package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"testing"
	"time"
)

// freePort reserves a port and releases it, so the server can bind it.
func freePort(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}
	defer func() { _ = ln.Close() }()

	_, port, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		t.Fatalf("split addr: %v", err)
	}
	return port
}

// The container must serve until told to stop and then shut down cleanly —
// the platform reclaims a session's sandbox by signalling the process.
func TestRunServerServesThenShutsDownCleanly(t *testing.T) {
	port := freePort(t)
	mux := buildMux(okTurn, streamOK, nil)

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- runServer(ctx, discardLogger(), "127.0.0.1:"+port, mux) }()

	url := fmt.Sprintf("http://127.0.0.1:%s%s", port, routeReadiness)
	waitFor(t, func() bool {
		resp, err := http.Get(url) //nolint:noctx // liveness poll in a test
		if err != nil {
			return false
		}
		_ = resp.Body.Close()
		return resp.StatusCode == http.StatusOK
	})

	cancel()
	select {
	case err := <-errCh:
		if err != nil {
			t.Errorf("runServer = %v, want a clean shutdown", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("runServer did not return after its context was canceled")
	}
}

// A port already in use must fail loudly at startup rather than leave a
// container that reports ready and serves nothing.
func TestRunServerReportsABindFailure(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = ln.Close() }()

	err = runServer(context.Background(), discardLogger(), ln.Addr().String(),
		buildMux(okTurn, streamOK, nil))
	if err == nil {
		t.Fatal("runServer succeeded binding a port already in use")
	}
}
