package omnia

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/AltairaLabs/PromptKit/runtime/deploy"
	"github.com/AltairaLabs/PromptKit/runtime/deploy/adaptersdk"
)

// seedLabeledResource stores a resource in the simulated client with arbitrary
// labels (used to verify the client-side defensive double-check).
func seedLabeledResource(sim *simulatedClient, resType, name string, labels map[string]string) {
	sim.mu.Lock()
	defer sim.mu.Unlock()
	sim.resources[simKey(resType, name)] = &ResourceResponse{
		Kind: resType,
		Metadata: ResourceMetadata{
			Name:   name,
			UID:    "uid-" + name,
			Labels: labels,
		},
	}
}

func TestAdoptPriorState_AdoptsOnlyOurPackResources(t *testing.T) {
	sim := newSimulatedClient()
	p := &Provider{clientFunc: newSimulatedClientFactory(sim)}
	pack, err := adaptersdk.ParsePack([]byte(testPackJSON))
	if err != nil {
		t.Fatalf("parse pack: %v", err)
	}
	cfg, err := parseConfig(testDeployConfig)
	if err != nil {
		t.Fatalf("parse config: %v", err)
	}

	// Ours, for this pack — must be adopted.
	seedManagedResource(sim, ResTypePromptPack, "test-pack", "test-pack")
	seedManagedResource(sim, ResTypeAgentRuntime, "test-pack", "test-pack")
	// A different pack's resource (carries our managed-by but a different pack-id)
	// — must NOT be adopted.
	seedLabeledResource(sim, ResTypePromptPack, "other-pack", map[string]string{
		LabelManagedBy: managedByValue,
		LabelPackID:    "other-pack",
	})
	// A hand-made resource (no managed-by) for THIS pack-id — must NOT be adopted.
	seedLabeledResource(sim, ResTypeToolRegistry, "splitpantz-api", map[string]string{
		LabelPackID: "test-pack",
	})

	adopted, err := p.adoptPriorState(context.Background(), pack, cfg)
	if err != nil {
		t.Fatalf("adopt: %v", err)
	}

	got := make(map[string]bool, len(adopted))
	for _, r := range adopted {
		got[resourceKey(r.Type, r.Name)] = true
	}
	if len(adopted) != 2 {
		t.Fatalf("expected exactly 2 adopted resources, got %d: %+v", len(adopted), adopted)
	}
	if !got[resourceKey(ResTypePromptPack, "test-pack")] ||
		!got[resourceKey(ResTypeAgentRuntime, "test-pack")] {
		t.Errorf("missing our pack's resources, got %+v", adopted)
	}
	if got[resourceKey(ResTypePromptPack, "other-pack")] {
		t.Error("must not adopt another pack's resource")
	}
	if got[resourceKey(ResTypeToolRegistry, "splitpantz-api")] {
		t.Error("must not adopt a hand-made (unmanaged) resource")
	}
}

func TestAdoptPriorState_ListErrorPropagates(t *testing.T) {
	sim := newSimulatedClient()
	// A list error for any resType (keyed by name "") propagates so the caller
	// can fall back to the local state file.
	sim.failOn[simKey(ResTypeToolRegistry, "")] = fmt.Errorf("list forbidden")
	p := &Provider{clientFunc: newSimulatedClientFactory(sim)}
	pack, _ := adaptersdk.ParsePack([]byte(testPackJSON))
	cfg, _ := parseConfig(testDeployConfig)

	if _, err := p.adoptPriorState(context.Background(), pack, cfg); err == nil {
		t.Fatal("expected list error to propagate")
	}
}

func TestOwnedByThisPack(t *testing.T) {
	if !ownedByThisPack(map[string]string{
		LabelManagedBy: managedByValue, LabelPackID: "p",
	}, "p") {
		t.Error("our resource for this pack should be owned")
	}
	if ownedByThisPack(map[string]string{LabelPackID: "p"}, "p") {
		t.Error("missing managed-by must not be owned")
	}
	if ownedByThisPack(map[string]string{
		LabelManagedBy: managedByValue, LabelPackID: "other",
	}, "p") {
		t.Error("different pack-id must not be owned")
	}
	if ownedByThisPack(nil, "p") {
		t.Error("nil labels must not be owned")
	}
}

func TestPlan_AdoptSupersedesEmptyPriorState_ShowsUpdate(t *testing.T) {
	// The cluster has our resource but req.PriorState is empty: adopt makes plan
	// show UPDATE, not CREATE.
	p, sim := newPlanTestProvider()
	seedManagedResource(sim, ResTypePromptPack, "test-pack", "test-pack")

	resp, err := p.Plan(context.Background(), &deploy.PlanRequest{
		PackJSON:     testPackJSON,
		DeployConfig: testDeployConfig,
	})
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	for _, c := range resp.Changes {
		if c.Type == ResTypePromptPack && c.Name == "test-pack" {
			if c.Action != deploy.ActionUpdate {
				t.Errorf("adopted resource should plan as UPDATE, got %s", c.Action)
			}
		}
	}
}

