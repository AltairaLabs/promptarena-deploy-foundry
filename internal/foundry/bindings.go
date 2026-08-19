package foundry

import (
	"fmt"
	"sort"
)

// Binding role values, mirroring the roles the omnia and vertex adapters accept.
//
// Voice is not a separate config concern: STT and TTS are bindings like any
// other, so a pack's voice path is configured the same way its text path is.
const (
	RoleLLM       = "llm"
	RoleEmbedding = "embedding"
	RoleTTS       = "tts"
	RoleSTT       = "stt"
	RoleImage     = "image"
	RoleInference = "inference"
)

// DefaultBindingName is the logical binding name treated as primary.
const DefaultBindingName = "default"

// validRoles is the set of accepted binding roles.
var validRoles = map[string]bool{
	RoleLLM:       true,
	RoleEmbedding: true,
	RoleTTS:       true,
	RoleSTT:       true,
	RoleImage:     true,
	RoleInference: true,
}

// ProviderBinding maps a logical provider name to a concrete provider, either
// by referencing an arena provider or by declaring type and model inline.
//
// Resolving an ArenaProvider reference into a concrete type and model happens
// during Plan/Apply, which is where the arena config is available.
//
// For Azure OpenAI, Model carries the Azure *deployment* name rather than the
// model name — that is what the service addresses and what the Foundry sample
// passes as MODEL_DEPLOYMENT_NAME.
type ProviderBinding struct {
	Name          string `json:"name"`
	Role          string `json:"role,omitempty"`
	ArenaProvider string `json:"arena_provider,omitempty"`
	Type          string `json:"type,omitempty"`
	Model         string `json:"model,omitempty"`
}

// usesArena reports whether the binding inherits from an arena provider.
func (b *ProviderBinding) usesArena() bool {
	return b.ArenaProvider != ""
}

// usesInline reports whether the binding declares its provider inline.
func (b *ProviderBinding) usesInline() bool {
	return b.Type != "" || b.Model != ""
}

// validateBindings checks the binding list structurally. Resolution against the
// arena config happens later, during Plan.
func validateBindings(bindings []ProviderBinding) []string {
	if len(bindings) == 0 {
		return []string{"providers is required (at least one provider binding)"}
	}

	var errs []string
	seen := make(map[string]bool, len(bindings))

	for i := range bindings {
		b := &bindings[i]

		if b.Name == "" {
			errs = append(errs, "provider binding: name is required")
		}
		if seen[b.Name] {
			errs = append(errs, fmt.Sprintf("provider binding name %q is duplicated", b.Name))
		}
		seen[b.Name] = true

		if b.Role != "" && !validRoles[b.Role] {
			errs = append(errs, fmt.Sprintf(
				"provider binding %q: invalid role %q", b.Name, b.Role))
		}

		errs = append(errs, validateBindingSource(b)...)
	}

	return errs
}

// validateBindingSource checks that a binding names exactly one provider source.
func validateBindingSource(b *ProviderBinding) []string {
	if b.usesArena() == b.usesInline() {
		return []string{fmt.Sprintf(
			"provider binding %q: set exactly one of arena_provider or type+model", b.Name)}
	}
	if b.usesInline() {
		var errs []string
		if b.Type == "" {
			errs = append(errs, fmt.Sprintf("provider binding %q: type is required", b.Name))
		}
		if b.Model == "" {
			errs = append(errs, fmt.Sprintf("provider binding %q: model is required", b.Name))
		}
		return errs
	}
	return nil
}

// bindingWarnings returns non-blocking advisories. When no binding is named
// "default", the primary is the lexicographically first llm-role binding, which
// is rarely deliberate — so name the exact binding that will be used.
func bindingWarnings(bindings []ProviderBinding) []string {
	if len(bindings) == 0 {
		return nil
	}

	names := make([]string, 0, len(bindings))
	for i := range bindings {
		b := &bindings[i]
		if b.Name == DefaultBindingName {
			return nil
		}
		if b.Role == "" || b.Role == RoleLLM {
			names = append(names, b.Name)
		}
	}
	if len(names) == 0 {
		return nil
	}

	sort.Strings(names)
	return []string{fmt.Sprintf(
		"no provider binding is named %q; %q will be used as the primary provider",
		DefaultBindingName, names[0])}
}
