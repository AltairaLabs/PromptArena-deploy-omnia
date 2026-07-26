package omnia

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/AltairaLabs/PromptKit/runtime/deploy"
	"github.com/AltairaLabs/PromptKit/runtime/deploy/adaptersdk"
	"github.com/AltairaLabs/PromptKit/runtime/prompt"
)

// newTestReporter builds a ProgressReporter that records every emitted event.
func newTestReporter(events *[]*deploy.ApplyEvent) *adaptersdk.ProgressReporter {
	return adaptersdk.NewProgressReporter(capturingCallback(events))
}

// applyWithSim runs Apply against a simulated client and returns the decoded
// adapter state plus every emitted event.
func applyWithSim(
	t *testing.T, sim *simulatedClient, packJSON, configJSON string,
) (AdapterState, []*deploy.ApplyEvent, error) {
	t.Helper()
	reconcilePollInterval = 0
	p := &Provider{clientFunc: newSimulatedClientFactory(sim)}

	var events []*deploy.ApplyEvent
	stateJSON, err := p.Apply(context.Background(), &deploy.PlanRequest{
		PackJSON:     packJSON,
		DeployConfig: configJSON,
	}, capturingCallback(&events))

	var state AdapterState
	if stateJSON != "" {
		if uerr := json.Unmarshal([]byte(stateJSON), &state); uerr != nil {
			t.Fatalf("unmarshal state: %v", uerr)
		}
	}
	return state, events, err
}

// stateNames returns the recorded resource names of the given type.
func stateNames(state AdapterState, resType string) []string {
	var names []string
	for _, r := range state.Resources {
		if r.Type == resType {
			names = append(names, r.Name)
		}
	}
	return names
}

// containsName reports whether names includes want.
func containsName(names []string, want string) bool {
	for _, n := range names {
		if n == want {
			return true
		}
	}
	return false
}

func TestApply_UsesDeployIntentWhenServed(t *testing.T) {
	sim := newSimulatedClient()
	sim.intentEnabled = true
	sim.agentRuntimeReadyOnGet = true

	state, events, err := applyWithSim(t, sim, testPackJSON, testDeployConfig)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(sim.postedIntents) != 1 {
		t.Fatalf("want exactly one DeployIntent submitted, got %d", len(sim.postedIntents))
	}

	// The PromptPack is named by the SERVER, per pack version.
	wantPack := promptPackObjectName("test-pack", "1.0.0")
	packs := stateNames(state, ResTypePromptPack)
	if !containsName(packs, wantPack) {
		t.Errorf("state PromptPacks = %v, want the server-named %q", packs, wantPack)
	}
	if containsName(packs, "test-pack") {
		t.Error("the intent path must not record a passthrough-named PromptPack")
	}
	if agents := stateNames(state, ResTypeAgentRuntime); !containsName(agents, "test-pack") {
		t.Errorf("state AgentRuntimes = %v, want test-pack", agents)
	}
	if regs := stateNames(state, ResTypeToolRegistry); !containsName(regs, "test-pack-tools") {
		t.Errorf("state ToolRegistries = %v, want test-pack-tools", regs)
	}

	// The content ConfigMap is a server-side detail: reported, never tracked,
	// because the adapter has no route to delete it.
	for _, r := range state.Resources {
		if strings.Contains(r.Name, "-content") {
			t.Errorf("the content ConfigMap must not be tracked in state, got %+v", r)
		}
	}

	msgs := progressMessages(events)
	if countContaining(msgs, "deploy-intent API") == 0 {
		t.Errorf("expected a progress message naming the deploy-intent path, got %v", msgs)
	}
	if countContaining(msgs, "https://omnia.test.com/agents/test-pack?workspace=test-ws") != 1 {
		t.Errorf("expected the access URL after reconcile, got %v", msgs)
	}
}

