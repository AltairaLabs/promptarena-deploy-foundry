package main

import (
	"context"
	"testing"
)

// env builds a getenv function over a map, so config parsing is testable
// without mutating the process environment.
func env(pairs map[string]string) func(string) string {
	return func(k string) string { return pairs[k] }
}

func TestLoadConfigInlinePack(t *testing.T) {
	cfg, err := loadConfig(env(map[string]string{
		envPackJSON:  `{"id":"p"}`,
		envProviders: `[{"name":"default","role":"llm","type":"azure","model":"gpt-4o"}]`,
	}))
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if cfg.PackJSON != `{"id":"p"}` {
		t.Errorf("PackJSON = %q", cfg.PackJSON)
	}
	if cfg.Port != defaultPort {
		t.Errorf("Port = %q, want %q", cfg.Port, defaultPort)
	}
}

// The platform injects PORT and expects the container to honor it; 8088 is
// only the documented default for a local run.
func TestLoadConfigHonoursInjectedPort(t *testing.T) {
	cfg, err := loadConfig(env(map[string]string{
		envPackJSON: `{"id":"p"}`,
		"PORT":      "9001",
	}))
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if cfg.Port != "9001" {
		t.Errorf("Port = %q, want 9001", cfg.Port)
	}
}

// Without a pack there is nothing to serve, and failing at startup is far
// better than accepting traffic and erroring on every request.
func TestLoadConfigRequiresAPack(t *testing.T) {
	if _, err := loadConfig(env(map[string]string{})); err == nil {
		t.Fatal("loadConfig accepted a container with no pack")
	}
}

// Foundry injects the project endpoint; the runtime prefers its own name so
// the same image still runs on a host that injects nothing.
func TestLoadConfigReadsFoundryInjectedValues(t *testing.T) {
	cfg, err := loadConfig(env(map[string]string{
		envPackJSON:         `{"id":"p"}`,
		envFoundryProjectEP: "https://acct.services.ai.azure.com/api/projects/proj",
		envFoundrySessionID: "sess-123",
		envFoundryAgentName: "solo-pack",
	}))
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if cfg.ProjectEndpoint != "https://acct.services.ai.azure.com/api/projects/proj" {
		t.Errorf("ProjectEndpoint = %q", cfg.ProjectEndpoint)
	}
	if cfg.SessionID != "sess-123" {
		t.Errorf("SessionID = %q, want sess-123", cfg.SessionID)
	}
}

func TestLoadConfigTracing(t *testing.T) {
	cfg, err := loadConfig(env(map[string]string{
		envPackJSON:       `{"id":"p"}`,
		envTracingEnabled: "true",
		envOTLPEndpoint:   "https://collector:4318",
	}))
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if !cfg.TracingEnabled {
		t.Error("TracingEnabled = false, want true")
	}
	if cfg.OTLPEndpoint != "https://collector:4318" {
		t.Errorf("OTLPEndpoint = %q", cfg.OTLPEndpoint)
	}
}

func TestLoadConfigRejectsBadTracingFlag(t *testing.T) {
	_, err := loadConfig(env(map[string]string{
		envPackJSON:       `{"id":"p"}`,
		envTracingEnabled: "yes-please",
	}))
	if err == nil {
		t.Fatal("loadConfig accepted a non-boolean tracing flag")
	}
}

func TestLoadConfigTracingOffByDefault(t *testing.T) {
	cfg, err := loadConfig(env(map[string]string{envPackJSON: `{"id":"p"}`}))
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if cfg.TracingEnabled {
		t.Error("TracingEnabled = true; an unconfigured deployment must send nothing")
	}
}

// Foundry injects the container's identity as FOUNDRY_AGENT_INSTANCE_CLIENT_ID.
// Confirmed by probing a live sandbox, which showed no AZURE_CLIENT_ID at all —
// so reading only that name left the runtime with no identity to present.
func TestLoadConfigReadsTheInjectedAgentIdentity(t *testing.T) {
	cfg, err := loadConfig(env(map[string]string{
		envPackJSON:        `{"id":"p"}`,
		envFoundryClientID: "cfe80883-d40b-4aa2-94fa-2d3e15873e8c",
	}))
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if cfg.ClientID != "cfe80883-d40b-4aa2-94fa-2d3e15873e8c" {
		t.Errorf("ClientID = %q, want the injected instance client id", cfg.ClientID)
	}
}

// AZURE_CLIENT_ID still works, so the same image runs on hosts that use the
// conventional name.
func TestLoadConfigFallsBackToAzureClientID(t *testing.T) {
	cfg, err := loadConfig(env(map[string]string{
		envPackJSON: `{"id":"p"}`,
		envClientID: "conventional-id",
	}))
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if cfg.ClientID != "conventional-id" {
		t.Errorf("ClientID = %q, want the fallback", cfg.ClientID)
	}
}

func TestListenAddrBindsAllInterfaces(t *testing.T) {
	got := listenAddr(&runtimeConfig{Port: "8088"})

	if got != "0.0.0.0:8088" {
		t.Errorf("listenAddr = %q, want 0.0.0.0:8088", got)
	}
}

// Tracing is off unless asked for, so an unconfigured deployment sends nothing
// and pays nothing. Shutdown must still be safe to call.
func TestSetupTracingDisabled(t *testing.T) {
	shutdown, opts := setupTracing(&runtimeConfig{}, discardLogger())

	if len(opts) != 0 {
		t.Errorf("opts = %d, want none with tracing off", len(opts))
	}
	if err := shutdown(context.Background()); err != nil {
		t.Errorf("shutdown: %v", err)
	}
}

// Tracing that is asked for but has nowhere to send spans stays off rather
// than failing the container: an agent serving without traces beats one that
// does not serve.
func TestSetupTracingEnabledWithoutAnEndpoint(t *testing.T) {
	cfg := &runtimeConfig{TracingEnabled: true}

	shutdown, opts := setupTracing(cfg, discardLogger())
	if len(opts) != 0 {
		t.Errorf("opts = %d, want none without an endpoint", len(opts))
	}
	if err := shutdown(context.Background()); err != nil {
		t.Errorf("shutdown: %v", err)
	}
}
