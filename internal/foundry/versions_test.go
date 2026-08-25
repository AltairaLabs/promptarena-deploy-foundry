package foundry

import (
	"encoding/json"
	"strings"
	"testing"
)

func baseSpecInput() *specInput {
	return &specInput{
		Cfg: &Config{
			Account: "acct", Project: "proj",
			Image: "acr.azurecr.io/x:1", CPU: "1", Memory: "2Gi",
			Protocols: []string{ProtocolInvocations}, IdleTimeoutMinutes: 20,
			PackInlineLimitBytes: DefaultPackInlineLimitBytes,
			Tags:                 map[string]string{"team": "platform"},
		},
		AgentName: "solo-pack",
		PackID:    "solo-pack",
		PackJSON:  singleAgentPack,
		Bindings:  []ResolvedBinding{{Name: "default", Role: RoleLLM, Type: "azure", Model: "gpt4o-deploy"}},
		Delivery:  PackDelivery{Inline: true, SizeBytes: len(singleAgentPack)},
	}
}

func TestBuildAgentSpecCarriesConfig(t *testing.T) {
	spec, errs := buildAgentSpec(baseSpecInput())
	if len(errs) != 0 {
		t.Fatalf("buildAgentSpec errors = %v", errs)
	}

	if spec.Name != "solo-pack" {
		t.Errorf("Name = %q, want solo-pack", spec.Name)
	}
	if spec.Image != "acr.azurecr.io/x:1" || spec.CPU != "1" || spec.Memory != "2Gi" {
		t.Errorf("spec = %+v, want the configured image and sizing", spec)
	}
	if spec.IdleTimeoutMinutes != 20 {
		t.Errorf("IdleTimeoutMinutes = %d, want 20", spec.IdleTimeoutMinutes)
	}
}

func TestBuildAgentSpecInlinesThePack(t *testing.T) {
	spec, errs := buildAgentSpec(baseSpecInput())
	if len(errs) != 0 {
		t.Fatalf("buildAgentSpec errors = %v", errs)
	}

	if spec.Env[envPackJSON] != singleAgentPack {
		t.Errorf("%s = %q, want the pack inlined", envPackJSON, spec.Env[envPackJSON])
	}
	if _, present := spec.Env[envPackURI]; present {
		t.Errorf("%s was set for an inline pack", envPackURI)
	}
}

func TestBuildAgentSpecUsesTheStagedURI(t *testing.T) {
	in := baseSpecInput()
	in.Delivery = PackDelivery{Inline: false, SizeBytes: 40000}
	in.StagedPackURI = "https://acct.blob.core.windows.net/pk/pack.json"

	spec, errs := buildAgentSpec(in)
	if len(errs) != 0 {
		t.Fatalf("buildAgentSpec errors = %v", errs)
	}
	if spec.Env[envPackURI] != in.StagedPackURI {
		t.Errorf("%s = %q, want the staged URI", envPackURI, spec.Env[envPackURI])
	}
	if _, present := spec.Env[envPackJSON]; present {
		t.Errorf("%s was set alongside a staged pack", envPackJSON)
	}
}

// Deploying a staged pack with nowhere to read it from would start a container
// that cannot load its own pack.
func TestBuildAgentSpecRejectsStagedPackWithoutURI(t *testing.T) {
	in := baseSpecInput()
	in.Delivery = PackDelivery{Inline: false, SizeBytes: 40000}

	_, errs := buildAgentSpec(in)
	if !containsSubstring(errs, "inline limit") {
		t.Errorf("errors = %v, want one about the missing staged URI", errs)
	}
}

func TestBuildAgentSpecEncodesBindings(t *testing.T) {
	spec, errs := buildAgentSpec(baseSpecInput())
	if len(errs) != 0 {
		t.Fatalf("buildAgentSpec errors = %v", errs)
	}

	var decoded []ResolvedBinding
	if err := json.Unmarshal([]byte(spec.Env[envProviders]), &decoded); err != nil {
		t.Fatalf("%s is not valid JSON: %v", envProviders, err)
	}
	if len(decoded) != 1 || decoded[0].Model != "gpt4o-deploy" {
		t.Errorf("bindings = %+v, want the resolved binding", decoded)
	}
}

