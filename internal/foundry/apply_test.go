package foundry

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/AltairaLabs/PromptKit/runtime/deploy"
)

// recordingClient is a simulated control plane that records the calls made
// against it, so apply's sequencing can be asserted without touching Azure.
type recordingClient struct {
	*dryRunClient

	calls []string

	createAgentErr   error
	createVersionErr error
	servedVersionErr error
	deleteErr        error

	// versionStatus overrides what CreateVersion reports.
	versionStatus string
	// principalID is the identity a created agent reports.
	principalID string
	// gotProtocols records what the endpoint was configured with.
	gotProtocols []string
}

func newRecordingClient() *recordingClient {
	return &recordingClient{dryRunClient: newDryRunClient()}
}

// seedAgent puts an already-deployed agent into the simulated plane, for tests
// whose prior state describes a world where one exists.
func (c *recordingClient) seedAgent(name, servedVersion string) {
	c.dryRunClient.agents[name] = &Agent{Name: name, ServedVersion: servedVersion}
}

func (c *recordingClient) CreateAgent(ctx context.Context, spec *AgentSpec) (*Agent, error) {
	c.calls = append(c.calls, "CreateAgent")
	if c.createAgentErr != nil {
		return nil, c.createAgentErr
	}
	agent, err := c.dryRunClient.CreateAgent(ctx, spec)
	if err == nil {
		agent.PrincipalID = c.principalID
	}
	return agent, err
}

func (c *recordingClient) CreateVersion(
	ctx context.Context, name string, spec *AgentSpec,
) (*AgentVersion, error) {
	c.calls = append(c.calls, "CreateVersion")
	if c.createVersionErr != nil {
		return nil, c.createVersionErr
	}
	v, err := c.dryRunClient.CreateVersion(ctx, name, spec)
	if err == nil && c.versionStatus != "" {
		v.Status = c.versionStatus
	}
	return v, err
}

func (c *recordingClient) SetEndpoint(
	ctx context.Context, name, version string, protocols []string,
) error {
	c.calls = append(c.calls, "SetEndpoint")
	c.gotProtocols = protocols
	if c.servedVersionErr != nil {
		return c.servedVersionErr
	}
	return c.dryRunClient.SetEndpoint(ctx, name, version, protocols)
}

func (c *recordingClient) DeleteAgent(ctx context.Context, name string) error {
	c.calls = append(c.calls, "DeleteAgent")
	if c.deleteErr != nil {
		return c.deleteErr
	}
	return c.dryRunClient.DeleteAgent(ctx, name)
}

func applyProvider(t *testing.T, client foundryClient) *Provider {
	t.Helper()
	p := NewProvider()
	p.clientFunc = func(context.Context, *Config) (foundryClient, error) { return client, nil }
	return p
}

func applyRequest(cfg, packJSON, priorState string) *deploy.PlanRequest {
	return &deploy.PlanRequest{
		DeployConfig: cfg,
		PackJSON:     packJSON,
		PriorState:   priorState,
	}
}

func TestApplyFirstDeploySequence(t *testing.T) {
	client := newRecordingClient()
	p := applyProvider(t, client)

	state, err := p.Apply(context.Background(), applyRequest(validConfigJSON, singleAgentPack, ""), nil)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	// The agent must exist before a version can be created against it, and the
	// selector can only point at a version that already exists.
	want := []string{"CreateAgent", "CreateVersion", "SetEndpoint"}
	if len(client.calls) != len(want) {
		t.Fatalf("calls = %v, want %v", client.calls, want)
	}
	for i := range want {
		if client.calls[i] != want[i] {
			t.Fatalf("calls = %v, want %v", client.calls, want)
		}
	}

	// The endpoint must expose what the container serves; it defaults to
	// responses otherwise.
	if len(client.gotProtocols) != 1 || client.gotProtocols[0] != ProtocolInvocations {
		t.Errorf("endpoint protocols = %v, want the configured list", client.gotProtocols)
	}

	out, err := parseState(state)
	if err != nil {
		t.Fatalf("parseState: %v", err)
	}
	if out.AgentName != "solo-pack" {
		t.Errorf("AgentName = %q, want solo-pack", out.AgentName)
	}
	if out.ServedVersion == "" {
		t.Error("ServedVersion is empty, want the created version recorded")
	}
	if out.PackHash == "" || out.ConfigHash == "" {
		t.Error("hashes were not recorded, so the next plan would always show a change")
	}
	if out.InFlight != nil {
		t.Errorf("InFlight = %+v, want nil after a clean apply", out.InFlight)
	}
}

