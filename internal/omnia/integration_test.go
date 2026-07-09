//go:build integration

package omnia

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode"

	"github.com/AltairaLabs/PromptKit/runtime/deploy"
)

// These tests hit a REAL Omnia workspace. They are gated behind the
// `integration` build tag and parameterized entirely by environment variables,
// so the normal `go test ./...` run never compiles or executes them. Run with:
//
//	OMNIA_IT_ENDPOINT=https://omnia.example.com \
//	OMNIA_IT_TOKEN=omnia_sk_... \
//	go test -tags=integration ./internal/omnia/ -run Integration -count=1
//
// With no env set, every test t.Skip()s — proving the file compiles and gates
// cleanly without touching live infrastructure.

// Repeated string literals shared across cases. Pulled into consts so goconst
// doesn't flag them and so the canonical wording lives in one place.
const (
	itProviderDefault = "default"
	itRoleLLM         = "llm"
	itToolCreateName  = "create_thing"
	itToolListName    = "list_things"

	itSkipMsg = "set OMNIA_IT_ENDPOINT and OMNIA_IT_TOKEN to run integration tests"

	// itPackIDPrefix is the prefix every integration test pack ID carries, so a
	// leaked resource is obviously test-owned in the workspace.
	itPackIDPrefix = "it"
)

// itUniqueCounter makes per-call pack IDs unique within a process without
// relying on wall-clock time. pid + counter is enough isolation for parallel
// or repeated runs against the same workspace.
var itUniqueCounter atomic.Int64

// itConfig holds the live-workspace coordinates and the resource refs a run
// targets. Populated by itEnv from OMNIA_IT_* variables.
type itConfig struct {
	Endpoint     string
	Token        string
	Workspace    string
	Provider     string
	BindRegistry string
}

// itEnv reads the OMNIA_IT_* environment and skips the test when the required
// endpoint/token pair is absent. Optional values fall back to sensible
// workspace defaults.
func itEnv(t *testing.T) itConfig {
	t.Helper()

	endpoint := os.Getenv("OMNIA_IT_ENDPOINT")
	token := os.Getenv("OMNIA_IT_TOKEN")
	if endpoint == "" || token == "" {
		t.Skip(itSkipMsg)
	}

	cfg := itConfig{
		Endpoint:     endpoint,
		Token:        token,
		Workspace:    os.Getenv("OMNIA_IT_WORKSPACE"),
		Provider:     os.Getenv("OMNIA_IT_PROVIDER"),
		BindRegistry: os.Getenv("OMNIA_IT_BIND_REGISTRY"),
	}
	if cfg.Workspace == "" {
		cfg.Workspace = "demo"
	}
	if cfg.Provider == "" {
		cfg.Provider = "claude"
	}
	if cfg.BindRegistry == "" {
		cfg.BindRegistry = "splitpantz-api"
	}
	return cfg
}

// uniquePackID returns an RFC1123-safe, lowercase pack ID unique to this test
// invocation: it-<sanitized-test-name>-<pid>-<counter>. Deterministic-ish (no
// wall clock) so failures are reproducible while still isolating concurrent or
// repeated runs.
func uniquePackID(t *testing.T) string {
	t.Helper()
	name := strings.ToLower(t.Name())
	name = strings.NewReplacer("/", "-", "_", "-").Replace(name)
	n := itUniqueCounter.Add(1)
	id := fmt.Sprintf("%s-%s-%d-%d", itPackIDPrefix, name, os.Getpid(), n)
	// Round-trip through the adapter's own sanitizer so the ID is a valid k8s
	// name (the adapter would sanitize it anyway; doing it here keeps assertions
	// on sanitizeName(pack.ID) trivial).
	return sanitizeName(id)
}

