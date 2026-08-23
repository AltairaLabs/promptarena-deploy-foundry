package foundry

import (
	"context"
	"errors"
	"fmt"

	"github.com/AltairaLabs/promptarena/deploy"
	"github.com/AltairaLabs/promptarena/deploy/adaptersdk"
)

// Progress checkpoints, as fractions of the apply.
const (
	progressAgentReady   = 0.25
	progressVersionReady = 0.75
	progressServed       = 1.0
)

// applyInput is everything applyAgent needs, gathered by Apply.
type applyInput struct {
	Cfg        *Config
	Spec       *AgentSpec
	AgentName  string
	PackHash   string
	ConfigHash string
	// NeedsModelGrant is true when the pack declares speech bindings. Only
	// voice needs it: text inference goes through the project endpoint, where
	// the agent already has implicit access.
	NeedsModelGrant bool
	// Grant gives the agent identity account-level model access. Nil skips it.
	Grant modelAccessGranter
}

// modelAccessGranter grants an agent identity access to account-level models.
type modelAccessGranter interface {
	GrantModelAccess(ctx context.Context, account, principalID string) error
}

// Apply creates or updates the pack's hosted agent and returns the state to
// persist.
//
// State is returned even on partial failure: an agent created before a later
// step failed must be recorded, or the next apply cannot find it and the
// resource is orphaned in the project with nothing pointing at it.
func (p *Provider) Apply(
	ctx context.Context, req *deploy.PlanRequest, callback deploy.ApplyCallback,
) (string, error) {
	in, err := gatherPlanInput(req)
	if err != nil {
		return "", err
	}

	spec, specErrs := buildAgentSpec(&specInput{
		Cfg:           in.Cfg,
		AgentName:     in.AgentName,
		PackID:        in.PackID,
		PackJSON:      req.PackJSON,
		Bindings:      in.Bindings,
		Delivery:      in.Delivery,
		ToolSpecsJSON: in.ToolSpecsJSON,
	})
	if len(specErrs) != 0 {
		return "", fmt.Errorf("build agent spec: %s", joinErrors(specErrs))
	}

	client, err := p.newControlPlaneClient(ctx, in.Cfg)
	if err != nil {
		return "", err
	}

	var report *adaptersdk.ProgressReporter
	if callback != nil {
		report = adaptersdk.NewProgressReporter(callback)
	}

	next, applyErr := applyAgent(ctx, client, &applyInput{
		Cfg:             in.Cfg,
		Spec:            spec,
		AgentName:       in.AgentName,
		PackHash:        in.PackHash,
		ConfigHash:      in.ConfigHash,
		NeedsModelGrant: hasSpeechBindings(in.Bindings),
		Grant:           p.modelGranter(ctx),
	}, in.Prior, report)

	state, marshalErr := next.Marshal()
	if marshalErr != nil {
		return "", marshalErr
	}
	return state, applyErr
}

// applyAgent performs the create/version/serve sequence, returning the state to
// persist regardless of where it stopped.
func applyAgent(
	ctx context.Context, client foundryClient, in *applyInput,
	prior *State, report *adaptersdk.ProgressReporter,
) (next *State, err error) {
	state := *prior
	state.Version = StateVersion
	state.AdapterVersion = Version

	if ensureErr := ensureAgent(ctx, client, in, &state, report); ensureErr != nil {
		return &state, ensureErr
	}

	if !versionNeeded(in, &state) {
		reportProgress(report, "Already up to date", progressServed)
		return &state, nil
	}

	version, err := client.CreateVersion(ctx, in.AgentName, in.Spec)
	if err != nil {
		return &state, fmt.Errorf("create version of agent %q: %w", in.AgentName, err)
	}

	// A version that has not gone active must not be served — the endpoint
	// would route to a container that is not running. Record it as in-flight so
	// the next apply reconciles it rather than creating a duplicate.
	if version.Status != VersionStatusActive {
		state.InFlight = &InFlightVersion{
			Version:    version.Version,
			PackHash:   in.PackHash,
			ConfigHash: in.ConfigHash,
		}
		reportResource(report, ResTypeAgentVersion, version.Version,
			deploy.ActionCreate, "created", "Version is still provisioning")
		return &state, nil
	}

	state.InFlight = nil
	reportResource(report, ResTypeAgentVersion, version.Version,
		deploy.ActionCreate, "created", "Version is active")
	reportProgress(report, "Version is active", progressVersionReady)

	serveErr := client.SetEndpoint(ctx, in.AgentName, version.Version, in.Cfg.Protocols)
	if serveErr != nil {
		return &state, fmt.Errorf("configure endpoint of agent %q for version %q: %w",
			in.AgentName, version.Version, serveErr)
	}

	state.recordVersion(version.Version)
	state.PackHash = in.PackHash
	state.ConfigHash = in.ConfigHash

	reportResource(report, ResTypeServedVersion, servedVersionName,
		deploy.ActionUpdate, "updated",
		fmt.Sprintf("Serving version %s at 100%% traffic", version.Version),
		endpointLinks(in.Cfg, in.AgentName)...)
	reportProgress(report, "Deployment complete", progressServed)

	return &state, nil
}

