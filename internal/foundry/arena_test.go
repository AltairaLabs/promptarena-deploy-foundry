package foundry

import (
	"encoding/json"
	"testing"
)

func TestParseArenaConfigEmptyYieldsNil(t *testing.T) {
	cfg, err := parseArenaConfig("")
	if err != nil {
		t.Fatalf("parseArenaConfig: %v", err)
	}
	if cfg != nil {
		t.Errorf("parseArenaConfig(\"\") = %v, want nil", cfg)
	}
}

func TestParseArenaConfigRejectsMalformedJSON(t *testing.T) {
	if _, err := parseArenaConfig(`{`); err == nil {
		t.Fatal("parseArenaConfig accepted malformed JSON")
	}
}

func TestArenaProviderLookupPrefersLoadedOverSpecs(t *testing.T) {
	cfg := &ArenaConfig{
		LoadedProviders: map[string]*ArenaProvider{
			"gpt4o": {Type: "azure", Model: "loaded-deployment"},
		},
		ProviderSpecs: map[string]*ArenaProvider{
			"gpt4o": {Type: "azure", Model: "spec-deployment"},
		},
	}

	got := cfg.provider("gpt4o")
	if got == nil || got.Model != "loaded-deployment" {
		t.Errorf("provider() = %+v, want the loaded provider", got)
	}
}

func TestArenaProviderLookupFallsBackToSpecs(t *testing.T) {
	cfg := &ArenaConfig{
		ProviderSpecs: map[string]*ArenaProvider{"gpt4o": {Type: "azure", Model: "d"}},
	}

	if cfg.provider("gpt4o") == nil {
		t.Error("provider() = nil, want the spec provider")
	}
}

// A nil arena resolves nothing, so callers need not special-case it.
func TestArenaProviderLookupOnNilConfig(t *testing.T) {
	var cfg *ArenaConfig
	if got := cfg.provider("gpt4o"); got != nil {
		t.Errorf("provider() = %+v, want nil", got)
	}
}

func TestArenaProviderLookupMissing(t *testing.T) {
	cfg := &ArenaConfig{ProviderSpecs: map[string]*ArenaProvider{"a": {}}}
	if got := cfg.provider("b"); got != nil {
		t.Errorf("provider(\"b\") = %+v, want nil", got)
	}
}

func TestEncodeToolSpecsNilArena(t *testing.T) {
	got, err := encodeToolSpecs(nil)
	if err != nil {
		t.Fatalf("encodeToolSpecs: %v", err)
	}
	if got != "" {
		t.Errorf("encodeToolSpecs(nil) = %q, want empty", got)
	}
}

func TestEncodeToolSpecsInlineSpecs(t *testing.T) {
	arena := &ArenaConfig{
		ToolSpecs: map[string]json.RawMessage{"lookup": json.RawMessage(`{"kind":"http"}`)},
	}

	got, err := encodeToolSpecs(arena)
	if err != nil {
		t.Fatalf("encodeToolSpecs: %v", err)
	}

	var decoded map[string]json.RawMessage
	if err := json.Unmarshal([]byte(got), &decoded); err != nil {
		t.Fatalf("result is not valid JSON: %v", err)
	}
	if _, ok := decoded["lookup"]; !ok {
		t.Errorf("encodeToolSpecs() = %s, want a lookup entry", got)
	}
}

// An arena declaring `tools: - file: …` populates only LoadedTools. Reading
// tool_specs alone would deploy an agent whose tools cannot run.
func TestEncodeToolSpecsReadsLoadedToolManifests(t *testing.T) {
	manifest := []byte("kind: Tool\nmetadata:\n  name: weather\nspec:\n  name: weather\n  kind: http\n")
	arena := &ArenaConfig{LoadedTools: []ArenaToolData{{Data: manifest}}}

	got, err := encodeToolSpecs(arena)
	if err != nil {
		t.Fatalf("encodeToolSpecs: %v", err)
	}

	var decoded map[string]json.RawMessage
	if err := json.Unmarshal([]byte(got), &decoded); err != nil {
		t.Fatalf("result is not valid JSON: %v", err)
	}
	if _, ok := decoded["weather"]; !ok {
		t.Errorf("encodeToolSpecs() = %s, want a weather entry", got)
	}
}

