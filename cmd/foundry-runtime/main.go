// Package main implements foundry-runtime, the container entrypoint that
// serves a PromptKit pack over the Azure AI Foundry hosted-agent protocol
// contracts.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	"github.com/AltairaLabs/PromptKit/runtime/prompt"
)

// Version is the runtime build version, set at link time by the Dockerfile.
var Version = "dev"

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	if err := run(context.Background(), log); err != nil {
		log.Error("fatal", "error", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, log *slog.Logger) error {
	cfg, err := loadConfig(os.Getenv)
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}

	packFile, err := resolvePackFile(cfg, packDirCandidates(os.Getenv))
	if err != nil {
		return fmt.Errorf("resolve pack: %w", err)
	}

	pack, err := prompt.LoadPack(packFile)
	if err != nil {
		return fmt.Errorf("load pack: %w", err)
	}

	agentName, err := resolveAgentName(cfg, pack)
	if err != nil {
		return err
	}

	opts, err := buildSDKOptions(cfg)
	if err != nil {
		return fmt.Errorf("provider bindings: %w", err)
	}

	shutdownTracing, traceOpts := setupTracing(cfg, log)
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		if shutdownErr := shutdownTracing(shutdownCtx); shutdownErr != nil {
			log.Error("tracing shutdown", "error", shutdownErr)
		}
	}()
	opts = append(opts, traceOpts...)

	specs, err := parseToolSpecs(cfg.ToolSpecsJSON)
	if err != nil {
		return fmt.Errorf("tool specs: %w", err)
	}

	log.Info("runtime configured",
		"agent", agentName,
		"pack", packFile,
		"foundry_agent", cfg.FoundryAgent,
		"foundry_version", cfg.FoundryVersion,
		"azure_endpoint", cfg.AzureEndpoint,
		"provider_options", len(opts),
		"tool_specs", len(specs))

	mux := buildMux(
		newTurnFunc(packFile, agentName, opts, specs),
		newStreamFunc(packFile, agentName, opts, specs),
	)

	return runServer(ctx, log, listenAddr(cfg), mux)
}
