package foundry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/AltairaLabs/promptarena/deploy"
)

const validConfigJSON = `{
  "account": "acct",
  "project": "proj",
  "image": "myacr.azurecr.io/promptkit-foundry-runtime:v1",
  "cpu": "1",
  "memory": "2Gi",
  "protocols": ["invocations"],
  "providers": [{"name": "default", "role": "llm", "type": "azure", "model": "gpt4o-deploy"}]
}`

func TestGetProviderInfo(t *testing.T) {
	info, err := NewProvider().GetProviderInfo(context.Background())
	if err != nil {
		t.Fatalf("GetProviderInfo: %v", err)
	}
	if info.Name != ProviderName {
		t.Errorf("Name = %q, want %q", info.Name, ProviderName)
	}
	if info.Version != Version {
		t.Errorf("Version = %q, want %q", info.Version, Version)
	}

	// Capabilities must reflect what actually works end to end.
	want := map[string]bool{"validate": true, "plan": true, "apply": true, "destroy": true, "status": true}
	for _, c := range info.Capabilities {
		if !want[c] {
			t.Errorf("Capabilities includes %q, which is not implemented", c)
		}
		delete(want, c)
	}
	if len(want) != 0 {
		t.Errorf("Capabilities is missing %v", want)
	}

	var schema map[string]any
	if err := json.Unmarshal([]byte(info.ConfigSchema), &schema); err != nil {
		t.Fatalf("ConfigSchema is not valid JSON: %v", err)
	}
}

func TestValidateConfigAcceptsAValidConfig(t *testing.T) {
	got, err := NewProvider().ValidateConfig(
		context.Background(), &deploy.ValidateRequest{Config: validConfigJSON})
	if err != nil {
		t.Fatalf("ValidateConfig: %v", err)
	}
	if !got.Valid {
		t.Errorf("Valid = false, errors = %v", got.Errors)
	}
}

func TestValidateConfigRejectsMalformedJSON(t *testing.T) {
	got, err := NewProvider().ValidateConfig(
		context.Background(), &deploy.ValidateRequest{Config: `{`})
	if err != nil {
		t.Fatalf("ValidateConfig: %v", err)
	}
	if got.Valid {
		t.Error("Valid = true for malformed JSON")
	}
}

func TestValidateConfigReportsEveryProblemClass(t *testing.T) {
	// Bad structure, bad binding and a bad tag at once: all three must be
	// reported together rather than one at a time across repeated runs.
	const cfg = `{
      "account": "a", "project": "p", "image": "i", "cpu": "9", "memory": "4Gi",
      "providers": [{"name": "default"}],
      "tags": {"bad<name>": "v"}
    }`

	got, err := NewProvider().ValidateConfig(context.Background(), &deploy.ValidateRequest{Config: cfg})
	if err != nil {
		t.Fatalf("ValidateConfig: %v", err)
	}
	if got.Valid {
		t.Fatal("Valid = true, want false")
	}
	for _, want := range []string{`cpu "9"`, "exactly one of", "must not contain"} {
		if !containsSubstring(got.Errors, want) {
			t.Errorf("Errors = %v, want one containing %q", got.Errors, want)
		}
	}
}

// Warnings are advisory: they must not make an otherwise valid config invalid.
func TestValidateConfigWarningsDoNotInvalidate(t *testing.T) {
	const cfg = `{
      "account": "a", "project": "p", "image": "ghcr.io/x:1", "cpu": "1", "memory": "2Gi",
      "providers": [{"name": "primary", "role": "llm", "type": "azure", "model": "m"}]
    }`

	got, err := NewProvider().ValidateConfig(context.Background(), &deploy.ValidateRequest{Config: cfg})
	if err != nil {
		t.Fatalf("ValidateConfig: %v", err)
	}
	if !got.Valid {
		t.Errorf("Valid = false, errors = %v", got.Errors)
	}
	if len(got.Warnings) == 0 {
		t.Error("Warnings is empty, want advisories about the registry and the binding name")
	}
}

// stubClient records calls and returns canned results.
type stubClient struct {
	foundryClient
	agent    *Agent
	agentErr error
	gotName  string
}

