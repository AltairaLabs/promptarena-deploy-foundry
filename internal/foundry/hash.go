package foundry

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
)

// hashString returns the hex-encoded SHA-256 of s.
func hashString(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// hashPack hashes the serialized pack. The CLI hands the pack over as JSON text,
// so the bytes are hashed as received rather than re-serialized — re-encoding
// could reorder keys and produce a spurious diff.
func hashPack(packJSON string) string {
	return hashString(packJSON)
}

// planConfigFingerprint is the subset of config that, when changed, means a new
// agent version must be created.
//
// Fields absent here are deliberately not deployed state: DryRun changes
// adapter behavior, and PackInlineLimitBytes and StagingContainer only select a
// delivery mechanism for the pack — the pack's own hash already covers its
// contents.
//
// Map fields are serialized through encoding/json, which sorts map keys, so the
// fingerprint is stable regardless of iteration order.
type planConfigFingerprint struct {
	Account            string            `json:"account"`
	Project            string            `json:"project"`
	Image              string            `json:"image"`
	CPU                string            `json:"cpu"`
	Memory             string            `json:"memory"`
	Protocols          []string          `json:"protocols"`
	IdleTimeoutMinutes int               `json:"idle_timeout_minutes"`
	Tags               map[string]string `json:"tags"`
	Bindings           []ResolvedBinding `json:"bindings"`
	Observability      *Observability    `json:"observability"`
	ToolSpecs          string            `json:"tool_specs"`
}

// hashPlanConfig hashes the deploy-affecting parts of the config, the resolved
// bindings, and the tool specs. All three become the agent version's container
// configuration and environment, so a change to any of them must show up as a
// diff — otherwise editing a tool's mock_result would leave the old value
// running with the plan reporting no change.
func hashPlanConfig(cfg *Config, resolved []ResolvedBinding, toolSpecs string) (string, error) {
	fingerprint := planConfigFingerprint{
		Account:            cfg.Account,
		Project:            cfg.Project,
		Image:              cfg.Image,
		CPU:                cfg.CPU,
		Memory:             cfg.Memory,
		Protocols:          cfg.Protocols,
		IdleTimeoutMinutes: cfg.IdleTimeoutMinutes,
		Tags:               cfg.Tags,
		Bindings:           resolved,
		Observability:      cfg.Observability,
		ToolSpecs:          toolSpecs,
	}

	encoded, err := json.Marshal(fingerprint)
	if err != nil {
		return "", fmt.Errorf("hash deploy config: %w", err)
	}
	return hashString(string(encoded)), nil
}
