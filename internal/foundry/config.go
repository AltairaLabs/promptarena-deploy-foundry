package foundry

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// Protocol contract names a hosted agent can declare.
const (
	// ProtocolResponses is the OpenAI-compatible POST /responses contract. The
	// platform manages conversation history and the SSE framing.
	ProtocolResponses = "responses"
	// ProtocolInvocations is POST /invocations — arbitrary JSON in and out, with
	// raw SSE for streaming. The adapter owns this schema.
	ProtocolInvocations = "invocations"
	// ProtocolInvocationsWS is the full-duplex WS /invocations_ws relay used for
	// real-time voice.
	ProtocolInvocationsWS = "invocations_ws"
)

// DefaultPackInlineLimitBytes is the serialized pack size above which the pack
// is staged to Blob storage instead of injected as an environment variable.
//
// Foundry documents no environment size limit, so this starts at vertex's
// measured value rather than at a guess. Relax it only after measuring against
// a real project.
const DefaultPackInlineLimitBytes = 24576

// Idle timeout bounds, in minutes. The platform reclaims a session's sandbox
// after this long without traffic.
const (
	minIdleTimeoutMinutes = 5
	maxIdleTimeoutMinutes = 60
)

// httpsScheme prefixes a Blob storage container URL.
const httpsScheme = "https://"

// azureOpenAIHostSuffix completes the conventional Azure OpenAI endpoint for an
// account. PromptKit's azure platform expects this form.
const azureOpenAIHostSuffix = ".openai.azure.com/"

// legalResourcePairs maps each accepted cpu value to the memory value it must
// be paired with. Foundry offers exactly three immutable pairs; anything else
// is rejected by the API after the version has been created, so the pairing is
// enforced here at validation time instead.
var legalResourcePairs = map[string]string{
	"0.5": "1Gi",
	"1":   "2Gi",
	"2":   "4Gi",
}

// validProtocols is the set of protocol contracts a hosted agent can declare.
var validProtocols = map[string]bool{
	ProtocolResponses:     true,
	ProtocolInvocations:   true,
	ProtocolInvocationsWS: true,
}

// Observability configures what the deployed agent reports.
//
// Tracing is off by default: an unconfigured deployment should send nothing and
// pay nothing. Unlike vertex, no endpoint is required when it is on — Foundry
// injects OTEL_EXPORTER_OTLP_ENDPOINT and APPLICATIONINSIGHTS_CONNECTION_STRING
// into the container, so OTLPEndpoint only overrides them.
type Observability struct {
	TracingEnabled bool   `json:"tracing_enabled,omitempty"`
	OTLPEndpoint   string `json:"otlp_endpoint,omitempty"`
}

// Config holds foundry-specific deploy configuration.
type Config struct {
	Account string `json:"account"`
	Project string `json:"project"`
	Image   string `json:"image"`

	CPU                string   `json:"cpu,omitempty"`
	Memory             string   `json:"memory,omitempty"`
	Protocols          []string `json:"protocols,omitempty"`
	IdleTimeoutMinutes int      `json:"idle_timeout_minutes,omitempty"`

	// AzureEndpoint is the Azure OpenAI endpoint the deployed agent's provider
	// bindings address. Derived from Account when unset; set it explicitly for
	// an account whose inference endpoint is not the conventional one.
	AzureEndpoint string `json:"azure_endpoint,omitempty"`

	StagingContainer     string            `json:"staging_container,omitempty"`
	PackInlineLimitBytes int               `json:"pack_inline_limit_bytes,omitempty"`
	Tags                 map[string]string `json:"tags,omitempty"`
	DryRun               bool              `json:"dry_run,omitempty"`

	Providers     []ProviderBinding `json:"providers,omitempty"`
	Observability *Observability    `json:"observability,omitempty"`
	StateStore    *StateStore       `json:"state_store,omitempty"`
}

// State store kinds a deployed agent can use for conversation history.
const (
	// StateStoreMemory keeps history in the container. Two turns served by the
	// same container find each other; nothing survives it being replaced.
	StateStoreMemory = "memory"
	// StateStoreFile keeps history on the session sandbox filesystem, which
	// the platform persists across turns and idle periods.
	StateStoreFile = "file"
	// StateStoreRedis keeps history outside the container entirely, which is
	// the only option that holds when more than one serves a conversation.
	StateStoreRedis = "redis"
)