func (s *stubClient) GetAgent(_ context.Context, name string) (*Agent, error) {
	s.gotName = name
	return s.agent, s.agentErr
}

func planRequest(t *testing.T, cfg, packJSON, priorState string) *deploy.PlanRequest {
	t.Helper()
	return &deploy.PlanRequest{
		DeployConfig: cfg,
		PackJSON:     packJSON,
		PriorState:   priorState,
	}
}

func TestPlanFirstDeploy(t *testing.T) {
	p := NewProvider()
	got, err := p.Plan(context.Background(), planRequest(t, validConfigJSON, singleAgentPack, ""))
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}

	if findChange(got.Changes, ResTypeAgent) == nil {
		t.Errorf("Changes = %v, want an agent change", got.Changes)
	}
	// The agent name comes from the pack id, sanitized.
	if c := findChange(got.Changes, ResTypeAgent); c != nil && c.Name != "solo-pack" {
		t.Errorf("agent name = %q, want solo-pack", c.Name)
	}
}

func TestPlanRejectsAnInvalidConfig(t *testing.T) {
	_, err := NewProvider().Plan(
		context.Background(), planRequest(t, `{"account":"a"}`, singleAgentPack, ""))
	if err == nil {
		t.Fatal("Plan accepted a config with no project or image")
	}
}

func TestPlanRejectsAnUnresolvableBinding(t *testing.T) {
	const cfg = `{
      "account":"a","project":"p","image":"acr.azurecr.io/x:1",
      "cpu":"1","memory":"2Gi",
      "providers":[{"name":"default","arena_provider":"nope"}]
    }`

	_, err := NewProvider().Plan(context.Background(), planRequest(t, cfg, singleAgentPack, ""))
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("err = %v, want an unresolved binding error", err)
	}
}

// A pack with no llm binding has nothing to converse with.
func TestPlanRequiresAnLLMBinding(t *testing.T) {
	const cfg = `{
      "account":"a","project":"p","image":"acr.azurecr.io/x:1",
      "cpu":"1","memory":"2Gi",
      "providers":[{"name":"speech","role":"tts","type":"cartesia","model":"m"}]
    }`

	_, err := NewProvider().Plan(context.Background(), planRequest(t, cfg, singleAgentPack, ""))
	if err == nil || !strings.Contains(err.Error(), RoleLLM) {
		t.Fatalf("err = %v, want a missing-llm error", err)
	}
}

func TestPlanRejectsAMalformedPack(t *testing.T) {
	_, err := NewProvider().Plan(context.Background(), planRequest(t, validConfigJSON, `{`, ""))
	if err == nil {
		t.Fatal("Plan accepted a malformed pack")
	}
}

// Verification against the live control plane makes drift visible: an agent
// deleted out of band must plan as a create, with the reason stated.
func TestPlanReportsDriftWhenTheAgentIsGone(t *testing.T) {
	p := NewProvider()
	stub := &stubClient{agentErr: fmt.Errorf("gone: %w", ErrAgentNotFound)}
	p.clientFunc = func(context.Context, *Config) (foundryClient, error) { return stub, nil }

	prior := `{"version":1,"agent_name":"solo-pack","served_version":"1","pack_hash":"x","config_hash":"y"}`
	got, err := p.Plan(context.Background(), planRequest(t, validConfigJSON, singleAgentPack, prior))
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}

	if stub.gotName != "solo-pack" {
		t.Errorf("verified agent %q, want solo-pack", stub.gotName)
	}
	// Two changes for the one agent: the DRIFT saying what happened, then the
	// CREATE that replaces it.
	var actions []deploy.Action
	for _, c := range got.Changes {
		if c.Type == ResTypeAgent {
			actions = append(actions, c.Action)
		}
	}
	if len(actions) != 2 || actions[0] != deploy.ActionDrift || actions[1] != deploy.ActionCreate {
		t.Errorf("expected DRIFT then CREATE for an agent deleted out of band, got %v from %+v",
			actions, got.Changes)
	}
	if got.Changes[0].Detail == "" || !containsSubstring(
		[]string{got.Changes[0].Detail}, "no longer exists") {
		t.Errorf("drift change should say the agent is gone, got %+v", got.Changes[0])
	}
}

