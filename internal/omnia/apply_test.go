package omnia

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/AltairaLabs/PromptKit/runtime/deploy"
)

func noopApplyCallback(_ *deploy.ApplyEvent) error { return nil }

func TestUpdateWithRetry_RetriesOnConflict(t *testing.T) {
	orig := updateConflictBackoff
	updateConflictBackoff = 0
	defer func() { updateConflictBackoff = orig }()

	sim := newSimulatedClient()
	sim.resources[simKey(ResTypePromptPack, "p")] =
		&ResourceResponse{Metadata: ResourceMetadata{Name: "p", ResourceVersion: "1"}}
	sim.updateConflictsRemaining = 2 // conflict twice, then succeed

	resp, err := updateWithRetry(context.Background(), sim, ResTypePromptPack, "p", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("expected success after retries, got %v", err)
	}
	if resp == nil || sim.updateConflictsRemaining != 0 {
		t.Errorf("expected both conflicts consumed, remaining=%d", sim.updateConflictsRemaining)
	}
}

func TestUpdateWithRetry_GivesUpAfterMax(t *testing.T) {
	orig := updateConflictBackoff
	updateConflictBackoff = 0
	defer func() { updateConflictBackoff = orig }()

	sim := newSimulatedClient()
	sim.resources[simKey(ResTypePromptPack, "p")] = &ResourceResponse{Metadata: ResourceMetadata{Name: "p"}}
	sim.updateConflictsRemaining = 99 // always conflicts

	if _, err := updateWithRetry(context.Background(), sim, ResTypePromptPack, "p", json.RawMessage(`{}`)); err == nil {
		t.Fatal("expected error after exhausting retries")
	}
}

func TestIsRetryableConflict(t *testing.T) {
	if !isRetryableConflict(&HTTPError{StatusCode: 409, Body: `{"reason":"Conflict"}`}) {
		t.Error("Conflict (409) should be retryable")
	}
	if isRetryableConflict(&HTTPError{StatusCode: 409, Body: `{"reason":"AlreadyExists","message":"already exists"}`}) {
		t.Error("AlreadyExists (409) must NOT be retried")
	}
	if isRetryableConflict(fmt.Errorf("transport boom")) {
		t.Error("non-HTTP error must not be retryable")
	}
}

// capturingCallback records every ApplyEvent so tests can assert on emitted
// progress/resource messages.
func capturingCallback(events *[]*deploy.ApplyEvent) deploy.ApplyCallback {
	return func(e *deploy.ApplyEvent) error {
		*events = append(*events, e)
		return nil
	}
}

// progressMessages returns the messages of all progress events.
func progressMessages(events []*deploy.ApplyEvent) []string {
	var msgs []string
	for _, e := range events {
		if e.Type == "progress" {
			msgs = append(msgs, e.Message)
		}
	}
	return msgs
}

// countContaining counts how many strings contain the given substring.
func countContaining(strs []string, substr string) int {
	n := 0
	for _, s := range strs {
		if strings.Contains(s, substr) {
			n++
		}
	}
	return n
}

// multiAgentPackJSON is a pack with two agent members (alice, bob).
const multiAgentPackJSON = `{
	"id": "multi-pack",
	"version": "1.0.0",
	"description": "Multi-agent test pack",
	"prompts": {
		"alice": {"system": "You are Alice", "description": "Alice"},
		"bob": {"system": "You are Bob", "description": "Bob"}
	},
	"agents": {
		"entry": "alice",
		"members": {
			"alice": {"description": "Alice agent"},
			"bob": {"description": "Bob agent"}
		}
	}
}`

