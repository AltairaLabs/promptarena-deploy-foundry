package foundry

import "testing"

func TestResolveBindingsInline(t *testing.T) {
	bindings := []ProviderBinding{{Name: "default", Role: RoleLLM, Type: "azure", Model: "gpt4o-deploy"}}

	resolved, errs := resolveBindings(bindings, nil)
	if len(errs) != 0 {
		t.Fatalf("resolveBindings errors = %v, want none", errs)
	}
	if len(resolved) != 1 {
		t.Fatalf("len(resolved) = %d, want 1", len(resolved))
	}
	if resolved[0].Type != "azure" || resolved[0].Model != "gpt4o-deploy" {
		t.Errorf("resolved = %+v, want the inline type and model", resolved[0])
	}
}

func TestResolveBindingsFromArena(t *testing.T) {
	bindings := []ProviderBinding{{Name: "default", ArenaProvider: "gpt4o"}}
	arena := &ArenaConfig{
		ProviderSpecs: map[string]*ArenaProvider{"gpt4o": {Type: "azure", Model: "gpt4o-deploy"}},
	}

	resolved, errs := resolveBindings(bindings, arena)
	if len(errs) != 0 {
		t.Fatalf("resolveBindings errors = %v, want none", errs)
	}
	if resolved[0].Type != "azure" || resolved[0].Model != "gpt4o-deploy" {
		t.Errorf("resolved = %+v, want the arena provider's type and model", resolved[0])
	}
}

// An unresolvable reference must be reported, not silently deployed with an
// empty model.
func TestResolveBindingsMissingArenaProvider(t *testing.T) {
	bindings := []ProviderBinding{{Name: "default", ArenaProvider: "nope"}}

	resolved, errs := resolveBindings(bindings, &ArenaConfig{})
	if !containsSubstring(errs, `arena provider "nope" not found`) {
		t.Errorf("errors = %v, want a not-found error", errs)
	}
	if len(resolved) != 0 {
		t.Errorf("resolved = %v, want the unresolvable binding dropped", resolved)
	}
}

// An omitted role means llm — the common case is a single conversational
// provider, and requiring the role there would be noise.
func TestResolveBindingsDefaultsRoleToLLM(t *testing.T) {
	bindings := []ProviderBinding{{Name: "default", Type: "azure", Model: "m"}}

	resolved, _ := resolveBindings(bindings, nil)
	if resolved[0].Role != RoleLLM {
		t.Errorf("Role = %q, want %q", resolved[0].Role, RoleLLM)
	}
}

func TestPrimaryBindingPrefersDefaultName(t *testing.T) {
	resolved := []ResolvedBinding{
		{Name: "alpha", Role: RoleLLM},
		{Name: DefaultBindingName, Role: RoleLLM},
	}

	got, ok := primaryBinding(resolved)
	if !ok || got.Name != DefaultBindingName {
		t.Errorf("primaryBinding() = %+v, %v; want the default binding", got, ok)
	}
}

func TestPrimaryBindingFallsBackToFirstLLM(t *testing.T) {
	resolved := []ResolvedBinding{
		{Name: "speech", Role: RoleTTS},
		{Name: "alpha", Role: RoleLLM},
		{Name: "beta", Role: RoleLLM},
	}

	got, ok := primaryBinding(resolved)
	if !ok || got.Name != "alpha" {
		t.Errorf("primaryBinding() = %+v, %v; want alpha", got, ok)
	}
}

// A pack with no llm binding has nothing to converse with; Plan turns this
// into a hard error rather than deploying a mute agent.
func TestPrimaryBindingReportsMissing(t *testing.T) {
	resolved := []ResolvedBinding{{Name: "speech", Role: RoleTTS}}

	if _, ok := primaryBinding(resolved); ok {
		t.Error("primaryBinding() reported a primary, want none")
	}
}
