package foundry

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
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
	// agentsPath is the data-plane prefix every agent route hangs off.
	agentsPath = "/agents/"
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
	// cred is retained for Blob storage, which needs its own client rather
	// than the control-plane pipeline.
	cred azcore.TokenCredential
	// blobTransport overrides the Blob client's transport. Nil uses the
	// default; tests point it at an httptest server.
	blobTransport policy.Transporter
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
		cred:        cred,
		pollDelay:   defaultPollDelay,
		pollTimeout: defaultPollTimeout,
	}, nil
}

// Wire types for the data-plane REST bodies. They are kept separate from the
// adapter's domain types so a change in the API shape does not ripple through
// plan, apply and state.
type (
	agentWire struct {
		Name             string             `json:"name"`
		AgentEndpoint    *agentEndpointWire `json:"agent_endpoint,omitempty"`
		Metadata         map[string]string  `json:"metadata,omitempty"`
		InstanceIdentity *identityWire      `json:"instance_identity,omitempty"`
	}

	// identityWire is the agent's managed identity, minted with the agent.
	identityWire struct {
		PrincipalID string `json:"principal_id"`
		ClientID    string `json:"client_id"`
	}

	agentEndpointWire struct {
		VersionSelector *versionSelectorWire `json:"version_selector,omitempty"`
		Protocols       []string             `json:"protocols,omitempty"`
	}

	versionSelectorWire struct {
		Rules []versionRuleWire `json:"version_selection_rules,omitempty"`
	}

	versionRuleWire struct {
		Kind              string `json:"kind,omitempty"`
		Version           string `json:"version"`
		TrafficPercentage int    `json:"traffic_percentage"`
	}

	// createAgentWire is the create body for an agent or one of its versions.
	//
	// Key/value data goes under "metadata". Verified against a live project: a
	// "tags" field is accepted by the API and then silently discarded, which
	// loses the managed attribution with no error to notice.
	createAgentWire struct {
		Name       string            `json:"name"`
		Definition definitionWire    `json:"definition"`
		Metadata   map[string]string `json:"metadata,omitempty"`
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
		Code    string `json:"code"`
		Message string `json:"message"`
	}

	// listAgentsWire is the list envelope. Verified against a live project:
	// this API is OpenAI-shaped, so the collection is under "data" with a
	// "has_more" cursor flag — not the ARM-style "value" the management plane
	// uses elsewhere in Azure.
	listAgentsWire struct {
		Object  string      `json:"object"`
		Data    []agentWire `json:"data"`
		HasMore bool        `json:"has_more"`
	}
)

