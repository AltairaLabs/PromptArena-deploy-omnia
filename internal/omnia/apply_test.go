package omnia

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/AltairaLabs/PromptKit/runtime/deploy"
)

func noopApplyCallback(_ *deploy.ApplyEvent) error { return nil }

func TestApply_SingleAgent(t *testing.T) {
	sim := newSimulatedClient()
	p := &Provider{clientFunc: newSimulatedClientFactory(sim)}

	stateJSON, err := p.Apply(context.Background(), &deploy.PlanRequest{
		PackJSON:     testPackJSON,
		DeployConfig: testDeployConfig,
	}, noopApplyCallback)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var state AdapterState
	if err := json.Unmarshal([]byte(stateJSON), &state); err != nil {
		t.Fatalf("failed to parse state: %v", err)
	}
	if state.PackID != "test-pack" {
		t.Errorf("expected pack_id %q, got %q", "test-pack", state.PackID)
	}
	if state.Version != "1.0.0" {
		t.Errorf("expected version %q, got %q", "1.0.0", state.Version)
	}
	if len(state.Resources) == 0 {
		t.Fatal("expected resources in state")
	}

	// All resources should have "created" status.
	for _, r := range state.Resources {
		if r.Status != ResStatusCreated {
			t.Errorf("expected status %q for %s %q, got %q", ResStatusCreated, r.Type, r.Name, r.Status)
		}
	}

	// Verify expected resource types were created.
	typeSet := map[string]bool{}
	for _, r := range state.Resources {
		typeSet[r.Type] = true
	}
	for _, expected := range []string{ResTypeConfigMap, ResTypePromptPack, ResTypeToolRegistry, ResTypeAgentRuntime} {
		if !typeSet[expected] {
			t.Errorf("expected resource type %q in state", expected)
		}
	}
}

func TestApply_DryRun(t *testing.T) {
	sim := newSimulatedClient()
	p := &Provider{clientFunc: newSimulatedClientFactory(sim)}

	dryRunConfig := `{
		"api_endpoint": "https://omnia.test.com",
		"workspace": "test-ws",
		"api_token": "test-token",
		"providers": {"default": "claude-prod"},
		"dry_run": true
	}`

	stateJSON, err := p.Apply(context.Background(), &deploy.PlanRequest{
		PackJSON:     testPackJSON,
		DeployConfig: dryRunConfig,
	}, noopApplyCallback)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var state AdapterState
	if err := json.Unmarshal([]byte(stateJSON), &state); err != nil {
		t.Fatalf("failed to parse state: %v", err)
	}

	// All resources should have "planned" status (no actual API calls).
	for _, r := range state.Resources {
		if r.Status != ResStatusPlanned {
			t.Errorf("expected status %q for %s %q, got %q", ResStatusPlanned, r.Type, r.Name, r.Status)
		}
	}

	// Simulated client should have no stored resources (dry run skips API).
	if len(sim.resources) > 0 {
		t.Errorf("expected no resources in simulated client for dry run, got %d", len(sim.resources))
	}
}

func TestApply_WithPriorState(t *testing.T) {
	sim := newSimulatedClient()
	// Pre-populate the simulated client with existing resources so updates succeed.
	for _, r := range []struct{ typ, name string }{
		{ResTypeConfigMap, "test-pack-packdata"},
		{ResTypePromptPack, "test-pack"},
		{ResTypeToolRegistry, "test-pack-tools"},
		{ResTypeAgentRuntime, "test-pack"},
	} {
		sim.resources[simKey(r.typ, r.name)] = &ResourceResponse{
			Kind:     r.typ,
			Metadata: ResourceMetadata{Name: r.name, UID: "old-uid", ResourceVersion: "1"},
		}
	}
	p := &Provider{clientFunc: newSimulatedClientFactory(sim)}

	priorState := AdapterState{
		Resources: []ResourceState{
			{Type: ResTypeConfigMap, Name: "test-pack-packdata", UID: "old-uid-1"},
			{Type: ResTypePromptPack, Name: "test-pack", UID: "old-uid-2"},
			{Type: ResTypeToolRegistry, Name: "test-pack-tools", UID: "old-uid-3"},
			{Type: ResTypeAgentRuntime, Name: "test-pack", UID: "old-uid-4"},
		},
		PackID:  "test-pack",
		Version: "0.9.0",
	}
	priorJSON, _ := json.Marshal(priorState)

	stateJSON, err := p.Apply(context.Background(), &deploy.PlanRequest{
		PackJSON:     testPackJSON,
		DeployConfig: testDeployConfig,
		PriorState:   string(priorJSON),
	}, noopApplyCallback)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var state AdapterState
	if err := json.Unmarshal([]byte(stateJSON), &state); err != nil {
		t.Fatalf("failed to parse state: %v", err)
	}

	// All resources should have "updated" status since they existed before.
	for _, r := range state.Resources {
		if r.Status != ResStatusUpdated {
			t.Errorf("expected status %q for %s %q, got %q", ResStatusUpdated, r.Type, r.Name, r.Status)
		}
	}
}

func TestApply_ResourceFailure(t *testing.T) {
	sim := newSimulatedClient()
	sim.failOn[resourceKey(ResTypeToolRegistry, "test-pack-tools")] = fmt.Errorf("simulated API failure")
	p := &Provider{clientFunc: newSimulatedClientFactory(sim)}

	stateJSON, err := p.Apply(context.Background(), &deploy.PlanRequest{
		PackJSON:     testPackJSON,
		DeployConfig: testDeployConfig,
	}, noopApplyCallback)

	// Should return a combined error but still produce state.
	if err == nil {
		t.Fatal("expected error due to resource failure")
	}

	var state AdapterState
	if err := json.Unmarshal([]byte(stateJSON), &state); err != nil {
		t.Fatalf("failed to parse state: %v", err)
	}

	// Other resources should still have been created.
	var failedCount, createdCount int
	for _, r := range state.Resources {
		switch r.Status {
		case ResStatusFailed:
			failedCount++
		case ResStatusCreated:
			createdCount++
		}
	}
	if failedCount != 1 {
		t.Errorf("expected 1 failed resource, got %d", failedCount)
	}
	if createdCount == 0 {
		t.Error("expected other resources to be created despite failure")
	}
}
