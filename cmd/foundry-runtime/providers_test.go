package main

import "testing"

func TestAzureDeploymentEndpointIncludesTheDeploymentPath(t *testing.T) {
	got := azureDeploymentEndpoint("https://acct.openai.azure.com/", "gpt-4-1-mini")

	want := "https://acct.openai.azure.com/openai/deployments/gpt-4-1-mini"
	if got != want {
		t.Errorf("azureDeploymentEndpoint = %q, want %q", got, want)
	}
}

func TestAzureDeploymentEndpointWithoutTrailingSlash(t *testing.T) {
	got := azureDeploymentEndpoint("https://acct.openai.azure.com", "d")

	want := "https://acct.openai.azure.com/openai/deployments/d"
	if got != want {
		t.Errorf("azureDeploymentEndpoint = %q, want %q", got, want)
	}
}

// An endpoint that already names the deployment must not gain a second copy.
func TestAzureDeploymentEndpointIsIdempotent(t *testing.T) {
	base := "https://acct.openai.azure.com/openai/deployments/d"

	if got := azureDeploymentEndpoint(base, "d"); got != base {
		t.Errorf("azureDeploymentEndpoint = %q, want %q", got, base)
	}
}

func TestBuildSDKOptionsRequiresAnEndpoint(t *testing.T) {
	cfg := &runtimeConfig{
		ProvidersJSON: `[{"name":"default","role":"llm","type":"openai","model":"m"}]`,
	}

	if _, err := buildSDKOptions(cfg); err == nil {
		t.Fatal("buildSDKOptions accepted bindings with no Azure endpoint")
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