func TestApply_FallsBackWhenDeployIntentRouteMissing(t *testing.T) {
	// The default simulated client is an Omnia predating the deploy-intent API:
	// the route answers 404.
	sim := newSimulatedClient()
	sim.agentRuntimeReadyOnGet = true

	state, events, err := applyWithSim(t, sim, testPackJSON, testDeployConfig)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(sim.postedIntents) != 1 {
		t.Fatalf("want one attempted DeployIntent before falling back, got %d", len(sim.postedIntents))
	}
	if packs := stateNames(state, ResTypePromptPack); !containsName(packs, "test-pack") {
		t.Errorf("state PromptPacks = %v, want the per-resource name test-pack", packs)
	}
	if countContaining(progressMessages(events), "does not serve the deploy-intent API") == 0 {
		t.Errorf("expected an explanation of the fallback, got %v", progressMessages(events))
	}
}

func TestApply_DoesNotFallBackOnPermissionDenied(t *testing.T) {
	sim := newSimulatedClient()
	sim.intentEnabled = true
	sim.deployErr = &HTTPError{
		StatusCode: httpStatusForbidden,
		Body:       `{"error":"editor role required"}`,
		Category:   ErrCategoryPermission,
	}

	_, _, err := applyWithSim(t, sim, testPackJSON, testDeployConfig)
	if err == nil {
		t.Fatal("a permission denial must fail the deploy, not fall back")
	}

	// Nothing may have been written down the per-resource path.
	sim.mu.Lock()
	defer sim.mu.Unlock()
	for key := range sim.resources {
		t.Errorf("no resource may be written after a denied intent, found %q", key)
	}
}

func TestApply_DoesNotFallBackOnServerError(t *testing.T) {
	sim := newSimulatedClient()
	sim.intentEnabled = true
	sim.deployErr = &HTTPError{
		StatusCode: httpStatusServerError,
		Body:       `{"error":"boom"}`,
		Category:   ErrCategoryNetwork,
	}

	if _, _, err := applyWithSim(t, sim, testPackJSON, testDeployConfig); err == nil {
		t.Fatal("a server error must fail the deploy, not trigger a second apply attempt")
	}
	if len(sim.postedIntents) != 1 {
		t.Errorf("want exactly one submission attempt, got %d", len(sim.postedIntents))
	}
}

