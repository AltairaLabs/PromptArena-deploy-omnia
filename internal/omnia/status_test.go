package omnia

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/AltairaLabs/PromptKit/runtime/deploy"
)

func TestStatus_Deployed(t *testing.T) {
	sim := newSimulatedClient()
	// Pre-populate resources so GetResource returns them as healthy.
	sim.resources[resourceKey(ResTypePromptPack, "test-pack")] = &ResourceResponse{
		Kind:     "PromptPack",
		Metadata: ResourceMetadata{Name: "test-pack"},
		Status:   &ResourceStatus{Conditions: []ResourceCondition{{Type: "Ready", Status: "True"}}},
	}
	sim.resources[resourceKey(ResTypeToolRegistry, "test-pack-tools")] = &ResourceResponse{
		Kind:     "ToolRegistry",
		Metadata: ResourceMetadata{Name: "test-pack-tools"},
		Status:   &ResourceStatus{Phase: "Active"},
	}
	sim.resources[resourceKey(ResTypeAgentRuntime, "test-pack")] = &ResourceResponse{
		Kind:     "AgentRuntime",
		Metadata: ResourceMetadata{Name: "test-pack"},
		Status:   &ResourceStatus{Phase: "Running"},
	}
	p := &Provider{clientFunc: newSimulatedClientFactory(sim)}

	state := AdapterState{
		Resources: []ResourceState{
			{Type: ResTypePromptPack, Name: "test-pack"},
			{Type: ResTypeToolRegistry, Name: "test-pack-tools"},
			{Type: ResTypeAgentRuntime, Name: "test-pack"},
		},
	}
	stateJSON, _ := json.Marshal(state)

	resp, err := p.Status(context.Background(), &deploy.StatusRequest{
		DeployConfig: testDeployConfig,
		PriorState:   string(stateJSON),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Status != "deployed" {
		t.Errorf("expected status %q, got %q", "deployed", resp.Status)
	}
	if len(resp.Resources) != 3 {
		t.Errorf("expected 3 resources, got %d", len(resp.Resources))
	}
	for _, r := range resp.Resources {
		if r.Status != StatusHealthy {
			t.Errorf("expected resource %s %q to be healthy, got %q", r.Type, r.Name, r.Status)
		}
	}
}

func TestStatus_Degraded(t *testing.T) {
	sim := newSimulatedClient()
	// Only populate some resources — the missing one will cause "degraded".
	sim.resources[resourceKey(ResTypePromptPack, "test-pack")] = &ResourceResponse{
		Kind:     "PromptPack",
		Metadata: ResourceMetadata{Name: "test-pack"},
	}
	// Do NOT add the AgentRuntime — it will be "missing".
	p := &Provider{clientFunc: newSimulatedClientFactory(sim)}

	state := AdapterState{
		Resources: []ResourceState{
			{Type: ResTypePromptPack, Name: "test-pack"},
			{Type: ResTypeAgentRuntime, Name: "test-pack"},
		},
	}
	stateJSON, _ := json.Marshal(state)

	resp, err := p.Status(context.Background(), &deploy.StatusRequest{
		DeployConfig: testDeployConfig,
		PriorState:   string(stateJSON),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Status != "degraded" {
		t.Errorf("expected status %q, got %q", "degraded", resp.Status)
	}

	// Verify at least one resource is missing.
	var hasMissing bool
	for _, r := range resp.Resources {
		if r.Status == StatusMissing {
			hasMissing = true
		}
	}
	if !hasMissing {
		t.Error("expected at least one resource with 'missing' status")
	}
}

func TestStatus_NotDeployed(t *testing.T) {
	sim := newSimulatedClient()
	p := &Provider{clientFunc: newSimulatedClientFactory(sim)}

	resp, err := p.Status(context.Background(), &deploy.StatusRequest{
		DeployConfig: testDeployConfig,
		PriorState:   "",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Status != "not_deployed" {
		t.Errorf("expected status %q, got %q", "not_deployed", resp.Status)
	}
}
