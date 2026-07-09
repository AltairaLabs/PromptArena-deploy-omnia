package omnia

import (
	"context"
	"errors"
	"testing"
)

func TestWaitForReconcile_ReadyAfterPending(t *testing.T) {
	reconcilePollInterval = 0
	reconcileMaxAttempts = 30
	sim := newSimulatedClient()
	name := "agent-x"
	sim.resources[simKey(ResTypeAgentRuntime, name)] = &ResourceResponse{
		Kind: ResTypeAgentRuntime, Metadata: ResourceMetadata{Name: name},
	}
	sim.statusQueue = map[string][]*ResourceStatus{
		simKey(ResTypeAgentRuntime, name): {
			{Phase: "Pending"},
			{Conditions: []ResourceCondition{{Type: "Ready", Status: "True"}}},
		},
	}
	if err := waitForReconcile(context.Background(), sim, ResTypeAgentRuntime, name); err != nil {
		t.Fatalf("expected reconcile success, got %v", err)
	}
}

func TestWaitForReconcile_TerminalFailure(t *testing.T) {
	reconcilePollInterval = 0
	reconcileMaxAttempts = 30
	sim := newSimulatedClient()
	name := "agent-bad"
	sim.resources[simKey(ResTypeAgentRuntime, name)] = &ResourceResponse{
		Kind: ResTypeAgentRuntime, Metadata: ResourceMetadata{Name: name},
		Status: &ResourceStatus{Phase: "Error"},
	}
	err := waitForReconcile(context.Background(), sim, ResTypeAgentRuntime, name)
	if err == nil {
		t.Fatal("expected a failure error for phase Error")
	}
	if de := IsDeployError(err); de == nil {
		t.Errorf("want *DeployError, got %T", err)
	}
}

func TestWaitForReconcile_TimesOut(t *testing.T) {
	reconcilePollInterval = 0
	reconcileMaxAttempts = 3
	sim := newSimulatedClient()
	name := "agent-silent"
	// Never gets a Ready condition and stays on an empty phase — the "created but
	// silent" case (e.g. a CRD schema the operator won't reconcile).
	sim.resources[simKey(ResTypeAgentRuntime, name)] = &ResourceResponse{
		Kind: ResTypeAgentRuntime, Metadata: ResourceMetadata{Name: name},
		Status: &ResourceStatus{Phase: ""},
	}
	err := waitForReconcile(context.Background(), sim, ResTypeAgentRuntime, name)
	if err == nil || IsDeployError(err) == nil {
		t.Fatalf("expected a timeout DeployError, got %v", err)
	}
	if IsDeployError(err).Category != ErrCategoryTimeout {
		t.Errorf("category = %q, want timeout", IsDeployError(err).Category)
	}
}

func TestWaitForReconcile_GetResourceError(t *testing.T) {
	reconcilePollInterval = 0
	reconcileMaxAttempts = 30
	sim := newSimulatedClient()
	name := "agent-unreadable"
	sim.resources[simKey(ResTypeAgentRuntime, name)] = &ResourceResponse{
		Kind: ResTypeAgentRuntime, Metadata: ResourceMetadata{Name: name},
	}
	sim.failOn[simKey(ResTypeAgentRuntime, name)] = errors.New("transport failure")

	err := waitForReconcile(context.Background(), sim, ResTypeAgentRuntime, name)
	de := IsDeployError(err)
	if de == nil {
		t.Fatalf("expected a resource-category DeployError, got %v", err)
	}
	if de.Category != ErrCategoryResource {
		t.Errorf("category = %q, want resource", de.Category)
	}
}