// One Foundry agent serves the whole pack, so the entry must be left to the
// pack's own agents.entry. Pinning PROMPTPACK_AGENT would defeat that.
func TestBuildAgentSpecDoesNotPinAnAgent(t *testing.T) {
	spec, errs := buildAgentSpec(baseSpecInput())
	if len(errs) != 0 {
		t.Fatalf("buildAgentSpec errors = %v", errs)
	}
	if v, present := spec.Env[envAgentName]; present {
		t.Errorf("%s = %q, want it unset so the pack's entry wins", envAgentName, v)
	}
}

func TestBuildAgentSpecTracingOff(t *testing.T) {
	spec, _ := buildAgentSpec(baseSpecInput())

	if _, present := spec.Env[envTracingEnabled]; present {
		t.Error("tracing env was set with tracing disabled; an unconfigured deploy must send nothing")
	}
}

// Foundry injects OTEL_EXPORTER_OTLP_ENDPOINT, so leaving it unset is correct;
// setting it empty would override the platform's own value with nothing.
func TestBuildAgentSpecTracingOnWithoutOverride(t *testing.T) {
	in := baseSpecInput()
	in.Cfg.Observability = &Observability{TracingEnabled: true}

	spec, _ := buildAgentSpec(in)

	if spec.Env[envTracingEnabled] != "true" {
		t.Errorf("%s = %q, want true", envTracingEnabled, spec.Env[envTracingEnabled])
	}
	if _, present := spec.Env[envOTLPEndpoint]; present {
		t.Errorf("%s was set with no override configured", envOTLPEndpoint)
	}
}

func TestBuildAgentSpecTracingOnWithOverride(t *testing.T) {
	in := baseSpecInput()
	in.Cfg.Observability = &Observability{TracingEnabled: true, OTLPEndpoint: "https://collector:4318"}

	spec, _ := buildAgentSpec(in)

	if spec.Env[envOTLPEndpoint] != "https://collector:4318" {
		t.Errorf("%s = %q, want the override", envOTLPEndpoint, spec.Env[envOTLPEndpoint])
	}
}

func TestBuildAgentSpecToolSpecs(t *testing.T) {
	in := baseSpecInput()
	in.ToolSpecsJSON = `{"lookup":{"kind":"http"}}`

	spec, _ := buildAgentSpec(in)

	if spec.Env[envToolSpecs] != in.ToolSpecsJSON {
		t.Errorf("%s = %q, want the tool specs", envToolSpecs, spec.Env[envToolSpecs])
	}
}

func TestBuildAgentSpecOmitsEmptyToolSpecs(t *testing.T) {
	spec, _ := buildAgentSpec(baseSpecInput())

	if _, present := spec.Env[envToolSpecs]; present {
		t.Errorf("%s was set with no tools declared", envToolSpecs)
	}
}

// Managed tags identify what the adapter owns; user tags must not be able to
// overwrite them, or a deployment becomes unattributable.
func TestBuildAgentSpecTagsAreManagedAndMerged(t *testing.T) {
	in := baseSpecInput()
	in.Cfg.Tags = map[string]string{"team": "platform", tagManagedBy: "someone-else"}

	spec, _ := buildAgentSpec(in)

	if spec.Metadata["team"] != "platform" {
		t.Errorf("user tag lost: %v", spec.Metadata)
	}
	if spec.Metadata[tagManagedBy] != managedByValue {
		t.Errorf("%s = %q, want %q — managed tags must win",
			tagManagedBy, spec.Metadata[tagManagedBy], managedByValue)
	}
	if spec.Metadata[tagPackID] != "solo-pack" {
		t.Errorf("%s = %q, want the pack id", tagPackID, spec.Metadata[tagPackID])
	}
}

// The env carries the whole pack, so an oversized environment is worth
// catching before the API rejects it mid-apply.
func TestBuildAgentSpecEnvIsDeterministic(t *testing.T) {
	first, _ := buildAgentSpec(baseSpecInput())
	for range 20 {
		next, _ := buildAgentSpec(baseSpecInput())
		if len(next.Env) != len(first.Env) {
			t.Fatalf("env size drifted: %d vs %d", len(next.Env), len(first.Env))
		}
		for k, v := range first.Env {
			if next.Env[k] != v {
				t.Fatalf("env[%s] drifted: %q vs %q", k, next.Env[k], v)
			}
		}
	}
}