// The agent persists across deploys; only versions churn. Re-creating it would
// fail against an existing name.
func TestApplyExistingAgentSkipsCreate(t *testing.T) {
	client := newRecordingClient()
	// The prior state below says the agent already exists, so the simulated
	// plane has to know about it too.
	client.seedAgent("solo-pack", "1")
	client.calls = nil
	p := applyProvider(t, client)
	prior := `{"version":1,"agent_name":"solo-pack","served_version":"1","pack_hash":"old","config_hash":"old"}`

	if _, err := p.Apply(context.Background(), applyRequest(validConfigJSON, singleAgentPack, prior), nil); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	for _, c := range client.calls {
		if c == "CreateAgent" {
			t.Errorf("calls = %v, want no CreateAgent for an existing agent", client.calls)
		}
	}
}

// Versions are immutable and billed. An apply that changes nothing must not
// create one.
func TestApplyNoChangeCreatesNoVersion(t *testing.T) {
	client := newRecordingClient()
	p := applyProvider(t, client)

	first, err := p.Apply(context.Background(), applyRequest(validConfigJSON, singleAgentPack, ""), nil)
	if err != nil {
		t.Fatalf("first Apply: %v", err)
	}

	client.calls = nil
	if _, err := p.Apply(context.Background(), applyRequest(validConfigJSON, singleAgentPack, first), nil); err != nil {
		t.Fatalf("second Apply: %v", err)
	}

	if len(client.calls) != 0 {
		t.Errorf("calls = %v, want none when nothing changed", client.calls)
	}
}

func TestApplyCreatesVersionWhenPackChanges(t *testing.T) {
	client := newRecordingClient()
	p := applyProvider(t, client)

	first, err := p.Apply(context.Background(), applyRequest(validConfigJSON, singleAgentPack, ""), nil)
	if err != nil {
		t.Fatalf("first Apply: %v", err)
	}
	firstState, _ := parseState(first)

	client.calls = nil
	changed := `{"id": "solo-pack", "prompts": {"main": {"system": "changed"}}}`
	second, err := p.Apply(context.Background(), applyRequest(validConfigJSON, changed, first), nil)
	if err != nil {
		t.Fatalf("second Apply: %v", err)
	}

	secondState, _ := parseState(second)
	if secondState.ServedVersion == firstState.ServedVersion {
		t.Errorf("ServedVersion did not move: %q", secondState.ServedVersion)
	}
	// The version it replaced must be recoverable, so a rollback is a selector
	// PATCH rather than an image rebuild.
	if len(secondState.PriorVersions) == 0 ||
		secondState.PriorVersions[0] != firstState.ServedVersion {
		t.Errorf("PriorVersions = %v, want %q at the head",
			secondState.PriorVersions, firstState.ServedVersion)
	}
}

// State must come back even on failure, or resources already created are
// orphaned with nothing recording them.
func TestApplyReturnsStateAfterVersionFailure(t *testing.T) {
	client := newRecordingClient()
	client.createVersionErr = errors.New("boom")
	p := applyProvider(t, client)

	state, err := p.Apply(context.Background(), applyRequest(validConfigJSON, singleAgentPack, ""), nil)
	if err == nil {
		t.Fatal("Apply succeeded despite a version failure")
	}
	if state == "" {
		t.Fatal("Apply returned no state, orphaning the agent it created")
	}

	out, parseErr := parseState(state)
	if parseErr != nil {
		t.Fatalf("parseState: %v", parseErr)
	}
	if out.AgentName != "solo-pack" {
		t.Errorf("AgentName = %q, want the created agent recorded", out.AgentName)
	}
}

