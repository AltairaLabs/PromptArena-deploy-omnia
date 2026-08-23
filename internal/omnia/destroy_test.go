package omnia

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/AltairaLabs/promptarena/deploy"
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

	// Adapter-owned resources (PromptPack + AgentRuntime) are deleted; the
	// ToolRegistry is operator-owned and must be LEFT in place, with an advisory.
	deletedTypes := deletedResourceTypes(*events)
	if len(deletedTypes) != 2 {
		t.Errorf("expected 2 delete events (pack + runtime), got %d: %v", len(deletedTypes), deletedTypes)
	}
	if slices.Contains(deletedTypes, ResTypeToolRegistry) {
		t.Error("tool_registry is operator-owned and must NOT be deleted")
	}
	if !hasProgressContaining(*events, "operator-owned", ResTypeToolRegistry) {
		t.Error("expected a 'left ... (operator-owned)' advisory for the tool_registry")
	}

	// Verify destroy order via events: AgentRuntime delete before PromptPack
	// delete (reverse dependency order; PromptPack is last).
	runtimeIdx := slices.Index(deletedTypes, ResTypeAgentRuntime)
	promptPackIdx := slices.Index(deletedTypes, ResTypePromptPack)
	if runtimeIdx == -1 || promptPackIdx == -1 || runtimeIdx > promptPackIdx {
		t.Errorf("expected agent_runtime to be deleted before prompt_pack (runtime=%d promptpack=%d)",
			runtimeIdx, promptPackIdx)
	}
}

// deletedResourceTypes returns the resource types destroy issued a delete for,
// in the order the events arrived.
func deletedResourceTypes(events []*deploy.DestroyEvent) []string {
	var types []string
	for _, e := range events {
		if e.Type == "resource" && e.Resource != nil && e.Resource.Action == deploy.ActionDelete {
			types = append(types, e.Resource.Type)
		}
	}
	return types
}

// hasProgressContaining reports whether any progress event mentions every one
// of the given substrings.
func hasProgressContaining(events []*deploy.DestroyEvent, substrings ...string) bool {
	for _, e := range events {
		if e.Type != "progress" {
			continue
		}
		if containsAll(e.Message, substrings) {
			return true
		}
	}
	return false
}

func containsAll(s string, substrings []string) bool {
	for _, sub := range substrings {
		if !strings.Contains(s, sub) {
			return false
		}
	}
	return true
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
		{ResTypePromptPack, "test-pack"},
		{ResTypeToolRegistry, "test-pack-tools"},
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
			{Type: ResTypePromptPack, Name: "test-pack"},
			{Type: ResTypeToolRegistry, Name: "test-pack-tools"},
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
	// AgentRuntime deletes successfully; PromptPack fails; ToolRegistry is
	// operator-owned and left — so exactly one successful delete.
	if deleteCount != 1 {
		t.Errorf("expected 1 successful delete (agent_runtime), got %d", deleteCount)
	}
}
