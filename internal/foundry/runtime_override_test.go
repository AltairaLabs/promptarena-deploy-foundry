package foundry

import (
	"context"
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/AltairaLabs/promptarena/v2/deploy"
)

const overrideImage = "myacr.azurecr.io/other/runtime:1.0.0"

// overrideConfigJSON is a valid config whose runtime is replaced. It sets no
// top-level image, which the override makes optional.
const overrideConfigJSON = `{
  "account": "acct",
  "project": "proj",
  "cpu": "1",
  "memory": "2Gi",
  "providers": [{"name": "default", "role": "llm", "type": "openai", "model": "gpt4o-deploy"}],
  "runtime_override": {
    "image": "` + overrideImage + `",
    "env": {"RELAY_URL": "wss://relay.example.com/v1/channel"},
    "secret_env": {"RELAY_KEY": {"name": "relay-secrets/relay_key"}}
  }
}`

// specCapturingClient records the spec each version was created from.
type specCapturingClient struct {
	*dryRunClient
	versionSpecs []*AgentSpec
}

func (c *specCapturingClient) CreateVersion(
	ctx context.Context, name string, spec *AgentSpec,
) (*AgentVersion, error) {
	c.versionSpecs = append(c.versionSpecs, spec)
	return c.dryRunClient.CreateVersion(ctx, name, spec)
}

func overrideSpecInput(o *deploy.RuntimeOverride) *specInput {
	in := baseSpecInput()
	in.Cfg.RuntimeOverride = o
	in.EntryAgent = "main"
	return in
}

func TestGetProviderInfoAdvertisesRuntimeOverride(t *testing.T) {
	info, err := NewProvider().GetProviderInfo(context.Background())
	if err != nil {
		t.Fatalf("GetProviderInfo: %v", err)
	}
	if !slices.Contains(info.Capabilities, deploy.RuntimeOverrideCapability) {
		t.Errorf("Capabilities = %v, want %q", info.Capabilities, deploy.RuntimeOverrideCapability)
	}
	var schema struct {
		Required   []string                   `json:"required"`
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal([]byte(info.ConfigSchema), &schema); err != nil {
		t.Fatalf("ConfigSchema: %v", err)
	}
	if _, ok := schema.Properties[deploy.RuntimeOverrideConfigKey]; !ok {
		t.Errorf("ConfigSchema has no %q property", deploy.RuntimeOverrideConfigKey)
	}
	if slices.Contains(schema.Required, "image") {
		t.Error("ConfigSchema requires image, which runtime_override.image makes optional")
	}
}

// Every field Config decodes must be in the schema, which sets
// additionalProperties false: a schema-validating client would otherwise reject
// a config the adapter accepts.
func TestConfigSchemaCoversEveryConfigField(t *testing.T) {
	var schema struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal([]byte(configSchema), &schema); err != nil {
		t.Fatalf("configSchema: %v", err)
	}
	typ := reflect.TypeOf(Config{})
	for i := 0; i < typ.NumField(); i++ {
		name, _, _ := strings.Cut(typ.Field(i).Tag.Get("json"), ",")
		if _, ok := schema.Properties[name]; !ok {
			t.Errorf("Config field %q is not in the schema", name)
		}
	}
}

func TestWithRuntimeOverridePanicsOnABadSchema(t *testing.T) {
	for name, schema := range map[string]string{
		"not JSON":      "{",
		"no properties": `{"type": "object"}`,
	} {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Error("withRuntimeOverride did not panic")
				}
			}()
			withRuntimeOverride(schema)
		})
	}
}

func TestValidateConfigAcceptsARuntimeOverride(t *testing.T) {
	got, err := NewProvider().ValidateConfig(
		context.Background(), &deploy.ValidateRequest{Config: overrideConfigJSON})
	if err != nil {
		t.Fatalf("ValidateConfig: %v", err)
	}
	if !got.Valid {
		t.Fatalf("Valid = false, errors = %v", got.Errors)
	}
	if !slices.ContainsFunc(got.Warnings, func(w string) bool {
		return strings.Contains(w, "only when a session is created")
	}) {
		t.Errorf("Warnings = %v, want the session start advisory", got.Warnings)
	}
}

func TestParseConfigDropsAnEmptyOverride(t *testing.T) {
	cfg, err := parseConfig(`{"runtime_override": {}}`)
	if err != nil {
		t.Fatalf("parseConfig: %v", err)
	}
	if cfg.RuntimeOverride != nil {
		t.Errorf("RuntimeOverride = %+v, want nil for an empty block", cfg.RuntimeOverride)
	}
}