// A version that provisions but never becomes active must be recorded as
// in-flight so the next apply reconciles it instead of creating a duplicate.
func TestApplyRecordsAnInFlightVersion(t *testing.T) {
	client := newRecordingClient()
	client.versionStatus = VersionStatusCreating
	p := applyProvider(t, client)

	state, err := p.Apply(context.Background(), applyRequest(validConfigJSON, singleAgentPack, ""), nil)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	out, _ := parseState(state)
	if out.InFlight == nil {
		t.Fatal("InFlight is nil, want the unfinished version recorded")
	}
	if out.InFlight.Version == "" {
		t.Error("InFlight.Version is empty")
	}
}

// Pointing the endpoint at a version that never went active would route
// traffic to a container that is not running.
func TestApplyDoesNotServeAnInFlightVersion(t *testing.T) {
	client := newRecordingClient()
	client.versionStatus = VersionStatusCreating
	p := applyProvider(t, client)

	if _, err := p.Apply(context.Background(), applyRequest(validConfigJSON, singleAgentPack, ""), nil); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	for _, c := range client.calls {
		if c == "SetEndpoint" {
			t.Errorf("calls = %v, want no SetEndpoint for a version still creating", client.calls)
		}
	}
}

func TestApplyReportsProgress(t *testing.T) {
	client := newRecordingClient()
	p := applyProvider(t, client)

	var events []string
	cb := func(e *deploy.ApplyEvent) error {
		events = append(events, e.Type)
		return nil
	}

	if _, err := p.Apply(context.Background(), applyRequest(validConfigJSON, singleAgentPack, ""), cb); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(events) == 0 {
		t.Error("no apply events were emitted")
	}
}

func TestApplyRejectsAnInvalidConfig(t *testing.T) {
	p := applyProvider(t, newRecordingClient())

	if _, err := p.Apply(context.Background(), applyRequest(`{"account":"a"}`, singleAgentPack, ""), nil); err == nil {
		t.Fatal("Apply accepted a config with no project or image")
	}
}

func TestDestroyDeletesTheAgent(t *testing.T) {
	client := newRecordingClient()
	p := applyProvider(t, client)

	state, err := p.Apply(context.Background(), applyRequest(validConfigJSON, singleAgentPack, ""), nil)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	client.calls = nil
	err = p.Destroy(context.Background(), &deploy.DestroyRequest{
		DeployConfig: validConfigJSON, PriorState: state,
	}, nil)
	if err != nil {
		t.Fatalf("Destroy: %v", err)
	}

	if len(client.calls) != 1 || client.calls[0] != "DeleteAgent" {
		t.Errorf("calls = %v, want a single DeleteAgent", client.calls)
	}
}

// Destroying a deployment that was never applied must converge rather than
// fail on an empty project.
func TestDestroyWithNoPriorStateIsANoOp(t *testing.T) {
	client := newRecordingClient()
	p := applyProvider(t, client)

	err := p.Destroy(context.Background(), &deploy.DestroyRequest{
		DeployConfig: validConfigJSON, PriorState: "",
	}, nil)
	if err != nil {
		t.Fatalf("Destroy: %v", err)
	}
	if len(client.calls) != 0 {
		t.Errorf("calls = %v, want none with nothing deployed", client.calls)
	}
}

func TestDestroyPropagatesFailures(t *testing.T) {
	client := newRecordingClient()
	client.deleteErr = errors.New("boom")
	p := applyProvider(t, client)

	prior := `{"version":1,"agent_name":"solo-pack","served_version":"1"}`
	err := p.Destroy(context.Background(), &deploy.DestroyRequest{
		DeployConfig: validConfigJSON, PriorState: prior,
	}, nil)
	if err == nil {
		t.Fatal("Destroy reported success despite a delete failure")
	}
}

