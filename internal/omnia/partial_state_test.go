package omnia

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/AltairaLabs/promptarena/v2/deploy"
)

// priorStateForPartialApply is a stored state from an earlier deploy: the
// pack's resources, plus an older pack object and an agent this deploy no
// longer names, neither of which a later apply touches.
func priorStateForPartialApply(t *testing.T) string {
	t.Helper()
	prior := AdapterState{
		PackID: "test-pack", Version: "0.9.0",
		Resources: []ResourceState{
			{Type: ResTypePromptPack, Name: "test-pack-v0-9-0", UID: "old-pack-uid", Status: ResStatusCreated},
			{Type: ResTypePromptPack, Name: "test-pack", UID: "pack-uid", Status: ResStatusCreated},
			{Type: ResTypeToolRegistry, Name: "test-pack-tools", UID: "tools-uid", Status: ResStatusCreated},
			{Type: ResTypeAgentRuntime, Name: "test-pack", UID: "agent-uid", Status: ResStatusCreated},
			{Type: ResTypeAgentRuntime, Name: "retired-agent", UID: "retired-uid", Status: ResStatusCreated},
		},
	}
	b, err := json.Marshal(prior)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func parseState(t *testing.T, stateJSON string) AdapterState {
	t.Helper()
	var state AdapterState
	if err := json.Unmarshal([]byte(stateJSON), &state); err != nil {
		t.Fatalf("parse state %q: %v", stateJSON, err)
	}
	return state
}

func findResource(state AdapterState, resType, name string) (ResourceState, bool) {
	for _, r := range state.Resources {
		if r.Type == resType && r.Name == name {
			return r, true
		}
	}
	return ResourceState{}, false
}

func assertNoFailedResources(t *testing.T, state AdapterState) {
	t.Helper()
	for _, r := range state.Resources {
		if r.Status == ResStatusFailed {
			t.Errorf("a failed write must not be recorded as a resource: %+v", r)
		}
	}
}

// TestApply_PartialFailureKeepsUntouchedPriorResources pins the state a failed
// apply returns on the per-resource path: the CLI saves it in place of the
// stored state, so the prior resources the apply never reached must survive,
// a resource whose update failed keeps its prior entry, and what the apply
// wrote is recorded. The cluster holds what the prior state records, so the
// apply updates rather than creates.
func TestApply_PartialFailureKeepsUntouchedPriorResources(t *testing.T) {
	fastReconcile(t)
	sim := newSimulatedClient()
	sim.agentRuntimeReadyOnGet = true
	seedManagedResource(sim, ResTypePromptPack, "test-pack", "test-pack")
	seedManagedResource(sim, ResTypeToolRegistry, "test-pack-tools", "test-pack")
	seedManagedResource(sim, ResTypeAgentRuntime, "test-pack", "test-pack")
	seedManagedResource(sim, ResTypeAgentRuntime, "retired-agent", "test-pack")
	sim.failOn[resourceKey(ResTypeAgentRuntime, "test-pack")] = fmt.Errorf("simulated API failure")
	p := &Provider{clientFunc: newSimulatedClientFactory(sim)}

	stateJSON, err := p.Apply(context.Background(), &deploy.PlanRequest{
		PackJSON:     testPackJSON,
		DeployConfig: testDeployConfig,
		PriorState:   priorStateForPartialApply(t),
	}, noopApplyCallback)
	if err == nil {
		t.Fatal("expected the AgentRuntime failure to fail the apply")
	}

	state := parseState(t, stateJSON)
	assertNoFailedResources(t, state)
	for _, want := range []struct{ resType, name, uid string }{
		{ResTypePromptPack, "test-pack-v0-9-0", "old-pack-uid"},
		{ResTypeToolRegistry, "test-pack-tools", "tools-uid"},
		{ResTypeAgentRuntime, "test-pack", "agent-uid"},
		{ResTypeAgentRuntime, "retired-agent", "retired-uid"},
	} {
		got, ok := findResource(state, want.resType, want.name)
		if !ok {
			t.Errorf("prior %s %q was dropped from the state; got %+v", want.resType, want.name, state.Resources)
			continue
		}
		if got.UID != want.uid {
			t.Errorf("%s %q UID = %q, want the prior entry's %q", want.resType, want.name, got.UID, want.uid)
		}
	}
	if got, _ := findResource(state, ResTypePromptPack, "test-pack"); got.Status != ResStatusUpdated || got.UID == "pack-uid" {
		t.Errorf("the PromptPack this apply updated must replace its prior entry, got %+v", got)
	}
	if len(state.Resources) != 5 {
		t.Errorf("want the 5 prior resources, got %+v", state.Resources)
	}
}

// TestApply_FailedUpdateOfAnAdoptedResourceIsKept covers a stored state that
// does not record a resource the cluster has: apply adopts it and updates it,
// and when that update fails the resource still exists, so the state records
// it rather than dropping it.
func TestApply_FailedUpdateOfAnAdoptedResourceIsKept(t *testing.T) {
	fastReconcile(t)
	sim := newSimulatedClient()
	sim.agentRuntimeReadyOnGet = true
	seedManagedResource(sim, ResTypeAgentRuntime, "test-pack", "test-pack")
	sim.failOn[resourceKey(ResTypeAgentRuntime, "test-pack")] = fmt.Errorf("simulated API failure")
	p := &Provider{clientFunc: newSimulatedClientFactory(sim)}

	stateJSON, err := p.Apply(context.Background(), &deploy.PlanRequest{
		PackJSON: testPackJSON, DeployConfig: testDeployConfig,
	}, noopApplyCallback)
	if err == nil {
		t.Fatal("expected the AgentRuntime failure to fail the apply")
	}

	state := parseState(t, stateJSON)
	assertNoFailedResources(t, state)
	if got, ok := findResource(state, ResTypeAgentRuntime, "test-pack"); !ok || got.Status != ResStatusUnchanged {
		t.Errorf("the adopted AgentRuntime whose update failed must be recorded as unchanged, got %+v", state.Resources)
	}
}

// TestApply_FailedUpdateAfterAlreadyExistsIsKept covers a create that answers
// AlreadyExists and an update that then fails: the answer proves the resource
// exists, so the state records it.
func TestApply_FailedUpdateAfterAlreadyExistsIsKept(t *testing.T) {
	fastReconcile(t)
	prevBackoff := updateConflictBackoff
	updateConflictBackoff = 0
	t.Cleanup(func() { updateConflictBackoff = prevBackoff })

	sim := newSimulatedClient()
	sim.agentRuntimeReadyOnGet = true
	sim.createAlreadyExists = map[string]bool{simKey(ResTypePromptPack, "test-pack"): true}
	// Every update attempt on the PromptPack, the first one made, conflicts.
	sim.updateConflictsRemaining = updateConflictRetries + 1
	p := &Provider{clientFunc: newSimulatedClientFactory(sim)}

	stateJSON, err := p.Apply(context.Background(), &deploy.PlanRequest{
		PackJSON: testPackJSON, DeployConfig: testDeployConfig,
	}, noopApplyCallback)
	if err == nil {
		t.Fatal("expected the PromptPack update failure to fail the apply")
	}

	state := parseState(t, stateJSON)
	assertNoFailedResources(t, state)
	if got, ok := findResource(state, ResTypePromptPack, "test-pack"); !ok || got.Status != ResStatusUnchanged {
		t.Errorf("the PromptPack that answered AlreadyExists must be recorded, got %+v", state.Resources)
	}
	if got, _ := findResource(state, ResTypeAgentRuntime, "test-pack"); got.Status != ResStatusCreated {
		t.Errorf("the AgentRuntime created after it must be recorded, got %+v", state.Resources)
	}
}

// TestApply_IntentPartialFailureKeepsUntouchedPriorResources is the same
// guarantee on the deploy-intent path, where the server reports one outcome
// per object.
func TestApply_IntentPartialFailureKeepsUntouchedPriorResources(t *testing.T) {
	sim := newSimulatedClient()
	sim.intentEnabled = true
	packObject := promptPackObjectName("test-pack", "1.0.0")
	sim.deployResult = &DeployResult{
		Results: []DeployResourceResult{
			{Kind: "PromptPack", Name: packObject, Action: intentActionCreated},
			{Kind: "AgentRuntime", Name: "test-pack", Action: intentActionFailed, Error: "rejected"},
		},
	}
	p := &Provider{clientFunc: newSimulatedClientFactory(sim)}

	stateJSON, err := p.Apply(context.Background(), &deploy.PlanRequest{
		PackJSON:     testPackJSON,
		DeployConfig: testDeployConfig,
		PriorState:   priorStateForPartialApply(t),
	}, noopApplyCallback)
	if err == nil {
		t.Fatal("expected the partly-failed deploy to fail the apply")
	}

	state := parseState(t, stateJSON)
	assertNoFailedResources(t, state)
	if _, ok := findResource(state, ResTypePromptPack, packObject); !ok {
		t.Errorf("the created %q must be recorded, got %+v", packObject, state.Resources)
	}
	if agent, _ := findResource(state, ResTypeAgentRuntime, "test-pack"); agent.UID != "agent-uid" {
		t.Errorf("the AgentRuntime whose write failed must keep its prior entry, got %+v", agent)
	}
	if _, ok := findResource(state, ResTypeAgentRuntime, "retired-agent"); !ok {
		t.Errorf("the untouched prior agent was dropped, got %+v", state.Resources)
	}
	if len(state.Resources) != 6 {
		t.Errorf("want the 5 prior resources plus the new pack object, got %+v", state.Resources)
	}
}

// TestApply_FailureBeforeAnyWriteReturnsPriorStateUnchanged pins the case
// where the apply fails without writing anything: it returns the prior state
// verbatim, which the CLI recognizes and keeps.
func TestApply_FailureBeforeAnyWriteReturnsPriorStateUnchanged(t *testing.T) {
	cases := map[string]func(*simulatedClient){
		"deploy-intent call fails": func(sim *simulatedClient) {
			sim.intentEnabled = true
			sim.deployErr = &HTTPError{StatusCode: 500, Body: "internal error"}
		},
		"every per-resource write fails": func(sim *simulatedClient) {
			for _, key := range []string{
				resourceKey(ResTypePromptPack, "test-pack"),
				resourceKey(ResTypeToolRegistry, "test-pack-tools"),
				resourceKey(ResTypeAgentRuntime, "test-pack"),
			} {
				sim.failOn[key] = fmt.Errorf("simulated API failure")
			}
		},
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			sim := newSimulatedClient()
			setup(sim)
			p := &Provider{clientFunc: newSimulatedClientFactory(sim)}
			prior := priorStateForPartialApply(t)

			stateJSON, err := p.Apply(context.Background(), &deploy.PlanRequest{
				PackJSON:     testPackJSON,
				DeployConfig: testDeployConfig,
				PriorState:   prior,
			}, noopApplyCallback)
			if err == nil {
				t.Fatal("expected the apply to fail")
			}
			if stateJSON != prior {
				t.Errorf("state = %s, want the prior state unchanged: %s", stateJSON, prior)
			}
		})
	}
}