func TestValidateRuntimeOverrideRefusals(t *testing.T) {
	cases := map[string]struct {
		override deploy.RuntimeOverride
		want     string
	}{
		"command": {deploy.RuntimeOverride{Image: overrideImage, Command: []string{"/bin/sh"}},
			"runtime_override.command is not supported"},
		"args": {deploy.RuntimeOverride{Image: overrideImage, Args: []string{"-v"}},
			"runtime_override.args is not supported"},
		"pack prefix": {deploy.RuntimeOverride{Env: map[string]string{"PROMPTPACK_X": "1"}},
			"PROMPTPACK_* names are set by the adapter"},
		"agent prefix": {deploy.RuntimeOverride{Env: map[string]string{"AGENT_X": "1"}},
			"AGENT_* names are set by the adapter"},
		"foundry prefix": {deploy.RuntimeOverride{Env: map[string]string{"FOUNDRY_X": "1"}},
			"FOUNDRY_* names are set by the adapter"},
		"port": {deploy.RuntimeOverride{Env: map[string]string{"PORT": "9000"}},
			`"PORT" is reserved`},
		"otlp": {deploy.RuntimeOverride{Env: map[string]string{envOTLPEndpoint: "http://x"}},
			`"OTEL_EXPORTER_OTLP_ENDPOINT" is reserved`},
		"secret version": {
			deploy.RuntimeOverride{SecretEnv: map[string]deploy.SecretRef{"K": {Name: "conn", Version: "3"}}},
			"not versioned"},
		"secret name form": {
			deploy.RuntimeOverride{SecretEnv: map[string]deploy.SecretRef{"K": {Name: "a.b/c}"}}},
			"must be <connection> or <connection>/<credential-key>"},
		"secret empty key": {
			deploy.RuntimeOverride{SecretEnv: map[string]deploy.SecretRef{"K": {Name: "conn/"}}},
			"must be <connection> or <connection>/<credential-key>"},
		"secret no name": {
			deploy.RuntimeOverride{SecretEnv: map[string]deploy.SecretRef{"K": {Name: " "}}},
			"needs a secret name"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			cfg := &Config{RuntimeOverride: &tc.override}
			errs := cfg.validateRuntimeOverride()
			if !slices.ContainsFunc(errs, func(e string) bool { return strings.Contains(e, tc.want) }) {
				t.Errorf("errors = %v, want one containing %q", errs, tc.want)
			}
		})
	}
}

func TestValidateStructureNeedsSomeImage(t *testing.T) {
	cfg := &Config{RuntimeOverride: &deploy.RuntimeOverride{Env: map[string]string{"A": "b"}}}
	errs := cfg.validateStructure()
	if !slices.ContainsFunc(errs, func(e string) bool { return strings.Contains(e, "image is required") }) {
		t.Errorf("errors = %v, want image required when the override names none", errs)
	}

	cfg.RuntimeOverride.Image = overrideImage
	errs = cfg.validateStructure()
	if slices.ContainsFunc(errs, func(e string) bool { return strings.Contains(e, "image is required") }) {
		t.Errorf("errors = %v, want runtime_override.image to satisfy the image requirement", errs)
	}
}

func TestBuildAgentSpecRunsTheOverride(t *testing.T) {
	spec, errs := buildAgentSpec(overrideSpecInput(&deploy.RuntimeOverride{
		Image: overrideImage,
		Env:   map[string]string{"RELAY_URL": "wss://relay.example.com"},
		SecretEnv: map[string]deploy.SecretRef{
			"RELAY_KEY": {Name: "relay-secrets/relay_key"},
			"API_KEY":   {Name: "api-conn"},
		},
	}))
	if len(errs) != 0 {
		t.Fatalf("buildAgentSpec errors = %v", errs)
	}
	if spec.Image != overrideImage {
		t.Errorf("Image = %q, want the override's", spec.Image)
	}
	want := map[string]string{
		"RELAY_URL":  "wss://relay.example.com",
		"RELAY_KEY":  "${{connections.relay-secrets.credentials.relay_key}}",
		"API_KEY":    "${{connections.api-conn.credentials.key}}",
		envAgentName: "main",
	}
	for name, value := range want {
		if spec.Env[name] != value {
			t.Errorf("Env[%s] = %q, want %q", name, spec.Env[name], value)
		}
	}
	// The pack handoff is unchanged.
	if spec.Env[envPackJSON] != singleAgentPack {
		t.Errorf("Env[%s] = %q, want the inline pack", envPackJSON, spec.Env[envPackJSON])
	}
}

