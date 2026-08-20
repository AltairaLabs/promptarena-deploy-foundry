package main

import (
	"fmt"
	"strconv"
)

// Environment variable names the adapter injects. These must match
// internal/foundry/versions.go exactly — the adapter setting a name the runtime
// does not read is the silent failure this pairing prevents.
const (
	envPackJSON  = "PROMPTPACK_PACK_JSON"
	envPackURI   = "PROMPTPACK_PACK_URI"
	envAgentName = "PROMPTPACK_AGENT"
	envProviders = "PROMPTPACK_PROVIDERS"
	envToolSpecs = "PROMPTPACK_TOOL_SPECS"

	// envTracingEnabled gates tracing. Off unless explicitly set, so an
	// unconfigured deployment sends nothing and pays nothing.
	envTracingEnabled = "PROMPTPACK_TRACING_ENABLED"
	// envOTLPEndpoint is the standard OpenTelemetry variable, used as-is so the
	// image works with any OTLP collector rather than a name we invented.
	// Foundry injects this itself; the adapter only sets it to override.
	envOTLPEndpoint = "OTEL_EXPORTER_OTLP_ENDPOINT"

	// envAzureEndpoint is the Azure OpenAI endpoint the provider bindings
	// address. Unlike vertex's project/location, there is no reserved-name
	// collision here — Foundry reserves only AGENT_* and FOUNDRY_*.
	envAzureEndpoint = "PROMPTPACK_AZURE_ENDPOINT"
)

// Environment variables Foundry injects into every hosted agent container.
// These are read, never set: the AGENT_* and FOUNDRY_* prefixes are reserved.
const (
	envPort             = "PORT"
	envFoundryProjectEP = "FOUNDRY_PROJECT_ENDPOINT"
	envFoundrySessionID = "FOUNDRY_AGENT_SESSION_ID"
	envFoundryAgentName = "FOUNDRY_AGENT_NAME"
	envFoundryAgentVer  = "FOUNDRY_AGENT_VERSION"
	// envFoundryClientID is the per-version managed identity the container runs
	// as. Each agent version gets its own — confirmed on a live deploy, where
	// the version reported an instance_identity distinct from the project's.
	//
	// This is the name Foundry actually injects. AZURE_CLIENT_ID, the
	// conventional name, is NOT present in the sandbox: probing a live one
	// showed only the FOUNDRY_* names, so reading the conventional name alone
	// left the runtime with no identity and every model call unauthorized.
	envFoundryClientID = "FOUNDRY_AGENT_INSTANCE_CLIENT_ID"
	// envClientID is the conventional name, honored as a fallback so the same
	// image runs unchanged on hosts that set it.
	envClientID = "AZURE_CLIENT_ID"
)

// defaultPort is the port Foundry documents for hosted agents. The platform
// injects PORT, so this is only the fallback for a local run.
const defaultPort = "8088"

// firstEnv returns the first non-empty variable among names.
func firstEnv(getenv func(string) string, names ...string) string {
	for _, name := range names {
		if v := getenv(name); v != "" {
			return v
		}
	}
	return ""
}

// runtimeConfig holds all configuration parsed from the environment.
type runtimeConfig struct {
	PackJSON      string
	PackURI       string
	AgentName     string
	ProvidersJSON string
	ToolSpecsJSON string
	AzureEndpoint string

	Port            string
	ProjectEndpoint string
	SessionID       string
	FoundryAgent    string
	FoundryVersion  string
	ClientID        string

	OTLPEndpoint   string
	TracingEnabled bool
}

// loadConfig reads configuration through getenv, which tests substitute.
//
// Exactly one pack source is required. Failing at startup is far better than
// accepting traffic and erroring on every request — a container that cannot
// serve should never report itself ready.
func loadConfig(getenv func(string) string) (*runtimeConfig, error) {
	cfg := &runtimeConfig{
		PackJSON:      getenv(envPackJSON),
		PackURI:       getenv(envPackURI),
		AgentName:     getenv(envAgentName),
		ProvidersJSON: getenv(envProviders),
		ToolSpecsJSON: getenv(envToolSpecs),
		AzureEndpoint: getenv(envAzureEndpoint),

		Port:            getenv(envPort),
		ProjectEndpoint: getenv(envFoundryProjectEP),
		SessionID:       getenv(envFoundrySessionID),
		FoundryAgent:    getenv(envFoundryAgentName),
		FoundryVersion:  getenv(envFoundryAgentVer),
		ClientID:        firstEnv(getenv, envFoundryClientID, envClientID),

		OTLPEndpoint: getenv(envOTLPEndpoint),
	}

	if cfg.Port == "" {
		cfg.Port = defaultPort
	}

	if cfg.PackJSON == "" && cfg.PackURI == "" {
		return nil, fmt.Errorf("%s or %s is required", envPackJSON, envPackURI)
	}

	if raw := getenv(envTracingEnabled); raw != "" {
		enabled, err := strconv.ParseBool(raw)
		if err != nil {
			return nil, fmt.Errorf("invalid %s %q: %w", envTracingEnabled, raw, err)
		}
		cfg.TracingEnabled = enabled
	}

	return cfg, nil
}