// deployConfigOpts selects between the two mutually-exclusive tool modes plus
// optional overrides used by the error-path tests.
type deployConfigOpts struct {
	// createTools, when true, emits a `tools` block (create mode). When false and
	// bindRegistry is set, emits `tool_registry_ref` (bind mode).
	createTools  bool
	bindRegistry string

	// Overrides for error-path tests. Empty means "use env defaults".
	endpointOverride  string
	tokenOverride     string
	workspaceOverride string
	providerOverride  string

	// bothToolsAndRef forces BOTH tools and tool_registry_ref to be set, to
	// exercise the mutually-exclusive validation error.
	bothToolsAndRef string
}

// createModeHandlers returns the http handlers used in create mode. Only
// create_thing is configured; list_things is deliberately left WITHOUT a handler
// so the adapter synthesizes a placeholder for it (and warns) — exercising the
// uncovered-tool path. Keep this in sync with buildPack's tool set.
func createModeHandlers() []map[string]any {
	return []map[string]any{
		{
			"name": "create-thing",
			"type": "http",
			"tool": map[string]any{
				"name":        itToolCreateName,
				"description": "create a thing",
				"inputSchema": map[string]any{"type": "object"},
			},
			"httpConfig": map[string]any{
				"endpoint": "https://api.example.com/things",
				"method":   "POST",
			},
		},
	}
}

// buildDeployConfig marshals a deploy-config JSON document from the env plus the
// supplied options. The token is placed in the `api_token` field (the path
// cfg.resolveToken reads first).
func buildDeployConfig(env itConfig, opts deployConfigOpts) string {
	endpoint := env.Endpoint
	if opts.endpointOverride != "" {
		endpoint = opts.endpointOverride
	}
	token := env.Token
	if opts.tokenOverride != "" {
		token = opts.tokenOverride
	}
	workspace := env.Workspace
	if opts.workspaceOverride != "" {
		workspace = opts.workspaceOverride
	}
	provider := env.Provider
	if opts.providerOverride != "" {
		provider = opts.providerOverride
	}

	doc := map[string]any{
		"api_endpoint": endpoint,
		"workspace":    workspace,
		"api_token":    token,
		"providers": []map[string]any{
			{"name": itProviderDefault, "ref": provider, "role": itRoleLLM},
		},
	}

	switch {
	case opts.bothToolsAndRef != "":
		// Invalid: both blocks set at once (mutually exclusive).
		doc["tools"] = createModeHandlers()
		doc["tool_registry_ref"] = opts.bothToolsAndRef
	case opts.createTools:
		doc["tools"] = createModeHandlers()
	case opts.bindRegistry != "":
		doc["tool_registry_ref"] = opts.bindRegistry
	}

	b, err := json.Marshal(doc)
	if err != nil {
		panic(fmt.Sprintf("buildDeployConfig: marshal failed: %v", err))
	}
	return string(b)
}

// buildPack returns a minimal valid pack JSON with two tools: create_thing
// (covered by a configured handler in create mode) and list_things (uncovered,
// so it becomes a placeholder handler with an advisory warning).
func buildPack(packID string) string {
	// Shape mirrors a real compiled pack (omnia's PromptPack schema validates it):
	// root needs name + template_engine; each prompt needs id/name/version/
	// system_template (NOT "system"); each tool's parameters needs "properties".
	doc := map[string]any{
		"id":          packID,
		"name":        packID,
		"version":     "1.0.0",
		"description": "integration test",
		"template_engine": map[string]any{
			"version":  "v1",
			"syntax":   "{{variable}}",
			"features": []string{"basic_substitution"},
		},
		"prompts": map[string]any{
			"main": map[string]any{
				"id":              "main",
				"name":            "main",
				"description":     "main prompt",
				"version":         "1.0.0",
				"system_template": "You are a test agent.",
			},
		},
		"tools": map[string]any{
			itToolCreateName: map[string]any{
				"name":        itToolCreateName,
				"description": "create a thing",
				"parameters":  map[string]any{"type": "object", "properties": map[string]any{}},
			},
			itToolListName: map[string]any{
				"name":        itToolListName,
				"description": "list things",
				"parameters":  map[string]any{"type": "object", "properties": map[string]any{}},
			},
		},
	}
	b, err := json.Marshal(doc)
	if err != nil {
		panic(fmt.Sprintf("buildPack: marshal failed: %v", err))
	}
	return string(b)
}