func TestApply_EmitsAccessURL(t *testing.T) {
	reconcilePollInterval = 0
	sim := newSimulatedClient()
	sim.agentRuntimeReadyOnGet = true
	p := &Provider{clientFunc: newSimulatedClientFactory(sim)}

	var events []*deploy.ApplyEvent
	if _, err := p.Apply(context.Background(), &deploy.PlanRequest{
		PackJSON:     testPackJSON,
		DeployConfig: testDeployConfig,
	}, capturingCallback(&events)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Exactly one access-URL message for the single AgentRuntime.
	msgs := progressMessages(events)
	want := "https://omnia.test.com/agents/test-pack?workspace=test-ws"
	if got := countContaining(msgs, want); got != 1 {
		t.Errorf("expected 1 access-URL message containing %q, got %d (messages: %v)", want, got, msgs)
	}
}

func TestApply_AccessURL_MultiAgent(t *testing.T) {
	reconcilePollInterval = 0
	sim := newSimulatedClient()
	sim.agentRuntimeReadyOnGet = true
	p := &Provider{clientFunc: newSimulatedClientFactory(sim)}

	cfg := `{
		"api_endpoint": "https://omnia.test.com/",
		"workspace": "test-ws",
		"api_token": "test-token",
		"providers": {"default": "claude-prod"}
	}`

	var events []*deploy.ApplyEvent
	if _, err := p.Apply(context.Background(), &deploy.PlanRequest{
		PackJSON:     multiAgentPackJSON,
		DeployConfig: cfg,
	}, capturingCallback(&events)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	msgs := progressMessages(events)
	for _, name := range []string{"alice", "bob"} {
		want := "https://omnia.test.com/agents/" + name + "?workspace=test-ws"
		if got := countContaining(msgs, want); got != 1 {
			t.Errorf("expected 1 access-URL message containing %q, got %d (messages: %v)", want, got, msgs)
		}
	}
}

func TestAgentRuntimeSucceeded(t *testing.T) {
	tests := []struct {
		name string
		res  []ResourceState
		want bool
	}{
		{name: "created", res: []ResourceState{{Type: ResTypeAgentRuntime, Status: ResStatusCreated}}, want: true},
		{name: "updated", res: []ResourceState{{Type: ResTypeAgentRuntime, Status: ResStatusUpdated}}, want: true},
		{name: "failed", res: []ResourceState{{Type: ResTypeAgentRuntime, Status: ResStatusFailed}}, want: false},
		{name: "wrong type", res: []ResourceState{{Type: ResTypePromptPack, Status: ResStatusCreated}}, want: false},
		{name: "empty", res: nil, want: false},
		{
			name: "mixed with success",
			res: []ResourceState{
				{Type: ResTypePromptPack, Status: ResStatusCreated},
				{Type: ResTypeAgentRuntime, Status: ResStatusUpdated},
			},
			want: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := agentRuntimeSucceeded(tt.res); got != tt.want {
				t.Errorf("agentRuntimeSucceeded() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestApply_NoAccessURL_OnFailure(t *testing.T) {
	sim := newSimulatedClient()
	// Fail the AgentRuntime create so no access URL should be emitted for it.
	sim.failOn[resourceKey(ResTypeAgentRuntime, "test-pack")] = fmt.Errorf("simulated failure")
	p := &Provider{clientFunc: newSimulatedClientFactory(sim)}

	var events []*deploy.ApplyEvent
	if _, err := p.Apply(context.Background(), &deploy.PlanRequest{
		PackJSON:     testPackJSON,
		DeployConfig: testDeployConfig,
	}, capturingCallback(&events)); err == nil {
		t.Fatal("expected error due to AgentRuntime failure")
	}

	msgs := progressMessages(events)
	if got := countContaining(msgs, "/agents/test-pack?workspace="); got != 0 {
		t.Errorf("expected no access-URL message on failure, got %d (messages: %v)", got, msgs)
	}
}

func TestApply_SingleAgent(t *testing.T) {
	reconcilePollInterval = 0
	sim := newSimulatedClient()
	sim.agentRuntimeReadyOnGet = true
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
	for _, expected := range []string{ResTypePromptPack, ResTypeToolRegistry, ResTypeAgentRuntime} {
		if !typeSet[expected] {
			t.Errorf("expected resource type %q in state", expected)
		}
	}
}

func TestApply_MultiPromptFanOut_SharedPackNRuntimes(t *testing.T) {
	reconcilePollInterval = 0
	sim := newSimulatedClient()
	sim.agentRuntimeReadyOnGet = true
	p := &Provider{clientFunc: newSimulatedClientFactory(sim)}

	cfg := `{
		"api_endpoint": "https://omnia.test.com",
		"workspace": "test-ws",
		"api_token": "test-token",
		"providers": {"default": "claude-prod"}
	}`

	stateJSON, err := p.Apply(context.Background(), &deploy.PlanRequest{
		PackJSON:     testMultiPromptPackJSON, // plain pack, prompts: billing, triage
		DeployConfig: cfg,
	}, noopApplyCallback)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var state AdapterState
	if err := json.Unmarshal([]byte(stateJSON), &state); err != nil {
		t.Fatalf("failed to parse state: %v", err)
	}

	packs := 0
	runtimes := map[string]bool{}
	for _, r := range state.Resources {
		switch r.Type {
		case ResTypePromptPack:
			packs++
		case ResTypeAgentRuntime:
			runtimes[r.Name] = true
		}
	}
	// One shared PromptPack, one AgentRuntime per prompt.
	if packs != 1 {
		t.Errorf("expected exactly 1 shared PromptPack, got %d", packs)
	}
	if len(runtimes) != 2 || !runtimes["splitz-billing"] || !runtimes["splitz-triage"] {
		t.Errorf("expected per-prompt runtimes splitz-billing/splitz-triage, got %v", runtimes)
	}
}

func TestApply_BindMode_BindsRegistryWithoutCreating(t *testing.T) {
	reconcilePollInterval = 0
	sim := newSimulatedClient()
	sim.agentRuntimeReadyOnGet = true
	// Registry exists but doesn't provide the pack's "search" tool. Apply no
	// longer echoes the resolver advisory (the CLI surfaces the plan's warnings);
	// what matters here is that bind mode binds without creating a registry.
	sim.toolRegistries = []ToolRegistrySummary{{Name: "shared-tools"}}
	p := &Provider{clientFunc: newSimulatedClientFactory(sim)}

	cfg := `{
		"api_endpoint": "https://omnia.test.com",
		"workspace": "test-ws",
		"api_token": "test-token",
		"providers": {"default": "claude-prod"},
		"tool_registry_ref": "shared-tools"
	}`

	var events []*deploy.ApplyEvent
	stateJSON, err := p.Apply(context.Background(), &deploy.PlanRequest{
		PackJSON: testPackJSON, DeployConfig: cfg,
	}, capturingCallback(&events))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Bind mode must not create a ToolRegistry resource.
	var state AdapterState
	if err := json.Unmarshal([]byte(stateJSON), &state); err != nil {
		t.Fatalf("failed to parse state: %v", err)
	}
	for _, r := range state.Resources {
		if r.Type == ResTypeToolRegistry {
			t.Errorf("bind mode must not create a ToolRegistry, got %+v", r)
		}
	}
	// The AgentRuntime must still be bound to the named registry (verified via
	// the stored body on the simulated client).
	ar := sim.resources[simKey(ResTypeAgentRuntime, "test-pack")]
	if ar == nil {
		t.Fatal("expected AgentRuntime to be created")
	}
	if !strings.Contains(string(ar.Spec), "shared-tools") {
		t.Errorf("expected AgentRuntime spec to bind shared-tools, got %s", ar.Spec)
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
	reconcilePollInterval = 0
	sim := newSimulatedClient()
	sim.agentRuntimeReadyOnGet = true
	// Pre-populate the simulated client with existing LABELED resources so adopt
	// reconciles them as the prior state (the cluster is the source of truth) and
	// apply updates rather than creates — req.PriorState is intentionally empty.
	seedManagedResource(sim, ResTypePromptPack, "test-pack", "test-pack")
	seedManagedResource(sim, ResTypeToolRegistry, "test-pack-tools", "test-pack")
	seedManagedResource(sim, ResTypeAgentRuntime, "test-pack", "test-pack")
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

	// All resources existed before, so they update — EXCEPT the tool_registry,
	// which is CREATE-ONLY and is left unchanged (operator-owned) once it exists.
	for _, r := range state.Resources {
		want := ResStatusUpdated
		if r.Type == ResTypeToolRegistry {
			want = ResStatusUnchanged
		}
		if r.Status != want {
			t.Errorf("expected status %q for %s %q, got %q", want, r.Type, r.Name, r.Status)
		}
	}
}

func TestApply_FailsWhenAgentRuntimeNeverReconciles(t *testing.T) {
	reconcilePollInterval = 0
	reconcileMaxAttempts = 2

	sim := newSimulatedClient()
	// agentRuntimeReadyOnGet is intentionally left false: the created AgentRuntime
	// reads back with a nil status (pending) and never reaches Ready.
	p := &Provider{clientFunc: newSimulatedClientFactory(sim)}

	_, err := p.Apply(context.Background(), &deploy.PlanRequest{
		PackJSON:     testPackJSON,
		DeployConfig: testDeployConfig,
	}, noopApplyCallback)
	if err == nil {
		t.Fatal("expected Apply to fail on a non-reconciling AgentRuntime")
	}
	if de := IsDeployError(err); de == nil || de.Operation != opReconcile {
		t.Errorf("want a reconcile DeployError, got %v", err)
	}
}

func TestApply_ResourceFailure(t *testing.T) {
	reconcilePollInterval = 0
	sim := newSimulatedClient()
	// The AgentRuntime itself succeeds despite the ToolRegistry failure below, so
	// it goes through reconcile — make it reconcile immediately so the only
	// resource failure this test observes is the injected ToolRegistry one.
	sim.agentRuntimeReadyOnGet = true
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
