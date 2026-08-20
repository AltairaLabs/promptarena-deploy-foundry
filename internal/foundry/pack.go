package foundry

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// a2aToolPrefix marks a tool that calls another agent. PromptKit namespaces
// these as a2a__<agent>__<skill> (runtime/tools/types.go).
const a2aToolPrefix = "a2a__"

// PackDelivery records how the pack reaches the runtime.
type PackDelivery struct {
	// Inline is true when the pack is injected as an environment variable
	// rather than staged to Blob storage.
	Inline bool
	// SizeBytes is the serialized pack size that drove the decision.
	SizeBytes int
}

// decidePackDelivery chooses between inline and staged delivery. A pack exactly
// at the limit is still inline; only exceeding it forces staging.
func decidePackDelivery(packJSON string, cfg *Config) PackDelivery {
	limit := cfg.PackInlineLimitBytes
	if limit <= 0 {
		limit = DefaultPackInlineLimitBytes
	}
	size := len(packJSON)
	return PackDelivery{Inline: size <= limit, SizeBytes: size}
}

// packView is the narrow slice of the pack this adapter reads. The full pack
// type lives in PromptKit; decoding only what is needed keeps the adapter
// tolerant of pack schema growth.
type packView struct {
	ID      string                     `json:"id"`
	Prompts map[string]json.RawMessage `json:"prompts"`
	Tools   map[string]json.RawMessage `json:"tools"`
	Agents  *packAgentsView            `json:"agents"`
}

// packAgentsView is the pack's agents section.
type packAgentsView struct {
	Entry   string                     `json:"entry"`
	Members map[string]json.RawMessage `json:"members"`
}

// decodePack unmarshals the narrow pack view.
func decodePack(packJSON string) (*packView, error) {
	var pack packView
	if err := json.Unmarshal([]byte(packJSON), &pack); err != nil {
		return nil, fmt.Errorf("invalid pack JSON: %w", err)
	}
	return &pack, nil
}

// packID returns the pack's id, which seeds the Foundry agent name.
func packID(packJSON string) (string, error) {
	pack, err := decodePack(packJSON)
	if err != nil {
		return "", err
	}
	if pack.ID == "" {
		return "", fmt.Errorf("pack has no id")
	}
	return pack.ID, nil
}

// packMembers lists the agents the deployment will serve and names the entry.
//
// Unlike vertex, these do not become separate resources: one Foundry agent
// serves the whole pack and PromptKit routes between members in-process. They
// are enumerated so the plan can say what is inside the version it is about to
// create, and so a multi-member pack can be called out.
//
// Names are sorted so plans are stable across runs.
func packMembers(packJSON string) (members []string, entry string, err error) {
	pack, err := decodePack(packJSON)
	if err != nil {
		return nil, "", err
	}

	names, entry := memberNames(pack)
	if len(names) == 0 {
		return nil, "", fmt.Errorf("pack has no prompts to deploy")
	}

	sort.Strings(names)
	return names, entry, nil
}

// memberNames returns the pack's member names and which one is the entry.
func memberNames(pack *packView) (names []string, entry string) {
	if pack.Agents != nil && len(pack.Agents.Members) > 0 {
		for name := range pack.Agents.Members {
			names = append(names, name)
		}
		return names, pack.Agents.Entry
	}

	for name := range pack.Prompts {
		names = append(names, name)
	}
	if len(names) == 1 {
		return names, names[0]
	}
	return names, ""
}

// hasA2ATools reports whether the pack declares agent-to-agent tools.
//
// These are remote calls — PromptKit's A2A bridge discovers an agent card over
// HTTP — so serving the whole pack from one agent does not resolve them.
// Malformed JSON reports false; packMembers surfaces the parse error.
func hasA2ATools(packJSON string) bool {
	pack, err := decodePack(packJSON)
	if err != nil {
		return false
	}
	for name := range pack.Tools {
		if strings.HasPrefix(name, a2aToolPrefix) {
			return true
		}
	}
	return false
}
