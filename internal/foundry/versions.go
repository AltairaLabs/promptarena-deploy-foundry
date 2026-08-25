package foundry

import (
	"encoding/json"
	"fmt"
	"maps"
	"os"
)

// Environment variable names injected into the runtime container. These must
// match cmd/foundry-runtime's config exactly — the adapter setting a name the
// runtime does not read is the silent failure this pairing prevents.
//
// Unlike vertex, no prefixed-duplicate workaround is needed: Foundry reserves
// the AGENT_* and FOUNDRY_* prefixes, and PROMPTPACK_* is clear of both. The
// platform also injects its own coordinates (FOUNDRY_PROJECT_ENDPOINT,
// FOUNDRY_AGENT_SESSION_ID and friends), so the adapter does not repeat them.
const (
	envPackJSON = "PROMPTPACK_PACK_JSON"
	envPackURI  = "PROMPTPACK_PACK_URI"
	// envAgentName pins a single agent. It is deliberately never set here: one
	// Foundry agent serves the whole pack, and the runtime's existing
	// precedence — PROMPTPACK_AGENT, then agents.entry, then the sole prompt —
	// already means "serve the pack, entry first" when it is absent.
	envAgentName      = "PROMPTPACK_AGENT"
	envProviders      = "PROMPTPACK_PROVIDERS"
	envToolSpecs      = "PROMPTPACK_TOOL_SPECS"
	envTracingEnabled = "PROMPTPACK_TRACING_ENABLED"
	// State store selection. The URL variable is named, never copied: the
	// adapter tells the runtime which variable holds the connection string and
	// the secret stays wherever the operator put it.
	envStateStoreKind = "PROMPTPACK_STATE_STORE"
	envStateStoreRoot = "PROMPTPACK_STATE_STORE_ROOT"
	envStateStoreURL  = "PROMPTPACK_STATE_STORE_URL"
	envOTLPEndpoint   = "OTEL_EXPORTER_OTLP_ENDPOINT"
	// envAzureEndpoint is the Azure OpenAI endpoint the runtime binds providers
	// against. Foundry reserves only AGENT_* and FOUNDRY_*, so there is no
	// reserved-name collision to work around here.
	envAzureEndpoint = "PROMPTPACK_AZURE_ENDPOINT"
)

// Managed tag keys and values. These identify what the adapter owns, so they
// always win over a user tag of the same name.
const (
	tagManagedBy   = "app.altairalabs.ai/managed-by"
	tagPackID      = "promptkit.altairalabs.ai/pack-id"
	managedByValue = "promptarena"
	// managedTagCount is how many managed tags buildTags adds, used to size the
	// merged map in one allocation.
	managedTagCount = 2
)

// specInput is everything buildAgentSpec needs, gathered once by Apply.
type specInput struct {
	Cfg           *Config
	AgentName     string
	PackID        string
	PackJSON      string
	Bindings      []ResolvedBinding
	Delivery      PackDelivery
	StagedPackURI string
	ToolSpecsJSON string
}

// buildAgentSpec turns the deployment inputs into the desired spec for the
// pack's agent. It is pure: no I/O, so the whole mapping is testable offline.
func buildAgentSpec(in *specInput) (spec *AgentSpec, errors []string) {
	env, errs := buildAgentEnv(in)
	if len(errs) != 0 {
		return nil, errs
	}

	return &AgentSpec{
		Name:               in.AgentName,
		Image:              in.Cfg.Image,
		CPU:                in.Cfg.CPU,
		Memory:             in.Cfg.Memory,
		Protocols:          in.Cfg.Protocols,
		IdleTimeoutMinutes: in.Cfg.IdleTimeoutMinutes,
		Env:                env,
		Metadata:           buildTags(in.Cfg, in.PackID),
	}, nil
}

// buildAgentEnv assembles the runtime's environment.
func buildAgentEnv(in *specInput) (vars map[string]string, errors []string) {
	encodedBindings, err := json.Marshal(in.Bindings)
	if err != nil {
		return nil, []string{fmt.Sprintf("encode provider bindings: %v", err)}
	}

	env := map[string]string{
		envProviders:     string(encodedBindings),
		envAzureEndpoint: in.Cfg.azureEndpoint(),
	}

	if in.ToolSpecsJSON != "" {
		env[envToolSpecs] = in.ToolSpecsJSON
	}

	if storeErrs := addStateStoreEnv(env, in.Cfg.StateStore, os.Getenv); len(storeErrs) != 0 {
		return nil, storeErrs
	}

	if in.Cfg.Observability != nil && in.Cfg.Observability.TracingEnabled {
		env[envTracingEnabled] = "true"
		// Only set the endpoint when overriding: Foundry injects its own, and
		// writing an empty value would replace it with nothing.
		if in.Cfg.Observability.OTLPEndpoint != "" {
			env[envOTLPEndpoint] = in.Cfg.Observability.OTLPEndpoint
		}
	}

	if in.Delivery.Inline {
		env[envPackJSON] = in.PackJSON
		return env, nil
	}

	if in.StagedPackURI == "" {
		return nil, []string{fmt.Sprintf(
			"the pack is %d bytes, over the %d byte inline limit, but no staged pack URI is "+
				"available; raise pack_inline_limit_bytes or set staging_container",
			in.Delivery.SizeBytes, in.Cfg.PackInlineLimitBytes)}
	}
	env[envPackURI] = in.StagedPackURI
	return env, nil
}

// addStateStoreEnv tells the runtime where to keep conversation history.
//
// Nothing is written for the default: an absent variable already means memory,
// and setting it would only add a value to keep in step.
//
// The redis URL is read from the deploying environment and written into the
// agent definition, because environment_variables is the only channel Foundry
// gives a hosted agent and it takes literal values -- there is no reference
// syntax to defer the lookup to start-up. That puts the connection string
// where anyone who can read the agent can read it, which is why the config
// names a variable rather than carrying the string: the secret stays out of
// the deploy config and out of version control, even though it lands in the
// definition.
func addStateStoreEnv(env map[string]string, store *StateStore, lookupEnv func(string) string) []string {
	if store == nil || store.Kind == "" || store.Kind == StateStoreMemory {
		return nil
	}

	env[envStateStoreKind] = store.Kind
	if store.Root != "" {
		env[envStateStoreRoot] = store.Root
	}
	if store.URLFromEnv == "" {
		return nil
	}

	url := lookupEnv(store.URLFromEnv)
	if url == "" {
		return []string{fmt.Sprintf(
			"state_store.url_from_env names %s, which is unset or empty in this "+
				"environment; the deployed agent would have no store to connect to",
			store.URLFromEnv)}
	}
	env[envStateStoreURL] = url
	return nil
}

// buildTags merges the user's tags with the managed ones. Managed tags are
// applied last so a user tag cannot overwrite them and leave a deployment
// unattributable.
func buildTags(cfg *Config, packID string) map[string]string {
	tags := make(map[string]string, len(cfg.Tags)+managedTagCount)
	maps.Copy(tags, cfg.Tags)

	tags[tagManagedBy] = managedByValue
	tags[tagPackID] = packID

	return tags
}