// buildCreateBody turns a spec into the create request body.
func buildCreateBody(spec *AgentSpec) createAgentWire {
	protocols := make([]protocolWire, 0, len(spec.Protocols))
	for _, p := range spec.Protocols {
		protocols = append(protocols, protocolWire{Protocol: p, Version: protocolVersion})
	}

	return createAgentWire{
		Name:     spec.Name,
		Metadata: spec.Metadata,
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
func (c *restClient) url(path string, extraParams ...string) string {
	u := fmt.Sprintf("%s%s?api-version=%s", c.baseURL, path, apiVersion)
	for _, p := range extraParams {
		u += "&" + p
	}
	return u
}

// do issues a request and returns the response, leaving status handling to the
// caller so each operation can map its own not-found semantics.
func (c *restClient) do(
	ctx context.Context, method, path string, body any, extraParams ...string,
) (*http.Response, error) {
	req, err := runtime.NewRequest(ctx, method, c.url(path, extraParams...))
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
	// Deliberately not closed here: the response is handed to the caller, which
	// owns closing it.
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
	resp, err := c.do(ctx, http.MethodPost, agentsPath+name+"/versions", buildCreateBody(spec))
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
	resp, err := c.do(ctx, http.MethodGet, agentsPath+name, nil)
	if err != nil {
		return nil, err
	}
	defer closeBody(resp)
	if runtime.HasStatusCode(resp, http.StatusNotFound) {
		return nil, notFoundError(resp, fmt.Errorf("agent %q: %w", name, ErrAgentNotFound))
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
	resp, err := c.do(ctx, http.MethodGet, agentsPath+name+"/versions/"+version, nil)
	if err != nil {
		return nil, err
	}
	defer closeBody(resp)
	if runtime.HasStatusCode(resp, http.StatusNotFound) {
		return nil, notFoundError(resp,
			fmt.Errorf("agent %q version %q: %w", name, version, ErrVersionNotFound))
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

// SetEndpoint points the selector at one version at full traffic and declares
// the protocols the endpoint exposes.
//
// The protocol list is not inherited from the version's protocol_versions and
// defaults to ["responses"]. Verified against a live project: an agent whose
// version declares only invocations still gets a responses endpoint, so
// omitting this leaves the deployment unreachable over the protocol it
// actually serves.
func (c *restClient) SetEndpoint(
	ctx context.Context, name, version string, protocols []string,
) error {
	body := agentWire{
		AgentEndpoint: &agentEndpointWire{
			VersionSelector: &versionSelectorWire{
				Rules: []versionRuleWire{{
					Kind:              fixedRatioRuleKind,
					Version:           version,
					TrafficPercentage: fullTrafficPercentage,
				}},
			},
			Protocols: protocols,
		},
	}

	req, err := runtime.NewRequest(ctx, http.MethodPatch, c.url(agentsPath+name))
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
		return notFoundError(resp, fmt.Errorf("agent %q: %w", name, ErrAgentNotFound))
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

	agents := make([]Agent, 0, len(wire.Data))
	for i := range wire.Data {
		agents = append(agents, *toAgent(&wire.Data[i]))
	}
	return agents, nil
}

// DeleteAgent removes an agent. An agent that is already gone is not an error,
// so destroy converges on an already-clean project.
// DeleteAgent removes an agent, cascading through any sessions it still holds.
//
// force=true is not optional. Foundry refuses to delete an agent with active
// sessions — 409 "Agent has active sessions" — and a session lingers for the
// idle timeout, 5 to 60 minutes. Without it, destroying an agent anyone has
// actually talked to fails, leaving the caller holding a resource they
// explicitly asked to remove. Destroy is an unambiguous instruction to tear
// the agent down; waiting out an idle timer is not a reading of it.
func (c *restClient) DeleteAgent(ctx context.Context, name string) error {
	resp, err := c.do(ctx, http.MethodDelete, agentsPath+name, nil, "force=true")
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

// 404 discriminators, both verified against a live project. Foundry answers a
// missing agent and a missing project with the same status but different codes:
//
//	agent   {"error":{"code":"not_found","message":"Agent x doesn't exist [Request ID: …]"}}
//	project {"error":{"code":"ResourceNotFound","message":"The project does not exist."}}
//
// The codes differ because the two come from different layers — the gateway
// rejects an unknown project before the request reaches the agents API at all.
const (
	// agentNotFoundCode is what the agents API returns for its own misses.
	agentNotFoundCode = "not_found"
	// projectMissingMarker is a fallback, consulted only when the code is not
	// the agents API's own. Prose is the part most likely to be reworded, so it
	// is never allowed to override the code.
	projectMissingMarker = "project does not exist"
)

// notFoundError decides what a 404 actually means. resourceErr is returned for
// an ordinary missing agent or version; a missing project is reported as
// ErrProjectNotFound, because that is a configuration error rather than drift.
func notFoundError(resp *http.Response, resourceErr error) error {
	// runtime.Payload, not io.ReadAll: the pipeline may already have consumed
	// the body, and Payload returns azcore's cached copy and restores it for
	// anything downstream.
	body, err := runtime.Payload(resp)
	if err != nil {
		return resourceErr
	}

	var envelope struct {
		Error errorWire `json:"error"`
	}
	// An empty or unparseable body still means the resource is not there.
	if json.Unmarshal(body, &envelope) != nil {
		return resourceErr
	}

	// The agents API answering with its own not-found code is conclusive: the
	// project was reached, and the agent within it is genuinely absent.
	if envelope.Error.Code == agentNotFoundCode {
		return resourceErr
	}

	if strings.Contains(strings.ToLower(envelope.Error.Message), projectMissingMarker) {
		return fmt.Errorf("%s: %w", envelope.Error.Message, ErrProjectNotFound)
	}
	return resourceErr
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
	agent := &Agent{Name: w.Name, Metadata: w.Metadata}
	if w.InstanceIdentity != nil {
		agent.PrincipalID = w.InstanceIdentity.PrincipalID
	}
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