// ensureAgent creates the agent shell when it does not exist yet. The agent
// persists across deploys; only its versions churn.
func ensureAgent(
	ctx context.Context, client foundryClient, in *applyInput,
	state *State, report *adaptersdk.ProgressReporter,
) error {
	if state.AgentName != "" {
		return nil
	}

	agent, err := client.CreateAgent(ctx, in.Spec)
	if err != nil {
		return fmt.Errorf("create agent %q: %w", in.AgentName, err)
	}

	state.AgentName = agent.Name
	reportResource(report, ResTypeAgent, agent.Name,
		deploy.ActionCreate, "created", "Foundry hosted agent created")

	grantModelAccess(ctx, in, agent, report)

	reportProgress(report, "Agent created", progressAgentReady)
	return nil
}

// grantModelAccess gives a voice agent the account-level access its speech
// bindings need.
//
// A failure is reported, never fatal. The agent is already created and its
// text path works; refusing to finish the deploy over a permission the
// operator can grant in one command would be worse than saying so. The grant
// is idempotent and the identity is stable across versions, so it runs once
// per agent.
func grantModelAccess(
	ctx context.Context, in *applyInput, agent *Agent, report *adaptersdk.ProgressReporter,
) {
	if !in.NeedsModelGrant || in.Grant == nil {
		return
	}
	if agent.PrincipalID == "" {
		reportProgress(report,
			"Speech bindings are configured but the agent reported no identity; "+
				"grant model access manually", progressAgentReady)
		return
	}

	err := in.Grant.GrantModelAccess(ctx, in.Cfg.Account, agent.PrincipalID)
	if err == nil {
		reportProgress(report, "Granted the agent access to speech models", progressAgentReady)
		return
	}

	// Hand over the exact command rather than a permission name.
	reportProgress(report, fmt.Sprintf(
		"Could not grant the agent access to speech models (%v). Voice will fail "+
			"until someone with permission runs:\n  %s",
		err, manualGrantCommand(in.Cfg.Account, agent.PrincipalID)), progressAgentReady)
}

// versionNeeded reports whether this apply must create a version. Versions are
// immutable and billed, so one is created only when something actually moved.
func versionNeeded(in *applyInput, state *State) bool {
	if state.InFlight != nil {
		return true
	}
	if state.ServedVersion == "" {
		return true
	}
	return state.PackHash != in.PackHash || state.ConfigHash != in.ConfigHash
}

// reportProgress emits a progress event when a reporter is attached.
func reportProgress(report *adaptersdk.ProgressReporter, message string, pct float64) {
	if report == nil {
		return
	}
	// A callback that fails must not fail the deploy: the resources are already
	// created, and losing the stream is not a reason to abandon them.
	_ = report.Progress(message, pct)
}

// reportResource emits a resource event when a reporter is attached.
func reportResource(
	report *adaptersdk.ProgressReporter,
	resType, name string, action deploy.Action, status, detail string,
	links ...deploy.ResourceLink,
) {
	if report == nil {
		return
	}
	_ = report.Resource(&deploy.ResourceResult{
		Type:   resType,
		Name:   name,
		Action: action,
		Status: status,
		Detail: detail,
		Links:  links,
	})
}

// joinErrors renders a list of validation errors for a wrapped error.
func joinErrors(errs []string) string {
	out := ""
	for i, e := range errs {
		if i > 0 {
			out += "; "
		}
		out += e
	}
	return out
}

