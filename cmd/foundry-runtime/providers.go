package main

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/AltairaLabs/PromptKit/sdk"
)

// roleLLM is the binding role that supplies the conversation's language model.
const roleLLM = "llm"

// defaultBindingName is the logical binding name treated as primary.
const defaultBindingName = "default"

// providerBinding is one resolved provider binding as injected by the adapter
// through PROMPTPACK_PROVIDERS. Type and Model are always concrete here: the
// adapter resolves arena_provider references before injection.
//
// For Azure OpenAI, Model carries the *deployment* name rather than the model
// name — that is what the service addresses.
type providerBinding struct {
	Name  string `json:"name"`
	Role  string `json:"role"`
	Type  string `json:"type"`
	Model string `json:"model"`
}

// parseProviderBindings decodes the PROMPTPACK_PROVIDERS JSON list. An empty
// string yields no bindings, which callers treat as "use the pack's own config".
func parseProviderBindings(raw string) ([]providerBinding, error) {
	if raw == "" {
		return nil, nil
	}
	var bindings []providerBinding
	if err := json.Unmarshal([]byte(raw), &bindings); err != nil {
		return nil, fmt.Errorf("invalid %s JSON: %w", envProviders, err)
	}
	return bindings, nil
}

// primaryBinding returns the binding that supplies the conversation's LLM.
// The binding named "default" wins; otherwise the first llm-role binding is
// used. Returns false when no llm-role binding exists.
func primaryBinding(bindings []providerBinding) (providerBinding, bool) {
	var first providerBinding
	var found bool
	for _, b := range bindings {
		if b.Role != "" && b.Role != roleLLM {
			continue
		}
		if b.Name == defaultBindingName {
			return b, true
		}
		if !found {
			first, found = b, true
		}
	}
	return first, found
}

// buildSDKOptions creates PromptKit SDK options from the resolved bindings.
//
// Authentication uses the per-version managed identity Foundry assigns the
// container, so no secret is ever injected. Each agent version gets its own
// identity — confirmed on a live deploy, where the version reported an
// instance_identity distinct from the project's.
func buildSDKOptions(cfg *runtimeConfig) ([]sdk.Option, error) {
	bindings, err := parseProviderBindings(cfg.ProvidersJSON)
	if err != nil {
		return nil, err
	}

	primary, ok := primaryBinding(bindings)
	if !ok {
		return nil, nil
	}

	if cfg.AzureEndpoint == "" {
		return nil, fmt.Errorf(
			"%s is required when provider bindings are set", envAzureEndpoint)
	}

	var platformOpts []sdk.PlatformOption
	if cfg.ClientID != "" {
		platformOpts = append(platformOpts, sdk.WithAzureManagedIdentity(cfg.ClientID))
	}

	// Pass the full deployment URL, not the bare account endpoint.
	//
	// PromptKit's SDK copies the platform endpoint straight into
	// ProviderSpec.BaseURL, and its openai factory only derives the Azure
	// deployment path when BaseURL is empty — so a bare endpoint makes that
	// branch unreachable and produces {endpoint}/chat/completions with no
	// deployment segment and no api-version, which Azure answers 404. Supplying
	// the deployment URL here yields the correct final request, and the
	// api-version is still appended because the platform is azure.
	//
	// Remove this once the upstream bug is fixed; the helper is idempotent, so
	// it stays correct if the SDK starts building the path itself.
	endpoint := azureDeploymentEndpoint(cfg.AzureEndpoint, primary.Model)
	setBindingDescription(endpoint, primary.Type, primary.Model, cfg.ClientID)

	return []sdk.Option{
		sdk.WithAzure(endpoint, primary.Type, primary.Model, platformOpts...),
	}, nil
}

// azureDeploymentPath is the segment Azure OpenAI addresses a deployment
// through.
const azureDeploymentPath = "/openai/deployments/"

// azureDeploymentEndpoint builds the deployment URL for a model. It is
// idempotent: an endpoint that already names the deployment is returned
// unchanged.
func azureDeploymentEndpoint(endpoint, deployment string) string {
	trimmed := strings.TrimRight(endpoint, "/")
	if strings.Contains(trimmed, azureDeploymentPath) {
		return trimmed
	}
	return trimmed + azureDeploymentPath + deployment
}