// validStateStores is the set of kinds the runtime can build.
var validStateStores = map[string]bool{
	StateStoreMemory: true,
	StateStoreFile:   true,
	StateStoreRedis:  true,
}

// StateStore selects where a deployed agent keeps conversation history.
//
// Defaults to memory, which needs nothing and remembers a conversation for as
// long as one container serves it. Reach past it when a conversation has to
// outlive that.
type StateStore struct {
	// Kind is memory, file or redis.
	Kind string `json:"kind,omitempty"`

	// Root overrides the file store's directory. Defaults to a directory under
	// the sandbox's $HOME.
	Root string `json:"root,omitempty"`

	// URLFromEnv names the environment variable holding the redis connection
	// string. The variable itself, not the URL: a connection string carries a
	// credential, and the deploy config is not a place to keep one.
	URLFromEnv string `json:"url_from_env,omitempty"`
}

// parseConfig unmarshals the provider config JSON and applies defaults.
func parseConfig(raw string) (*Config, error) {
	var cfg Config
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		return nil, fmt.Errorf("invalid config JSON: %w", err)
	}
	cfg.applyDefaults()
	return &cfg, nil
}

// applyDefaults fills in values the user may omit.
//
// Protocols defaults to invocations alone rather than to every contract: an
// agent version that declares a protocol its container does not serve is
// accepted at create time and then fails when the platform routes to it.
func (c *Config) applyDefaults() {
	if c.PackInlineLimitBytes == 0 {
		c.PackInlineLimitBytes = DefaultPackInlineLimitBytes
	}
	if len(c.Protocols) == 0 {
		c.Protocols = []string{ProtocolInvocations}
	}
}

// validateStructure checks the config's own fields. Provider bindings and tags
// are validated separately so their errors read independently.
func (c *Config) validateStructure() []string {
	var errs []string

	if c.Account == "" {
		errs = append(errs, "account is required (the {account} in {account}.services.ai.azure.com)")
	}
	if c.Project == "" {
		errs = append(errs, "project is required")
	}
	if c.Image == "" {
		errs = append(errs,
			"image is required: an Azure Container Registry reference to the linux/amd64 runtime image")
	}

	errs = append(errs, c.validateResources()...)
	errs = append(errs, c.validateProtocols()...)
	errs = append(errs, c.validateIdleTimeout()...)
	errs = append(errs, c.validateStateStore()...)
	errs = append(errs, c.validateStagingContainer()...)
	errs = append(errs, c.validateObservability()...)

	return errs
}

// validateResources enforces the three legal cpu/memory pairs. They are fixed
// and immutable per version, so a mismatched pair is a hard error rather than
// something to normalize silently.
//
// Both are required. Foundry has no default sizing: a create without them is
// rejected with HTTP 400 "Required property 'cpu' is missing". Treating them
// as optional turned that into a failure minutes into an apply, which is
// exactly what this validation exists to prevent.
func (c *Config) validateResources() []string {
	if c.CPU == "" || c.Memory == "" {
		return []string{fmt.Sprintf(
			"cpu and memory are required and must be set together; Foundry has no "+
				"default sizing and rejects a deploy without them. The fixed pairs are %s",
			describeResourcePairs())}
	}

	wantMemory, ok := legalResourcePairs[c.CPU]
	if !ok {
		return []string{fmt.Sprintf(
			"cpu %q is not one of the values Foundry accepts; the fixed pairs are %s",
			c.CPU, describeResourcePairs())}
	}
	if c.Memory != wantMemory {
		return []string{fmt.Sprintf(
			"cpu %s pairs with memory %s, not %q; the pairs are fixed and immutable per version",
			c.CPU, wantMemory, c.Memory)}
	}

	return nil
}

// describeResourcePairs renders the legal pairs in a stable order for messages.
func describeResourcePairs() string {
	cpus := make([]string, 0, len(legalResourcePairs))
	for cpu := range legalResourcePairs {
		cpus = append(cpus, cpu)
	}
	sort.Strings(cpus)

	parts := make([]string, 0, len(cpus))
	for _, cpu := range cpus {
		parts = append(parts, fmt.Sprintf("%s/%s", cpu, legalResourcePairs[cpu]))
	}
	return strings.Join(parts, ", ")
}

