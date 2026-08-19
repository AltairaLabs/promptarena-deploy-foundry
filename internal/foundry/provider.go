// Package foundry implements the PromptKit deploy provider for Azure AI
// Foundry hosted agents — bring-your-own-container agents running on
// Microsoft-managed, per-session-isolated infrastructure.
package foundry

import (
	"context"
	"fmt"

	"github.com/AltairaLabs/PromptKit/runtime/deploy"
)

// ProviderName is the provider id used in arena config and the binary name.
const ProviderName = "foundry"

// Provider implements deploy.Provider for Azure AI Foundry hosted agents.
type Provider struct{}

// NewProvider creates a Provider.
func NewProvider() *Provider {
	return &Provider{}
}

// GetProviderInfo returns metadata about the foundry adapter. Capabilities is
// empty until the lifecycle operations below are implemented — an adapter that
// advertised capabilities it does not have would fail at deploy time rather
// than at discovery time.
func (p *Provider) GetProviderInfo(_ context.Context) (*deploy.ProviderInfo, error) {
	return &deploy.ProviderInfo{
		Name:         ProviderName,
		Version:      Version,
		Capabilities: []string{},
		ConfigSchema: configSchema,
	}, nil
}

// ValidateConfig is implemented in phase 1.
func (p *Provider) ValidateConfig(
	_ context.Context, _ *deploy.ValidateRequest,
) (*deploy.ValidateResponse, error) {
	return nil, fmt.Errorf("validate: %w", ErrNotImplemented)
}

// Plan is implemented in phase 1.
func (p *Provider) Plan(_ context.Context, _ *deploy.PlanRequest) (*deploy.PlanResponse, error) {
	return nil, fmt.Errorf("plan: %w", ErrNotImplemented)
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

// Import is deferred; see docs/local-backlog for the phasing.
func (p *Provider) Import(
	_ context.Context, _ *deploy.ImportRequest,
) (*deploy.ImportResponse, error) {
	return nil, fmt.Errorf("import: %w", ErrNotImplemented)
}

// Compile-time assertion that Provider still satisfies the SDK interface, so a
// signature change in PromptKit fails the build here rather than at runtime.
var _ deploy.Provider = (*Provider)(nil)
