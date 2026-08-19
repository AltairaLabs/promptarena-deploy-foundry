// Package foundry implements the PromptKit deploy provider for Azure AI
// Foundry hosted agents — bring-your-own-container agents running on
// Microsoft-managed, per-session-isolated infrastructure.
package foundry

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/AltairaLabs/PromptKit/runtime/deploy"
)

// ProviderName is the provider id used in arena config and the binary name.
const ProviderName = "foundry"

// Provider implements deploy.Provider for Azure AI Foundry hosted agents.
type Provider struct {
	// clientFunc builds the control-plane client. Tests substitute it; when nil
	// the real newClient is used.
	clientFunc func(ctx context.Context, cfg *Config) (foundryClient, error)
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
		Capabilities: []string{"validate", "plan"},
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
	Cfg        *Config
	AgentName  string
	Members    []string
	Prior      *State
	PackHash   string
	ConfigHash string
	Delivery   PackDelivery
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
		Cfg:        cfg,
		AgentName:  sanitizeAgentName(id),
		Members:    members,
		Prior:      prior,
		PackHash:   hashPack(req.PackJSON),
		ConfigHash: configHash,
		Delivery:   decidePackDelivery(req.PackJSON, cfg),
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

	prior, drift, err := p.verifiedPriorState(ctx, in.Cfg, in.Prior)
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
	}), nil
}

// verifiedPriorState checks recorded state against the live control plane and
// returns the corrected state plus warnings describing any drift.
//
// Verification is best-effort. A control plane that cannot be reached degrades
// to an unverified plan rather than failing the operation — a plan the user
// cannot run is worse than one carrying a caveat.
func (p *Provider) verifiedPriorState(
	ctx context.Context, cfg *Config, prior *State,
) (verified *State, drift []string, err error) {
	// Nothing recorded means a first deploy: there is nothing to verify, and a
	// dry run must stay fully offline.
	if cfg.DryRun || prior.AgentName == "" {
		return prior, nil, nil
	}

	client, err := p.newControlPlaneClient(ctx, cfg)
	if err != nil {
		return prior, []string{unverifiedWarning(err)}, nil
	}

	agent, err := client.GetAgent(ctx, prior.AgentName)
	if err != nil {
		// A missing project is a configuration error, not drift: every
		// operation against it will fail, so surface it rather than planning
		// resources that can never be created.
		if errors.Is(err, ErrProjectNotFound) {
			// Wrap the sentinel rather than err: the API's own phrasing says
			// less than this message does, and echoing both reads as a stutter.
			return nil, nil, fmt.Errorf(
				"project %q does not exist in account %q; check the deploy config: %w",
				cfg.Project, cfg.Account, ErrProjectNotFound)
		}
		verified, drift = driftedState(prior, err)
		return verified, drift, nil
	}

	if agent.ServedVersion != prior.ServedVersion {
		return prior, []string{fmt.Sprintf(
			"agent %q reports served version %q but state records %q; "+
				"the endpoint may have been repointed outside this adapter",
			prior.AgentName, agent.ServedVersion, prior.ServedVersion)}, nil
	}

	return prior, nil, nil
}

// unverifiedWarning explains that a plan is based on unverified state.
func unverifiedWarning(err error) string {
	return fmt.Sprintf(
		"prior state could not be verified against the control plane (%v); "+
			"the plan assumes the recorded state is accurate", err)
}

// driftedState turns a failed lookup into corrected state plus an explanation.
func driftedState(prior *State, lookupErr error) (verified *State, drift []string) {
	if !isAgentNotFound(lookupErr) {
		return prior, []string{unverifiedWarning(lookupErr)}
	}

	// The agent is genuinely gone. Clear what depended on it so the plan shows
	// a create rather than a no-change against a resource that does not exist.
	corrected := *prior
	name := prior.AgentName
	corrected.AgentName = ""
	corrected.ServedVersion = ""
	corrected.PriorVersions = nil
	corrected.InFlight = nil

	return &corrected, []string{fmt.Sprintf(
		"agent %q is recorded in state but no longer exists; it will be recreated", name)}
}

// Apply is implemented in phase 2.
func (p *Provider) Apply(
	_ context.Context, _ *deploy.PlanRequest, _ deploy.ApplyCallback,
) (string, error) {
	return "", fmt.Errorf("apply: %w", ErrNotImplemented)
}

// Destroy is implemented in phase 2.
func (p *Provider) Destroy(
	_ context.Context, _ *deploy.DestroyRequest, _ deploy.DestroyCallback,
) error {
	return fmt.Errorf("destroy: %w", ErrNotImplemented)
}

// Status is implemented in phase 2.
func (p *Provider) Status(
	_ context.Context, _ *deploy.StatusRequest,
) (*deploy.StatusResponse, error) {
	return nil, fmt.Errorf("status: %w", ErrNotImplemented)
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
