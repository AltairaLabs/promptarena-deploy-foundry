package main

import (
	"context"
	"log/slog"

	"github.com/AltairaLabs/PromptKit/runtime/telemetry"
	"github.com/AltairaLabs/PromptKit/sdk"
)

// serviceName identifies this runtime in traces.
const serviceName = "foundry-runtime"

// tracingShutdown flushes and shuts down the trace exporter.
type tracingShutdown func(context.Context) error

// noopShutdown is returned whenever tracing is not active.
func noopShutdown(context.Context) error { return nil }

// setupTracing configures OTLP export when enabled, returning a shutdown
// function and the SDK options that attach the tracer to conversations.
//
// The options are returned together with the shutdown so a caller cannot start
// an exporter and then forget to trace the conversation. With them attached the
// SDK emits spans for pipeline, provider, tool and middleware events, and the
// eval listener records gen_ai.evaluation.score — the scores every turn already
// computes and otherwise discards.
//
// A misconfiguration disables tracing rather than failing the container: an
// agent that serves without traces is better than one that does not serve.
func setupTracing(cfg *runtimeConfig, log *slog.Logger) (tracingShutdown, []sdk.Option) {
	if !cfg.TracingEnabled {
		log.Info("tracing disabled")
		return noopShutdown, nil
	}
	if cfg.OTLPEndpoint == "" {
		log.Warn("tracing enabled but no OTLP endpoint configured; tracing stays off",
			"variable", envOTLPEndpoint)
		return noopShutdown, nil
	}

	provider, err := telemetry.NewTracerProvider(
		context.Background(), cfg.OTLPEndpoint, serviceName)
	if err != nil {
		log.Error("failed to create tracer provider; continuing without tracing",
			"endpoint", cfg.OTLPEndpoint, "error", err)
		return noopShutdown, nil
	}

	telemetry.SetupPropagation()
	log.Info("tracing enabled", "endpoint", cfg.OTLPEndpoint, "service", serviceName)

	return provider.Shutdown, []sdk.Option{sdk.WithTracerProvider(provider)}
}
