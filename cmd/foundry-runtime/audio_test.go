package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
)

type stubCred struct{ err error }

func (s stubCred) GetToken(
	context.Context, policy.TokenRequestOptions,
) (azcore.AccessToken, error) {
	if s.err != nil {
		return azcore.AccessToken{}, s.err
	}
	return azcore.AccessToken{Token: "tok", ExpiresOn: time.Now().Add(time.Hour)}, nil
}

// The audio routes hang off a deployment path, and the account endpoint — unlike
// the project proxy — requires an api-version.
func TestAzureAudioBaseURL(t *testing.T) {
	got := azureAudioBaseURL("https://acct.openai.azure.com/", "gpt-4o-mini-tts")

	want := "https://acct.openai.azure.com/openai/deployments/gpt-4o-mini-tts"
	if got != want {
		t.Errorf("azureAudioBaseURL = %q, want %q", got, want)
	}
}

func TestEntraTransportAddsTokenAndAPIVersion(t *testing.T) {
	var gotAuth, gotVersion string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotVersion = r.URL.Query().Get("api-version")
	}))
	defer srv.Close()

	client := &http.Client{Transport: &entraTransport{
		cred: stubCred{}, scope: azureAudioScope, apiVersion: azureAudioAPIVersion,
	}}
	resp, err := client.Post(srv.URL+"/audio/speech", "application/json", http.NoBody)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()

	if gotAuth != "Bearer tok" {
		t.Errorf("Authorization = %q, want the bearer token", gotAuth)
	}
	if gotVersion != azureAudioAPIVersion {
		t.Errorf("api-version = %q, want %q", gotVersion, azureAudioAPIVersion)
	}
}

// A caller that already chose an api-version must keep it.
func TestEntraTransportPreservesAnExplicitAPIVersion(t *testing.T) {
	var gotVersion string
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		gotVersion = r.URL.Query().Get("api-version")
	}))
	defer srv.Close()

	client := &http.Client{Transport: &entraTransport{
		cred: stubCred{}, scope: azureAudioScope, apiVersion: azureAudioAPIVersion,
	}}
	resp, err := client.Post(srv.URL+"/x?api-version=chosen", "application/json", http.NoBody)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()

	if gotVersion != "chosen" {
		t.Errorf("api-version = %q, want the caller's value", gotVersion)
	}
}

// Half a pair would leave a caller talking to something that cannot answer.
func TestVoiceEnabledRequiresBothSpeechBindings(t *testing.T) {
	tests := []struct {
		name     string
		bindings []providerBinding
		want     bool
	}{
		{"both", []providerBinding{{Role: roleSTT}, {Role: roleTTS}}, true},
		{"stt only", []providerBinding{{Role: roleSTT}}, false},
		{"tts only", []providerBinding{{Role: roleTTS}}, false},
		{"text only", []providerBinding{{Role: roleLLM}}, false},
		{"none", nil, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := voiceEnabled(tt.bindings); got != tt.want {
				t.Errorf("voiceEnabled = %v, want %v", got, tt.want)
			}
		})
	}
}

// A text-only pack must not get a voice route it cannot serve.
func TestBuildVoiceHandlerNilWithoutSpeechBindings(t *testing.T) {
	cfg := &runtimeConfig{
		ProvidersJSON: `[{"name":"default","role":"llm","type":"openai","model":"m"}]`,
	}

	got, err := buildVoiceHandler(cfg, "pack.json", "main", nil, discardLogger())
	if err != nil {
		t.Fatalf("buildVoiceHandler: %v", err)
	}
	if got != nil {
		t.Error("a text-only pack got a voice handler")
	}
}

func TestBuildVoiceHandlerRequiresTheAccountEndpoint(t *testing.T) {
	cfg := &runtimeConfig{
		ProvidersJSON: `[{"role":"stt","model":"t"},{"role":"tts","model":"s"}]`,
	}

	if _, err := buildVoiceHandler(cfg, "pack.json", "main", nil, discardLogger()); err == nil {
		t.Fatal("buildVoiceHandler accepted voice bindings with no account endpoint")
	}
}

func TestAzureAudioClientUsesTheEntraTransport(t *testing.T) {
	client := azureAudioClient(stubCred{})

	transport, ok := client.Transport.(*entraTransport)
	if !ok {
		t.Fatalf("Transport = %T, want *entraTransport", client.Transport)
	}
	if transport.scope != azureAudioScope {
		t.Errorf("scope = %q, want %q", transport.scope, azureAudioScope)
	}
}

// Voice needs both services; a pack with both bindings must get both.
func TestAudioServicesBuildsBoth(t *testing.T) {
	cfg := &runtimeConfig{AzureEndpoint: "https://acct.openai.azure.com/"}
	bindings := []providerBinding{
		{Role: roleSTT, Model: "whisper"},
		{Role: roleTTS, Model: "gpt-4o-mini-tts"},
	}

	sttService, ttsService, err := audioServices(cfg, bindings, stubCred{})
	if err != nil {
		t.Fatalf("audioServices: %v", err)
	}
	if sttService == nil || ttsService == nil {
		t.Errorf("stt=%v tts=%v, want both built", sttService != nil, ttsService != nil)
	}
}
