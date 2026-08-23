// Package foundry implements the PromptKit deploy provider for Azure AI
// Foundry hosted agents — bring-your-own-container agents running on
// Microsoft-managed, per-session-isolated infrastructure.
package foundry

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/AltairaLabs/promptarena/deploy"
	"github.com/AltairaLabs/promptarena/deploy/adaptersdk"
)

// ProviderName is the provider id used in arena config and the binary name.
const ProviderName = "foundry"

// Provider implements deploy.Provider for Azure AI Foundry hosted agents.
type Provider struct {
	// clientFunc builds the control-plane client. Tests substitute it; when nil
	// the real newClient is used.
	clientFunc func(ctx context.Context, cfg *Config) (foundryClient, error)
	// granterFunc builds the ARM client that grants a voice agent model access.
	// Tests substitute it; when nil the real one is used.
	granterFunc func(ctx context.Context) (modelAccessGranter, error)
}

// modelGranter returns the model-access granter, honoring a test override.
// A granter that cannot be built is not fatal: Apply reports the failure and
// hands the operator the command instead.
func (p *Provider) modelGranter(ctx context.Context) modelAccessGranter {
	if p.granterFunc != nil {
		granter, err := p.granterFunc(ctx)
		if err != nil {
			return nil
		}
		return granter
	}
	granter, err := newARMGrantClient(ctx)
	if err != nil {
		return nil
	}
	return granter
}

// hasSpeechBindings reports whether the pack declares speech in or out. Only
// those packs reach the account endpoint, and only they need the grant.
func hasSpeechBindings(bindings []ResolvedBinding) bool {
	for i := range bindings {
		if bindings[i].Role == RoleSTT || bindings[i].Role == RoleTTS {
			return true
		}
	}
	return false
}

// NewProvider creates a Provider.
func NewProvider() *Provider {
	return &Provider{}
}

// newControlPlaneClient returns the control-plane client, honoring a test
// override.
func (p *Provider) newControlPlaneClient(ctx context.Context, cfg *Config) (foundryClient, error) {
	if p.clientFunc != nil {
		return p.clientFunc(ctx, cfg)
	}
	if cfg.DryRun {
		return newDryRunClient(), nil
	}
	return newClient(ctx, cfg)
}

// GetProviderInfo returns metadata about the foundry adapter. Capabilities
// lists only what works end to end — advertising more would fail at deploy
// time rather than at discovery time.
func (p *Provider) GetProviderInfo(_ context.Context) (*deploy.ProviderInfo, error) {
	return &deploy.ProviderInfo{
		Name:         ProviderName,
		Version:      Version,
		Capabilities: []string{"validate", "plan", "apply", "destroy", "status"},
		ConfigSchema: configSchema,
	}, nil
}

// ValidateConfig parses and validates the provider configuration. Structural
// problems become Errors; advisories become Warnings, which do not make the
// config invalid.
func (p *Provider) ValidateConfig(
	_ context.Context, req *deploy.ValidateRequest,
) (*deploy.ValidateResponse, error) {
	cfg, err := parseConfig(req.Config)
	if err != nil {
		return &deploy.ValidateResponse{
			Valid:  false,
			Errors: []string{err.Error()},
		}, nil
	}

	var errs []string
	errs = append(errs, cfg.validateStructure()...)
	errs = append(errs, validateBindings(cfg.Providers)...)
	errs = append(errs, validateTags(cfg.Tags)...)

	warnings := bindingWarnings(cfg.Providers)
	warnings = append(warnings, diagnoseConfig(cfg)...)

	return &deploy.ValidateResponse{
		Valid:    len(errs) == 0,
		Errors:   errs,
		Warnings: warnings,
	}, nil
}

// planContext is everything Plan derives from a request before diffing.
type planContext struct {
	Cfg           *Config
	AgentName     string
	PackID        string
	Members       []string
	Prior         *State
	PackHash      string
	ConfigHash    string
	Bindings      []ResolvedBinding
	ToolSpecsJSON string
	Delivery      PackDelivery
}

// gatherPlanInput validates the request and derives everything buildPlan needs
// that does not require I/O.
func gatherPlanInput(req *deploy.PlanRequest) (*planContext, error) {
	cfg, err := parseConfig(req.DeployConfig)
	if err != nil {
		return nil, err
	}
	if errs := cfg.validateStructure(); len(errs) != 0 {
		return nil, fmt.Errorf("invalid deploy config: %s", strings.Join(errs, "; "))
	}

	prior, err := parseState(req.PriorState)
	if err != nil {
		return nil, err
	}

	arena, err := parseArenaConfig(req.ArenaConfig)
	if err != nil {
		return nil, err
	}
	resolved, resolveErrs := resolveBindings(cfg.Providers, arena)
	if len(resolveErrs) != 0 {
		return nil, fmt.Errorf("provider bindings: %s", strings.Join(resolveErrs, "; "))
	}
	if _, ok := primaryBinding(resolved); !ok {
		return nil, fmt.Errorf("no provider binding with role %q; one is required", RoleLLM)
	}

	toolSpecs, err := encodeToolSpecs(arena)
	if err != nil {
		return nil, err
	}
	configHash, err := hashPlanConfig(cfg, resolved, toolSpecs)
	if err != nil {
		return nil, err
	}

	id, err := packID(req.PackJSON)
	if err != nil {
		return nil, err
	}
	members, _, err := packMembers(req.PackJSON)
	if err != nil {
		return nil, err
	}

	return &planContext{
		Cfg:           cfg,
		AgentName:     sanitizeAgentName(id),
		PackID:        id,
		Members:       members,
		Prior:         prior,
		PackHash:      hashPack(req.PackJSON),
		ConfigHash:    configHash,
		Bindings:      resolved,
		ToolSpecsJSON: toolSpecs,
		Delivery:      decidePackDelivery(req.PackJSON, cfg),
	}, nil
}

