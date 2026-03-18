package omnia

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/AltairaLabs/PromptKit/runtime/deploy"
)

func collectDestroyEvents(t *testing.T) (deploy.DestroyCallback, *[]*deploy.DestroyEvent) {
	t.Helper()
	var events []*deploy.DestroyEvent
	cb := func(event *deploy.DestroyEvent) error {
		events = append(events, event)
		return nil
	}
	return cb, &events
}

func TestDestroy_AllResources(t *testing.T) {
	sim := newSimulatedClient()
	// Pre-populate resources so deletes succeed.
	for _, r := range []struct{ typ, name string }{
		{ResTypeConfigMap, "test-pack-packdata"},
		{ResTypePromptPack, "test-pack"},
		{ResTypeToolRegistry, "test-pack-tools"},
		{ResTypeAgentRuntime, "test-pack"},
	} {
		sim.resources[simKey(r.typ, r.name)] = &ResourceResponse{
			Kind:     r.typ,
			Metadata: ResourceMetadata{Name: r.name},
		}
	}
	p := &Provider{clientFunc: newSimulatedClientFactory(sim)}

	state := AdapterState{
		Resources: []ResourceState{
			{Type: ResTypeConfigMap, Name: "test-pack-packdata"},
			{Type: ResTypePromptPack, Name: "test-pack"},
			{Type: ResTypeToolRegistry, Name: "test-pack-tools"},
			{Type: ResTypeAgentRuntime, Name: "test-pack"},
		},
		PackID: "test-pack",
	}
	stateJSON, _ := json.Marshal(state)

	cb, events := collectDestroyEvents(t)

	err := p.Destroy(context.Background(), &deploy.DestroyRequest{
		DeployConfig: testDeployConfig,
		PriorState:   string(stateJSON),
	}, cb)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify all resources were deleted.
	var resourceEvents int
	for _, e := range *events {
		if e.Type == "resource" && e.Resource != nil && e.Resource.Action == deploy.ActionDelete {
			resourceEvents++
		}
	}
	if resourceEvents != len(state.Resources) {
		t.Errorf("expected %d resource delete events, got %d", len(state.Resources), resourceEvents)
	}

	// Verify destroy order via events: AgentRuntime delete before ConfigMap delete.
	var runtimeIdx, configmapIdx int
	idx := 0
	for _, e := range *events {
		if e.Type == "resource" && e.Resource != nil && e.Resource.Action == deploy.ActionDelete {
			if e.Resource.Type == ResTypeAgentRuntime {
				runtimeIdx = idx
			}
			if e.Resource.Type == ResTypeConfigMap {
				configmapIdx = idx
			}
			idx++
		}
	}
	if runtimeIdx > configmapIdx && configmapIdx > 0 {
		t.Error("expected agent_runtime to be deleted before configmap")
	}
}

func TestDestroy_EmptyState(t *testing.T) {
	sim := newSimulatedClient()
	p := &Provider{clientFunc: newSimulatedClientFactory(sim)}

	cb, events := collectDestroyEvents(t)

	err := p.Destroy(context.Background(), &deploy.DestroyRequest{
		DeployConfig: testDeployConfig,
		PriorState:   "",
	}, cb)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should emit "nothing to do" style events.
	var hasComplete bool
	for _, e := range *events {
		if e.Type == "complete" {
			hasComplete = true
		}
	}
	if !hasComplete {
		t.Error("expected complete event for empty state destroy")
	}

	// No resources should have been touched.
	if len(sim.resources) > 0 {
		t.Errorf("expected no resources in simulated client, got %d", len(sim.resources))
	}
}

func TestDestroy_PartialFailure(t *testing.T) {
	sim := newSimulatedClient()
	// Pre-populate all resources so deletes work (except the one we inject failure on).
	for _, r := range []struct{ typ, name string }{
		{ResTypeConfigMap, "test-pack-packdata"},
		{ResTypePromptPack, "test-pack"},
		{ResTypeAgentRuntime, "test-pack"},
	} {
		sim.resources[simKey(r.typ, r.name)] = &ResourceResponse{
			Kind:     r.typ,
			Metadata: ResourceMetadata{Name: r.name},
		}
	}
	sim.failOn[simKey(ResTypePromptPack, "test-pack")] = fmt.Errorf("simulated delete failure")
	p := &Provider{clientFunc: newSimulatedClientFactory(sim)}

	state := AdapterState{
		Resources: []ResourceState{
			{Type: ResTypeConfigMap, Name: "test-pack-packdata"},
			{Type: ResTypePromptPack, Name: "test-pack"},
			{Type: ResTypeAgentRuntime, Name: "test-pack"},
		},
		PackID: "test-pack",
	}
	stateJSON, _ := json.Marshal(state)

	cb, events := collectDestroyEvents(t)

	// Destroy does not return an error for partial failures — it emits error events.
	err := p.Destroy(context.Background(), &deploy.DestroyRequest{
		DeployConfig: testDeployConfig,
		PriorState:   string(stateJSON),
	}, cb)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should have error event for failed resource and resource events for others.
	var errorCount, deleteCount int
	for _, e := range *events {
		if e.Type == "error" {
			errorCount++
		}
		if e.Type == "resource" && e.Resource != nil && e.Resource.Status == "deleted" {
			deleteCount++
		}
	}
	if errorCount != 1 {
		t.Errorf("expected 1 error event, got %d", errorCount)
	}
	if deleteCount < 2 {
		t.Errorf("expected at least 2 successful deletes, got %d", deleteCount)
	}
}