// TestApply_FreshDeployPartialFailureReturnsWhatItCreated covers a first
// deploy that fails partway: there is no prior state, so the state is exactly
// the resources the apply created, without the one whose create failed.
func TestApply_FreshDeployPartialFailureReturnsWhatItCreated(t *testing.T) {
	sim := newSimulatedClient()
	sim.failOn[resourceKey(ResTypeAgentRuntime, "test-pack")] = fmt.Errorf("simulated API failure")
	p := &Provider{clientFunc: newSimulatedClientFactory(sim)}

	stateJSON, err := p.Apply(context.Background(), &deploy.PlanRequest{
		PackJSON:     testPackJSON,
		DeployConfig: testDeployConfig,
	}, noopApplyCallback)
	if err == nil {
		t.Fatal("expected the AgentRuntime failure to fail the apply")
	}

	state := parseState(t, stateJSON)
	if state.PackID != "test-pack" || state.Version != "1.0.0" {
		t.Errorf("state pack = %q@%q, want test-pack@1.0.0", state.PackID, state.Version)
	}
	if len(state.Resources) != 2 {
		t.Fatalf("want the PromptPack and ToolRegistry, got %+v", state.Resources)
	}
	for _, want := range []struct{ resType, name string }{
		{ResTypePromptPack, "test-pack"},
		{ResTypeToolRegistry, "test-pack-tools"},
	} {
		got, ok := findResource(state, want.resType, want.name)
		if !ok || got.Status != ResStatusCreated || got.UID == "" {
			t.Errorf("want %s %q recorded as created with its UID, got %+v", want.resType, want.name, got)
		}
	}
}

