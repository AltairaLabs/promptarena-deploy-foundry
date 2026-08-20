package foundry

import (
	"context"
	"errors"
	"testing"
)

func dryRunSpec() *AgentSpec {
	return &AgentSpec{
		Name:      "support-pack",
		Image:     "acr.azurecr.io/x:1",
		CPU:       "1",
		Memory:    "2Gi",
		Protocols: []string{ProtocolInvocations},
	}
}

// A dry run must never reach the network, and must return a result shaped like
// the real one so apply's own logic is exercised end to end.
func TestDryRunCreateAgent(t *testing.T) {
	agent, err := newDryRunClient().CreateAgent(context.Background(), dryRunSpec())
	if err != nil {
		t.Fatalf("CreateAgent: %v", err)
	}
	if agent.Name != "support-pack" {
		t.Errorf("Name = %q, want support-pack", agent.Name)
	}
}

func TestDryRunCreateVersionIsActive(t *testing.T) {
	c := newDryRunClient()

	version, err := c.CreateVersion(context.Background(), "support-pack", dryRunSpec())
	if err != nil {
		t.Fatalf("CreateVersion: %v", err)
	}
	// A dry run must not report a version stuck in `creating`, or callers would
	// poll forever against a client that never advances.
	if version.Status != VersionStatusActive {
		t.Errorf("Status = %q, want %q", version.Status, VersionStatusActive)
	}
	if version.Version == "" {
		t.Error("Version is empty, want a simulated version id")
	}
}

// Versions are immutable and monotonic, so successive creates must not reuse
// an id — a plan that shows two versions must simulate two.
func TestDryRunCreateVersionAssignsDistinctIDs(t *testing.T) {
	c := newDryRunClient()
	ctx := context.Background()

	first, err := c.CreateVersion(ctx, "a", dryRunSpec())
	if err != nil {
		t.Fatalf("CreateVersion: %v", err)
	}
	second, err := c.CreateVersion(ctx, "a", dryRunSpec())
	if err != nil {
		t.Fatalf("CreateVersion: %v", err)
	}

	if first.Version == second.Version {
		t.Errorf("both versions are %q, want distinct ids", first.Version)
	}
}

// Nothing was ever created, so a lookup must report not-found via errors.Is
// rather than inventing an agent that does not exist.
func TestDryRunGetAgentReportsNotFound(t *testing.T) {
	_, err := newDryRunClient().GetAgent(context.Background(), "nope")

	if !errors.Is(err, ErrAgentNotFound) {
		t.Errorf("err = %v, want ErrAgentNotFound", err)
	}
}

func TestDryRunGetAgentFindsWhatItCreated(t *testing.T) {
	c := newDryRunClient()
	ctx := context.Background()

	if _, err := c.CreateAgent(ctx, dryRunSpec()); err != nil {
		t.Fatalf("CreateAgent: %v", err)
	}

	agent, err := c.GetAgent(ctx, "support-pack")
	if err != nil {
		t.Fatalf("GetAgent: %v", err)
	}
	if agent.Name != "support-pack" {
		t.Errorf("Name = %q, want support-pack", agent.Name)
	}
}

func TestDryRunSetEndpoint(t *testing.T) {
	c := newDryRunClient()
	ctx := context.Background()

	if _, err := c.CreateAgent(ctx, dryRunSpec()); err != nil {
		t.Fatalf("CreateAgent: %v", err)
	}
	if err := c.SetEndpoint(ctx, "support-pack", "1", []string{ProtocolInvocations}); err != nil {
		t.Fatalf("SetEndpoint: %v", err)
	}

	agent, err := c.GetAgent(ctx, "support-pack")
	if err != nil {
		t.Fatalf("GetAgent: %v", err)
	}
	if agent.ServedVersion != "1" {
		t.Errorf("ServedVersion = %q, want 1", agent.ServedVersion)
	}
}

func TestDryRunDeleteAgentIsIdempotent(t *testing.T) {
	c := newDryRunClient()
	ctx := context.Background()

	// Deleting something that was never created is not an error: destroy must
	// converge on "gone" rather than fail on an already-clean project.
	if err := c.DeleteAgent(ctx, "nope"); err != nil {
		t.Errorf("DeleteAgent on a missing agent = %v, want nil", err)
	}

	if _, err := c.CreateAgent(ctx, dryRunSpec()); err != nil {
		t.Fatalf("CreateAgent: %v", err)
	}
	if err := c.DeleteAgent(ctx, "support-pack"); err != nil {
		t.Fatalf("DeleteAgent: %v", err)
	}
	if _, err := c.GetAgent(ctx, "support-pack"); !errors.Is(err, ErrAgentNotFound) {
		t.Errorf("after delete, GetAgent err = %v, want ErrAgentNotFound", err)
	}
}

func TestDryRunListAgents(t *testing.T) {
	c := newDryRunClient()
	ctx := context.Background()

	if _, err := c.CreateAgent(ctx, dryRunSpec()); err != nil {
		t.Fatalf("CreateAgent: %v", err)
	}

	agents, err := c.ListAgents(ctx)
	if err != nil {
		t.Fatalf("ListAgents: %v", err)
	}
	if len(agents) != 1 || agents[0].Name != "support-pack" {
		t.Errorf("ListAgents() = %v, want the created agent", agents)
	}
}

func TestDryRunGetVersionReportsNotFound(t *testing.T) {
	_, err := newDryRunClient().GetVersion(context.Background(), "a", "1")

	if !errors.Is(err, ErrVersionNotFound) {
		t.Errorf("err = %v, want ErrVersionNotFound", err)
	}
}

// The dry-run client must satisfy the same interface the real one does, or it
// is not exercising the code path apply will take.
func TestDryRunClientSatisfiesInterface(t *testing.T) {
	var _ foundryClient = newDryRunClient()
}
