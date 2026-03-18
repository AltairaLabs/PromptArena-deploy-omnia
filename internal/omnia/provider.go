// Package omnia implements the Omnia Kubernetes deploy adapter for PromptKit.
package omnia

import (
	"context"
	"fmt"

	"github.com/AltairaLabs/PromptKit/runtime/deploy"
)

// Provider implements deploy.Provider for the Omnia Kubernetes platform.
type Provider struct {
	clientFunc omniaClientFactory
}

// NewProvider creates a new Provider with the real HTTP client factory.
func NewProvider() *Provider {
	return &Provider{
		clientFunc: newHTTPClient,
	}
}

// GetProviderInfo returns metadata about the omnia adapter.
func (p *Provider) GetProviderInfo(_ context.Context) (*deploy.ProviderInfo, error) {
	return &deploy.ProviderInfo{
		Name:         "omnia",
		Version:      Version,
		Capabilities: []string{"plan", "apply", "destroy", "status"},
		ConfigSchema: configSchema,
	}, nil
}

// ValidateConfig parses and validates the provider configuration.
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

	errs := cfg.validate()
	return &deploy.ValidateResponse{
		Valid:  len(errs) == 0,
		Errors: errs,
	}, nil
}

// Import imports an existing Omnia resource into the adapter state.
func (p *Provider) Import(
	_ context.Context, _ *deploy.ImportRequest,
) (*deploy.ImportResponse, error) {
	return nil, fmt.Errorf("omnia: import is not yet supported")
}
