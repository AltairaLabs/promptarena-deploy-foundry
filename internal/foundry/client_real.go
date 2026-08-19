package foundry

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/runtime"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
)

// Data-plane contract constants.
const (
	// apiVersion is the data-plane API version this adapter speaks.
	apiVersion = "v1"
	// authScope is the Entra scope for the Foundry data plane.
	authScope = "https://ai.azure.com/.default"
	// protocolVersion is the contract version declared for every protocol.
	protocolVersion = "1.0.0"
	// mergePatchContentType is required for the served-version PATCH.
	mergePatchContentType = "application/merge-patch+json"
	// fixedRatioRuleKind is the only version selection rule kind Foundry
	// supports; traffic splitting is not available.
	fixedRatioRuleKind = "FixedRatio"
	// fullTrafficPercentage is the share the served version always takes.
	fullTrafficPercentage = 100
	// moduleName identifies this adapter in the telemetry User-Agent.
	moduleName = "promptarena-deploy-foundry"
)

// Polling defaults for a version's provisioning wait.
const (
	defaultPollDelay   = 2 * time.Second
	defaultPollTimeout = 10 * time.Minute
)

// restClient talks to the Foundry data plane over hand-rolled REST.
//
// No Go SDK exists for this data plane, and that is an advantage: anything the
// API accepts can be sent, with no gap between the REST surface and a set of
// published protos.
type restClient struct {
	baseURL  string
	pipeline runtime.Pipeline

	pollDelay   time.Duration
	pollTimeout time.Duration
}

// newClient builds a client for cfg using the ambient Azure credential.
func newClient(_ context.Context, cfg *Config) (*restClient, error) {
	cred, err := azidentity.NewDefaultAzureCredential(nil)
	if err != nil {
		return nil, fmt.Errorf("resolve Azure credential: %w", err)
	}
	return newRESTClient(projectEndpoint(cfg), cred, nil)
}

// projectEndpoint builds the data-plane base URL for a config.
func projectEndpoint(cfg *Config) string {
	return fmt.Sprintf("https://%s.services.ai.azure.com/api/projects/%s", cfg.Account, cfg.Project)
}

// newRESTClient builds a client against an explicit base URL. transport may be
// nil to use the default; tests pass an httptest client.
func newRESTClient(
	baseURL string, cred azcore.TokenCredential, transport policy.Transporter,
) (*restClient, error) {
	opts := &policy.ClientOptions{}
	if transport != nil {
		opts.Transport = transport
	}

	authPolicy := runtime.NewBearerTokenPolicy(cred, []string{authScope}, nil)
	pipeline := runtime.NewPipeline(
		moduleName, Version,
		runtime.PipelineOptions{PerRetry: []policy.Policy{authPolicy}},
		opts,
	)

	return &restClient{
		baseURL:     baseURL,
		pipeline:    pipeline,
		pollDelay:   defaultPollDelay,
		pollTimeout: defaultPollTimeout,
	}, nil
}

// Wire types for the data-plane REST bodies. They are kept separate from the
// adapter's domain types so a change in the API shape does not ripple through
// plan, apply and state.
type (
	agentWire struct {
		Name          string             `json:"name"`
		AgentEndpoint *agentEndpointWire `json:"agent_endpoint,omitempty"`
		Tags          map[string]string  `json:"tags,omitempty"`
	}

	agentEndpointWire struct {
		VersionSelector *versionSelectorWire `json:"version_selector,omitempty"`
	}

	versionSelectorWire struct {
		Rules []versionRuleWire `json:"version_selection_rules,omitempty"`
	}

	versionRuleWire struct {
		Kind              string `json:"kind,omitempty"`
		Version           string `json:"version"`
		TrafficPercentage int    `json:"traffic_percentage"`
	}

	createAgentWire struct {
		Name       string            `json:"name"`
		Definition definitionWire    `json:"definition"`
		Tags       map[string]string `json:"tags,omitempty"`
	}

	definitionWire struct {
		Kind                   string            `json:"kind"`
		ContainerConfiguration containerWire     `json:"container_configuration"`
		CPU                    string            `json:"cpu,omitempty"`
		Memory                 string            `json:"memory,omitempty"`
		ProtocolVersions       []protocolWire    `json:"protocol_versions,omitempty"`
		IdleTimeoutMinutes     int               `json:"idle_timeout_minutes,omitempty"`
		EnvironmentVariables   map[string]string `json:"environment_variables,omitempty"`
	}

	containerWire struct {
		Image string `json:"image"`
	}

	protocolWire struct {
		Protocol string `json:"protocol"`
		Version  string `json:"version"`
	}

	versionWire struct {
		Version string     `json:"version"`
		Status  string     `json:"status"`
		Error   *errorWire `json:"error,omitempty"`
	}

	errorWire struct {
		Message string `json:"message"`
	}

	listAgentsWire struct {
		Value []agentWire `json:"value"`
	}
)