// applyAndCollect runs Apply, collecting every streamed event. It fails the test
// on an Apply error and returns the new opaque adapter state plus the events.
func applyAndCollect(
	t *testing.T, p *Provider, req *deploy.PlanRequest,
) (string, []*deploy.ApplyEvent) {
	t.Helper()
	var events []*deploy.ApplyEvent
	state, err := p.Apply(context.Background(), req, func(e *deploy.ApplyEvent) error {
		events = append(events, e)
		return nil
	})
	if err != nil {
		t.Fatalf("Apply failed: %v", err)
	}
	return state, events
}

// pollStatus polls Status until the aggregate equals want or the timeout
// elapses, returning the last response seen. Apply only creates the CRDs; the
// pods then take time to become Ready, so Status is transiently "degraded"
// immediately after Apply and converges to "deployed" once the agent is running.
func pollStatus(
	t *testing.T, p *Provider, deployConfig, workspace, state, want string, timeout time.Duration,
) *deploy.StatusResponse {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var last *deploy.StatusResponse
	for {
		st, err := p.Status(context.Background(), &deploy.StatusRequest{
			DeployConfig: deployConfig,
			Environment:  workspace,
			PriorState:   state,
		})
		if err != nil {
			t.Fatalf("Status: %v", err)
		}
		last = st
		if st.Status == want || time.Now().After(deadline) {
			return last
		}
		time.Sleep(5 * time.Second)
	}
}

// cleanupDeploy registers a best-effort Destroy via t.Cleanup so a test that
// applies real resources never leaks them, even on a mid-test failure. Cleanup
// errors are logged, not fatal — a test must not fail because teardown of an
// already-torn-down deploy returned an error.
func cleanupDeploy(t *testing.T, p *Provider, env itConfig, deployConfig, state string) {
	t.Helper()
	t.Cleanup(func() {
		if state == "" {
			return
		}
		err := p.Destroy(context.Background(), &deploy.DestroyRequest{
			DeployConfig: deployConfig,
			Environment:  env.Workspace,
			PriorState:   state,
		}, func(_ *deploy.DestroyEvent) error { return nil })
		if err != nil {
			t.Logf("cleanup Destroy returned (non-fatal): %v", err)
		}
	})
}

// resourceEventsByType indexes the resource-bearing apply events by resource
// type, for assertions that a phase emitted the expected resource event.
func resourceEventsByType(events []*deploy.ApplyEvent) map[string]*deploy.ResourceResult {
	byType := make(map[string]*deploy.ResourceResult)
	for _, e := range events {
		if e.Resource != nil {
			byType[e.Resource.Type] = e.Resource
		}
	}
	return byType
}

// changeByType indexes plan changes by resource type.
func changeByType(changes []deploy.ResourceChange) map[string]deploy.ResourceChange {
	byType := make(map[string]deploy.ResourceChange)
	for _, c := range changes {
		byType[c.Type] = c
	}
	return byType
}

// titleFirst uppercases the first rune of s, leaving the rest unchanged. Used
// to force a mixed-case workspace slug for the normalization test without the
// deprecated strings.Title.
func titleFirst(s string) string {
	if s == "" {
		return s
	}
	r := []rune(s)
	r[0] = unicode.ToUpper(r[0])
	return string(r)
}

// hasWarningContaining reports whether any warning contains the substring.
func hasWarningContaining(warnings []string, sub string) bool {
	for _, w := range warnings {
		if strings.Contains(w, sub) {
			return true
		}
	}
	return false
}

// ----------------------------------------------------------------------------
// HAPPY paths
// ----------------------------------------------------------------------------

