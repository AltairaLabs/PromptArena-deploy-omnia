package omnia

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/AltairaLabs/PromptKit/runtime/deploy"
)

// Condition type and status constants.
const (
	conditionReady = "Ready"
	conditionTrue  = "True"
)

// Status returns the current deployment status by checking each resource.
func (p *Provider) Status(
	ctx context.Context, req *deploy.StatusRequest,
) (*deploy.StatusResponse, error) {
	state, err := parseAdapterState(req.PriorState)
	if err != nil {
		return nil, fmt.Errorf("omnia: failed to parse prior state: %w", err)
	}

	if len(state.Resources) == 0 {
		return &deploy.StatusResponse{
			Status: "not_deployed",
		}, nil
	}

	cfg, err := parseConfig(req.DeployConfig)
	if err != nil {
		return nil, fmt.Errorf("omnia: failed to parse deploy config: %w", err)
	}

	client, err := p.clientFunc(cfg)
	if err != nil {
		return nil, fmt.Errorf("omnia: failed to create client: %w", err)
	}

	var resources []deploy.ResourceStatus
	hasUnhealthy := false

	for i := range state.Resources {
		health := checkResource(ctx, client, &state.Resources[i])
		if health != StatusHealthy {
			hasUnhealthy = true
		}
		resources = append(resources, deploy.ResourceStatus{
			Type:   state.Resources[i].Type,
			Name:   state.Resources[i].Name,
			Status: health,
		})
	}

	aggregateStatus := "deployed"
	if hasUnhealthy {
		aggregateStatus = "degraded"
	}

	stateJSON, _ := json.Marshal(state)

	return &deploy.StatusResponse{
		Status:    aggregateStatus,
		Resources: resources,
		State:     string(stateJSON),
	}, nil
}

// checkResource queries the Omnia API to determine a resource's health.
func checkResource(ctx context.Context, client omniaClient, res *ResourceState) string {
	resp, err := client.GetResource(ctx, res.Type, res.Name)
	if err != nil {
		return StatusMissing
	}

	if resp.Status == nil {
		return StatusHealthy
	}

	// Check for Ready condition.
	for _, cond := range resp.Status.Conditions {
		if cond.Type == conditionReady && cond.Status == conditionTrue {
			return StatusHealthy
		}
	}

	// If phase is Running/Active, consider healthy.
	switch resp.Status.Phase {
	case "Running", "Active", "Ready":
		return StatusHealthy
	case "Failed", "Error":
		return StatusUnhealthy
	}

	return StatusHealthy
}