func TestMergePartialResources(t *testing.T) {
	prior := []ResourceState{
		{Type: ResTypePromptPack, Name: "p", UID: "p0"},
		{Type: ResTypeToolRegistry, Name: "r", UID: "r0"},
		{Type: ResTypeAgentRuntime, Name: "a", UID: "a0"},
		{Type: ResTypeAgentRuntime, Name: "a", UID: "duplicate"},
		{Type: ResTypeAgentRuntime, Name: "dry-run", Status: ResStatusPlanned},
		{Type: ResTypeAgentRuntime, Name: "old-failure", Status: ResStatusFailed},
	}
	touched := []ResourceState{
		{Type: ResTypePromptPack, Name: "p", UID: "p1", Status: ResStatusUpdated},
		{Type: ResTypeToolRegistry, Name: "r", Status: ResStatusUnchanged},
		{Type: ResTypeAgentRuntime, Name: "a", Status: ResStatusFailed},
		{Type: ResTypeAgentRuntime, Name: "new", UID: "n1", Status: ResStatusCreated},
		{Type: ResTypeAgentPolicy, Name: "never-created", Status: ResStatusFailed},
	}

	existing := map[string]ResourceState{
		resourceKey(ResTypeAgentPolicy, "adopted"): {Type: ResTypeAgentPolicy, Name: "adopted"},
	}
	touched = append(touched, ResourceState{Type: ResTypeAgentPolicy, Name: "adopted", Status: ResStatusFailed})

	merged, changed := mergePartialResources(prior, existing, touched)
	if !changed {
		t.Error("an update and a create were reported; changed must be true")
	}
	want := []string{"p1", "r0", "a0", "n1", ""}
	if len(merged) != len(want) {
		t.Fatalf("merged = %+v, want UIDs %v", merged, want)
	}
	for i, uid := range want {
		if merged[i].UID != uid {
			t.Errorf("merged[%d].UID = %q, want %q (merged %+v)", i, merged[i].UID, uid, merged)
		}
	}

	if last := merged[len(merged)-1]; last.Name != "adopted" || last.Status != ResStatusUnchanged {
		t.Errorf("a failed write to an existing resource must be recorded as unchanged, got %+v", last)
	}

	if _, changed := mergePartialResources(prior, existing, []ResourceState{
		{Type: ResTypePromptPack, Name: "p", Status: ResStatusUnchanged},
		{Type: ResTypeAgentRuntime, Name: "a", Status: ResStatusFailed},
	}); changed {
		t.Error("nothing new was learned; changed must be false")
	}
	if _, changed := mergePartialResources(prior, nil, []ResourceState{
		{Type: ResTypeToolRegistry, Name: "adopted-registry", Status: ResStatusUnchanged},
	}); !changed {
		t.Error("an unchanged resource the prior state lacks is new; changed must be true")
	}
}

func TestPartialApplyState_UnreadablePriorIsReturnedUnchanged(t *testing.T) {
	stateJSON, err := partialApplyState("not json", nil, []ResourceState{
		{Type: ResTypePromptPack, Name: "p", UID: "p1", Status: ResStatusCreated},
	}, "pack", "1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	if stateJSON != "not json" {
		t.Errorf("state = %q, want the unreadable prior state unchanged, so the CLI keeps it", stateJSON)
	}
}
