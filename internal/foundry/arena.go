package foundry

import (
	"encoding/json"
	"fmt"

	sigsyaml "sigs.k8s.io/yaml"
)

// ArenaConfig is the narrow view of the arena config this adapter needs. The
// CLI serializes the whole config into PlanRequest.ArenaConfig; only provider
// definitions matter here.
type ArenaConfig struct {
	LoadedProviders map[string]*ArenaProvider `json:"loaded_providers,omitempty"`
	ProviderSpecs   map[string]*ArenaProvider `json:"provider_specs,omitempty"`

	// ToolSpecs carries each tool's execution config as raw JSON. The compiled
	// pack has only tool schemas, so without forwarding these a deployed engine
	// receives tool calls it cannot fulfill. The runtime owns this schema; the
	// adapter makes no decisions on its contents, so modeling it here would
	// create a second definition to drift.
	//
	// This holds only tools declared inline under the arena's `tool_specs:`.
	// Tools declared as `tools: - file: …` arrive in LoadedTools instead.
	ToolSpecs map[string]json.RawMessage `json:"tool_specs,omitempty"`

	// LoadedTools carries tool manifests the CLI read from disk, as raw YAML.
	// The arena loader also copies inline tool_specs into this list, so a tool
	// can appear in both — ToolSpecs wins, since it is already JSON and needs no
	// conversion.
	LoadedTools []ArenaToolData `json:"loaded_tools,omitempty"`
}

// ArenaToolData is one tool manifest as the CLI loaded it from disk.
type ArenaToolData struct {
	FilePath string `json:"file_path,omitempty"`
	Data     []byte `json:"data,omitempty"`
}

// arenaToolManifest is the envelope of a `kind: Tool` YAML manifest. Only the
// name is read here; the spec passes through to the runtime untouched.
type arenaToolManifest struct {
	Metadata struct {
		Name string `json:"name"`
	} `json:"metadata"`
	Spec struct {
		Name string `json:"name"`
	} `json:"spec"`
}

// ArenaProvider is a provider definition from the arena config.
type ArenaProvider struct {
	ID    string `json:"id,omitempty"`
	Type  string `json:"type"`
	Model string `json:"model"`
}

// parseArenaConfig unmarshals the arena config JSON. An empty string yields a
// nil config, which callers treat as "no arena providers available".
func parseArenaConfig(raw string) (*ArenaConfig, error) {
	if raw == "" {
		return nil, nil
	}
	var cfg ArenaConfig
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		return nil, fmt.Errorf("invalid arena config JSON: %w", err)
	}
	return &cfg, nil
}

// encodeToolSpecs collects every tool's execution config and serializes it for
// injection. Returns an empty string when the arena declares none, so the env
// var stays unset.
//
// Both sources are read. An arena that declares `tools: - file: …` populates
// only LoadedTools, so reading tool_specs alone would silently deploy an agent
// whose tools cannot run — the exact failure this whole path exists to prevent.
func encodeToolSpecs(a *ArenaConfig) (string, error) {
	if a == nil {
		return "", nil
	}

	specs := map[string]json.RawMessage{}
	for name, spec := range a.ToolSpecs {
		specs[name] = spec
	}

	mergeLoadedTools(specs, a.LoadedTools)

	if len(specs) == 0 {
		return "", nil
	}

	encoded, err := json.Marshal(specs)
	if err != nil {
		return "", fmt.Errorf("encode tool specs: %w", err)
	}
	return string(encoded), nil
}

// mergeLoadedTools converts YAML tool manifests to JSON specs and adds those
// not already present. Inline tool_specs win: the loader copies them into
// LoadedTools too, and taking the copy would be a needless round trip.
//
// A manifest that does not parse, or that names no tool, is skipped rather than
// failing the deploy — one malformed tool file should not block the agent.
func mergeLoadedTools(specs map[string]json.RawMessage, loaded []ArenaToolData) {
	for i := range loaded {
		name, spec := parseToolManifest(loaded[i].Data)
		if name == "" || spec == nil {
			continue
		}
		if _, exists := specs[name]; exists {
			continue
		}
		specs[name] = spec
	}
}

// parseToolManifest converts one YAML manifest into a tool name and its spec as
// JSON. Returns empty values for anything unparseable or unnamed, so a single
// malformed tool file cannot block the deploy.
func parseToolManifest(data []byte) (name string, spec json.RawMessage) {
	if len(data) == 0 {
		return "", nil
	}

	asJSON, err := sigsyaml.YAMLToJSON(data)
	if err != nil {
		return "", nil
	}

	var manifest arenaToolManifest
	if unmarshalErr := json.Unmarshal(asJSON, &manifest); unmarshalErr != nil {
		return "", nil
	}

	name = manifest.Spec.Name
	if name == "" {
		name = manifest.Metadata.Name
	}
	if name == "" {
		return "", nil
	}

	var envelope struct {
		Spec json.RawMessage `json:"spec"`
	}
	if unmarshalErr := json.Unmarshal(asJSON, &envelope); unmarshalErr != nil {
		return "", nil
	}
	return name, envelope.Spec
}

// provider looks up a provider by id, preferring loaded providers over specs.
// A nil receiver resolves nothing, so callers need not special-case it.
func (a *ArenaConfig) provider(id string) *ArenaProvider {
	if a == nil {
		return nil
	}
	if p, ok := a.LoadedProviders[id]; ok {
		return p
	}
	if p, ok := a.ProviderSpecs[id]; ok {
		return p
	}
	return nil
}
