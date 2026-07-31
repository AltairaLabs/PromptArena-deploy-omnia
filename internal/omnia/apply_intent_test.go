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
	fastReconcile(t)
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
	if countContaining(msgs, "https://omnia.test.com/console?workspace=test-ws&agent=test-pack") != 1 {
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

	// The one remaining divert: the pack denies a tool but no registry is bound,
	// so the server has nothing to build the policy rule against. (autoscaling
	// used to divert too, until omnia#1916 added it to the contract.)
	cfg := `{
		"api_endpoint": "https://omnia.test.com",
		"workspace": "test-ws",
		"api_token": "test-token",
		"providers": {"default": "claude-prod"}
	}`
	const blocklistNoToolsPack = `{
		"id": "test-pack",
		"version": "1.0.0",
		"prompts": {
			"main": {
				"system": "hi",
				"description": "main",
				"tool_policy": {"blocklist": ["danger"]}
			}
		}
	}`

	state, events, err := applyWithSim(t, sim, blocklistNoToolsPack, cfg)
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
	if countContaining(progressMessages(events), "tool blocklist") == 0 {
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
	fastReconcile(t, 1)

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

// TestApply_IntentCarriesCredentialsAndWritesNoSecret pins the ownership split
// the deploy-intent API exists to enforce (Omnia#2008).
//
// The adapter resolves the credential VALUES — only it can, they come from env
// vars in the deploy environment — and hands them to the server inside the
// intent. It must NOT write the Secret itself: an adapter-written Secret has no
// owner reference and no managed labels, so nothing reaps it and every deploy
// leaks one holding live tokens.
func TestApply_IntentCarriesCredentialsAndWritesNoSecret(t *testing.T) {
	t.Setenv("SEARCH_TOKEN", "tok-live")
	t.Setenv("ACT_AS", "user-42")

	sim := newSimulatedClient()
	sim.intentEnabled = true
	sim.agentRuntimeReadyOnGet = true
	sim.workspaces = map[string]*WorkspaceInfo{"test-ws": {Namespace: "ns"}}

	fastReconcile(t)
	p := &Provider{clientFunc: newSimulatedClientFactory(sim)}
	var events []*deploy.ApplyEvent
	// No "tools" in the config: the registry is auto-created from the arena
	// source, which is the only path that carries headers_from_env.
	if _, err := p.Apply(context.Background(), &deploy.PlanRequest{
		PackJSON: testPackJSON,
		DeployConfig: `{"api_endpoint":"https://omnia.test.com","workspace":"test-ws",` +
			`"api_token":"test-token","providers":{"default":"claude-prod"}}`,
		ArenaConfig: `{"tool_specs":{"search":{"name":"search","mode":"live","http":{` +
			`"url":"https://api.example.com/search","method":"POST",` +
			`"headers_from_env":["Authorization=SEARCH_TOKEN","X-Act-As-User=ACT_AS"]}}}}`,
	}, capturingCallback(&events)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(sim.postedIntents) != 1 {
		t.Fatalf("want the intent path, got %d submissions", len(sim.postedIntents))
	}
	if len(sim.createdSecrets) != 0 {
		t.Errorf("the intent path must write no Secret — the server owns it; got %v",
			sim.createdSecrets)
	}

	var intent struct {
		Credentials *struct {
			Data map[string]string `json:"data"`
		} `json:"credentials"`
		Tools *struct {
			Handlers []map[string]interface{} `json:"handlers"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(sim.postedIntents[0], &intent); err != nil {
		t.Fatalf("decode intent: %v", err)
	}
	if intent.Credentials == nil {
		t.Fatal("intent carries no credentials block")
	}
	// Both the bearer token and the header value travel, keyed by env var name.
	if intent.Credentials.Data["SEARCH_TOKEN"] != "tok-live" {
		t.Errorf("auth credential missing: %v", intent.Credentials.Data)
	}
	if intent.Credentials.Data["ACT_AS"] != "user-42" {
		t.Errorf("header credential missing: %v", intent.Credentials.Data)
	}

	// The values travel in the credentials block ONLY. A handler carries the
	// reference; a value there would end up in the ToolRegistry spec, which is
	// the leak headersFromSecret exists to close.
	handlers, _ := json.Marshal(intent.Tools)
	for _, v := range []string{"tok-live", "user-42"} {
		if strings.Contains(string(handlers), v) {
			t.Errorf("handler block leaks a resolved credential %q: %s", v, handlers)
		}
	}
	if !strings.Contains(string(handlers), keyHeadersFromSecret) {
		t.Errorf("handler carries no headersFromSecret reference: %s", handlers)
	}
}

// TestIntentCredentials_OmittedInBindMode guards a combination the server
// rejects outright: with tools.ref the registry is operator-owned and its Secret
// references are never rewritten, so credentials would write a Secret nothing
// points at.
func TestIntentCredentials_OmittedInBindMode(t *testing.T) {
	t.Setenv("SEARCH_TOKEN", "tok-live")
	pack := &prompt.Pack{ID: "p", Tools: map[string]*prompt.PackTool{"a": {Name: "a"}}}
	cfg := &Config{sourceTools: map[string]*httpToolSource{
		"a": {URL: "https://x", HeadersFromEnv: []string{"Authorization=SEARCH_TOKEN"}}}}

	if got := intentCredentials(pack, cfg, &toolsIntent{Ref: "shared-registry"}); got != nil {
		t.Errorf("bind mode must send no credentials, got %+v", got)
	}
	if got := intentCredentials(pack, cfg, nil); got != nil {
		t.Errorf("no tools must send no credentials, got %+v", got)
	}
}

// TestIntentCredentials_UnsetEnvContributesNoKey pins that an unset var is
// omitted rather than written as an empty value: an empty Secret key resolves to
// nothing at call time, which is indistinguishable from a header that was never
// configured. When nothing resolves the whole block is dropped — the server
// rejects an empty data map.
func TestIntentCredentials_UnsetEnvContributesNoKey(t *testing.T) {
	t.Setenv("SET_TOKEN", "v")
	pack := &prompt.Pack{ID: "p", Tools: map[string]*prompt.PackTool{"a": {Name: "a"}}}
	cfg := &Config{sourceTools: map[string]*httpToolSource{
		"a": {URL: "https://x", HeadersFromEnv: []string{
			"Authorization=SET_TOKEN", "X-Probe=UNSET_VAR_XYZ"}}}}
	tools := &toolsIntent{Handlers: []handlerIntent{{Name: "a"}}}

	got := intentCredentials(pack, cfg, tools)
	if got == nil {
		t.Fatal("expected the resolvable key to still be carried")
	}
	if _, ok := got.Data["UNSET_VAR_XYZ"]; ok {
		t.Errorf("an unset var must not become an empty Secret key: %v", got.Data)
	}
	if got.Data["SET_TOKEN"] != "v" {
		t.Errorf("resolvable key missing: %v", got.Data)
	}

	// Nothing resolvable at all → no block, rather than an empty one.
	cfg.sourceTools["a"].HeadersFromEnv = []string{"X-Probe=UNSET_VAR_XYZ"}
	if got := intentCredentials(pack, cfg, tools); got != nil {
		t.Errorf("all-unset must omit the block entirely, got %+v", got)
	}
}

func TestReportIntentResults_AttachesConsoleLinkToAgents(t *testing.T) {
	pack, err := adaptersdk.ParsePack([]byte(testPackJSON))
	if err != nil {
		t.Fatalf("ParsePack: %v", err)
	}
	cfg, err := parseConfig(testDeployConfig)
	if err != nil {
		t.Fatalf("parseConfig: %v", err)
	}

	var events []*deploy.ApplyEvent
	ac := &applyContext{pack: pack, cfg: cfg, reporter: newTestReporter(&events)}

	// The URL comes from Omnia (#1978). Note it is the /console deep link, a
	// route this adapter does not know and must never construct — which is the
	// point: Omnia moved from /agents/{name} to this without an adapter change.
	const serverURL = "https://omnia.test.com/console?workspace=test-ws&agent=my-agent"

	if _, rerr := reportIntentResults(ac, &DeployResult{
		Succeeded: true,
		Results: []DeployResourceResult{
			{Kind: "PromptPack", Name: "pp-abc", Action: intentActionCreated},
			{Kind: "AgentRuntime", Name: "my-agent", Action: intentActionCreated, URL: serverURL},
		},
	}); rerr != nil {
		t.Fatalf("unexpected error: %v", rerr)
	}

	byType := map[string]*deploy.ResourceResult{}
	for _, e := range events {
		if e.Resource != nil {
			byType[e.Resource.Type] = e.Resource
		}
	}

	agent := byType[ResTypeAgentRuntime]
	if agent == nil || len(agent.Links) != 1 {
		t.Fatalf("agent result = %+v, want one console link", agent)
	}
	if agent.Links[0].URL != serverURL {
		t.Errorf("console link = %q, want the URL Omnia returned verbatim", agent.Links[0].URL)
	}

	// Only agents have a console page — a pack object must carry no link.
	if p := byType[ResTypePromptPack]; p == nil || p.Links != nil {
		t.Errorf("promptpack result = %+v, want no links", p)
	}
}

func TestReportIntentResults_NoLinkWhenServerReturnsNoURL(t *testing.T) {
	pack, err := adaptersdk.ParsePack([]byte(testPackJSON))
	if err != nil {
		t.Fatalf("ParsePack: %v", err)
	}
	cfg, err := parseConfig(testDeployConfig)
	if err != nil {
		t.Fatalf("parseConfig: %v", err)
	}

	var events []*deploy.ApplyEvent
	ac := &applyContext{pack: pack, cfg: cfg, reporter: newTestReporter(&events)}

	// Omnia omits the URL when it does not know one. The adapter must report no
	// link rather than fall back to constructing an Omnia route — that fallback
	// is exactly what pins our repo to their URL shape.
	if _, rerr := reportIntentResults(ac, &DeployResult{
		Succeeded: true,
		Results: []DeployResourceResult{
			{Kind: "AgentRuntime", Name: "my-agent", Action: intentActionCreated},
		},
	}); rerr != nil {
		t.Fatalf("unexpected error: %v", rerr)
	}

	for _, e := range events {
		if e.Resource != nil && e.Resource.Type == ResTypeAgentRuntime && e.Resource.Links != nil {
			t.Errorf("agent links = %+v, want none when Omnia returned no URL", e.Resource.Links)
		}
	}
}

func TestAgentConsoleURLs_IndexesOnlyAgentsWithURLs(t *testing.T) {
	got := agentConsoleURLs(&DeployResult{Results: []DeployResourceResult{
		{Kind: "AgentRuntime", Name: "a", URL: "https://x/console?agent=a"},
		{Kind: "AgentRuntime", Name: "b"},                               // no URL known
		{Kind: "PromptPack", Name: "pp-1", URL: "https://x/should-not"}, // not an agent
	}})
	if len(got) != 1 || got["a"] != "https://x/console?agent=a" {
		t.Errorf("console URLs = %v, want only agent a", got)
	}
}