// validateProtocols checks every declared protocol is one the platform knows.
func (c *Config) validateProtocols() []string {
	var errs []string
	seen := make(map[string]bool, len(c.Protocols))

	for _, p := range c.Protocols {
		if !validProtocols[p] {
			errs = append(errs, fmt.Sprintf(
				"protocol %q is not one of %s, %s, %s",
				p, ProtocolResponses, ProtocolInvocations, ProtocolInvocationsWS))
			continue
		}
		if seen[p] {
			errs = append(errs, fmt.Sprintf("protocol %q is duplicated", p))
		}
		seen[p] = true
	}

	return errs
}

// validateIdleTimeout checks the bound the platform enforces. Zero means unset,
// which leaves the platform's own 15 minute default in place.
func (c *Config) validateIdleTimeout() []string {
	if c.IdleTimeoutMinutes == 0 {
		return nil
	}
	if c.IdleTimeoutMinutes < minIdleTimeoutMinutes || c.IdleTimeoutMinutes > maxIdleTimeoutMinutes {
		return []string{fmt.Sprintf(
			"idle_timeout_minutes %d is outside the %d-%d range Foundry accepts",
			c.IdleTimeoutMinutes, minIdleTimeoutMinutes, maxIdleTimeoutMinutes)}
	}
	return nil
}

// azureEndpoint returns the endpoint the deployed agent binds providers
// against, deriving it from the account when it is not set explicitly.
func (c *Config) azureEndpoint() string {
	if c.AzureEndpoint != "" {
		return c.AzureEndpoint
	}
	return httpsScheme + c.Account + azureOpenAIHostSuffix
}

// validateStateStore checks the store the agent will build at startup.
//
// A store the runtime cannot build leaves an agent that answers but forgets,
// which is not visible from outside it. Catching it here means the deploy
// fails instead.
func (c *Config) validateStateStore() []string {
	if c.StateStore == nil || c.StateStore.Kind == "" {
		return nil
	}

	var errs []string
	if !validStateStores[c.StateStore.Kind] {
		errs = append(errs, fmt.Sprintf(
			"state_store.kind %q is not one of %s, %s, %s",
			c.StateStore.Kind, StateStoreMemory, StateStoreFile, StateStoreRedis))
		return errs
	}

	if c.StateStore.Kind == StateStoreRedis && c.StateStore.URLFromEnv == "" {
		errs = append(errs, "state_store.kind redis requires url_from_env "+
			"naming the environment variable that holds the connection string")
	}
	if c.StateStore.Kind != StateStoreFile && c.StateStore.Root != "" {
		errs = append(errs, fmt.Sprintf(
			"state_store.root only applies to kind %s, not %q",
			StateStoreFile, c.StateStore.Kind))
	}
	if c.StateStore.Kind != StateStoreRedis && c.StateStore.URLFromEnv != "" {
		errs = append(errs, fmt.Sprintf(
			"state_store.url_from_env only applies to kind %s, not %q",
			StateStoreRedis, c.StateStore.Kind))
	}
	return errs
}

// validateStagingContainer checks the Blob container URL is absolute. A bare
// host/path would be resolved relative to nothing and fail at upload time,
// after the plan has already reported the staging step as fine.
func (c *Config) validateStagingContainer() []string {
	if c.StagingContainer == "" {
		return nil
	}
	if !strings.HasPrefix(c.StagingContainer, httpsScheme) {
		return []string{fmt.Sprintf(
			"staging_container %q must be a full URL starting with %s "+
				"(for example https://acct.blob.core.windows.net/promptkit)",
			c.StagingContainer, httpsScheme)}
	}
	return nil
}

// validateObservability checks an overriding OTLP endpoint is usable.
//
// The exporter builds its target with otlptracehttp.WithEndpointURL, which needs
// a full URL. A host:port value yields "http:///v1/traces" — no host — and every
// export fails at runtime while the deployment looks healthy.
func (c *Config) validateObservability() []string {
	if c.Observability == nil || c.Observability.OTLPEndpoint == "" {
		return nil
	}
	if !strings.HasPrefix(c.Observability.OTLPEndpoint, "http://") &&
		!strings.HasPrefix(c.Observability.OTLPEndpoint, httpsScheme) {
		return []string{fmt.Sprintf(
			"observability.otlp_endpoint %q must be a full URL including scheme "+
				"(for example http://collector:4318), not host:port",
			c.Observability.OTLPEndpoint)}
	}
	return nil
}