// buildCreateBody turns a spec into the create request body.
func buildCreateBody(spec *AgentSpec) createAgentWire {
	protocols := make([]protocolWire, 0, len(spec.Protocols))
	for _, p := range spec.Protocols {
		protocols = append(protocols, protocolWire{Protocol: p, Version: protocolVersion})
	}

	return createAgentWire{
		Name: spec.Name,
		Tags: spec.Tags,
		Definition: definitionWire{
			Kind:                   hostedAgentKind,
			ContainerConfiguration: containerWire{Image: spec.Image},
			CPU:                    spec.CPU,
			Memory:                 spec.Memory,
			ProtocolVersions:       protocols,
			IdleTimeoutMinutes:     spec.IdleTimeoutMinutes,
			// encoding/json sorts map keys, so the body is byte-identical for
			// identical input.
			EnvironmentVariables: spec.Env,
		},
	}
}

// url builds an absolute data-plane URL carrying the api-version parameter.
func (c *restClient) url(path string) string {
	return fmt.Sprintf("%s%s?api-version=%s", c.baseURL, path, apiVersion)
}

// do issues a request and returns the response, leaving status handling to the
// caller so each operation can map its own not-found semantics.
func (c *restClient) do(
	ctx context.Context, method, path string, body any,
) (*http.Response, error) {
	req, err := runtime.NewRequest(ctx, method, c.url(path))
	if err != nil {
		return nil, fmt.Errorf("build %s %s: %w", method, path, err)
	}
	if body != nil {
		if marshalErr := runtime.MarshalAsJSON(req, body); marshalErr != nil {
			return nil, fmt.Errorf("encode %s %s body: %w", method, path, marshalErr)
		}
	}

	resp, err := c.pipeline.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s %s: %w", method, path, err)
	}
	defer closeBody(resp)
	return resp, nil
}

// CreateAgent creates the agent shell.
func (c *restClient) CreateAgent(ctx context.Context, spec *AgentSpec) (*Agent, error) {
	resp, err := c.do(ctx, http.MethodPost, "/agents", buildCreateBody(spec))
	if err != nil {
		return nil, err
	}
	defer closeBody(resp)
	if !runtime.HasStatusCode(resp, http.StatusOK, http.StatusCreated) {
		return nil, runtime.NewResponseError(resp)
	}

	var wire agentWire
	if err := runtime.UnmarshalAsJSON(resp, &wire); err != nil {
		return nil, fmt.Errorf("decode created agent: %w", err)
	}
	return toAgent(&wire), nil
}

// CreateVersion creates an immutable version and waits for it to settle.
//
// The create call returns before the version is serving, so this polls until
// the status leaves creating. Returning early would report a version that
// cannot yet take traffic.
func (c *restClient) CreateVersion(
	ctx context.Context, name string, spec *AgentSpec,
) (*AgentVersion, error) {
	resp, err := c.do(ctx, http.MethodPost, "/agents/"+name+"/versions", buildCreateBody(spec))
	if err != nil {
		return nil, err
	}
	defer closeBody(resp)
	if !runtime.HasStatusCode(resp, http.StatusOK, http.StatusCreated, http.StatusAccepted) {
		return nil, runtime.NewResponseError(resp)
	}

	var wire versionWire
	if err := runtime.UnmarshalAsJSON(resp, &wire); err != nil {
		return nil, fmt.Errorf("decode created version: %w", err)
	}

	return c.awaitVersion(ctx, name, toVersion(&wire))
}

// awaitVersion polls until the version is active or failed.
func (c *restClient) awaitVersion(
	ctx context.Context, name string, version *AgentVersion,
) (*AgentVersion, error) {
	deadline := time.Now().Add(c.pollTimeout)

	for version.Status == VersionStatusCreating {
		if time.Now().After(deadline) {
			// Not an error the caller can fix by retrying immediately, but the
			// version is real and recorded, so apply keeps it as in-flight.
			return version, fmt.Errorf(
				"agent %q version %q still creating after %s", name, version.Version, c.pollTimeout)
		}

		select {
		case <-ctx.Done():
			return version, ctx.Err()
		case <-time.After(c.pollDelay):
		}

		next, err := c.GetVersion(ctx, name, version.Version)
		if err != nil {
			return version, err
		}
		version = next
	}

	if version.Status == VersionStatusFailed {
		return version, fmt.Errorf(
			"agent %q version %q: %w: %s",
			name, version.Version, ErrVersionFailed, version.FailureReason)
	}
	return version, nil
}

// GetAgent fetches one agent.
func (c *restClient) GetAgent(ctx context.Context, name string) (*Agent, error) {
	resp, err := c.do(ctx, http.MethodGet, "/agents/"+name, nil)
	if err != nil {
		return nil, err
	}
	defer closeBody(resp)
	if runtime.HasStatusCode(resp, http.StatusNotFound) {
		return nil, fmt.Errorf("agent %q: %w", name, ErrAgentNotFound)
	}
	if !runtime.HasStatusCode(resp, http.StatusOK) {
		return nil, runtime.NewResponseError(resp)
	}

	var wire agentWire
	if err := runtime.UnmarshalAsJSON(resp, &wire); err != nil {
		return nil, fmt.Errorf("decode agent: %w", err)
	}
	return toAgent(&wire), nil
}

