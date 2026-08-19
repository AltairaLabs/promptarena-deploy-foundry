package foundry

import "testing"

func TestValidateBindings(t *testing.T) {
	tests := []struct {
		name       string
		bindings   []ProviderBinding
		wantErrSub string
	}{
		{
			name:       "at least one binding is required",
			bindings:   nil,
			wantErrSub: "providers is required",
		},
		{
			name:       "name is required",
			bindings:   []ProviderBinding{{ArenaProvider: "gpt4o"}},
			wantErrSub: "name is required",
		},
		{
			name: "duplicate names are rejected",
			bindings: []ProviderBinding{
				{Name: "default", ArenaProvider: "a"},
				{Name: "default", ArenaProvider: "b"},
			},
			wantErrSub: `"default" is duplicated`,
		},
		{
			name:       "unknown role is rejected",
			bindings:   []ProviderBinding{{Name: "default", Role: "oracle", ArenaProvider: "a"}},
			wantErrSub: `invalid role "oracle"`,
		},
		{
			name:       "a binding with no source is rejected",
			bindings:   []ProviderBinding{{Name: "default"}},
			wantErrSub: "exactly one of arena_provider or type+model",
		},
		{
			name: "a binding with both sources is rejected",
			bindings: []ProviderBinding{
				{Name: "default", ArenaProvider: "a", Type: "openai", Model: "gpt-4o"},
			},
			wantErrSub: "exactly one of arena_provider or type+model",
		},
		{
			name:       "inline binding without model is rejected",
			bindings:   []ProviderBinding{{Name: "default", Type: "azure"}},
			wantErrSub: "model is required",
		},
		{
			name:       "inline binding without type is rejected",
			bindings:   []ProviderBinding{{Name: "default", Model: "my-deployment"}},
			wantErrSub: "type is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			errs := validateBindings(tt.bindings)
			if !containsSubstring(errs, tt.wantErrSub) {
				t.Errorf("validateBindings() = %v, want an error containing %q", errs, tt.wantErrSub)
			}
		})
	}
}

func TestValidateBindingsAcceptsEveryRole(t *testing.T) {
	for role := range validRoles {
		bindings := []ProviderBinding{{Name: "b", Role: role, ArenaProvider: "a"}}
		if errs := validateBindings(bindings); len(errs) != 0 {
			t.Errorf("role %q: validateBindings() = %v, want no errors", role, errs)
		}
	}
}

func TestBindingWarningsNamesThePrimaryThatWillBeUsed(t *testing.T) {
	bindings := []ProviderBinding{
		{Name: "zulu", Role: RoleLLM, ArenaProvider: "a"},
		{Name: "alpha", Role: RoleLLM, ArenaProvider: "b"},
	}

	warnings := bindingWarnings(bindings)
	if !containsSubstring(warnings, `"alpha" will be used`) {
		t.Errorf("bindingWarnings() = %v, want it to name alpha as the primary", warnings)
	}
}

func TestBindingWarningsSilentWhenDefaultExists(t *testing.T) {
	bindings := []ProviderBinding{
		{Name: "default", Role: RoleLLM, ArenaProvider: "a"},
		{Name: "alpha", Role: RoleLLM, ArenaProvider: "b"},
	}

	if warnings := bindingWarnings(bindings); len(warnings) != 0 {
		t.Errorf("bindingWarnings() = %v, want none when a default binding exists", warnings)
	}
}

// A pack with only non-llm bindings has no primary to guess at, so there is
// nothing to warn about.
func TestBindingWarningsSilentWithoutLLMBindings(t *testing.T) {
	bindings := []ProviderBinding{{Name: "speech", Role: RoleTTS, ArenaProvider: "a"}}

	if warnings := bindingWarnings(bindings); len(warnings) != 0 {
		t.Errorf("bindingWarnings() = %v, want none", warnings)
	}
}