func TestApply_PreflightDivertsToPerResourcePath(t *testing.T) {
	sim := newSimulatedClient()
	sim.intentEnabled = true
	sim.agentRuntimeReadyOnGet = true

	// runtime.autoscaling cannot be expressed in the v1 contract.
	cfg := `{
		"api_endpoint": "https://omnia.test.com",
		"workspace": "test-ws",
		"api_token": "test-token",
		"providers": {"default": "claude-prod"},
		"runtime": {"replicas": 2, "autoscaling": {"enabled": true, "min_replicas": 1, "max_replicas": 5}}
	}`

	state, events, err := applyWithSim(t, sim, testPackJSON, cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(sim.postedIntents) != 0 {
		t.Errorf("a config the contract cannot carry must never be submitted, got %d submissions",
			len(sim.postedIntents))
	}
	if packs := stateNames(state, ResTypePromptPack); !containsName(packs, "test-pack") {
		t.Errorf("state PromptPacks = %v, want the per-resource name", packs)
	}
	if countContaining(progressMessages(events), "runtime.autoscaling") == 0 {
		t.Errorf("expected the blocker to be named, got %v", progressMessages(events))
	}
}

func TestApply_IntentPartialFailureReportsError(t *testing.T) {
	sim := newSimulatedClient()
	sim.intentEnabled = true
	packObject := promptPackObjectName("test-pack", "1.0.0")
	sim.deployResult = &DeployResult{
		Succeeded: false,
		Results: []DeployResourceResult{
			{Kind: "PromptPack", Name: packObject, Action: intentActionCreated},
			{Kind: "AgentRuntime", Name: "test-pack", Action: intentActionFailed,
				Error: "admission webhook rejected the object"},
		},
	}

	state, _, err := applyWithSim(t, sim, testPackJSON, testDeployConfig)
	if err == nil {
		t.Fatal("a partly-failed apply must return an error")
	}
	if !strings.Contains(err.Error(), "admission webhook") {
		t.Errorf("error must name the underlying failure, got %v", err)
	}
	if len(sim.postedIntents) != 1 {
		t.Errorf("a partial failure must NOT be retried on the other path, got %d submissions",
			len(sim.postedIntents))
	}
	// The resources that did apply are still recorded, so a later destroy finds them.
	if packs := stateNames(state, ResTypePromptPack); !containsName(packs, packObject) {
		t.Errorf("state PromptPacks = %v, want the created %q recorded", packs, packObject)
	}
}

func TestApply_CarriesSupersededPackObjectsIntoState(t *testing.T) {
	sim := newSimulatedClient()
	sim.intentEnabled = true
	sim.agentRuntimeReadyOnGet = true
	// A workspace previously deployed through the per-resource path: its PromptPack
	// is named after the pack, and the new deploy will not write that name.
	seedManagedResource(sim, ResTypePromptPack, "test-pack", "test-pack")

	state, events, err := applyWithSim(t, sim, testPackJSON, testDeployConfig)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	packs := stateNames(state, ResTypePromptPack)
	wantNew := promptPackObjectName("test-pack", "1.0.0")
	if !containsName(packs, wantNew) {
		t.Errorf("state PromptPacks = %v, want the new %q", packs, wantNew)
	}
	if !containsName(packs, "test-pack") {
		t.Errorf("state PromptPacks = %v, want the superseded object tracked for destroy", packs)
	}
	if countContaining(progressMessages(events), "Superseded PromptPack") == 0 {
		t.Errorf("expected the superseded object to be reported, got %v", progressMessages(events))
	}
}

func TestApply_IntentReconcileFailureFailsDeploy(t *testing.T) {
	origAttempts := reconcileMaxAttempts
	reconcileMaxAttempts = 1
	defer func() { reconcileMaxAttempts = origAttempts }()

	sim := newSimulatedClient()
	sim.intentEnabled = true
	// The server writes the AgentRuntime but the operator never reconciles it.
	sim.agentRuntimeReadyOnGet = false

	_, events, err := applyWithSim(t, sim, testPackJSON, testDeployConfig)
	if err == nil {
		t.Fatal("an AgentRuntime that never reconciles must fail the deploy")
	}
	if countContaining(progressMessages(events), "?workspace=") != 0 {
		t.Error("no access URL may be surfaced for an agent that never became Ready")
	}
}

func TestIntentTools_CreateModeWithNoHandlers(t *testing.T) {
	// Create mode with nothing to put in the registry emits no tools block at all,
	// rather than an empty one the server would reject.
	pack := &prompt.Pack{ID: "p", Prompts: map[string]*prompt.PackPrompt{"main": {}}}
	got := intentTools(pack, &Config{}, ToolBinding{Mode: toolModeCreate, RegistryName: "p-tools"})
	if got != nil {
		t.Errorf("tools = %+v, want nil when there is nothing to create", got)
	}
}

func TestIntentTools_BindModeWithoutName(t *testing.T) {
	pack := &prompt.Pack{ID: "p"}
	if got := intentTools(pack, &Config{}, ToolBinding{Mode: toolModeBind}); got != nil {
		t.Errorf("tools = %+v, want nil when bind mode names no registry", got)
	}
}

func TestRawField(t *testing.T) {
	if got := rawField(map[string]interface{}{}, "missing"); got != nil {
		t.Errorf("an absent key must map to nil, got %s", *got)
	}
	if got := rawField(map[string]interface{}{"k": nil}, "k"); got != nil {
		t.Errorf("a nil value must map to nil, got %s", *got)
	}
	// A value encoding/json cannot marshal degrades to nil so the server applies
	// its own default rather than receiving a malformed block.
	if got := rawField(map[string]interface{}{"k": make(chan int)}, "k"); got != nil {
		t.Errorf("an unmarshalable value must map to nil, got %s", *got)
	}
	got := rawField(map[string]interface{}{"k": map[string]string{"a": "b"}}, "k")
	if got == nil || !strings.Contains(string(*got), `"a":"b"`) {
		t.Errorf("raw block = %v, want the value re-marshaled", got)
	}
}

func TestIntentProviders_DefaultsRole(t *testing.T) {
	got := intentProviders(Providers{
		{Name: "default", Ref: "claude"},
		{Name: "embed", Ref: "ada", Role: "embedding"},
	})
	if len(got) != 2 {
		t.Fatalf("providers = %+v", got)
	}
	if got[0].Role != roleLLM {
		t.Errorf("an unset role must default to %q, got %q", roleLLM, got[0].Role)
	}
	if got[1].Role != "embedding" {
		t.Errorf("an explicit role must be preserved, got %q", got[1].Role)
	}
}

func TestIsIntentUnsupported(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "404 route absent", err: &HTTPError{StatusCode: httpStatusNotFound}, want: true},
		{name: "405 no POST", err: &HTTPError{StatusCode: httpStatusMethodNotAllowed}, want: true},
		{
			name: "400 unknown apiVersion",
			err: &HTTPError{StatusCode: httpStatusBadRequest,
				Body: `{"error":"unsupported apiVersion \"deploy.omnia.altairalabs.ai/v1\""}`},
			want: true,
		},
		{
			name: "400 invalid body",
			err: &HTTPError{StatusCode: httpStatusBadRequest,
				Body: `{"error":"agents[0].name is required"}`},
		},
		{name: "401 unauthenticated", err: &HTTPError{StatusCode: httpStatusUnauthorized}},
		{name: "403 not an editor", err: &HTTPError{StatusCode: httpStatusForbidden}},
		{name: "409 conflict", err: &HTTPError{StatusCode: httpStatusConflict}},
		{name: "422 rejected", err: &HTTPError{StatusCode: httpStatusUnprocessable}},
		{name: "500 server error", err: &HTTPError{StatusCode: httpStatusServerError}},
		{name: "transport failure", err: errors.New("connection refused")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isIntentUnsupported(tt.err); got != tt.want {
				t.Errorf("isIntentUnsupported() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestIntentResultError(t *testing.T) {
	if err := intentResultError(&DeployResult{Succeeded: true}); err != nil {
		t.Errorf("a successful result must yield no error, got %v", err)
	}

	err := intentResultError(&DeployResult{
		Succeeded: false,
		Results: []DeployResourceResult{
			{Kind: "PromptPack", Name: "pp-1", Action: intentActionCreated},
			{Kind: "AgentRuntime", Name: "a", Action: intentActionFailed, Error: "boom"},
		},
	})
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Errorf("error must name the failed resource, got %v", err)
	}

	// A server that reports failure without flagging any resource still errors.
	bare := intentResultError(&DeployResult{Succeeded: false})
	if bare == nil || !strings.Contains(bare.Error(), "did not succeed") {
		t.Errorf("want a generic failure message, got %v", bare)
	}
}

func TestIntentResourceAction(t *testing.T) {
	if got := intentResourceAction(intentActionCreated); got != deploy.ActionCreate {
		t.Errorf("created maps to %v, want %v", got, deploy.ActionCreate)
	}
	for _, action := range []string{intentActionUpdated, intentActionUnchanged, intentActionFailed} {
		if got := intentResourceAction(action); got != deploy.ActionUpdate {
			t.Errorf("%s maps to %v, want %v", action, got, deploy.ActionUpdate)
		}
	}
}

func TestReportIntentResults_SkipsUntrackedKinds(t *testing.T) {
	pack, err := adaptersdk.ParsePack([]byte(testPackJSON))
	if err != nil {
		t.Fatalf("ParsePack: %v", err)
	}
	var events []*deploy.ApplyEvent
	ac := &applyContext{pack: pack, reporter: newTestReporter(&events)}

	resources, rerr := reportIntentResults(ac, &DeployResult{
		Succeeded: true,
		Results: []DeployResourceResult{
			{Kind: "ConfigMap", Name: "pp-abc-content", Action: intentActionCreated},
			{Kind: "PromptPack", Name: "pp-abc", Action: intentActionCreated},
			{Kind: "AgentRuntime", Name: "a", Action: intentActionUnchanged},
			{Kind: "Mystery", Name: "m", Action: intentActionCreated},
		},
	})
	if err := rerr; err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resources) != 2 {
		t.Fatalf("want only the tracked kinds recorded, got %+v", resources)
	}
	if resources[0].Type != ResTypePromptPack || resources[0].Status != ResStatusCreated {
		t.Errorf("PromptPack state = %+v", resources[0])
	}
	if resources[1].Type != ResTypeAgentRuntime || resources[1].Status != ResStatusUnchanged {
		t.Errorf("AgentRuntime state = %+v", resources[1])
	}
	// Untracked kinds are still surfaced to the user as progress.
	if countContaining(progressMessages(events), "pp-abc-content") == 0 {
		t.Errorf("the ConfigMap must still be reported, got %v", progressMessages(events))
	}
}

func TestReportIntentResults_UnknownActionDefaultsToUnchanged(t *testing.T) {
	pack, err := adaptersdk.ParsePack([]byte(testPackJSON))
	if err != nil {
		t.Fatalf("ParsePack: %v", err)
	}
	var events []*deploy.ApplyEvent
	ac := &applyContext{pack: pack, reporter: newTestReporter(&events)}

	resources, rerr := reportIntentResults(ac, &DeployResult{
		Succeeded: true,
		Results:   []DeployResourceResult{{Kind: "PromptPack", Name: "pp-x", Action: "reconciled"}},
	})
	if err := rerr; err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resources) != 1 || resources[0].Status != ResStatusUnchanged {
		t.Errorf("an unrecognized action must degrade to unchanged, got %+v", resources)
	}
}

