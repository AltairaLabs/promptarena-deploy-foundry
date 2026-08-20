package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/AltairaLabs/PromptKit/runtime/providers"
	"github.com/AltairaLabs/PromptKit/sdk"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
)

// Binding roles the runtime acts on. Voice is not a separate config concern:
// speech in and speech out are bindings like any other.
const (
	roleLLM = "llm"
	roleSTT = "stt"
	roleTTS = "tts"
)

// defaultBindingName is the logical binding name treated as primary.
const defaultBindingName = "default"

// foundryScope is the Entra scope for the Foundry data plane, which fronts the
// project's inference proxy.
const foundryScope = "https://ai.azure.com/.default"

// projectInferencePath is the OpenAI-compatible inference surface exposed on a
// Foundry project endpoint. No api-version belongs on it — the platform
// answers one that carries it with "api-version=v1 is not allowed, use /v1
// path instead".
const projectInferencePath = "/openai/v1"

// Model defaults for a turn.
const (
	defaultTemperature = 0.7
	defaultTopP        = 1.0
	defaultMaxTokens   = 4096
)

// providerBinding is one resolved provider binding as injected by the adapter
// through PROMPTPACK_PROVIDERS. Type and Model are always concrete here: the
// adapter resolves arena_provider references before injection.
//
// For Azure, Model carries the *deployment* name rather than the model name —
// that is what the service addresses.
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

// entraCredential applies an Entra bearer token to outbound provider requests.
//
// The token comes from the agent's own managed identity, which Foundry injects
// into the sandbox. No secret is ever supplied to the container.
type entraCredential struct {
	cred  azcore.TokenCredential
	scope string
}

// Apply attaches the bearer token.
func (c *entraCredential) Apply(ctx context.Context, req *http.Request) error {
	tok, err := c.cred.GetToken(ctx, policy.TokenRequestOptions{Scopes: []string{c.scope}})
	if err != nil {
		return fmt.Errorf("acquire token for %s: %w", c.scope, err)
	}
	req.Header.Set("Authorization", "Bearer "+tok.Token)
	return nil
}

// Type identifies the credential in diagnostics.
func (c *entraCredential) Type() string { return "entra-managed-identity" }

// projectInferenceEndpoint builds the project's inference base URL.
func projectInferenceEndpoint(projectEndpoint string) string {
	return strings.TrimRight(projectEndpoint, "/") + projectInferencePath
}

// buildSDKOptions creates PromptKit SDK options from the resolved bindings.
//
// Inference goes through the *project* endpoint, not the account's Azure
// OpenAI endpoint, and that choice is what makes a deploy need no manual RBAC
// step. Foundry grants an agent implicit access to model inferencing through
// its own project; an agent that calls the account endpoint directly bypasses
// that proxy and must be granted Cognitive Services OpenAI User by hand, once
// per agent. Measured both ways against a live sandbox: the project route
// answers 200 for an agent with no role assignment at all.
//
// The provider is built directly rather than through sdk.WithAzure because the
// azure platform forces the legacy Chat Completions path, while the project
// proxy exposes the Responses API. Going through the platform helper would
// therefore aim at a URL the proxy does not serve.
func buildSDKOptions(cfg *runtimeConfig) ([]sdk.Option, error) {
	bindings, err := parseProviderBindings(cfg.ProvidersJSON)
	if err != nil {
		return nil, err
	}

	primary, ok := primaryBinding(bindings)
	if !ok {
		return nil, nil
	}

	if cfg.ProjectEndpoint == "" {
		return nil, fmt.Errorf(
			"%s is required to reach the project's inference proxy; it is injected "+
				"automatically when running as a hosted agent", envFoundryProjectEP)
	}

	cred, err := azidentity.NewDefaultAzureCredential(nil)
	if err != nil {
		return nil, fmt.Errorf("resolve Azure credential: %w", err)
	}

	endpoint := projectInferenceEndpoint(cfg.ProjectEndpoint)
	setBindingDescription(endpoint, primary.Type, primary.Model, cfg.ClientID)

	provider, err := providers.CreateProviderFromSpec(providers.ProviderSpec{
		ID:         primary.Name,
		Type:       primary.Type,
		Model:      primary.Model,
		BaseURL:    endpoint,
		Credential: &entraCredential{cred: cred, scope: foundryScope},
		// The proxy serves the Responses API. Left to itself the provider would
		// pick Chat Completions for most models.
		AdditionalConfig: map[string]any{"api_mode": "responses"},
		Defaults: providers.ProviderDefaults{
			Temperature: defaultTemperature,
			TopP:        defaultTopP,
			MaxTokens:   defaultMaxTokens,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("build %s provider: %w", primary.Type, err)
	}

	return []sdk.Option{sdk.WithProvider(provider)}, nil
}