// Destroy tears down the pack's agent. Deleting its versions is implicit:
// removing the agent takes them with it.
func (p *Provider) Destroy(
	ctx context.Context, req *deploy.DestroyRequest, callback deploy.DestroyCallback,
) error {
	cfg, err := parseConfig(req.DeployConfig)
	if err != nil {
		return err
	}
	prior, err := parseState(req.PriorState)
	if err != nil {
		return err
	}

	// Nothing was ever deployed, so there is nothing to remove. Destroy must
	// converge on "gone" rather than fail on an already-clean project.
	if prior.AgentName == "" {
		return nil
	}

	client, err := p.newControlPlaneClient(ctx, cfg)
	if err != nil {
		return err
	}

	if err := client.DeleteAgent(ctx, prior.AgentName); err != nil {
		return fmt.Errorf("delete agent %q: %w", prior.AgentName, err)
	}

	if callback != nil {
		_ = callback(&deploy.DestroyEvent{
			Type:    "resource",
			Message: fmt.Sprintf("Deleted agent %s", prior.AgentName),
		})
	}
	return nil
}

// Deployment status values reported by Status.
const (
	StatusDeployed    = "deployed"
	StatusNotDeployed = "not_deployed"
	StatusDegraded    = "degraded"
	StatusUnknown     = "unknown"
)

// Resource-level status values.
const (
	ResourceHealthy   = "healthy"
	ResourceUnhealthy = "unhealthy"
	ResourceMissing   = "missing"
)

// Status reports the live health of the deployed agent.
func (p *Provider) Status(
	ctx context.Context, req *deploy.StatusRequest,
) (*deploy.StatusResponse, error) {
	cfg, err := parseConfig(req.DeployConfig)
	if err != nil {
		return nil, err
	}
	prior, err := parseState(req.PriorState)
	if err != nil {
		return nil, err
	}

	if prior.AgentName == "" {
		return &deploy.StatusResponse{Status: StatusNotDeployed, State: req.PriorState}, nil
	}

	client, err := p.newControlPlaneClient(ctx, cfg)
	if err != nil {
		return nil, err
	}

	agent, err := client.GetAgent(ctx, prior.AgentName)
	if err != nil {
		if errors.Is(err, ErrAgentNotFound) {
			return &deploy.StatusResponse{
				Status: StatusNotDeployed,
				State:  req.PriorState,
				Resources: []deploy.ResourceStatus{{
					Type:   ResTypeAgent,
					Name:   prior.AgentName,
					Status: ResourceMissing,
					Detail: "The agent is recorded in state but does not exist",
				}},
			}, nil
		}
		return nil, err
	}

	return &deploy.StatusResponse{
		Status:    deploymentStatus(agent, prior),
		State:     req.PriorState,
		Resources: agentResources(cfg, agent, prior),
	}, nil
}

// deploymentStatus summarizes the deployment from the live agent.
func deploymentStatus(agent *Agent, prior *State) string {
	if agent.ServedVersion == "" {
		return StatusDegraded
	}
	if prior.InFlight != nil {
		return StatusDegraded
	}
	return StatusDeployed
}

// agentResources describes the live agent and its served version.
func agentResources(cfg *Config, agent *Agent, prior *State) []deploy.ResourceStatus {
	resources := []deploy.ResourceStatus{{
		Type:   ResTypeAgent,
		Name:   agent.Name,
		Status: ResourceHealthy,
		Detail: "Foundry hosted agent exists",
		Links:  endpointLinks(cfg, prior.AgentName),
	}}

	served := deploy.ResourceStatus{
		Type: ResTypeServedVersion,
		Name: servedVersionName,
	}
	switch {
	case agent.ServedVersion == "":
		served.Status = ResourceUnhealthy
		served.Detail = "No version is being served"
	case prior.InFlight != nil:
		served.Status = ResourceUnhealthy
		served.Detail = fmt.Sprintf(
			"Version %s did not finish provisioning; serving %s",
			prior.InFlight.Version, agent.ServedVersion)
	default:
		served.Status = ResourceHealthy
		served.Detail = fmt.Sprintf("Serving version %s", agent.ServedVersion)
	}

	return append(resources, served)
}