// The loader copies inline tool_specs into LoadedTools too, so a tool can
// appear in both. The inline spec is already JSON and wins.
func TestEncodeToolSpecsInlineWinsOverLoaded(t *testing.T) {
	manifest := []byte("kind: Tool\nspec:\n  name: weather\n  source: loaded\n")
	arena := &ArenaConfig{
		ToolSpecs:   map[string]json.RawMessage{"weather": json.RawMessage(`{"source":"inline"}`)},
		LoadedTools: []ArenaToolData{{Data: manifest}},
	}

	got, err := encodeToolSpecs(arena)
	if err != nil {
		t.Fatalf("encodeToolSpecs: %v", err)
	}

	var decoded map[string]struct {
		Source string `json:"source"`
	}
	if err := json.Unmarshal([]byte(got), &decoded); err != nil {
		t.Fatalf("result is not valid JSON: %v", err)
	}
	if decoded["weather"].Source != "inline" {
		t.Errorf("weather.source = %q, want inline", decoded["weather"].Source)
	}
}

// One malformed tool file must not block the whole deploy.
func TestEncodeToolSpecsSkipsUnparseableManifests(t *testing.T) {
	arena := &ArenaConfig{
		LoadedTools: []ArenaToolData{
			{Data: []byte("\t: not: valid: yaml:\n  -")},
			{Data: []byte("kind: Tool\nspec:\n  name: good\n")},
		},
	}

	got, err := encodeToolSpecs(arena)
	if err != nil {
		t.Fatalf("encodeToolSpecs: %v", err)
	}

	var decoded map[string]json.RawMessage
	if err := json.Unmarshal([]byte(got), &decoded); err != nil {
		t.Fatalf("result is not valid JSON: %v", err)
	}
	if _, ok := decoded["good"]; !ok {
		t.Errorf("encodeToolSpecs() = %s, want the well-formed tool to survive", got)
	}
}

func TestEncodeToolSpecsSkipsUnnamedManifests(t *testing.T) {
	arena := &ArenaConfig{LoadedTools: []ArenaToolData{{Data: []byte("kind: Tool\nspec:\n  kind: http\n")}}}

	got, err := encodeToolSpecs(arena)
	if err != nil {
		t.Fatalf("encodeToolSpecs: %v", err)
	}
	if got != "" {
		t.Errorf("encodeToolSpecs() = %q, want empty when no tool is named", got)
	}
}

// Falls back to metadata.name when spec.name is absent.
func TestEncodeToolSpecsUsesMetadataName(t *testing.T) {
	arena := &ArenaConfig{
		LoadedTools: []ArenaToolData{{Data: []byte("kind: Tool\nmetadata:\n  name: fromMeta\nspec:\n  kind: http\n")}},
	}

	got, err := encodeToolSpecs(arena)
	if err != nil {
		t.Fatalf("encodeToolSpecs: %v", err)
	}

	var decoded map[string]json.RawMessage
	if err := json.Unmarshal([]byte(got), &decoded); err != nil {
		t.Fatalf("result is not valid JSON: %v", err)
	}
	if _, ok := decoded["fromMeta"]; !ok {
		t.Errorf("encodeToolSpecs() = %s, want a fromMeta entry", got)
	}
}

func TestEncodeToolSpecsEmptyArena(t *testing.T) {
	got, err := encodeToolSpecs(&ArenaConfig{})
	if err != nil {
		t.Fatalf("encodeToolSpecs: %v", err)
	}
	if got != "" {
		t.Errorf("encodeToolSpecs() = %q, want empty", got)
	}
}
