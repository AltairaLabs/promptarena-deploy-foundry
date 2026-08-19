package foundry

import (
	"context"
	"fmt"
	"maps"
	"sort"
	"strconv"
)

// dryRunClient simulates the control plane in memory. It exists so a dry run
// exercises the adapter's own apply and plan logic — sequencing, state
// transitions, served-version promotion — without creating billable Azure
// resources or needing credentials.
type dryRunClient struct {
	agents map[string]*Agent
	// nextVersion assigns simulated version ids. Versions are immutable and
	// monotonic on the real platform, so reusing an id here would let a bug
	// that depends on distinct ids pass a dry run.
	nextVersion int
}

// newDryRunClient returns an empty simulated control plane.
func newDryRunClient() *dryRunClient {
	return &dryRunClient{
		agents:      map[string]*Agent{},
		nextVersion: 1,
	}
}

// CreateAgent records the agent shell.
func (c *dryRunClient) CreateAgent(_ context.Context, spec *AgentSpec) (*Agent, error) {
	agent := &Agent{Name: spec.Name, Tags: maps.Clone(spec.Tags)}
	c.agents[spec.Name] = agent
	return cloneAgent(agent), nil
}

// CreateVersion returns an immediately-active version. A dry run must not
// report one stuck in creating, or a caller polling for readiness would never
// terminate.
func (c *dryRunClient) CreateVersion(
	_ context.Context, name string, _ *AgentSpec,
) (*AgentVersion, error) {
	version := strconv.Itoa(c.nextVersion)
	c.nextVersion++

	if agent, ok := c.agents[name]; ok {
		agent.ServedVersion = version
	}

	return &AgentVersion{Version: version, Status: VersionStatusActive}, nil
}

// GetAgent returns a previously simulated agent.
func (c *dryRunClient) GetAgent(_ context.Context, name string) (*Agent, error) {
	agent, ok := c.agents[name]
	if !ok {
		return nil, fmt.Errorf("agent %q: %w", name, ErrAgentNotFound)
	}
	return cloneAgent(agent), nil
}

// GetVersion reports not-found: the simulated plane keeps no version history,
// and inventing one would hide a real lookup bug.
func (c *dryRunClient) GetVersion(_ context.Context, name, version string) (*AgentVersion, error) {
	return nil, fmt.Errorf("agent %q version %q: %w", name, version, ErrVersionNotFound)
}

// SetServedVersion promotes a version on a simulated agent.
func (c *dryRunClient) SetServedVersion(_ context.Context, name, version string) error {
	agent, ok := c.agents[name]
	if !ok {
		return fmt.Errorf("agent %q: %w", name, ErrAgentNotFound)
	}
	agent.ServedVersion = version
	return nil
}

// ListAgents returns the simulated agents, sorted by name for stable output.
func (c *dryRunClient) ListAgents(_ context.Context) ([]Agent, error) {
	names := make([]string, 0, len(c.agents))
	for name := range c.agents {
		names = append(names, name)
	}
	sort.Strings(names)

	agents := make([]Agent, 0, len(names))
	for _, name := range names {
		agents = append(agents, *cloneAgent(c.agents[name]))
	}
	return agents, nil
}

// DeleteAgent removes a simulated agent. Deleting one that was never created is
// not an error.
func (c *dryRunClient) DeleteAgent(_ context.Context, name string) error {
	delete(c.agents, name)
	return nil
}

// cloneAgent returns a copy so callers cannot mutate the simulated plane's
// records by holding onto a returned pointer.
func cloneAgent(a *Agent) *Agent {
	out := *a
	out.Tags = maps.Clone(a.Tags)
	return &out
}
