package foundry

import (
	"strings"
	"testing"
)

func TestParseConfigAppliesDefaults(t *testing.T) {
	cfg, err := parseConfig(`{"account":"acct","project":"proj","image":"acr.azurecr.io/x:1"}`)
	if err != nil {
		t.Fatalf("parseConfig: %v", err)
	}
	if cfg.PackInlineLimitBytes != DefaultPackInlineLimitBytes {
		t.Errorf("PackInlineLimitBytes = %d, want %d", cfg.PackInlineLimitBytes, DefaultPackInlineLimitBytes)
	}
	// A config that names no protocol must not deploy an agent declaring
	// contracts the container does not serve.
	if len(cfg.Protocols) != 1 || cfg.Protocols[0] != ProtocolInvocations {
		t.Errorf("Protocols = %v, want [%s]", cfg.Protocols, ProtocolInvocations)
	}
}

func TestParseConfigRejectsMalformedJSON(t *testing.T) {
	if _, err := parseConfig(`{`); err == nil {
		t.Fatal("parseConfig accepted malformed JSON")
	}
}

func TestParseConfigPreservesExplicitValues(t *testing.T) {
	cfg, err := parseConfig(`{
		"account":"acct","project":"proj","image":"acr.azurecr.io/x:1",
		"protocols":["responses"],"pack_inline_limit_bytes":10
	}`)
	if err != nil {
		t.Fatalf("parseConfig: %v", err)
	}
	if cfg.PackInlineLimitBytes != 10 {
		t.Errorf("PackInlineLimitBytes = %d, want 10", cfg.PackInlineLimitBytes)
	}
	if len(cfg.Protocols) != 1 || cfg.Protocols[0] != ProtocolResponses {
		t.Errorf("Protocols = %v, want [responses]", cfg.Protocols)
	}
}

// validateStructure checks the config's own fields; bindings and tags are
// validated separately so their errors read independently.
func TestValidateStructure(t *testing.T) {
	tests := []struct {
		name       string
		cfg        Config
		wantErrSub string
	}{
		{
			name:       "account is required",
			cfg:        Config{Project: "p", Image: "acr.azurecr.io/x:1"},
			wantErrSub: "account is required",
		},
		{
			name:       "project is required",
			cfg:        Config{Account: "a", Image: "acr.azurecr.io/x:1"},
			wantErrSub: "project is required",
		},
		{
			name:       "image is required",
			cfg:        Config{Account: "a", Project: "p"},
			wantErrSub: "image is required",
		},
		{
			name:       "cpu must be a legal value",
			cfg:        Config{Account: "a", Project: "p", Image: "i", CPU: "4", Memory: "4Gi"},
			wantErrSub: `cpu "4"`,
		},
		{
			name:       "memory must pair with cpu",
			cfg:        Config{Account: "a", Project: "p", Image: "i", CPU: "1", Memory: "4Gi"},
			wantErrSub: "cpu 1 pairs with memory 2Gi",
		},
		{
			name:       "cpu without memory is rejected",
			cfg:        Config{Account: "a", Project: "p", Image: "i", CPU: "1"},
			wantErrSub: "cpu and memory are required",
		},
		{
			name:       "memory without cpu is rejected",
			cfg:        Config{Account: "a", Project: "p", Image: "i", Memory: "2Gi"},
			wantErrSub: "cpu and memory are required",
		},
		{
			name:       "unknown protocol is rejected",
			cfg:        Config{Account: "a", Project: "p", Image: "i", Protocols: []string{"grpc"}},
			wantErrSub: `protocol "grpc"`,
		},
		{
			name:       "idle timeout below the floor is rejected",
			cfg:        Config{Account: "a", Project: "p", Image: "i", IdleTimeoutMinutes: 4},
			wantErrSub: "idle_timeout_minutes 4",
		},
		{
			name:       "idle timeout above the ceiling is rejected",
			cfg:        Config{Account: "a", Project: "p", Image: "i", IdleTimeoutMinutes: 61},
			wantErrSub: "idle_timeout_minutes 61",
		},
		{
			name: "staging container must be an https URL",
			cfg: Config{Account: "a", Project: "p", Image: "i",
				StagingContainer: "acct.blob.core.windows.net/pk"},
			wantErrSub: "staging_container",
		},
		{
			name: "otlp endpoint needs a scheme",
			cfg: Config{Account: "a", Project: "p", Image: "i",
				Observability: &Observability{TracingEnabled: true, OTLPEndpoint: "collector:4318"}},
			wantErrSub: "must be a full URL",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			errs := tt.cfg.validateStructure()
			if !containsSubstring(errs, tt.wantErrSub) {
				t.Errorf("validateStructure() = %v, want an error containing %q", errs, tt.wantErrSub)
			}
		})
	}
}