// The runtime needs an Azure OpenAI endpoint to bind a provider against.
// Deriving it from the account means the common case needs no extra config.
func TestBuildAgentSpecDerivesTheAzureEndpoint(t *testing.T) {
	spec, errs := buildAgentSpec(baseSpecInput())
	if len(errs) != 0 {
		t.Fatalf("buildAgentSpec errors = %v", errs)
	}

	want := "https://acct.openai.azure.com/"
	if spec.Env[envAzureEndpoint] != want {
		t.Errorf("%s = %q, want %q", envAzureEndpoint, spec.Env[envAzureEndpoint], want)
	}
}

// An account whose inference endpoint is not the conventional one still has to
// be deployable, so an explicit value wins.
func TestBuildAgentSpecHonoursAnExplicitAzureEndpoint(t *testing.T) {
	in := baseSpecInput()
	in.Cfg.AzureEndpoint = "https://custom.openai.azure.com/"

	spec, _ := buildAgentSpec(in)

	if spec.Env[envAzureEndpoint] != "https://custom.openai.azure.com/" {
		t.Errorf("%s = %q, want the override", envAzureEndpoint, spec.Env[envAzureEndpoint])
	}
}

// The default writes nothing: an absent variable already means memory, and a
// value the runtime would have to keep in step is a value that can drift.
func TestAddStateStoreEnvWritesNothingForTheDefault(t *testing.T) {
	for _, store := range []*StateStore{nil, {}, {Kind: StateStoreMemory}} {
		env := map[string]string{}
		if errs := addStateStoreEnv(env, store, func(string) string { return "" }); len(errs) != 0 {
			t.Fatalf("addStateStoreEnv: %v", errs)
		}
		if len(env) != 0 {
			t.Errorf("env = %v, want empty", env)
		}
	}
}

func TestAddStateStoreEnvCarriesTheFileRoot(t *testing.T) {
	env := map[string]string{}
	addStateStoreEnv(env, &StateStore{Kind: StateStoreFile, Root: "/mnt/state"},
		func(string) string { return "" })

	if env[envStateStoreKind] != StateStoreFile {
		t.Errorf("%s = %q", envStateStoreKind, env[envStateStoreKind])
	}
	if env[envStateStoreRoot] != "/mnt/state" {
		t.Errorf("%s = %q", envStateStoreRoot, env[envStateStoreRoot])
	}
}

// The connection string is read at apply time from the named variable. It is
// never taken from the deploy config, which is what keeps the secret out of
// version control even though it lands in the agent definition.
func TestAddStateStoreEnvResolvesTheRedisURL(t *testing.T) {
	env := map[string]string{}
	errs := addStateStoreEnv(env, &StateStore{Kind: StateStoreRedis, URLFromEnv: "MY_REDIS"},
		func(name string) string {
			if name == "MY_REDIS" {
				return "redis://cache:6379/0"
			}
			return ""
		})

	if len(errs) != 0 {
		t.Fatalf("addStateStoreEnv: %v", errs)
	}
	if env[envStateStoreURL] != "redis://cache:6379/0" {
		t.Errorf("%s = %q", envStateStoreURL, env[envStateStoreURL])
	}
}

// An unset variable has to fail the deploy. The alternative is an agent that
// starts, answers, and quietly remembers nothing.
func TestAddStateStoreEnvRefusesAnUnsetURLVariable(t *testing.T) {
	env := map[string]string{}
	errs := addStateStoreEnv(env, &StateStore{Kind: StateStoreRedis, URLFromEnv: "MISSING"},
		func(string) string { return "" })

	if len(errs) == 0 {
		t.Fatal("addStateStoreEnv accepted an unset URL variable")
	}
	if !strings.Contains(errs[0], "MISSING") {
		t.Errorf("error = %q, want it to name the variable", errs[0])
	}
}
