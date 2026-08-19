// Package main implements foundry-runtime, the container entrypoint that
// serves the Azure AI Foundry hosted-agent protocol contracts for a PromptKit
// pack.
package main

import (
	"errors"
	"fmt"
	"net/http"
	"os"
)

// Version is the runtime build version, set at link time by the Dockerfile.
var Version = "dev"

func main() {
	srv := newServer()
	fmt.Fprintf(os.Stderr, "foundry-runtime %s listening on %s\n", Version, srv.Addr)

	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		fmt.Fprintf(os.Stderr, "foundry-runtime: %v\n", err)
		os.Exit(1)
	}
}