// Unlike vertex, tracing_enabled alone is valid here: Foundry injects
// OTEL_EXPORTER_OTLP_ENDPOINT, so an explicit endpoint only overrides it.
func TestTracingEnabledWithoutEndpointIsValid(t *testing.T) {
	cfg := Config{Account: "a", Project: "p", Image: "i", CPU: "1", Memory: "2Gi",
		Observability: &Observability{TracingEnabled: true}}

	if errs := cfg.validateStructure(); len(errs) != 0 {
		t.Errorf("validateStructure() = %v, want no errors", errs)
	}
}

func TestValidateStructureAcceptsEveryLegalResourcePair(t *testing.T) {
	for cpu, memory := range legalResourcePairs {
		cfg := Config{Account: "a", Project: "p", Image: "i", CPU: cpu, Memory: memory}
		if errs := cfg.validateStructure(); len(errs) != 0 {
			t.Errorf("cpu %q / memory %q: validateStructure() = %v, want no errors", cpu, memory, errs)
		}
	}
}

func TestValidateStructureAcceptsAMinimalConfig(t *testing.T) {
	cfg, err := parseConfig(
		`{"account":"a","project":"p","image":"acr.azurecr.io/x:1","cpu":"1","memory":"2Gi"}`)
	if err != nil {
		t.Fatalf("parseConfig: %v", err)
	}
	if errs := cfg.validateStructure(); len(errs) != 0 {
		t.Errorf("validateStructure() = %v, want no errors", errs)
	}
}

// Foundry has no default sizing: a create without cpu and memory comes back
// 400 "Required property 'cpu' is missing". The adapter used to accept such a
// config and merely warn, which turned a config error into an apply failure.
func TestValidateStructureRequiresSizing(t *testing.T) {
	cfg, err := parseConfig(`{"account":"a","project":"p","image":"acr.azurecr.io/x:1"}`)
	if err != nil {
		t.Fatalf("parseConfig: %v", err)
	}
	errs := cfg.validateStructure()
	if !containsSubstring(errs, "cpu and memory are required") {
		t.Errorf("validateStructure() = %v, want a required-sizing error", errs)
	}
}

// containsSubstring reports whether any string in list contains sub.
func containsSubstring(list []string, sub string) bool {
	for _, s := range list {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

// The default is memory, so an unconfigured deploy needs no state_store block.
func TestValidateStateStoreAcceptsAnAbsentBlock(t *testing.T) {
	cfg := &Config{}
	if errs := cfg.validateStateStore(); len(errs) != 0 {
		t.Errorf("validateStateStore = %v, want none", errs)
	}
}

func TestValidateStateStoreRejectsAnUnknownKind(t *testing.T) {
	cfg := &Config{StateStore: &StateStore{Kind: "postgres"}}

	errs := cfg.validateStateStore()
	if len(errs) == 0 {
		t.Fatal("validateStateStore accepted an unknown kind")
	}
	if !strings.Contains(errs[0], "postgres") {
		t.Errorf("error = %q, want it to name the bad kind", errs[0])
	}
}

// redis without a URL variable would deploy an agent that cannot reach its
// store, and nothing outside the container would show it.
func TestValidateStateStoreRedisNeedsAURLVariable(t *testing.T) {
	cfg := &Config{StateStore: &StateStore{Kind: StateStoreRedis}}

	errs := cfg.validateStateStore()
	if len(errs) == 0 {
		t.Fatal("validateStateStore accepted redis with no url_from_env")
	}
	if !strings.Contains(errs[0], "url_from_env") {
		t.Errorf("error = %q, want it to name the missing field", errs[0])
	}
}

// A field that applies to another kind is a misunderstanding worth reporting:
// silently ignoring root on a redis store leaves the operator believing they
// configured something.
func TestValidateStateStoreRejectsFieldsForTheWrongKind(t *testing.T) {
	cfg := &Config{StateStore: &StateStore{Kind: StateStoreMemory, Root: "/tmp/x"}}
	if errs := cfg.validateStateStore(); len(errs) == 0 {
		t.Error("validateStateStore accepted root on a memory store")
	}

	cfg = &Config{StateStore: &StateStore{
		Kind: StateStoreFile, Root: "/tmp/x", URLFromEnv: "REDIS_URL"}}
	if errs := cfg.validateStateStore(); len(errs) == 0 {
		t.Error("validateStateStore accepted url_from_env on a file store")
	}
}

func TestValidateStateStoreAcceptsEachKind(t *testing.T) {
	for _, cfg := range []*Config{
		{StateStore: &StateStore{Kind: StateStoreMemory}},
		{StateStore: &StateStore{Kind: StateStoreFile, Root: "/mnt/state"}},
		{StateStore: &StateStore{Kind: StateStoreRedis, URLFromEnv: "REDIS_URL"}},
	} {
		if errs := cfg.validateStateStore(); len(errs) != 0 {
			t.Errorf("kind %s: %v", cfg.StateStore.Kind, errs)
		}
	}
}
