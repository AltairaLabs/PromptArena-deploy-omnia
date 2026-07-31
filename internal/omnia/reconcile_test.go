package omnia

import (
	"context"
	"errors"
	"testing"
)

// fastReconcile zeroes the reconcile poll interval — and optionally caps the
// attempt count — for the duration of one test, restoring both afterwards.
//
// Restoring is the point. These are package vars and the integration tests live
// in the same package, so a bare assignment leaks into every later test in the
// binary. Running the package once with -tags=integration left every integration
// test failing "did not reach Ready within 2 polls" against a perfectly healthy
// cluster, which reads exactly like a broken adapter.
func fastReconcile(t *testing.T, maxAttempts ...int) {
	t.Helper()
	origInterval, origAttempts := reconcilePollInterval, reconcileMaxAttempts
	t.Cleanup(func() {
		reconcilePollInterval, reconcileMaxAttempts = origInterval, origAttempts
	})
	reconcilePollInterval = 0
	if len(maxAttempts) > 0 {
		reconcileMaxAttempts = maxAttempts[0]
	}
}

func TestWaitForReconcile_ReadyAfterPending(t *testing.T) {
	fastReconcile(t, 30)
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
	fastReconcile(t, 30)
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
	de := IsDeployError(err)
	if de == nil {
		t.Fatalf("want *DeployError, got %T", err)
	}
	// Assert the TERMINAL-failure path specifically: a resource-category reconcile
	// error, NOT a timeout. Without this, a regression that misclassified phase
	// Error as pending would loop to exhaustion and still return a (timeout)
	// DeployError, so the test would pass vacuously.
	if de.Category != ErrCategoryResource {
		t.Errorf("category = %q, want %q (terminal failure, not timeout)", de.Category, ErrCategoryResource)
	}
	if de.Operation != opReconcile {
		t.Errorf("operation = %q, want %q", de.Operation, opReconcile)
	}
}

func TestWaitForReconcile_TimesOut(t *testing.T) {
	fastReconcile(t, 3)
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
	fastReconcile(t, 30)
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
