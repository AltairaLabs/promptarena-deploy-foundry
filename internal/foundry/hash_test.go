package foundry

import "testing"

func TestHashPackDistinguishesDifferentPacks(t *testing.T) {
	if hashPack(`{"id":"p","prompts":{"a":{}}}`) == hashPack(`{"id":"q","prompts":{"a":{}}}`) {
		t.Error("hashPack collided on different packs")
	}
}

// Every compile stamps a new created_at; a pack that differs only there, or
// only in key order, is the same pack and must not create a version.
func TestHashPackIgnoresCompileTimeAndKeyOrder(t *testing.T) {
	a := `{"id":"p","compilation":{"created_at":"2026-11-19T19:49:45Z","schema":"v1"},"prompts":{"a":{}}}`
	b := `{"prompts":{"a":{}},"compilation":{"schema":"v1","created_at":"2026-11-19T20:01:02Z"},"id":"p"}`
	if hashPack(a) != hashPack(b) {
		t.Error("hashPack changed with only created_at and key order, so every apply would create a version")
	}
}

func baseHashConfig() *Config {
	return &Config{
		Account: "acct", Project: "proj", Image: "acr.azurecr.io/x:1",
		CPU: "1", Memory: "2Gi",
		Protocols:          []string{ProtocolInvocations},
		IdleTimeoutMinutes: 15,
		Tags:               map[string]string{"team": "platform"},
	}
}

func hashOf(t *testing.T, cfg *Config, resolved []ResolvedBinding, toolSpecs string) string {
	t.Helper()
	got, err := hashPlanConfig(cfg, resolved, toolSpecs)
	if err != nil {
		t.Fatalf("hashPlanConfig: %v", err)
	}
	return got
}

func TestHashPlanConfigIsStable(t *testing.T) {
	cfg := baseHashConfig()
	resolved := []ResolvedBinding{{Name: "default", Role: RoleLLM, Type: "azure", Model: "m"}}

	first := hashOf(t, cfg, resolved, "")
	for range 20 {
		if got := hashOf(t, cfg, resolved, ""); got != first {
			t.Fatalf("hashPlanConfig is not stable: %q vs %q", got, first)
		}
	}
}

// Every field that becomes part of the deployed agent version must move the
// hash — otherwise an edit deploys nothing while the plan reports no change.
func TestHashPlanConfigTracksDeployAffectingFields(t *testing.T) {
	resolved := []ResolvedBinding{{Name: "default", Role: RoleLLM, Type: "azure", Model: "m"}}
	base := hashOf(t, baseHashConfig(), resolved, "")

	tests := []struct {
		name   string
		mutate func(*Config)
	}{
		{"account", func(c *Config) { c.Account = "other" }},
		{"project", func(c *Config) { c.Project = "other" }},
		{"image", func(c *Config) { c.Image = "acr.azurecr.io/x:2" }},
		{"cpu", func(c *Config) { c.CPU = "2"; c.Memory = "4Gi" }},
		{"protocols", func(c *Config) { c.Protocols = []string{ProtocolResponses} }},
		{"idle timeout", func(c *Config) { c.IdleTimeoutMinutes = 30 }},
		{"tags", func(c *Config) { c.Tags = map[string]string{"team": "other"} }},
		{"observability", func(c *Config) { c.Observability = &Observability{TracingEnabled: true} }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := baseHashConfig()
			tt.mutate(cfg)
			if hashOf(t, cfg, resolved, "") == base {
				t.Errorf("changing %s did not move the config hash", tt.name)
			}
		})
	}
}

// Editing a tool's execution config must show up as a diff: tool specs become
// container environment, so otherwise the old value keeps running.
func TestHashPlanConfigTracksToolSpecs(t *testing.T) {
	cfg := baseHashConfig()
	resolved := []ResolvedBinding{{Name: "default", Role: RoleLLM, Type: "azure", Model: "m"}}

	if hashOf(t, cfg, resolved, `{"a":1}`) == hashOf(t, cfg, resolved, `{"a":2}`) {
		t.Error("changing tool specs did not move the config hash")
	}
}

func TestHashPlanConfigTracksBindings(t *testing.T) {
	cfg := baseHashConfig()
	a := []ResolvedBinding{{Name: "default", Role: RoleLLM, Type: "azure", Model: "m1"}}
	b := []ResolvedBinding{{Name: "default", Role: RoleLLM, Type: "azure", Model: "m2"}}

	if hashOf(t, cfg, a, "") == hashOf(t, cfg, b, "") {
		t.Error("changing a binding's model did not move the config hash")
	}
}

// These change adapter behavior, not the deployed agent, so they must NOT
// force a new version.
func TestHashPlanConfigIgnoresNonDeployedFields(t *testing.T) {
	resolved := []ResolvedBinding{{Name: "default", Role: RoleLLM, Type: "azure", Model: "m"}}
	base := hashOf(t, baseHashConfig(), resolved, "")

	tests := []struct {
		name   string
		mutate func(*Config)
	}{
		{"dry_run", func(c *Config) { c.DryRun = true }},
		{"pack_inline_limit_bytes", func(c *Config) { c.PackInlineLimitBytes = 1 }},
		{"staging_container", func(c *Config) { c.StagingContainer = "https://a.blob.core.windows.net/x" }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := baseHashConfig()
			tt.mutate(cfg)
			if hashOf(t, cfg, resolved, "") != base {
				t.Errorf("changing %s moved the config hash, want it ignored", tt.name)
			}
		})
	}
}