// TestIntegration_GetProviderInfo verifies the adapter identity and advertised
// capabilities. Does not require live infra but is gated for suite cohesion.
func TestIntegration_GetProviderInfo(t *testing.T) {
	_ = itEnv(t) // gate on env presence

	p := NewProvider()
	info, err := p.GetProviderInfo(context.Background())
	if err != nil {
		t.Fatalf("GetProviderInfo: %v", err)
	}
	if info.Name != "omnia" {
		t.Errorf("Name = %q, want omnia", info.Name)
	}
	for _, want := range []string{"plan", "apply", "destroy", "status"} {
		found := false
		for _, c := range info.Capabilities {
			if c == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("Capabilities %v missing %q", info.Capabilities, want)
		}
	}
}

// TestIntegration_ValidateConfig_Valid checks a well-formed create-mode config
// validates clean.
func TestIntegration_ValidateConfig_Valid(t *testing.T) {
	env := itEnv(t)
	p := NewProvider()

	cfg := buildDeployConfig(env, deployConfigOpts{createTools: true})
	resp, err := p.ValidateConfig(context.Background(), &deploy.ValidateRequest{Config: cfg})
	if err != nil {
		t.Fatalf("ValidateConfig: %v", err)
	}
	if !resp.Valid {
		t.Errorf("Valid = false, want true; Errors = %v", resp.Errors)
	}
	if len(resp.Errors) != 0 {
		t.Errorf("Errors = %v, want none", resp.Errors)
	}
}

// TestIntegration_Plan_CreateMode plans a create-mode deploy and asserts the
// three core resources are all CREATE plus the list_things placeholder warning.
func TestIntegration_Plan_CreateMode(t *testing.T) {
	env := itEnv(t)
	p := NewProvider()

	packID := uniquePackID(t)
	cfg := buildDeployConfig(env, deployConfigOpts{createTools: true})
	resp, err := p.Plan(context.Background(), &deploy.PlanRequest{
		PackJSON:     buildPack(packID),
		DeployConfig: cfg,
		Environment:  env.Workspace,
	})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(resp.Changes) < 3 {
		t.Fatalf("Changes = %d, want >= 3: %+v", len(resp.Changes), resp.Changes)
	}

	byType := changeByType(resp.Changes)
	for _, rt := range []string{ResTypePromptPack, ResTypeToolRegistry, ResTypeAgentRuntime} {
		c, ok := byType[rt]
		if !ok {
			t.Errorf("missing change for type %q", rt)
			continue
		}
		if c.Action != deploy.ActionCreate {
			t.Errorf("%s action = %q, want %q", rt, c.Action, deploy.ActionCreate)
		}
	}

	if !hasWarningContaining(resp.Warnings, itToolListName) {
		t.Errorf("expected a placeholder warning mentioning %q; warnings = %v",
			itToolListName, resp.Warnings)
	}
}

// TestIntegration_Lifecycle is the end-to-end create → status → re-apply →
// destroy round-trip. It asserts: each core resource is created on first apply;
// status reports deployed; the create-only ToolRegistry stays "unchanged" on
// re-apply while PromptPack/AgentRuntime update; destroy succeeds.
// buildSimplePack returns a minimal, tool-free pack with the given prompt names.
// One prompt => one agent; 2+ prompts => one agent per prompt (fan-out).
func buildSimplePack(packID string, promptNames ...string) string {
	prompts := map[string]any{}
	for _, n := range promptNames {
		prompts[n] = map[string]any{
			"id": n, "name": n, "version": "1.0.0",
			"description":     n + " prompt",
			"system_template": "You are a helpful test agent. Answer briefly.",
		}
	}
	doc := map[string]any{
		"id": packID, "name": packID, "version": "1.0.0",
		"description": "e2e converse pack",
		"template_engine": map[string]any{
			"version": "v1", "syntax": "{{variable}}",
			"features": []string{"basic_substitution"},
		},
		"prompts": prompts,
	}
	b, err := json.Marshal(doc)
	if err != nil {
		panic(fmt.Sprintf("buildSimplePack: marshal failed: %v", err))
	}
	return string(b)
}

