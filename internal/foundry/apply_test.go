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
	return c.dryRunClient.CreateAgent(ctx, spec)
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

func (c *recordingClient) SetServedVersion(ctx context.Context, name, version string) error {
	c.calls = append(c.calls, "SetServedVersion")
	if c.servedVersionErr != nil {
		return c.servedVersionErr
	}
	return c.dryRunClient.SetServedVersion(ctx, name, version)
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
	want := []string{"CreateAgent", "CreateVersion", "SetServedVersion"}
	if len(client.calls) != len(want) {
		t.Fatalf("calls = %v, want %v", client.calls, want)
	}
	for i := range want {
		if client.calls[i] != want[i] {
			t.Fatalf("calls = %v, want %v", client.calls, want)
		}
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
		if c == "SetServedVersion" {
			t.Errorf("calls = %v, want no SetServedVersion for a version still creating", client.calls)
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
