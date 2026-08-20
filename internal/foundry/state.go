package foundry

import (
	"encoding/json"
	"fmt"
	"maps"
)

// StateVersion is the current adapter state schema version. Bump it only for
// changes an older adapter cannot read.
const StateVersion = 1

// maxPriorVersions bounds the rollback history. Versions are immutable and
// cheap to record, but state travels in every request, so the list is capped
// rather than grown without limit.
const maxPriorVersions = 20

// InFlightVersion records a version whose creation was still running when the
// adapter stopped waiting. A later apply reconciles it rather than orphaning
// the resource or creating a duplicate alongside it.
type InFlightVersion struct {
	Version string `json:"version"`
	// PackHash and ConfigHash are the hashes the in-flight version was created
	// for, so a reconcile can tell whether it is still the version wanted.
	PackHash   string `json:"pack_hash,omitempty"`
	ConfigHash string `json:"config_hash,omitempty"`
}

// State is the opaque adapter state the CLI persists between operations.
//
// One pack maps to one agent with N immutable versions, so the agent name and
// the served version are load-bearing for idempotency: neither can be derived
// from the pack alone once sanitizing has been applied.
//
// Unknown fields survive a parse/marshal cycle so a newer adapter's state is
// not silently truncated by an older one reading and rewriting it.
type State struct {
	Version        int              `json:"version"`
	AdapterVersion string           `json:"adapter_version,omitempty"`
	PackHash       string           `json:"pack_hash,omitempty"`
	ConfigHash     string           `json:"config_hash,omitempty"`
	AgentName      string           `json:"agent_name,omitempty"`
	ServedVersion  string           `json:"served_version,omitempty"`
	PriorVersions  []string         `json:"prior_versions,omitempty"` // newest first
	StagedPackURI  string           `json:"staged_pack_uri,omitempty"`
	InFlight       *InFlightVersion `json:"in_flight,omitempty"`

	// unknown carries fields this adapter version does not recognize.
	unknown map[string]json.RawMessage
}

// newState returns an empty state at the current version.
func newState() *State {
	return &State{Version: StateVersion}
}

// knownStateFields lists the JSON keys State owns. Anything else is preserved
// verbatim in unknown.
var knownStateFields = map[string]bool{
	"version":         true,
	"adapter_version": true,
	"pack_hash":       true,
	"config_hash":     true,
	"agent_name":      true,
	"served_version":  true,
	"prior_versions":  true,
	"staged_pack_uri": true,
	"in_flight":       true,
}

// parseState unmarshals prior adapter state. An empty string yields a fresh
// empty state, which is the first-deploy case.
func parseState(raw string) (*State, error) {
	if raw == "" {
		return newState(), nil
	}

	var probe map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &probe); err != nil {
		return nil, fmt.Errorf("invalid adapter state JSON: %w", err)
	}

	var s State
	if err := json.Unmarshal([]byte(raw), &s); err != nil {
		return nil, fmt.Errorf("invalid adapter state: %w", err)
	}

	if s.Version > StateVersion {
		return nil, fmt.Errorf(
			"adapter state version %d is newer than this adapter supports (%d); upgrade the adapter",
			s.Version, StateVersion)
	}

	s.unknown = make(map[string]json.RawMessage)
	for k, v := range probe {
		if !knownStateFields[k] {
			s.unknown[k] = v
		}
	}

	return &s, nil
}

// recordVersion promotes version to the served version, pushing the one it
// replaces onto the history.
//
// The history makes a rollback a served-version PATCH rather than an image
// rebuild, which is the whole reason immutable versions are worth tracking.
func (s *State) recordVersion(version string) {
	if s.ServedVersion == version {
		return
	}
	if s.ServedVersion != "" {
		s.PriorVersions = append([]string{s.ServedVersion}, s.PriorVersions...)
		if len(s.PriorVersions) > maxPriorVersions {
			s.PriorVersions = s.PriorVersions[:maxPriorVersions]
		}
	}
	s.ServedVersion = version
}

// Marshal serializes the state, re-emitting any unknown fields it preserved.
func (s *State) Marshal() (string, error) {
	type stateAlias State
	encoded, err := json.Marshal((*stateAlias)(s))
	if err != nil {
		return "", fmt.Errorf("marshal adapter state: %w", err)
	}

	if len(s.unknown) == 0 {
		return string(encoded), nil
	}

	var merged map[string]json.RawMessage
	if decodeErr := json.Unmarshal(encoded, &merged); decodeErr != nil {
		return "", fmt.Errorf("merge adapter state: %w", decodeErr)
	}
	maps.Copy(merged, s.unknown)

	out, err := json.Marshal(merged)
	if err != nil {
		return "", fmt.Errorf("marshal merged adapter state: %w", err)
	}
	return string(out), nil
}