// withSharedToken adds an externalAuth.sharedToken block naming the given Secret
// to a deploy-config JSON document.
func withSharedToken(t *testing.T, cfg, secretName string) string {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal([]byte(cfg), &doc); err != nil {
		t.Fatalf("withSharedToken: unmarshal cfg: %v", err)
	}
	doc["externalAuth"] = map[string]any{
		"sharedToken": map[string]any{"secretRef": secretName},
	}
	b, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("withSharedToken: marshal: %v", err)
	}
	return string(b)
}

// TestIntegration_E2EDeployAndKeep deploys a persistent agent (deliberately NO
// destroy) and waits for it to become "deployed" — pods Ready — so a subsequent
// websocket converse step can talk to it. Gated on OMNIA_IT_KEEP so it stays out
// of the normal integration run (which always cleans up after itself).
//
// OMNIA_IT_PACK_ID names the pack (default "e2e-basic"); OMNIA_IT_PROMPTS is a
// comma-separated prompt list (default "main"). 2+ prompts exercise fan-out.
func TestIntegration_E2EDeployAndKeep(t *testing.T) {
	if os.Getenv("OMNIA_IT_KEEP") == "" {
		t.Skip("set OMNIA_IT_KEEP=1 to deploy a persistent agent (converse e2e)")
	}
	env := itEnv(t)
	p := NewProvider()

	packID := os.Getenv("OMNIA_IT_PACK_ID")
	if packID == "" {
		packID = "e2e-basic"
	}
	prompts := []string{"main"}
	if v := os.Getenv("OMNIA_IT_PROMPTS"); v != "" {
		prompts = strings.Split(v, ",")
	}
	pack := buildSimplePack(packID, prompts...)
	cfg := buildDeployConfig(env, deployConfigOpts{})
	// Agents are data-plane-auth-gated by default. When a shared-token secret is
	// named, add an externalAuth.sharedToken block so the deployed agent accepts
	// websocket traffic bearing that token (the secret must pre-exist).
	if sec := os.Getenv("OMNIA_IT_SHARED_TOKEN_SECRET"); sec != "" {
		cfg = withSharedToken(t, cfg, sec)
	}

	state, _ := applyAndCollect(t, p, &deploy.PlanRequest{
		PackJSON: pack, DeployConfig: cfg, Environment: env.Workspace,
	})
	st := pollStatus(t, p, cfg, env.Workspace, state, "deployed", 180*time.Second)
	if st.Status != "deployed" {
		t.Fatalf("agent(s) for pack %q did not reach deployed; last = %q (%+v)",
			packID, st.Status, st.Resources)
	}
	t.Logf("agent(s) for pack %q deployed and ready", packID)
}