func TestStatusReportsDeployed(t *testing.T) {
	client := newRecordingClient()
	p := applyProvider(t, client)

	state, err := p.Apply(context.Background(), applyRequest(validConfigJSON, singleAgentPack, ""), nil)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	got, err := p.Status(context.Background(), &deploy.StatusRequest{
		DeployConfig: validConfigJSON, PriorState: state,
	})
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if got.Status != StatusDeployed {
		t.Errorf("Status = %q, want %q", got.Status, StatusDeployed)
	}
	if len(got.Resources) == 0 {
		t.Error("Resources is empty, want the agent reported")
	}
}

func TestStatusWithNoPriorStateIsNotDeployed(t *testing.T) {
	p := applyProvider(t, newRecordingClient())

	got, err := p.Status(context.Background(), &deploy.StatusRequest{
		DeployConfig: validConfigJSON, PriorState: "",
	})
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if got.Status != StatusNotDeployed {
		t.Errorf("Status = %q, want %q", got.Status, StatusNotDeployed)
	}
}

// An agent deleted out of band must read as missing, not healthy.
func TestStatusReportsAMissingAgent(t *testing.T) {
	p := NewProvider()
	p.clientFunc = func(context.Context, *Config) (foundryClient, error) {
		return &stubClient{agentErr: fmt.Errorf("gone: %w", ErrAgentNotFound)}, nil
	}

	prior := `{"version":1,"agent_name":"solo-pack","served_version":"1"}`
	got, err := p.Status(context.Background(), &deploy.StatusRequest{
		DeployConfig: validConfigJSON, PriorState: prior,
	})
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if got.Status != StatusNotDeployed {
		t.Errorf("Status = %q, want %q", got.Status, StatusNotDeployed)
	}
	if len(got.Resources) == 0 || got.Resources[0].Status != ResourceMissing {
		t.Errorf("Resources = %+v, want the agent reported missing", got.Resources)
	}
}

func TestJoinErrors(t *testing.T) {
	if got := joinErrors([]string{"a", "b"}); got != "a; b" {
		t.Errorf("joinErrors = %q, want \"a; b\"", got)
	}
	if got := joinErrors([]string{"only"}); got != "only" {
		t.Errorf("joinErrors = %q, want \"only\"", got)
	}
	if got := joinErrors(nil); got != "" {
		t.Errorf("joinErrors(nil) = %q, want empty", got)
	}
}