func TestReportIntentResults_WarnsWhenPackAlreadyPublished(t *testing.T) {
	pack, err := adaptersdk.ParsePack([]byte(testPackJSON))
	if err != nil {
		t.Fatalf("ParsePack: %v", err)
	}

	var events []*deploy.ApplyEvent
	ac := &applyContext{pack: pack, reporter: newTestReporter(&events)}

	if _, err := reportIntentResults(ac, &DeployResult{
		Succeeded: true,
		Results:   []DeployResourceResult{{Kind: "PromptPack", Name: "pp-x", Action: intentActionUnchanged}},
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	msgs := progressMessages(events)
	if countContaining(msgs, "already published") == 0 {
		t.Errorf("an unchanged pack must warn that content was not republished, got %v", msgs)
	}
	if countContaining(msgs, "bump its version") == 0 {
		t.Errorf("the advisory must name the remedy, got %v", msgs)
	}
}

func TestReportIntentResults_NoWarningWhenPackCreated(t *testing.T) {
	pack, err := adaptersdk.ParsePack([]byte(testPackJSON))
	if err != nil {
		t.Fatalf("ParsePack: %v", err)
	}

	var events []*deploy.ApplyEvent
	ac := &applyContext{pack: pack, reporter: newTestReporter(&events)}

	if _, err := reportIntentResults(ac, &DeployResult{
		Succeeded: true,
		Results: []DeployResourceResult{
			{Kind: "PromptPack", Name: "pp-x", Action: intentActionCreated},
			{Kind: "AgentRuntime", Name: "a", Action: intentActionUnchanged},
		},
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// A fresh publish is not a no-op, and an unchanged AGENT says nothing about
	// pack content — neither may trigger the advisory.
	if got := countContaining(progressMessages(events), "already published"); got != 0 {
		t.Errorf("unexpected already-published advisory (%d) for a created pack", got)
	}
}

func TestSortResourceStates(t *testing.T) {
	got := []ResourceState{{Name: "c"}, {Name: "a"}, {Name: "b"}}
	sortResourceStates(got)
	for i, want := range []string{"a", "b", "c"} {
		if got[i].Name != want {
			t.Fatalf("sorted = %+v, want a,b,c", got)
		}
	}
	sortResourceStates(nil) // must not panic
}

func TestIntentSupported_ProbeFailureIsNotFatal(t *testing.T) {
	sim := newSimulatedClient()
	sim.deployProfileErr = errors.New("profile route absent")
	p := &Provider{clientFunc: newSimulatedClientFactory(sim)}

	cfg, err := parseConfig(testDeployConfig)
	if err != nil {
		t.Fatalf("parseConfig: %v", err)
	}
	if p.intentSupported(context.Background(), cfg) {
		t.Error("a failed probe must read as no deploy-intent support")
	}

	sim.deployProfileErr = nil
	sim.intentEnabled = true
	if !p.intentSupported(context.Background(), cfg) {
		t.Error("an advertised v1 contract must read as supported")
	}
}

func TestIntentSupported_ClientFailureIsNotFatal(t *testing.T) {
	p := &Provider{clientFunc: func(*Config) (omniaClient, error) {
		return nil, errors.New("no token")
	}}
	if p.intentSupported(context.Background(), &Config{}) {
		t.Error("a client that cannot be built must read as no support")
	}
}

func TestPlan_PreviewsServerNamedPackObject(t *testing.T) {
	p, sim := newPlanTestProvider()
	sim.intentEnabled = true

	resp, err := p.Plan(context.Background(), &deploy.PlanRequest{
		PackJSON:     testPackJSON,
		DeployConfig: testDeployConfig,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	want := promptPackObjectName("test-pack", "1.0.0")
	var found bool
	for _, c := range resp.Changes {
		if c.Type == ResTypePromptPack && c.Name == want {
			found = true
		}
		if c.Type == ResTypePromptPack && c.Name == "test-pack" {
			t.Error("plan previewed the per-resource pack name while the server serves the intent API")
		}
	}
	if !found {
		t.Errorf("plan changes = %+v, want a PromptPack named %q", resp.Changes, want)
	}
	if countContaining(resp.Warnings, "deploy-intent API") == 0 {
		t.Errorf("plan warnings = %v, want the chosen path named", resp.Warnings)
	}
}

func TestPlan_PreviewsPerResourceNameWhenUnsupported(t *testing.T) {
	p, _ := newPlanTestProvider()

	resp, err := p.Plan(context.Background(), &deploy.PlanRequest{
		PackJSON:     testPackJSON,
		DeployConfig: testDeployConfig,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var found bool
	for _, c := range resp.Changes {
		if c.Type == ResTypePromptPack && c.Name == "test-pack" {
			found = true
		}
	}
	if !found {
		t.Errorf("plan changes = %+v, want the per-resource PromptPack name", resp.Changes)
	}
	if countContaining(resp.Warnings, "deploy-intent API") != 0 {
		t.Errorf("an older server must not advertise the intent path, warnings = %v", resp.Warnings)
	}
}

func TestPlanDeployPath_ReportsBlockers(t *testing.T) {
	p, sim := newPlanTestProvider()
	sim.intentEnabled = true

	cfg := `{
		"api_endpoint": "https://omnia.test.com",
		"workspace": "test-ws",
		"api_token": "test-token",
		"providers": {"default": "claude-prod"},
		"externalAuth": {"sharedToken": {"secretRef": "agent-token"}}
	}`
	resp, err := p.Plan(context.Background(), &deploy.PlanRequest{
		PackJSON:     testPackJSON,
		DeployConfig: cfg,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if countContaining(resp.Warnings, "externalAuth.sharedToken") == 0 {
		t.Errorf("plan warnings = %v, want the blocker named", resp.Warnings)
	}
}

func TestPlanDeployPath_DryRunSkipsProbe(t *testing.T) {
	p := &Provider{clientFunc: func(*Config) (omniaClient, error) {
		t.Error("dry-run must not contact the API")
		return nil, errors.New("unreachable")
	}}
	intentPath, warnings := p.planDeployPath(
		context.Background(), &prompt.Pack{ID: "p"}, &Config{DryRun: true}, ToolBinding{})
	if intentPath || warnings != nil {
		t.Errorf("dry-run = (%v, %v), want the per-resource preview with no advisories",
			intentPath, warnings)
	}
}

func TestPlannedPackObjectName(t *testing.T) {
	pack := &prompt.Pack{ID: "Test_Pack", Version: "1.0.0"}
	if got := plannedPackObjectName(pack, false); got != "test-pack" {
		t.Errorf("per-resource name = %q, want test-pack", got)
	}
	want := promptPackObjectName("test-pack", "1.0.0")
	if got := plannedPackObjectName(pack, true); got != want {
		t.Errorf("intent name = %q, want %q", got, want)
	}
}

func TestHTTPClient_PostDeployment(t *testing.T) {
	var gotBody string
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/deployments") {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		if auth := r.Header.Get("Authorization"); auth != "Bearer test-token" {
			t.Errorf("Authorization = %q", auth)
		}
		buf := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(buf)
		gotBody = string(buf)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"succeeded":true,"results":[
			{"kind":"PromptPack","name":"pp-abc","action":"created"},
			{"kind":"AgentRuntime","name":"a","action":"unchanged"}
		]}`))
	})
	hc := newTestHTTPClient(t, handler)

	result, err := hc.PostDeployment(context.Background(), json.RawMessage(`{"apiVersion":"v1"}`))
	if err != nil {
		t.Fatalf("PostDeployment: %v", err)
	}
	if !strings.Contains(gotBody, "apiVersion") {
		t.Errorf("intent body not forwarded, got %q", gotBody)
	}
	if !result.Succeeded || len(result.Results) != 2 {
		t.Fatalf("result = %+v", result)
	}
	if result.Results[0].Kind != "PromptPack" || result.Results[0].Action != intentActionCreated {
		t.Errorf("first result = %+v", result.Results[0])
	}
}

func TestHTTPClient_PostDeployment_TypedError(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`Cannot POST /api/workspaces/ws/deployments`))
	})
	hc := newTestHTTPClient(t, handler)

	_, err := hc.PostDeployment(context.Background(), json.RawMessage(`{}`))
	if err == nil {
		t.Fatal("expected an error on 404")
	}
	if !isIntentUnsupported(err) {
		t.Errorf("a 404 must classify as 'no deploy-intent API', got %v", err)
	}
}

func TestHTTPClient_PostDeployment_BadResponseBody(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`not json`))
	})
	hc := newTestHTTPClient(t, handler)

	if _, err := hc.PostDeployment(context.Background(), json.RawMessage(`{}`)); err == nil {
		t.Fatal("expected a decode error")
	}
}

func TestHTTPClient_GetDeployProfile(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/deploy-profile") {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"workspace":"ws","supportedDeployIntentVersions":
			["deploy.omnia.altairalabs.ai/v1"]}`))
	})
	hc := newTestHTTPClient(t, handler)

	profile, err := hc.GetDeployProfile(context.Background())
	if err != nil {
		t.Fatalf("GetDeployProfile: %v", err)
	}
	if !profile.supportsIntentV1() {
		t.Errorf("profile = %+v, want v1 advertised", profile)
	}
}

func TestHTTPClient_GetDeployProfile_Errors(t *testing.T) {
	forbidden := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	})
	if _, err := newTestHTTPClient(t, forbidden).GetDeployProfile(context.Background()); err == nil {
		t.Fatal("expected an error on 403")
	}

	garbage := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{`))
	})
	if _, err := newTestHTTPClient(t, garbage).GetDeployProfile(context.Background()); err == nil {
		t.Fatal("expected a decode error")
	}
}