func TestIntegration_Lifecycle(t *testing.T) {
	env := itEnv(t)
	p := NewProvider()

	packID := uniquePackID(t)
	pack := buildPack(packID)
	cfg := buildDeployConfig(env, deployConfigOpts{createTools: true})

	// --- Apply #1: create from empty prior state ---
	req1 := &deploy.PlanRequest{
		PackJSON:     pack,
		DeployConfig: cfg,
		Environment:  env.Workspace,
		PriorState:   "",
	}
	state1, events1 := applyAndCollect(t, p, req1)
	cleanupDeploy(t, p, env, cfg, state1)
	if state1 == "" {
		t.Fatal("Apply #1 returned empty state")
	}

	created := resourceEventsByType(events1)
	for _, rt := range []string{ResTypePromptPack, ResTypeToolRegistry, ResTypeAgentRuntime} {
		r, ok := created[rt]
		if !ok {
			t.Errorf("Apply #1: no resource event for %q", rt)
			continue
		}
		if r.Status != ResStatusCreated {
			t.Errorf("Apply #1: %s status = %q, want %q", rt, r.Status, ResStatusCreated)
		}
	}

	// --- Status: should converge to deployed ---
	// Right after Apply the pods are still starting, so the agent (and its
	// PromptPack) are transiently not-ready and Status reports "degraded". Poll
	// until the deployment converges; a converged agent reports "deployed".
	st := pollStatus(t, p, cfg, env.Workspace, state1, "deployed", 150*time.Second)
	if st.Status != "deployed" {
		t.Errorf("Status did not converge to deployed within timeout; last = %q, resources = %+v",
			st.Status, st.Resources)
	}

	// --- Apply #2: re-apply against the prior state ---
	req2 := &deploy.PlanRequest{
		PackJSON:     pack,
		DeployConfig: cfg,
		Environment:  env.Workspace,
		PriorState:   state1,
	}
	state2, events2 := applyAndCollect(t, p, req2)
	cleanupDeploy(t, p, env, cfg, state2)

	reapplied := resourceEventsByType(events2)

	// The ToolRegistry is create-only: on re-apply it must be left UNCHANGED,
	// never updated. It surfaces as a ResourceState (in the returned state) with
	// status "unchanged"; it does NOT emit a resource event (the unchanged path
	// reports via a Progress message, not Resource). So check the state, not the
	// event stream, for the registry.
	assertToolRegistryUnchanged(t, state2)

	// PromptPack and AgentRuntime DO emit resource events, with status updated.
	for _, rt := range []string{ResTypePromptPack, ResTypeAgentRuntime} {
		r, ok := reapplied[rt]
		if !ok {
			t.Errorf("Apply #2: no resource event for %q", rt)
			continue
		}
		if r.Status != ResStatusUpdated {
			t.Errorf("Apply #2: %s status = %q, want %q", rt, r.Status, ResStatusUpdated)
		}
	}

	// --- Destroy ---
	destroyState := state2
	if destroyState == "" {
		destroyState = state1
	}
	derr := p.Destroy(context.Background(), &deploy.DestroyRequest{
		DeployConfig: cfg,
		Environment:  env.Workspace,
		PriorState:   destroyState,
	}, func(_ *deploy.DestroyEvent) error { return nil })
	if derr != nil {
		t.Errorf("Destroy: %v", derr)
	}
}

// assertToolRegistryUnchanged parses the apply state JSON and asserts the
// tool_registry resource recorded status "unchanged" (the create-only rule).
func assertToolRegistryUnchanged(t *testing.T, state string) {
	t.Helper()
	var s AdapterState
	if err := json.Unmarshal([]byte(state), &s); err != nil {
		t.Fatalf("parse apply state: %v", err)
	}
	for _, r := range s.Resources {
		if r.Type == ResTypeToolRegistry {
			if r.Status != ResStatusUnchanged {
				t.Errorf("re-apply: tool_registry status = %q, want %q",
					r.Status, ResStatusUnchanged)
			}
			return
		}
	}
	t.Errorf("re-apply: no tool_registry resource in state %s", state)
}

// TestIntegration_BindMode plans a bind-mode deploy (tool_registry_ref to an
// existing workspace registry, no tools block) and asserts it does NOT plan a
// ToolRegistry CREATE — binding an existing registry creates nothing. Plan-only
// so the bind registry need not exist for an apply.
func TestIntegration_BindMode(t *testing.T) {
	env := itEnv(t)
	p := NewProvider()

	packID := uniquePackID(t)
	cfg := buildDeployConfig(env, deployConfigOpts{bindRegistry: env.BindRegistry})
	resp, err := p.Plan(context.Background(), &deploy.PlanRequest{
		PackJSON:     buildPack(packID),
		DeployConfig: cfg,
		Environment:  env.Workspace,
	})
	if err != nil {
		t.Fatalf("Plan (bind mode): %v", err)
	}

	for _, c := range resp.Changes {
		if c.Type == ResTypeToolRegistry && c.Action == deploy.ActionCreate {
			t.Errorf("bind mode planned a ToolRegistry CREATE (%+v); binding an "+
				"existing registry must create nothing", c)
		}
	}
}