// An agent that exists but serves nothing is degraded, not deployed: the
// endpoint would route traffic nowhere.
func TestDeploymentStatus(t *testing.T) {
	tests := []struct {
		name  string
		agent *Agent
		prior *State
		want  string
	}{
		{"serving", &Agent{ServedVersion: "1"}, &State{}, StatusDeployed},
		{"nothing served", &Agent{}, &State{}, StatusDegraded},
		{
			"version still provisioning",
			&Agent{ServedVersion: "1"},
			&State{InFlight: &InFlightVersion{Version: "2"}},
			StatusDegraded,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := deploymentStatus(tt.agent, tt.prior); got != tt.want {
				t.Errorf("deploymentStatus = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestAgentResourcesReportsTheServedVersion(t *testing.T) {
	got := agentResources(&Agent{Name: "a", ServedVersion: "3"}, &State{})

	if len(got) != 2 {
		t.Fatalf("resources = %d, want the agent and its served version", len(got))
	}
	if got[0].Status != ResourceHealthy {
		t.Errorf("agent status = %q, want %q", got[0].Status, ResourceHealthy)
	}
	if !containsSubstring([]string{got[1].Detail}, "3") {
		t.Errorf("served version detail = %q, want it to name version 3", got[1].Detail)
	}
}

func TestAgentResourcesFlagsAnUnservedAgent(t *testing.T) {
	got := agentResources(&Agent{Name: "a"}, &State{})

	if got[1].Status != ResourceUnhealthy {
		t.Errorf("served version status = %q, want %q", got[1].Status, ResourceUnhealthy)
	}
}

// An in-flight version means the endpoint is serving something older than the
// deploy intended, which the operator needs to see.
func TestAgentResourcesFlagsAnInFlightVersion(t *testing.T) {
	prior := &State{InFlight: &InFlightVersion{Version: "4"}}
	got := agentResources(&Agent{Name: "a", ServedVersion: "3"}, prior)

	if got[1].Status != ResourceUnhealthy {
		t.Errorf("served version status = %q, want %q", got[1].Status, ResourceUnhealthy)
	}
	if !containsSubstring([]string{got[1].Detail}, "4") {
		t.Errorf("detail = %q, want it to name the unfinished version", got[1].Detail)
	}
}

func TestProjectEndpoint(t *testing.T) {
	got := projectEndpoint(&Config{Account: "acct", Project: "proj"})

	want := "https://acct.services.ai.azure.com/api/projects/proj"
	if got != want {
		t.Errorf("projectEndpoint = %q, want %q", got, want)
	}
}

// Destroy and Status both reach the control plane, so both must surface a
// config they cannot parse rather than acting on a half-understood one.
func TestDestroyAndStatusRejectMalformedInput(t *testing.T) {
	p := applyProvider(t, newRecordingClient())
	ctx := context.Background()

	if err := p.Destroy(ctx, &deploy.DestroyRequest{DeployConfig: `{`}, nil); err == nil {
		t.Error("Destroy accepted malformed config")
	}
	if err := p.Destroy(ctx, &deploy.DestroyRequest{
		DeployConfig: validConfigJSON, PriorState: `{`,
	}, nil); err == nil {
		t.Error("Destroy accepted malformed state")
	}
	if _, err := p.Status(ctx, &deploy.StatusRequest{DeployConfig: `{`}); err == nil {
		t.Error("Status accepted malformed config")
	}
	if _, err := p.Status(ctx, &deploy.StatusRequest{
		DeployConfig: validConfigJSON, PriorState: `{`,
	}); err == nil {
		t.Error("Status accepted malformed state")
	}
}

// The operator is told what was removed, not left to infer it.
func TestDestroyReportsTheDeletedAgent(t *testing.T) {
	client := newRecordingClient()
	client.seedAgent("solo-pack", "1")
	p := applyProvider(t, client)

	var messages []string
	cb := func(e *deploy.DestroyEvent) error {
		messages = append(messages, e.Message)
		return nil
	}
	prior := `{"version":1,"agent_name":"solo-pack","served_version":"1"}`
	if err := p.Destroy(context.Background(), &deploy.DestroyRequest{
		DeployConfig: validConfigJSON, PriorState: prior,
	}, cb); err != nil {
		t.Fatalf("Destroy: %v", err)
	}

	if !containsSubstring(messages, "solo-pack") {
		t.Errorf("events = %v, want the agent named", messages)
	}
}

// A control plane that cannot be reached must fail Status rather than report
// a deployment healthy on no evidence.
func TestStatusPropagatesAClientFailure(t *testing.T) {
	p := NewProvider()
	p.clientFunc = func(context.Context, *Config) (foundryClient, error) {
		return nil, errors.New("no credential")
	}

	prior := `{"version":1,"agent_name":"solo-pack","served_version":"1"}`
	if _, err := p.Status(context.Background(), &deploy.StatusRequest{
		DeployConfig: validConfigJSON, PriorState: prior,
	}); err == nil {
		t.Fatal("Status succeeded with no control plane")
	}
}

// fakeGranter records grant attempts.
type fakeGranter struct {
	calls   int
	account string
	gotID   string
	err     error
}

func (g *fakeGranter) GrantModelAccess(_ context.Context, account, principalID string) error {
	g.calls++
	g.account, g.gotID = account, principalID
	return g.err
}

const voiceConfigJSON = `{
  "account": "acct", "project": "proj",
  "image": "myacr.azurecr.io/promptkit-foundry-runtime:v1",
  "cpu": "1", "memory": "2Gi", "protocols": ["invocations_ws"],
  "providers": [
    {"name": "default",   "role": "llm", "type": "openai", "model": "gpt-4-1-mini"},
    {"name": "speech-in", "role": "stt", "type": "openai", "model": "whisper"},
    {"name": "speech-out","role": "tts", "type": "openai", "model": "tts-1"}
  ]
}`

func voiceProvider(t *testing.T, client foundryClient, granter modelAccessGranter) *Provider {
	t.Helper()
	p := applyProvider(t, client)
	p.granterFunc = func(context.Context) (modelAccessGranter, error) { return granter, nil }
	return p
}

// A voice pack reaches the account endpoint, which the project proxy does not
// cover, so the agent needs the grant — and getting it automatically is what
// keeps a voice deploy a single command.
func TestApplyGrantsModelAccessForVoice(t *testing.T) {
	client := newRecordingClient()
	client.principalID = "principal-1"
	granter := &fakeGranter{}

	p := voiceProvider(t, client, granter)
	if _, err := p.Apply(context.Background(),
		applyRequest(voiceConfigJSON, singleAgentPack, ""), nil); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	if granter.calls != 1 {
		t.Fatalf("grant calls = %d, want 1", granter.calls)
	}
	if granter.gotID != "principal-1" {
		t.Errorf("granted to %q, want the agent's identity", granter.gotID)
	}
	if granter.account != "acct" {
		t.Errorf("granted on %q, want the configured account", granter.account)
	}
}

// Text-only packs go through the project proxy, where access is implicit.
// Granting anyway would hand out a permission nothing needs.
func TestApplyDoesNotGrantForTextOnlyPacks(t *testing.T) {
	granter := &fakeGranter{}

	p := voiceProvider(t, newRecordingClient(), granter)
	if _, err := p.Apply(context.Background(),
		applyRequest(validConfigJSON, singleAgentPack, ""), nil); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	if granter.calls != 0 {
		t.Errorf("grant calls = %d, want none for a text-only pack", granter.calls)
	}
}

// The agent is created and its text path works; refusing to finish over a
// permission the operator can grant in one command would be worse.
func TestApplySurvivesAGrantFailure(t *testing.T) {
	client := newRecordingClient()
	client.principalID = "principal-1"
	granter := &fakeGranter{err: ErrRoleAssignmentDenied}

	var messages []string
	cb := func(e *deploy.ApplyEvent) error {
		messages = append(messages, e.Message)
		return nil
	}

	p := voiceProvider(t, client, granter)
	state, err := p.Apply(context.Background(),
		applyRequest(voiceConfigJSON, singleAgentPack, ""), cb)
	if err != nil {
		t.Fatalf("Apply failed over a grant it could not make: %v", err)
	}
	if state == "" {
		t.Fatal("Apply returned no state")
	}

	// The operator must be handed the command, not just a permission name.
	if !containsSubstring(messages, "az role assignment create") {
		t.Errorf("events = %v, want the exact command", messages)
	}
}

// The identity is created with the agent, so an existing agent needs no
// re-grant on redeploy.
func TestApplyDoesNotRegrantForAnExistingAgent(t *testing.T) {
	client := newRecordingClient()
	client.seedAgent("solo-pack", "1")
	granter := &fakeGranter{}

	p := voiceProvider(t, client, granter)
	prior := `{"version":1,"agent_name":"solo-pack","served_version":"1","pack_hash":"old","config_hash":"old"}`
	if _, err := p.Apply(context.Background(),
		applyRequest(voiceConfigJSON, singleAgentPack, prior), nil); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	if granter.calls != 0 {
		t.Errorf("grant calls = %d, want none when the agent already exists", granter.calls)
	}
}

func TestHasSpeechBindings(t *testing.T) {
	tests := []struct {
		name     string
		bindings []ResolvedBinding
		want     bool
	}{
		{"stt", []ResolvedBinding{{Role: RoleSTT}}, true},
		{"tts", []ResolvedBinding{{Role: RoleTTS}}, true},
		{"llm only", []ResolvedBinding{{Role: RoleLLM}}, false},
		{"none", nil, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := hasSpeechBindings(tt.bindings); got != tt.want {
				t.Errorf("hasSpeechBindings = %v, want %v", got, tt.want)
			}
		})
	}
}