// An override without an image keeps the configured runtime image.
func TestBuildAgentSpecOverrideWithoutImageKeepsTheImage(t *testing.T) {
	spec, errs := buildAgentSpec(overrideSpecInput(&deploy.RuntimeOverride{Env: map[string]string{"A": "b"}}))
	if len(errs) != 0 {
		t.Fatalf("buildAgentSpec errors = %v", errs)
	}
	if spec.Image != "acr.azurecr.io/x:1" {
		t.Errorf("Image = %q, want the configured image", spec.Image)
	}
}

func TestHashPlanConfigTracksTheOverride(t *testing.T) {
	cfg := baseSpecInput().Cfg
	before, err := hashPlanConfig(cfg, nil, "")
	if err != nil {
		t.Fatal(err)
	}

	cfg.RuntimeOverride = &deploy.RuntimeOverride{Image: overrideImage}
	withImage, err := hashPlanConfig(cfg, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	cfg.RuntimeOverride.Env = map[string]string{"A": "b"}
	withEnv, err := hashPlanConfig(cfg, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if before == withImage || withImage == withEnv {
		t.Error("changing the override did not change the config hash, so no new version would be created")
	}
}

func TestApplyDeploysTheOverride(t *testing.T) {
	client := &specCapturingClient{dryRunClient: newDryRunClient()}
	p := applyProvider(t, client)

	if _, err := p.Apply(context.Background(),
		applyRequest(overrideConfigJSON, multiAgentPack, ""), nil); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(client.versionSpecs) != 1 {
		t.Fatalf("versions created = %d, want 1", len(client.versionSpecs))
	}
	spec := client.versionSpecs[0]
	if spec.Image != overrideImage {
		t.Errorf("Image = %q, want %q", spec.Image, overrideImage)
	}
	if spec.Env[envAgentName] != "triage" {
		t.Errorf("Env[%s] = %q, want the pack's entry agent", envAgentName, spec.Env[envAgentName])
	}
	if spec.Env["RELAY_KEY"] != "${{connections.relay-secrets.credentials.relay_key}}" {
		t.Errorf("Env[RELAY_KEY] = %q, want the connection placeholder", spec.Env["RELAY_KEY"])
	}
}

func TestPlanRefusesAnOverrideWithoutAnEntryAgent(t *testing.T) {
	const noEntryPack = `{"id": "two", "prompts": {"a": {}, "b": {}}}`
	_, err := NewProvider().Plan(context.Background(), applyRequest(
		strings.Replace(overrideConfigJSON, `"cpu"`, `"dry_run": true, "cpu"`, 1), noEntryPack, ""))
	if err == nil || !strings.Contains(err.Error(), "no entry agent") {
		t.Errorf("Plan error = %v, want the missing entry agent named", err)
	}
}

// The bundled runtime serves the pack entry first, so it is never pinned.
func TestBuildAgentSpecWithoutOverrideDoesNotPinAnAgent(t *testing.T) {
	in := baseSpecInput()
	in.EntryAgent = "main"
	spec, errs := buildAgentSpec(in)
	if len(errs) != 0 {
		t.Fatalf("buildAgentSpec errors = %v", errs)
	}
	if _, ok := spec.Env[envAgentName]; ok {
		t.Errorf("Env[%s] is set without an override", envAgentName)
	}
}

func TestDiagnoseConfigUsesTheOverrideImage(t *testing.T) {
	cfg := &Config{
		Image:           "acr.azurecr.io/x:1",
		Protocols:       []string{ProtocolResponses},
		RuntimeOverride: &deploy.RuntimeOverride{Image: "ghcr.io/example/other:1"},
	}
	warnings := diagnoseConfig(cfg)
	if !slices.ContainsFunc(warnings, func(w string) bool { return strings.Contains(w, "ghcr.io/example/other:1") }) {
		t.Errorf("warnings = %v, want the override image flagged as not in ACR", warnings)
	}
	// What a replacement serves is its own business.
	if slices.ContainsFunc(warnings, func(w string) bool { return strings.Contains(w, "bundled") }) {
		t.Errorf("warnings = %v, want no bundled-runtime protocol warning for a replacement image", warnings)
	}
}

// A misspelled key inside the block must not decode to an empty override,
// which would deploy the bundled runtime instead of the one asked for.
func TestParseConfigRefusesAnUnknownOverrideKey(t *testing.T) {
	_, err := parseConfig(`{"runtime_override": {"imagee": "x"}}`)
	if err == nil || !strings.Contains(err.Error(), "imagee") {
		t.Errorf("parseConfig error = %v, want the unknown key named", err)
	}
}

func TestParseConfigRefusesInvalidJSON(t *testing.T) {
	if _, err := parseConfig(`{`); err == nil {
		t.Error("parseConfig accepted invalid JSON")
	}
}

// Entry resolution matches the bundled runtime's: agents.entry, then the only
// prompt.
func TestEntryAgent(t *testing.T) {
	cases := map[string]struct{ pack, want string }{
		"entry with members":          {multiAgentPack, "triage"},
		"sole prompt":                 {singleAgentPack, "main"},
		"entry without members":       {`{"id":"p","prompts":{"a":{},"b":{}},"agents":{"entry":"b"}}`, "b"},
		"members, no entry, 1 prompt": {`{"id":"p","prompts":{"a":{}},"agents":{"members":{"a":{}}}}`, "a"},
		"no entry, two prompts":       {`{"id":"p","prompts":{"a":{},"b":{}}}`, ""},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := entryAgent(tc.pack)
			if err != nil {
				t.Fatalf("entryAgent: %v", err)
			}
			if got != tc.want {
				t.Errorf("entryAgent = %q, want %q", got, tc.want)
			}
		})
	}
	if _, err := entryAgent("{"); err == nil {
		t.Error("entryAgent accepted invalid JSON")
	}
}