// GetVersion fetches one version.
func (c *restClient) GetVersion(ctx context.Context, name, version string) (*AgentVersion, error) {
	resp, err := c.do(ctx, http.MethodGet, "/agents/"+name+"/versions/"+version, nil)
	if err != nil {
		return nil, err
	}
	defer closeBody(resp)
	if runtime.HasStatusCode(resp, http.StatusNotFound) {
		return nil, fmt.Errorf("agent %q version %q: %w", name, version, ErrVersionNotFound)
	}
	if !runtime.HasStatusCode(resp, http.StatusOK) {
		return nil, runtime.NewResponseError(resp)
	}

	var wire versionWire
	if err := runtime.UnmarshalAsJSON(resp, &wire); err != nil {
		return nil, fmt.Errorf("decode agent version: %w", err)
	}
	return toVersion(&wire), nil
}

// SetServedVersion points the endpoint selector at one version at full traffic.
func (c *restClient) SetServedVersion(ctx context.Context, name, version string) error {
	body := agentWire{
		AgentEndpoint: &agentEndpointWire{
			VersionSelector: &versionSelectorWire{
				Rules: []versionRuleWire{{
					Kind:              fixedRatioRuleKind,
					Version:           version,
					TrafficPercentage: fullTrafficPercentage,
				}},
			},
		},
	}

	req, err := runtime.NewRequest(ctx, http.MethodPatch, c.url("/agents/"+name))
	if err != nil {
		return fmt.Errorf("build served-version patch: %w", err)
	}
	if marshalErr := runtime.MarshalAsJSON(req, body); marshalErr != nil {
		return fmt.Errorf("encode served-version patch: %w", marshalErr)
	}
	// MarshalAsJSON sets application/json; the API requires merge-patch here.
	req.Raw().Header.Set("Content-Type", mergePatchContentType)

	resp, err := c.pipeline.Do(req)
	if err != nil {
		return fmt.Errorf("patch served version: %w", err)
	}
	defer closeBody(resp)
	if runtime.HasStatusCode(resp, http.StatusNotFound) {
		return fmt.Errorf("agent %q: %w", name, ErrAgentNotFound)
	}
	if !runtime.HasStatusCode(resp, http.StatusOK, http.StatusAccepted, http.StatusNoContent) {
		return runtime.NewResponseError(resp)
	}
	return nil
}

// ListAgents returns every agent in the project.
func (c *restClient) ListAgents(ctx context.Context) ([]Agent, error) {
	resp, err := c.do(ctx, http.MethodGet, "/agents", nil)
	if err != nil {
		return nil, err
	}
	defer closeBody(resp)
	if !runtime.HasStatusCode(resp, http.StatusOK) {
		return nil, runtime.NewResponseError(resp)
	}

	var wire listAgentsWire
	if err := runtime.UnmarshalAsJSON(resp, &wire); err != nil {
		return nil, fmt.Errorf("decode agent list: %w", err)
	}

	agents := make([]Agent, 0, len(wire.Value))
	for i := range wire.Value {
		agents = append(agents, *toAgent(&wire.Value[i]))
	}
	return agents, nil
}

// DeleteAgent removes an agent. An agent that is already gone is not an error,
// so destroy converges on an already-clean project.
func (c *restClient) DeleteAgent(ctx context.Context, name string) error {
	resp, err := c.do(ctx, http.MethodDelete, "/agents/"+name, nil)
	if err != nil {
		return err
	}
	defer closeBody(resp)
	if runtime.HasStatusCode(resp, http.StatusNotFound) {
		return nil
	}
	if !runtime.HasStatusCode(resp, http.StatusOK, http.StatusAccepted, http.StatusNoContent) {
		return runtime.NewResponseError(resp)
	}
	return nil
}

// closeBody drains and closes a response body so the connection can be
// reused. azcore closes the body when it unmarshals or builds a ResponseError,
// but the not-found and delete paths return without doing either.
func closeBody(resp *http.Response) {
	if resp == nil || resp.Body == nil {
		return
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
}

// toAgent converts the wire shape into the adapter's domain type.
func toAgent(w *agentWire) *Agent {
	agent := &Agent{Name: w.Name, Tags: w.Tags}
	if w.AgentEndpoint == nil || w.AgentEndpoint.VersionSelector == nil {
		return agent
	}
	// Traffic splitting is unsupported, so the first rule is the served one.
	rules := w.AgentEndpoint.VersionSelector.Rules
	if len(rules) > 0 {
		agent.ServedVersion = rules[0].Version
	}
	return agent
}

// toVersion converts the wire shape into the adapter's domain type.
func toVersion(w *versionWire) *AgentVersion {
	version := &AgentVersion{Version: w.Version, Status: w.Status}
	if w.Error != nil {
		version.FailureReason = w.Error.Message
	}
	return version
}
