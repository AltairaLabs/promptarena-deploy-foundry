package foundry

import "fmt"

// ResolvedBinding is a binding whose type and model are concrete. Its JSON tags
// match the providerBinding type foundry-runtime decodes from
// PROMPTPACK_PROVIDERS, so the resolved list is injected verbatim.
type ResolvedBinding struct {
	Name  string `json:"name"`
	Role  string `json:"role"`
	Type  string `json:"type"`
	Model string `json:"model"`
}

// resolveBindings turns bindings into concrete type/model pairs, reading from
// the arena config where a binding references one.
func resolveBindings(
	bindings []ProviderBinding, arena *ArenaConfig,
) (resolvedBindings []ResolvedBinding, errors []string) {
	resolved := make([]ResolvedBinding, 0, len(bindings))
	var errs []string

	for i := range bindings {
		b := &bindings[i]

		role := b.Role
		if role == "" {
			role = RoleLLM
		}

		out := ResolvedBinding{
			Name:  b.Name,
			Role:  role,
			Type:  b.Type,
			Model: b.Model,
		}

		if b.usesArena() {
			p := arena.provider(b.ArenaProvider)
			if p == nil {
				errs = append(errs, fmt.Sprintf(
					"provider binding %q: arena provider %q not found in the arena config",
					b.Name, b.ArenaProvider))
				continue
			}
			out.Type = p.Type
			out.Model = p.Model
		}

		resolved = append(resolved, out)
	}

	return resolved, errs
}

// primaryBinding returns the binding supplying the conversation's LLM: the one
// named "default", else the first llm-role binding. Reports false when none is
// an llm-role binding.
func primaryBinding(resolved []ResolvedBinding) (ResolvedBinding, bool) {
	var first ResolvedBinding
	var found bool

	for i := range resolved {
		b := &resolved[i]
		if b.Role != RoleLLM {
			continue
		}
		if b.Name == DefaultBindingName {
			return *b, true
		}
		if !found {
			first, found = *b, true
		}
	}

	return first, found
}