func TestPlan_AdoptError_FallsBackToPriorState(t *testing.T) {
	// Adoption fails (list error) → plan falls back to req.PriorState. The prior
	// state names a resource not in the desired set, so it must plan as DELETE.
	p, sim := newPlanTestProvider()
	sim.failOn[simKey(ResTypePromptPack, "")] = fmt.Errorf("list forbidden")

	prior := AdapterState{
		Resources: []ResourceState{
			{Type: ResTypeAgentPolicy, Name: "test-pack-policy"},
		},
		PackID: "test-pack",
	}
	priorJSON, _ := json.Marshal(prior)

	resp, err := p.Plan(context.Background(), &deploy.PlanRequest{
		PackJSON:     testPackJSON,
		DeployConfig: testDeployConfig,
		PriorState:   string(priorJSON),
	})
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	var deletes int
	for _, c := range resp.Changes {
		if c.Action == deploy.ActionDelete && c.Type == ResTypeAgentPolicy {
			deletes++
		}
	}
	if deletes != 1 {
		t.Errorf("expected fallback prior-state DELETE, got %d deletes in %+v", deletes, resp.Changes)
	}
}

func TestApply_AdoptError_FallsBackToPriorState(t *testing.T) {
	sim := newSimulatedClient()
	// Make adopt fail on the first resType so apply falls back to req.PriorState.
	sim.failOn[simKey(ResTypePromptPack, "")] = fmt.Errorf("list forbidden")
	// Pre-seed the resources so the fallback's UPDATE path succeeds.
	for _, r := range []struct{ typ, name string }{
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

	prior := AdapterState{
		Resources: []ResourceState{
			{Type: ResTypePromptPack, Name: "test-pack"},
			{Type: ResTypeToolRegistry, Name: "test-pack-tools"},
			{Type: ResTypeAgentRuntime, Name: "test-pack"},
		},
		PackID: "test-pack",
	}
	priorJSON, _ := json.Marshal(prior)

	stateJSON, err := p.Apply(context.Background(), &deploy.PlanRequest{
		PackJSON:     testPackJSON,
		DeployConfig: testDeployConfig,
		PriorState:   string(priorJSON),
	}, noopApplyCallback)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	var state AdapterState
	if err := json.Unmarshal([]byte(stateJSON), &state); err != nil {
		t.Fatalf("parse state: %v", err)
	}
	// Falling back to prior state, all three are known → UPDATED, except the
	// tool_registry which is CREATE-ONLY: a known (existing) registry is left
	// unchanged, never updated, even on the prior-state fallback path.
	for _, r := range state.Resources {
		want := ResStatusUpdated
		if r.Type == ResTypeToolRegistry {
			want = ResStatusUnchanged
		}
		if r.Status != want {
			t.Errorf("expected %s %q to be %q via fallback, got %q", r.Type, r.Name, want, r.Status)
		}
	}
}

func TestApply_CreateAlreadyExists_FallsBackToUpdate(t *testing.T) {
	orig := updateConflictBackoff
	updateConflictBackoff = 0
	defer func() { updateConflictBackoff = orig }()

	sim := newSimulatedClient()
	// adopt returns empty (nothing labeled) → apply decides CREATE for the
	// PromptPack. But the cluster actually has it: CreateResource returns 409
	// AlreadyExists, and applyResourcePhase must transparently switch to update.
	sim.createAlreadyExists = map[string]bool{
		simKey(ResTypePromptPack, "test-pack"): true,
	}
	p := &Provider{clientFunc: newSimulatedClientFactory(sim)}

	stateJSON, err := p.Apply(context.Background(), &deploy.PlanRequest{
		PackJSON:     testPackJSON,
		DeployConfig: testDeployConfig,
	}, noopApplyCallback)
	if err != nil {
		t.Fatalf("apply should recover from AlreadyExists, got %v", err)
	}
	var state AdapterState
	if err := json.Unmarshal([]byte(stateJSON), &state); err != nil {
		t.Fatalf("parse state: %v", err)
	}
	var found bool
	for _, r := range state.Resources {
		if r.Type == ResTypePromptPack && r.Name == "test-pack" {
			found = true
			if r.Status != ResStatusUpdated {
				t.Errorf("create→AlreadyExists must record an UPDATE, got %q", r.Status)
			}
		}
	}
	if !found {
		t.Fatal("PromptPack not present in state")
	}
}

func TestIsAlreadyExists(t *testing.T) {
	if !isAlreadyExists(&HTTPError{StatusCode: 409, Body: `{"reason":"AlreadyExists"}`}) {
		t.Error("AlreadyExists (409) should be detected")
	}
	if !isAlreadyExists(&HTTPError{StatusCode: 409, Body: `object already exists`}) {
		t.Error("'already exists' body should be detected")
	}
	if isAlreadyExists(&HTTPError{StatusCode: 409, Body: `{"reason":"Conflict"}`}) {
		t.Error("optimistic-lock Conflict must NOT be treated as AlreadyExists")
	}
	if isAlreadyExists(&HTTPError{StatusCode: 404, Body: `AlreadyExists`}) {
		t.Error("non-409 must not be AlreadyExists")
	}
	if isAlreadyExists(fmt.Errorf("transport boom")) {
		t.Error("non-HTTP error must not be AlreadyExists")
	}
}