// TestIntegration_WorkspaceNormalization sends a mixed-case workspace ("Demo")
// and asserts a normalization advisory mentioning the lowercase slug surfaces
// (via ValidateConfig; falls back to Plan warnings if needed).
func TestIntegration_WorkspaceNormalization(t *testing.T) {
	env := itEnv(t)
	p := NewProvider()

	// Force a mixed-case workspace regardless of the env default: uppercase the
	// first rune of the lowercase slug (e.g. "demo" → "Demo").
	mixed := titleFirst(strings.ToLower(env.Workspace))
	cfg := buildDeployConfig(env, deployConfigOpts{
		createTools:       true,
		workspaceOverride: mixed,
	})

	resp, err := p.ValidateConfig(context.Background(), &deploy.ValidateRequest{Config: cfg})
	if err != nil {
		t.Fatalf("ValidateConfig: %v", err)
	}
	want := strings.ToLower(env.Workspace)
	if !hasWarningContaining(resp.Warnings, want) {
		t.Errorf("expected a normalization warning containing %q; warnings = %v",
			want, resp.Warnings)
	}
}

// ----------------------------------------------------------------------------
// ERROR paths
// ----------------------------------------------------------------------------

// TestIntegration_Plan_InvalidProvider references a provider that does not exist
// and asserts Plan errors with a provider-not-found message.
func TestIntegration_Plan_InvalidProvider(t *testing.T) {
	env := itEnv(t)
	p := NewProvider()

	packID := uniquePackID(t)
	cfg := buildDeployConfig(env, deployConfigOpts{
		createTools:      true,
		providerOverride: "does-not-exist-xyz",
	})
	_, err := p.Plan(context.Background(), &deploy.PlanRequest{
		PackJSON:     buildPack(packID),
		DeployConfig: cfg,
		Environment:  env.Workspace,
	})
	if err == nil {
		t.Fatal("Plan with bad provider ref: expected error, got nil")
	}
	msg := strings.ToLower(err.Error())
	if !strings.Contains(msg, "does-not-exist-xyz") &&
		!strings.Contains(msg, "not found") &&
		!strings.Contains(msg, "provider") {
		t.Errorf("error %q should mention the provider name / not found / provider", err)
	}
}

// TestIntegration_Plan_BadToken supplies an invalid API token and asserts Plan
// fails at AUTH (401/403), NOT with a fake "not found in workspace" message.
// Exact wording varies by deployment, so the robust assertion is: error
// non-nil AND not a provider-validation "not found in workspace" message.
func TestIntegration_Plan_BadToken(t *testing.T) {
	env := itEnv(t)
	p := NewProvider()

	packID := uniquePackID(t)
	cfg := buildDeployConfig(env, deployConfigOpts{
		createTools:   true,
		tokenOverride: "omnia_sk_invalidtoken",
	})
	_, err := p.Plan(context.Background(), &deploy.PlanRequest{
		PackJSON:     buildPack(packID),
		DeployConfig: cfg,
		Environment:  env.Workspace,
	})
	if err == nil {
		t.Fatal("Plan with bad token: expected error, got nil")
	}
	msg := strings.ToLower(err.Error())

	// It must NOT have been misreported as a clean 404 "not found in workspace"
	// — a bad token should fail at auth, not validation. (See
	// describeRefValidationError: a genuine 404 keeps the "not found in
	// workspace" wording; anything else, including 401/403, surfaces the
	// underlying error.)
	if strings.Contains(msg, "not found in workspace") {
		t.Errorf("bad token surfaced as a 404 'not found in workspace' — auth failure "+
			"was mislabeled as validation: %q", err)
	}

	// Best-effort: prefer an auth-ish word if present, but don't hard-require it
	// (wording is deployment-specific). Non-nil + not-a-404 above is the contract.
	authWords := []string{"permission", "forbidden", "unauthorized", "401", "403", "token", "auth"}
	authy := false
	for _, w := range authWords {
		if strings.Contains(msg, w) {
			authy = true
			break
		}
	}
	if !authy {
		t.Logf("bad-token error did not contain an obvious auth keyword (acceptable, "+
			"wording varies): %q", err)
	}
}