func TestPlanUsesLiveServedVersion(t *testing.T) {
	p := NewProvider()
	// State says version 1 is served; the control plane says 2.
	p.clientFunc = func(context.Context, *Config) (foundryClient, error) {
		return &stubClient{agent: &Agent{Name: "solo-pack", ServedVersion: "2"}}, nil
	}

	prior := `{"version":1,"agent_name":"solo-pack","served_version":"1","pack_hash":"x","config_hash":"y"}`
	got, err := p.Plan(context.Background(), planRequest(t, validConfigJSON, singleAgentPack, prior))
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}

	if !containsSubstring(got.Warnings, "served version") {
		t.Errorf("Warnings = %v, want one about the served-version mismatch", got.Warnings)
	}
}

// A dry run must stay fully offline: no credential, no control-plane call.
func TestPlanDryRunSkipsVerification(t *testing.T) {
	p := NewProvider()
	p.clientFunc = func(context.Context, *Config) (foundryClient, error) {
		t.Fatal("dry run built a control-plane client")
		return nil, nil
	}

	cfg := strings.Replace(validConfigJSON, `"account": "acct"`, `"dry_run": true, "account": "acct"`, 1)
	prior := `{"version":1,"agent_name":"solo-pack","served_version":"1"}`
	if _, err := p.Plan(context.Background(), planRequest(t, cfg, singleAgentPack, prior)); err != nil {
		t.Fatalf("Plan: %v", err)
	}
}

// Verification is best-effort: a control plane that cannot be reached must
// degrade to an unverified plan, not fail the whole operation.
func TestPlanSurvivesAnUnreachableControlPlane(t *testing.T) {
	p := NewProvider()
	p.clientFunc = func(context.Context, *Config) (foundryClient, error) {
		return nil, errors.New("no credential available")
	}

	prior := `{"version":1,"agent_name":"solo-pack","served_version":"1","pack_hash":"x","config_hash":"y"}`
	got, err := p.Plan(context.Background(), planRequest(t, validConfigJSON, singleAgentPack, prior))
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if !containsSubstring(got.Warnings, "could not be verified") {
		t.Errorf("Warnings = %v, want one about unverified state", got.Warnings)
	}
}

// A first deploy has nothing recorded, so there is nothing to verify.
func TestPlanSkipsVerificationOnFirstDeploy(t *testing.T) {
	p := NewProvider()
	p.clientFunc = func(context.Context, *Config) (foundryClient, error) {
		t.Fatal("first deploy built a control-plane client")
		return nil, nil
	}

	if _, err := p.Plan(context.Background(), planRequest(t, validConfigJSON, singleAgentPack, "")); err != nil {
		t.Fatalf("Plan: %v", err)
	}
}

// Import is the only lifecycle operation still unbuilt.
func TestImportNotImplemented(t *testing.T) {
	_, err := NewProvider().Import(context.Background(), &deploy.ImportRequest{})
	if !errors.Is(err, ErrNotImplemented) {
		t.Errorf("err = %v, want ErrNotImplemented", err)
	}
}

// A missing project is a configuration error, not drift: every operation
// against it will fail, so Plan must say so rather than present a plan that
// cannot apply.
func TestPlanFailsOnAMissingProject(t *testing.T) {
	p := NewProvider()
	p.clientFunc = func(context.Context, *Config) (foundryClient, error) {
		return &stubClient{agentErr: fmt.Errorf("lookup: %w", ErrProjectNotFound)}, nil
	}

	prior := `{"version":1,"agent_name":"solo-pack","served_version":"1","pack_hash":"x","config_hash":"y"}`
	_, err := p.Plan(context.Background(), planRequest(t, validConfigJSON, singleAgentPack, prior))
	if err == nil {
		t.Fatal("Plan succeeded against a project that does not exist")
	}
	if !strings.Contains(err.Error(), "proj") {
		t.Errorf("err = %v, want it to name the project", err)
	}
}