// An env-only override still runs the bundled runtime, which is never pinned
// and needs no replacement-container advisory.
func TestEnvOnlyOverrideKeepsTheBundledRuntimeBehaviour(t *testing.T) {
	o := &deploy.RuntimeOverride{Env: map[string]string{"A": "b"}}
	spec, errs := buildAgentSpec(overrideSpecInput(o))
	if len(errs) != 0 {
		t.Fatalf("buildAgentSpec errors = %v", errs)
	}
	if _, ok := spec.Env[envAgentName]; ok {
		t.Errorf("Env[%s] is set for an env-only override", envAgentName)
	}
	if spec.Env["A"] != "b" {
		t.Errorf("Env[A] = %q, want b", spec.Env["A"])
	}
	if w := diagnoseRuntimeOverride(&Config{RuntimeOverride: o}); len(w) != 0 {
		t.Errorf("warnings = %v, want none for an env-only override", w)
	}
}

// goldenConfigHash is hashPlanConfig of goldenHashConfig as computed before
// runtime_override existed. A deployment that sets no override and no state
// store must keep it, or every existing deployment plans a spurious version.
const goldenConfigHash = "4db02c450c539d136dee696db80805418f8bbe8de99ef7f50e93d997b6c598b5"

func goldenHashConfig() (*Config, []ResolvedBinding) {
	return &Config{
		Account: "acct", Project: "proj", Image: "acr.azurecr.io/x:1", CPU: "1", Memory: "2Gi",
		Protocols: []string{ProtocolInvocations}, IdleTimeoutMinutes: 20,
		Tags: map[string]string{"team": "platform"},
	}, []ResolvedBinding{{Name: "default", Role: RoleLLM, Type: "openai", Model: "m"}}
}

func TestHashPlanConfigKeepsTheHashWithoutAnOverride(t *testing.T) {
	cfg, bindings := goldenHashConfig()
	got, err := hashPlanConfig(cfg, bindings, "")
	if err != nil {
		t.Fatal(err)
	}
	if got != goldenConfigHash {
		t.Errorf("hash = %s, want %s: existing deployments would all plan a new version", got, goldenConfigHash)
	}
}

// The unused top-level image beside an override image deploys nothing, so
// changing it must not create a version.
func TestHashPlanConfigUsesTheImageThatRuns(t *testing.T) {
	cfg, bindings := goldenHashConfig()
	cfg.RuntimeOverride = &deploy.RuntimeOverride{Image: overrideImage}
	before, err := hashPlanConfig(cfg, bindings, "")
	if err != nil {
		t.Fatal(err)
	}
	cfg.Image = "acr.azurecr.io/x:2"
	after, err := hashPlanConfig(cfg, bindings, "")
	if err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Error("changing an image the override replaces changed the hash")
	}
}

func TestHashPlanConfigTracksTheStateStore(t *testing.T) {
	cfg, bindings := goldenHashConfig()
	cfg.StateStore = &StateStore{Kind: StateStoreFile}
	got, err := hashPlanConfig(cfg, bindings, "")
	if err != nil {
		t.Fatal(err)
	}
	if got == goldenConfigHash {
		t.Error("setting state_store did not change the hash, so its env change would never deploy")
	}
}
