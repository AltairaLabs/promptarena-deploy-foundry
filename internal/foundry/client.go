package foundry

import "context"

// Version lifecycle states reported by the Foundry control plane.
const (
	// VersionStatusCreating means the version is still being provisioned.
	VersionStatusCreating = "creating"
	// VersionStatusActive means the version is serving.
	VersionStatusActive = "active"
	// VersionStatusFailed means provisioning finished unsuccessfully. The most
	// likely cause is an ACR pull denial: the create succeeds and the container
	// then cannot start.
	VersionStatusFailed = "failed"
	// VersionStatusDeleting means the version is being torn down.
	VersionStatusDeleting = "deleting"
	// VersionStatusDeleted means the version is gone.
	VersionStatusDeleted = "deleted"
)

// hostedAgentKind is the definition kind for a bring-your-own-container agent.
const hostedAgentKind = "hosted"

// Agent is the adapter's view of a Foundry agent.
type Agent struct {
	// Name is the agent name, which is also its path segment in the REST API.
	Name string
	// ServedVersion is the version the endpoint's selector currently points at.
	ServedVersion string
	// Metadata is the agent's key/value data, including any the user configured
	// under the config's `tags` key and the adapter's managed entries.
	Metadata map[string]string
}

// AgentVersion is one immutable version of an agent.
type AgentVersion struct {
	Version string
	// Status is one of the VersionStatus* values.
	Status string
	// FailureReason carries the platform's explanation when Status is failed.
	// Without it an ACR pull denial is indistinguishable from any other failure.
	FailureReason string
}

// AgentSpec is the adapter's desired-state description of an agent version.
// The client translates it into the REST body; keeping the adapter's own shape
// here means the builders are testable without touching Azure.
type AgentSpec struct {
	Name      string
	Image     string
	CPU       string
	Memory    string
	Protocols []string
	// IdleTimeoutMinutes is omitted from the request when zero, leaving the
	// platform's own default in place.
	IdleTimeoutMinutes int
	// Env becomes definition.environment_variables. The client emits it in
	// sorted key order so requests are deterministic.
	Env map[string]string
	// Metadata becomes the create body's "metadata". The config calls this
	// `tags`, which is the familiar Azure word, but the agents API stores it as
	// metadata and ignores a tags field outright.
	Metadata map[string]string
}

// foundryClient abstracts the Foundry data-plane control operations.
//
// There is no Go SDK for this data plane, which is an advantage: vertex cannot
// set fields that exist in the REST API but are absent from the published
// protos, whereas hand-rolled REST can send anything the API accepts.
type foundryClient interface {
	// CreateAgent creates the agent shell and returns it.
	CreateAgent(ctx context.Context, spec *AgentSpec) (*Agent, error)
	// CreateVersion creates a new immutable version and waits for it to leave
	// the creating state, returning it as active or failed.
	CreateVersion(ctx context.Context, name string, spec *AgentSpec) (*AgentVersion, error)
	// GetAgent fetches one agent, returning a wrapped ErrAgentNotFound when it
	// does not exist.
	GetAgent(ctx context.Context, name string) (*Agent, error)
	// GetVersion fetches one version, returning a wrapped ErrVersionNotFound
	// when it does not exist.
	GetVersion(ctx context.Context, name, version string) (*AgentVersion, error)
	// SetServedVersion points the endpoint's selector at version at 100%.
	// Traffic splitting is not supported by the platform.
	SetServedVersion(ctx context.Context, name, version string) error
	// ListAgents returns every agent in the project.
	ListAgents(ctx context.Context) ([]Agent, error)
	// DeleteAgent removes an agent. Deleting one that is already gone is not an
	// error, so destroy converges on an already-clean project.
	DeleteAgent(ctx context.Context, name string) error
}
