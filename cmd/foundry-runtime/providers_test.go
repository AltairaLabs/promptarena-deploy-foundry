package main

import (
	"context"
	"errors"
	"net/http"
	"testing"
)

// An endpoint that already names the deployment must not gain a second copy.
// Inference goes through the project proxy, so without the project endpoint
// there is nowhere to send a turn.
func TestBuildSDKOptionsRequiresTheProjectEndpoint(t *testing.T) {
	cfg := &runtimeConfig{
		ProvidersJSON: `[{"name":"default","role":"llm","type":"openai","model":"m"}]`,
	}

	if _, err := buildSDKOptions(cfg); err == nil {
		t.Fatal("buildSDKOptions accepted bindings with no project endpoint")
	}
}

// The project's inference surface takes no api-version; the platform rejects
// one with "api-version=v1 is not allowed, use /v1 path instead".
func TestProjectInferenceEndpoint(t *testing.T) {
	base := "https://acct.services.ai.azure.com/api/projects/proj"
	want := base + "/openai/v1"

	for _, in := range []string{base, base + "/"} {
		if got := projectInferenceEndpoint(in); got != want {
			t.Errorf("projectInferenceEndpoint(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestBuildSDKOptionsNoBindings(t *testing.T) {
	opts, err := buildSDKOptions(&runtimeConfig{})
	if err != nil {
		t.Fatalf("buildSDKOptions: %v", err)
	}
	if opts != nil {
		t.Errorf("opts = %v, want nil so the pack's own config applies", opts)
	}
}

func TestPrimaryBindingPrefersDefault(t *testing.T) {
	bindings := []providerBinding{
		{Name: "alpha", Role: roleLLM},
		{Name: defaultBindingName, Role: roleLLM},
	}

	got, ok := primaryBinding(bindings)
	if !ok || got.Name != defaultBindingName {
		t.Errorf("primaryBinding = %+v, %v; want the default binding", got, ok)
	}
}

// The provider credential must attach the agent's token to every outbound
// request; without it PromptKit would call the proxy unauthenticated.
func TestEntraCredentialApply(t *testing.T) {
	cred := &entraCredential{cred: stubCred{}, scope: foundryScope}

	req, err := http.NewRequest(http.MethodPost, "https://example.test", http.NoBody)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if err := cred.Apply(context.Background(), req); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	if got := req.Header.Get("Authorization"); got != "Bearer tok" {
		t.Errorf("Authorization = %q, want the bearer token", got)
	}
}

// A credential that cannot get a token must fail loudly rather than send an
// unauthenticated request that returns an opaque 404.
func TestEntraCredentialApplyReportsTokenFailure(t *testing.T) {
	cred := &entraCredential{cred: stubCred{err: errors.New("no identity")}, scope: foundryScope}

	req, err := http.NewRequest(http.MethodPost, "https://example.test", http.NoBody)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if err := cred.Apply(context.Background(), req); err == nil {
		t.Fatal("Apply succeeded with no token available")
	}
}

func TestEntraCredentialType(t *testing.T) {
	cred := &entraCredential{}
	if cred.Type() == "" {
		t.Error("Type() is empty; it identifies the credential in diagnostics")
	}
}
