package main

import (
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/AltairaLabs/PromptKit/runtime/providers/base"
	"github.com/AltairaLabs/PromptKit/runtime/stt"
	"github.com/AltairaLabs/PromptKit/runtime/tts"
	"github.com/AltairaLabs/PromptKit/sdk"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/gorilla/websocket"
)

// Audio inference does not go through the project proxy.
//
// Measured against a live project: /audio/speech and /audio/transcriptions
// both answer 404 there, while returning 200 at the account endpoint. So voice
// addresses the account directly, which is the one place this adapter needs a
// role assignment — created for the agent by Apply when the pack declares
// stt or tts bindings.
const (
	// azureAudioScope is the Entra scope for account-level Cognitive Services.
	azureAudioScope = "https://cognitiveservices.azure.com/.default"
	// azureAudioAPIVersion is the api-version the audio routes require. Unlike
	// the project proxy, the account endpoint demands one.
	azureAudioAPIVersion = "2024-12-01-preview"
	// azureDeploymentPath is how Azure OpenAI addresses a deployment.
	azureDeploymentPath = "/openai/deployments/"
)

// entraTransport attaches an Entra bearer token and the api-version query
// parameter to every request.
//
// The STT and TTS services build their own URLs from a base, so this is the
// one interception point where Azure's extra requirements can be applied
// without forking them.
type entraTransport struct {
	base       http.RoundTripper
	cred       azcore.TokenCredential
	scope      string
	apiVersion string
}

// RoundTrip authenticates the request and ensures it carries an api-version.
func (t *entraTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// Clone: RoundTrip must not mutate the caller's request.
	out := req.Clone(req.Context())

	tok, err := t.cred.GetToken(req.Context(), policy.TokenRequestOptions{Scopes: []string{t.scope}})
	if err != nil {
		return nil, fmt.Errorf("acquire token for %s: %w", t.scope, err)
	}
	out.Header.Set("Authorization", "Bearer "+tok.Token)

	if t.apiVersion != "" && out.URL.Query().Get("api-version") == "" {
		q := out.URL.Query()
		q.Set("api-version", t.apiVersion)
		out.URL.RawQuery = q.Encode()
	}

	transport := t.base
	if transport == nil {
		transport = http.DefaultTransport
	}
	return transport.RoundTrip(out)
}

// managedIdentityKeyPlaceholder stands in for an API key the services require
// to be non-empty. It is never sent: entraTransport replaces the Authorization
// header on every request with a token from the agent's managed identity.
const managedIdentityKeyPlaceholder = "managed-identity"

// azureAudioClient returns an HTTP client that authenticates as the agent.
func azureAudioClient(cred azcore.TokenCredential) *http.Client {
	return &http.Client{
		Transport: &entraTransport{
			cred:       cred,
			scope:      azureAudioScope,
			apiVersion: azureAudioAPIVersion,
		},
	}
}

// azureAudioBaseURL builds the deployment base the audio routes hang off, so
// the services' own "/audio/speech" and "/audio/transcriptions" suffixes land
// on a real Azure path.
func azureAudioBaseURL(accountEndpoint, deployment string) string {
	return strings.TrimRight(accountEndpoint, "/") + azureDeploymentPath + deployment
}

// audioServices builds the STT and TTS services for the pack's voice bindings.
// Either may be nil when the pack declares no binding for that role; the
// pipeline only needs both for a full cascaded voice turn.
//
// No API key is passed. Authentication is the agent's own managed identity, so
// no secret reaches the container — the same property the text path has.
func audioServices(
	cfg *runtimeConfig, bindings []providerBinding, cred azcore.TokenCredential,
) (sttService stt.Service, ttsService tts.Service, err error) {
	if cfg.AzureEndpoint == "" {
		return nil, nil, fmt.Errorf(
			"%s is required for voice: audio is not served by the project proxy, "+
				"so stt and tts address the account endpoint directly", envAzureEndpoint)
	}
	client := azureAudioClient(cred)

	if b, ok := bindingForRole(bindings, roleSTT); ok {
		sttService = stt.NewOpenAI(managedIdentityKeyPlaceholder,
			base.WithBaseURL(azureAudioBaseURL(cfg.AzureEndpoint, b.Model)),
			base.WithClient(client),
			base.WithModel(b.Model),
		)
	}
	if b, ok := bindingForRole(bindings, roleTTS); ok {
		ttsService = tts.NewOpenAI(managedIdentityKeyPlaceholder,
			base.WithBaseURL(azureAudioBaseURL(cfg.AzureEndpoint, b.Model)),
			base.WithClient(client),
			base.WithModel(b.Model),
		)
	}

	return sttService, ttsService, nil
}

// bindingForRole returns the first binding declared for a role.
func bindingForRole(bindings []providerBinding, role string) (providerBinding, bool) {
	for _, b := range bindings {
		if b.Role == role {
			return b, true
		}
	}
	return providerBinding{}, false
}

// voiceEnabled reports whether the pack has what a cascaded voice turn needs.
// Speech in and speech out are both required: half of the pair would leave a
// caller talking to something that cannot answer, or vice versa.
func voiceEnabled(bindings []providerBinding) bool {
	_, hasSTT := bindingForRole(bindings, roleSTT)
	_, hasTTS := bindingForRole(bindings, roleTTS)
	return hasSTT && hasTTS
}

// buildVoiceHandler wires the WebSocket voice route when the pack declares
// both speech bindings. It returns nil when the pack has no voice, so the
// route is simply absent rather than present and useless.
//
// The LLM options are reused unchanged: with VAD mode the pipeline owns the
// input side and the model only ever sees text, so a pack behaves identically
// over voice and over text.
func buildVoiceHandler(
	cfg *runtimeConfig, packFile, agentName string, llmOpts []sdk.Option, log *slog.Logger,
) (http.Handler, error) {
	bindings, err := parseProviderBindings(cfg.ProvidersJSON)
	if err != nil {
		return nil, err
	}
	if !voiceEnabled(bindings) {
		return nil, nil
	}

	cred, err := azidentity.NewDefaultAzureCredential(nil)
	if err != nil {
		return nil, fmt.Errorf("resolve Azure credential: %w", err)
	}

	sttService, ttsService, err := audioServices(cfg, bindings, cred)
	if err != nil {
		return nil, err
	}

	opts := make([]sdk.Option, 0, len(llmOpts)+1)
	opts = append(opts, llmOpts...)
	opts = append(opts, sdk.WithVADMode(sttService, ttsService, nil))

	upgrader := &websocket.Upgrader{
		// The platform has already authenticated and relays frames verbatim, so
		// an Origin check here would only reject its own proxy.
		CheckOrigin: func(*http.Request) bool { return true },
	}

	return newVoiceHandler(voiceDeps{
		PackFile:  packFile,
		AgentName: agentName,
		Opts:      opts,
		Log:       log,
	}, upgrader), nil
}