// TestIntegration_Plan_BadWorkspace points at a workspace that does not exist
// and asserts Plan errors (provider listing fails in a missing workspace).
func TestIntegration_Plan_BadWorkspace(t *testing.T) {
	env := itEnv(t)
	p := NewProvider()

	packID := uniquePackID(t)
	cfg := buildDeployConfig(env, deployConfigOpts{
		createTools:       true,
		workspaceOverride: "nonexistent-ws-zzz",
	})
	_, err := p.Plan(context.Background(), &deploy.PlanRequest{
		PackJSON:     buildPack(packID),
		DeployConfig: cfg,
		Environment:  "nonexistent-ws-zzz",
	})
	if err == nil {
		t.Fatal("Plan against a missing workspace: expected error, got nil")
	}
}

// TestIntegration_Plan_UnreachableEndpoint points at an unresolvable host with a
// short context deadline and asserts Plan returns a network/transport error.
func TestIntegration_Plan_UnreachableEndpoint(t *testing.T) {
	env := itEnv(t)
	p := NewProvider()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	packID := uniquePackID(t)
	cfg := buildDeployConfig(env, deployConfigOpts{
		createTools:      true,
		endpointOverride: "https://nonexistent.invalid",
	})
	_, err := p.Plan(ctx, &deploy.PlanRequest{
		PackJSON:     buildPack(packID),
		DeployConfig: cfg,
		Environment:  env.Workspace,
	})
	if err == nil {
		t.Fatal("Plan against an unreachable endpoint: expected error, got nil")
	}
}

// TestIntegration_ValidateConfig_Invalid sets BOTH tools and tool_registry_ref
// (mutually exclusive) and asserts validation fails. Pure validation — needs no
// live infra — but stays under the same gating for suite cohesion.
func TestIntegration_ValidateConfig_Invalid(t *testing.T) {
	env := itEnv(t)
	p := NewProvider()

	cfg := buildDeployConfig(env, deployConfigOpts{bothToolsAndRef: "some-registry"})
	resp, err := p.ValidateConfig(context.Background(), &deploy.ValidateRequest{Config: cfg})
	if err != nil {
		t.Fatalf("ValidateConfig: %v", err)
	}
	if resp.Valid {
		t.Error("Valid = true, want false (tools + tool_registry_ref are mutually exclusive)")
	}
	if len(resp.Errors) == 0 {
		t.Error("Errors = empty, want a mutual-exclusivity error")
	}
}

// TestIntegration_BindNonexistentRegistry binds a registry that doesn't exist
// and asserts the warn-don't-block contract: Plan returns NO error (and ideally
// an advisory warning). A missing bind registry must not hard-fail the plan.
func TestIntegration_BindNonexistentRegistry(t *testing.T) {
	env := itEnv(t)
	p := NewProvider()

	packID := uniquePackID(t)
	cfg := buildDeployConfig(env, deployConfigOpts{bindRegistry: "no-such-registry-zzz"})
	resp, err := p.Plan(context.Background(), &deploy.PlanRequest{
		PackJSON:     buildPack(packID),
		DeployConfig: cfg,
		Environment:  env.Workspace,
	})
	if err != nil {
		t.Fatalf("Plan binding a nonexistent registry must warn-don't-block, got error: %v", err)
	}
	if !hasWarningContaining(resp.Warnings, "no-such-registry-zzz") {
		t.Logf("expected an advisory warning mentioning the missing registry "+
			"(acceptable if wording differs); warnings = %v", resp.Warnings)
	}
}