// Plan reports the resource changes a deploy would make, without mutating
// anything. The diff comes from the pack and config hashes against prior
// adapter state, after that state has been verified against the live control
// plane so drift is visible. Dry run skips verification and stays fully
// offline.
func (p *Provider) Plan(ctx context.Context, req *deploy.PlanRequest) (*deploy.PlanResponse, error) {
	in, err := gatherPlanInput(req)
	if err != nil {
		return nil, err
	}

	prior, drift, advisories, err := p.verifiedPriorState(ctx, in.Cfg, in.Prior)
	if err != nil {
		return nil, err
	}

	return buildPlan(&planInput{
		AgentName:   in.AgentName,
		Members:     in.Members,
		Prior:       prior,
		PackHash:    in.PackHash,
		ConfigHash:  in.ConfigHash,
		CPU:         in.Cfg.CPU,
		Protocols:   in.Cfg.Protocols,
		Delivery:    in.Delivery,
		HasA2ATools: hasA2ATools(req.PackJSON),
		Drift:       drift,
		Advisories:  advisories,
	}), nil
}

// agentProbe answers the shared drift contract's existence question for the
// single agent this adapter records.
//
// It keeps the agent it fetched and the error it hit, because the caller needs
// both for concerns the contract does not model: a missing project is a
// configuration error rather than drift, and a served version that disagrees
// with state is drift of a kind absence-checking cannot express.
type agentProbe struct {
	client foundryClient
	found  *Agent
	err    error
}

// Exists reports whether the agent named by ref is still present.
func (p *agentProbe) Exists(
	ctx context.Context, ref adaptersdk.ResourceRef,
) (adaptersdk.Existence, error) {
	agent, err := p.client.GetAgent(ctx, ref.Name)
	if err != nil {
		p.err = err
		if isAgentNotFound(err) {
			return adaptersdk.ExistsNo, nil
		}
		return adaptersdk.ExistsUnknown, err
	}
	p.found = agent
	return adaptersdk.ExistsYes, nil
}

// verifiedPriorState checks recorded state against the live control plane and
// returns the corrected state, any drift, and advisories about the check
// itself.
//
// Verification is best-effort. A control plane that cannot be reached degrades
// to an unverified plan rather than failing the operation — a plan the user
// cannot run is worse than one carrying a caveat.
func (p *Provider) verifiedPriorState(
	ctx context.Context, cfg *Config, prior *State,
) (verified *State, drift []deploy.ResourceChange, advisories []string, err error) {
	// Nothing recorded means a first deploy: there is nothing to verify, and a
	// dry run must stay fully offline.
	if cfg.DryRun || prior.AgentName == "" {
		return prior, nil, nil, nil
	}

	client, clientErr := p.newControlPlaneClient(ctx, cfg)
	if clientErr != nil {
		return prior, nil, []string{unverifiedWarning(clientErr)}, nil
	}

	probe := &agentProbe{client: client}
	survivors, drift := adaptersdk.ReconcilePriorState(ctx, probe, []adaptersdk.ResourceRef{
		{Type: ResTypeAgent, Name: prior.AgentName},
	})

	// A missing project is a configuration error, not drift: every operation
	// against it will fail, so surface it rather than planning resources that
	// can never be created.
	if errors.Is(probe.err, ErrProjectNotFound) {
		// Wrap the sentinel rather than probe.err: the API's own phrasing says
		// less than this message does, and echoing both reads as a stutter.
		return nil, nil, nil, fmt.Errorf(
			"project %q does not exist in account %q; check the deploy config: %w",
			cfg.Project, cfg.Account, ErrProjectNotFound)
	}

	// The agent is gone. Clear what depended on it so the plan shows a create
	// rather than a no-change against a resource that does not exist.
	if len(survivors) == 0 {
		return clearedState(prior), drift, nil, nil
	}

	// Kept, but the lookup may have failed rather than succeeded — the shared
	// reconciler treats both alike, and the operator should know which it was.
	if probe.found == nil {
		return prior, nil, []string{unverifiedWarning(probe.err)}, nil
	}

	if probe.found.ServedVersion != prior.ServedVersion {
		return prior, nil, []string{fmt.Sprintf(
			"agent %q reports served version %q but state records %q; "+
				"the endpoint may have been repointed outside this adapter",
			prior.AgentName, probe.found.ServedVersion, prior.ServedVersion)}, nil
	}

	return prior, nil, nil, nil
}

// unverifiedWarning explains that a plan is based on unverified state.
func unverifiedWarning(err error) string {
	return fmt.Sprintf(
		"prior state could not be verified against the control plane (%v); "+
			"the plan assumes the recorded state is accurate", err)
}

// clearedState drops everything that depended on an agent that no longer
// exists, so the plan shows a create rather than a no-change.
func clearedState(prior *State) *State {
	corrected := *prior
	corrected.AgentName = ""
	corrected.ServedVersion = ""
	corrected.PriorVersions = nil
	corrected.InFlight = nil
	return &corrected
}

// Import is deferred; see the phasing in the design of record.
func (p *Provider) Import(
	_ context.Context, _ *deploy.ImportRequest,
) (*deploy.ImportResponse, error) {
	return nil, fmt.Errorf("import: %w", ErrNotImplemented)
}

// Compile-time assertion that Provider still satisfies the SDK interface, so a
// signature change in PromptKit fails the build here rather than at runtime.
var _ deploy.Provider = (*Provider)(nil)
