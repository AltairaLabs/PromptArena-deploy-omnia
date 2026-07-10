package omnia

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/AltairaLabs/PromptKit/runtime/deploy"
	"github.com/AltairaLabs/PromptKit/runtime/prompt"
)

// handlerToolName extracts handler.tool.name from a handler map (test helper for
// asserting the LLM-facing tool name a built handler serves).
func handlerToolName(h map[string]interface{}) string {
	tool, ok := h["tool"].(map[string]interface{})
	if !ok {
		return ""
	}
	name, _ := tool[keyName].(string)
	return name
}

// twoToolPackJSON declares two non-system tools; only `search` is configured in
// twoToolDeployConfig, so `lookup_order` exercises the placeholder path.
const twoToolPackJSON = `{
	"id": "test-pack",
	"version": "1.0.0",
	"prompts": {"main": {"system": "hi"}},
	"tools": {
		"search": {"name": "search", "description": "Search", "parameters": {"type": "object"}},
		"lookup_order": {"name": "lookup_order", "description": "Lookup", "parameters": {"type": "object"}}
	}
}`

const twoToolDeployConfig = `{
	"api_endpoint": "https://omnia.test.com",
	"workspace": "test-ws",
	"api_token": "test-token",
	"providers": {"default": "claude-prod"},
	"tools": [
		{"name": "search", "type": "http",
		 "tool": {"name": "search", "description": "Search", "inputSchema": {"type": "object"}},
		 "httpConfig": {"endpoint": "https://api.example.com/search"}}
	]
}`

// packWithTool builds a single-tool pack whose tool carries the given schema.
func packWithTool(toolName string, schema interface{}) *prompt.Pack {
	return &prompt.Pack{ID: "test-pack", Tools: map[string]*prompt.PackTool{
		toolName: {Name: toolName, Description: "desc-" + toolName, Parameters: schema},
	}}
}

// handlerFor builds an existing-registry handler map for a tool, with an inline
// tool block carrying the given inputSchema and an http endpoint. Used to seed a
// pre-existing operator-owned registry the CREATE-ONLY rule must leave untouched.
func handlerFor(toolName, endpoint string, schema interface{}) map[string]interface{} {
	return map[string]interface{}{
		keyName: sanitizeName(toolName),
		keyType: handlerTypeHTTP,
		"tool": map[string]interface{}{
			keyName:       toolName,
			"description": "operator",
			"inputSchema": schema,
		},
		"httpConfig": map[string]interface{}{"endpoint": endpoint},
	}
}

func TestBuildCreateRegistryHandlers_ConfigOnly(t *testing.T) {
	// The single pack tool IS configured → only config handlers, no extras.
	pack := packWithTool("search", map[string]interface{}{"type": "object"})
	cfg := &Config{Tools: []ToolHandler{{
		Name: "search", Type: handlerTypeHTTP,
		Tool:       &HandlerTool{Name: "search", InputSchema: map[string]interface{}{"type": "object"}},
		HTTPConfig: map[string]interface{}{"endpoint": "https://api.example.com"},
	}}}

	handlers, warnings := buildCreateRegistryHandlers(pack, cfg)
	if len(handlers) != 1 {
		t.Fatalf("want exactly the config handler, got %d", len(handlers))
	}
	if handlerToolName(handlers[0]) != "search" {
		t.Errorf("want config handler for search, got %v", handlers[0])
	}
	if warnings != nil {
		t.Errorf("want no warnings, got %v", warnings)
	}
}

func TestBuildCreateRegistryHandlers_PlaceholderForUnconfigured(t *testing.T) {
	pack := packWithTool("lookup_order", map[string]interface{}{"type": "object"})
	cfg := &Config{} // no configured tools at all

	handlers, warnings := buildCreateRegistryHandlers(pack, cfg)
	if len(handlers) != 1 {
		t.Fatalf("want one placeholder handler, got %d", len(handlers))
	}
	h := handlers[0]
	if h[keyType] != handlerTypeHTTP {
		t.Errorf("placeholder must be http, got %v", h[keyType])
	}
	hc, ok := h["httpConfig"].(map[string]interface{})
	if !ok {
		t.Fatalf("placeholder must carry httpConfig, got %v", h)
	}
	if hc["endpoint"] != "https://placeholder.invalid/lookup_order" {
		t.Errorf("placeholder endpoint = %v", hc["endpoint"])
	}
	if hc["method"] != "POST" {
		t.Errorf("placeholder method = %v, want POST", hc["method"])
	}
	tool := h["tool"].(map[string]interface{})
	if tool["description"] != "desc-lookup_order" {
		t.Errorf("placeholder must carry the pack description, got %v", tool["description"])
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "created 1 placeholder") ||
		!strings.Contains(warnings[0], "lookup_order") {
		t.Errorf("want one created-placeholder warning, got %v", warnings)
	}
}

func TestBuildCreateRegistryHandlers_SystemToolExcluded(t *testing.T) {
	// A system __ tool needs no handler — it must be neither placeholdered nor
	// counted, even with no config tools.
	pack := &prompt.Pack{ID: "test-pack", Tools: map[string]*prompt.PackTool{
		"image__generate": {Name: "image__generate"},
	}}
	handlers, warnings := buildCreateRegistryHandlers(pack, &Config{})
	if len(handlers) != 0 {
		t.Errorf("system tool must not be synthesized, got %d handlers", len(handlers))
	}
	if warnings != nil {
		t.Errorf("want no warnings for system-only pack, got %v", warnings)
	}
}

// --- registryExists --------------------------------------------------------

func TestRegistryExists_404(t *testing.T) {
	sim := newSimulatedClient() // empty store → GetResource returns a typed 404
	exists, err := registryExists(context.Background(), sim, &prompt.Pack{ID: "test-pack"})
	if err != nil {
		t.Fatalf("404 must yield nil error, got %v", err)
	}
	if exists {
		t.Errorf("404 must yield exists=false")
	}
}

func TestRegistryExists_Found(t *testing.T) {
	sim := newSimulatedClient()
	seedToolRegistry(sim, "test-pack", []map[string]interface{}{
		handlerFor("lookup_order", "https://real.example.com/lookup",
			map[string]interface{}{"type": "object"}),
	})
	exists, err := registryExists(context.Background(), sim, &prompt.Pack{ID: "test-pack"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !exists {
		t.Errorf("a seeded registry must yield exists=true")
	}
}

func TestRegistryExists_OtherError(t *testing.T) {
	sim := newSimulatedClient()
	sim.failOn[simKey(ResTypeToolRegistry, "test-pack-tools")] = &HTTPError{
		StatusCode: httpStatusForbidden, Body: "forbidden", Category: ErrCategoryPermission,
	}
	_, err := registryExists(context.Background(), sim, &prompt.Pack{ID: "test-pack"})
	if err == nil {
		t.Fatal("a non-404 error must propagate")
	}
}

// --- Plan create-mode warnings --------------------------------------------

func TestPlan_CreateMode_PlaceholderWarning(t *testing.T) {
	sim := newSimulatedClient()
	sim.validProviders["claude-prod"] = true
	// The registry doesn't exist yet (empty store → 404) → lookup_order gets a placeholder.
	p := &Provider{clientFunc: newSimulatedClientFactory(sim)}

	resp, err := p.Plan(context.Background(), &deploy.PlanRequest{
		PackJSON: twoToolPackJSON, DeployConfig: twoToolDeployConfig,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	joined := strings.Join(resp.Warnings, "\n")
	if !strings.Contains(joined, "created 1 placeholder") || !strings.Contains(joined, "lookup_order") {
		t.Errorf("want a created-placeholder warning naming lookup_order, got %v", resp.Warnings)
	}
	// Absent registry → the plan must show a CREATE for the tool_registry.
	if !hasChange(resp.Changes, ResTypeToolRegistry, deploy.ActionCreate) {
		t.Errorf("want a tool_registry CREATE in the plan, got %+v", resp.Changes)
	}
}

func TestPlan_CreateMode_SourceWiredAndPlaceholderSplit(t *testing.T) {
	sim := newSimulatedClient()
	sim.validProviders["claude-prod"] = true
	p := &Provider{clientFunc: newSimulatedClientFactory(sim)}

	// twoToolArenaConfig declares lookup_order as a live tool with a real URL →
	// source-wired. `search` is configured. No tool is left as a placeholder.
	resp, err := p.Plan(context.Background(), &deploy.PlanRequest{
		PackJSON: twoToolPackJSON, DeployConfig: twoToolDeployConfig, ArenaConfig: twoToolArenaConfig,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var detail string
	for _, c := range resp.Changes {
		if c.Type == ResTypeToolRegistry && c.Action == deploy.ActionCreate {
			detail = c.Detail
		}
	}
	want := "Create ToolRegistry: 2 handlers (1 configured, 1 from source, 0 placeholder)"
	if detail != want {
		t.Errorf("registry detail = %q, want %q", detail, want)
	}

	// The source-wired advisory must name lookup_order; no placeholder advisory.
	joined := strings.Join(resp.Warnings, "\n")
	if !strings.Contains(joined, "wired 1 handler(s) from the arena tool definitions (lookup_order)") {
		t.Errorf("want a source-wired advisory naming lookup_order, got %v", resp.Warnings)
	}
	if strings.Contains(joined, "placeholder handler(s) with no source URL") {
		t.Errorf("no placeholder advisory expected when all uncovered tools are source-wired, got %v", resp.Warnings)
	}
}

func TestPlan_CreateMode_ExistingRegistryLeftUnchanged(t *testing.T) {
	sim := newSimulatedClient()
	sim.validProviders["claude-prod"] = true
	// All managed resources already exist (seeded with managed labels so adopt
	// lists them) — including the operator-owned registry.
	seedManagedResource(sim, ResTypePromptPack, "test-pack", "test-pack")
	seedManagedResource(sim, ResTypeAgentRuntime, "test-pack", "test-pack")
	seedToolRegistry(sim, "test-pack", []map[string]interface{}{
		handlerFor("lookup_order", "https://real.example.com/lookup", map[string]interface{}{"type": "object"}),
	})
	p := &Provider{clientFunc: newSimulatedClientFactory(sim)}

	resp, err := p.Plan(context.Background(), &deploy.PlanRequest{
		PackJSON: twoToolPackJSON, DeployConfig: twoToolDeployConfig,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// CREATE-ONLY: an existing registry emits NO change (neither create nor update).
	for _, c := range resp.Changes {
		if c.Type == ResTypeToolRegistry {
			t.Errorf("an existing tool_registry must emit no change, got %+v", c)
		}
	}
	joined := strings.Join(resp.Warnings, "\n")
	if !strings.Contains(joined, "already exists") || !strings.Contains(joined, "left unchanged") {
		t.Errorf("want a left-unchanged advisory, got %v", resp.Warnings)
	}
	// The OTHER resources must still be updated (adopted as prior).
	if !hasChange(resp.Changes, ResTypePromptPack, deploy.ActionUpdate) {
		t.Errorf("want the prompt_pack to update, got %+v", resp.Changes)
	}
}

// hasChange reports whether changes contains a resource of the given type+action.
func hasChange(changes []deploy.ResourceChange, resType string, action deploy.Action) bool {
	for _, c := range changes {
		if c.Type == resType && c.Action == action {
			return true
		}
	}
	return false
}

// --- Apply create-mode body -----------------------------------------------

func TestApply_CreateMode_BodyHasConfigAndPlaceholder(t *testing.T) {
	reconcilePollInterval = 0
	sim := newSimulatedClient() // empty store → registry created fresh
	sim.agentRuntimeReadyOnGet = true
	sim.validProviders["claude-prod"] = true
	p := &Provider{clientFunc: newSimulatedClientFactory(sim)}

	_, err := p.Apply(context.Background(), &deploy.PlanRequest{
		PackJSON: twoToolPackJSON, DeployConfig: twoToolDeployConfig,
	}, noopApplyCallback)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	reg := sim.resources[simKey(ResTypeToolRegistry, "test-pack-tools")]
	if reg == nil {
		t.Fatal("expected the ToolRegistry to be created")
	}
	handlers := registryHandlers(t, reg.Spec)
	names := map[string]bool{}
	for _, h := range handlers {
		names[handlerToolName(h.(map[string]interface{}))] = true
	}
	if !names["search"] {
		t.Errorf("want the config handler for search, got %v", names)
	}
	if !names["lookup_order"] {
		t.Errorf("want a placeholder handler for lookup_order, got %v", names)
	}
	if !strings.Contains(string(reg.Spec), "https://placeholder.invalid/lookup_order") {
		t.Errorf("want the placeholder endpoint in the body, got %s", reg.Spec)
	}
}

func TestApply_CreateMode_ExistingRegistryNotUpdated(t *testing.T) {
	reconcilePollInterval = 0
	sim := newSimulatedClient()
	sim.agentRuntimeReadyOnGet = true
	sim.validProviders["claude-prod"] = true
	// The operator already owns the registry (seeded with managed labels so adopt
	// lists it as prior). CREATE-ONLY: apply must NOT update it. The other managed
	// resources are seeded too so they adopt-and-update as normal (#41).
	seedManagedResource(sim, ResTypePromptPack, "test-pack", "test-pack")
	seedManagedResource(sim, ResTypeAgentRuntime, "test-pack", "test-pack")
	seedToolRegistry(sim, "test-pack", []map[string]interface{}{
		handlerFor("lookup_order", "https://real.example.com/lookup", map[string]interface{}{"type": "object"}),
	})
	before := sim.resources[simKey(ResTypeToolRegistry, "test-pack-tools")].Metadata.ResourceVersion

	var events []*deploy.ApplyEvent
	p := &Provider{clientFunc: newSimulatedClientFactory(sim)}
	stateJSON, err := p.Apply(context.Background(), &deploy.PlanRequest{
		PackJSON: twoToolPackJSON, DeployConfig: twoToolDeployConfig,
	}, capturingCallback(&events))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	reg := sim.resources[simKey(ResTypeToolRegistry, "test-pack-tools")]
	// No UpdateResource was issued → the resourceVersion is unchanged AND the
	// operator handler URL survives verbatim.
	if reg.Metadata.ResourceVersion != before {
		t.Errorf("registry must not be updated (version changed %s→%s)", before, reg.Metadata.ResourceVersion)
	}
	if !strings.Contains(string(reg.Spec), "https://real.example.com/lookup") {
		t.Errorf("operator handler must survive untouched, got %s", reg.Spec)
	}
	// State records it as unchanged, never updated.
	var state AdapterState
	if uerr := json.Unmarshal([]byte(stateJSON), &state); uerr != nil {
		t.Fatalf("failed to parse state: %v", uerr)
	}
	for _, r := range state.Resources {
		if r.Type == ResTypeToolRegistry && r.Status != ResStatusUnchanged {
			t.Errorf("tool_registry status = %q, want %q", r.Status, ResStatusUnchanged)
		}
	}
	// The "left unchanged" advisory is reported through progress.
	if got := countContaining(progressMessages(events), "left unchanged"); got == 0 {
		t.Errorf("want a left-unchanged progress message, got %v", progressMessages(events))
	}
	// The OTHER resources still update (they were adopted as prior).
	for _, r := range state.Resources {
		if r.Type == ResTypePromptPack && r.Status != ResStatusUpdated {
			t.Errorf("prompt_pack status = %q, want %q", r.Status, ResStatusUpdated)
		}
	}
}

// twoToolArenaConfig is a valid arena source config declaring lookup_order as a
// live GET tool with a real URL, so the create-mode handler for it must carry
// method GET AND wire straight to that URL (source-wired, not a placeholder).
const twoToolArenaConfig = `{
	"tool_specs": {
		"lookup_order": {"description": "Lookup", "mode": "live", "http": {"url": "https://x/lookup", "method": "get"}}
	}
}`

// handlerHTTPConfigFor finds the create-mode handler for toolName in a stored
// registry body and returns its httpConfig map.
func handlerHTTPConfigFor(t *testing.T, spec json.RawMessage, toolName string) map[string]interface{} {
	t.Helper()
	for _, raw := range registryHandlers(t, spec) {
		h := raw.(map[string]interface{})
		if handlerToolName(h) != toolName {
			continue
		}
		hc, ok := h[keyHTTPConfig].(map[string]interface{})
		if !ok {
			t.Fatalf("handler for %q has no httpConfig: %v", toolName, h)
		}
		return hc
	}
	t.Fatalf("no handler found for tool %q", toolName)
	return nil
}

// placeholderMethodFor returns the httpConfig.method of the create-mode handler
// for toolName in a stored registry body.
func placeholderMethodFor(t *testing.T, spec json.RawMessage, toolName string) string {
	t.Helper()
	m, _ := handlerHTTPConfigFor(t, spec, toolName)[keyMethod].(string)
	return m
}

func TestApply_CreateMode_HandlerWiresSourceURLAndMethod(t *testing.T) {
	reconcilePollInterval = 0
	sim := newSimulatedClient() // empty store → registry created fresh
	sim.agentRuntimeReadyOnGet = true
	sim.validProviders["claude-prod"] = true
	p := &Provider{clientFunc: newSimulatedClientFactory(sim)}

	_, err := p.Apply(context.Background(), &deploy.PlanRequest{
		PackJSON: twoToolPackJSON, DeployConfig: twoToolDeployConfig, ArenaConfig: twoToolArenaConfig,
	}, noopApplyCallback)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	reg := sim.resources[simKey(ResTypeToolRegistry, "test-pack-tools")]
	if reg == nil {
		t.Fatal("expected the ToolRegistry to be created")
	}
	// lookup_order is a live GET in the arena source → its handler must carry GET
	// and wire to the real URL, not the hardcoded POST + placeholder.
	hc := handlerHTTPConfigFor(t, reg.Spec, "lookup_order")
	if hc[keyMethod] != "GET" {
		t.Errorf("lookup_order handler method = %q, want GET", hc[keyMethod])
	}
	if hc[keyEndpoint] != "https://x/lookup" {
		t.Errorf("lookup_order handler endpoint = %q, want https://x/lookup", hc[keyEndpoint])
	}
}

func TestApply_CreateMode_PlaceholderDefaultsPOSTWithoutArenaConfig(t *testing.T) {
	reconcilePollInterval = 0
	sim := newSimulatedClient()
	sim.agentRuntimeReadyOnGet = true
	sim.validProviders["claude-prod"] = true
	p := &Provider{clientFunc: newSimulatedClientFactory(sim)}

	// No ArenaConfig → sourceTools is empty → placeholder keeps POST.
	_, err := p.Apply(context.Background(), &deploy.PlanRequest{
		PackJSON: twoToolPackJSON, DeployConfig: twoToolDeployConfig,
	}, noopApplyCallback)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	reg := sim.resources[simKey(ResTypeToolRegistry, "test-pack-tools")]
	if reg == nil {
		t.Fatal("expected the ToolRegistry to be created")
	}
	if m := placeholderMethodFor(t, reg.Spec, "lookup_order"); m != defaultHTTPMethod {
		t.Errorf("lookup_order placeholder method = %q, want %q (POST default)", m, defaultHTTPMethod)
	}
}

func TestApply_CreateMode_AlreadyExistsIsNoOp(t *testing.T) {
	// The adopt-list missed the registry (it carries no managed labels, so it is
	// NOT in priorMap), but the CreateResource call races and returns a 409
	// AlreadyExists. CREATE-ONLY: this must become a no-op (unchanged), NOT an
	// update — unlike the generic applyResourcePhase belt-and-braces fallback.
	reconcilePollInterval = 0
	sim := newSimulatedClient()
	sim.agentRuntimeReadyOnGet = true
	sim.validProviders["claude-prod"] = true
	sim.createAlreadyExists = map[string]bool{
		simKey(ResTypeToolRegistry, "test-pack-tools"): true,
	}
	var events []*deploy.ApplyEvent
	p := &Provider{clientFunc: newSimulatedClientFactory(sim)}
	stateJSON, err := p.Apply(context.Background(), &deploy.PlanRequest{
		PackJSON: twoToolPackJSON, DeployConfig: twoToolDeployConfig,
	}, capturingCallback(&events))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// The seeded (raced) registry must keep its original resourceVersion "1" — no
	// update was issued against it.
	reg := sim.resources[simKey(ResTypeToolRegistry, "test-pack-tools")]
	if reg == nil {
		t.Fatal("expected the raced registry to exist")
	}
	if reg.Metadata.ResourceVersion != "1" {
		t.Errorf("registry must not be updated on AlreadyExists, version = %s", reg.Metadata.ResourceVersion)
	}
	var state AdapterState
	if uerr := json.Unmarshal([]byte(stateJSON), &state); uerr != nil {
		t.Fatalf("failed to parse state: %v", uerr)
	}
	for _, r := range state.Resources {
		if r.Type == ResTypeToolRegistry && r.Status != ResStatusUnchanged {
			t.Errorf("tool_registry status = %q, want %q on AlreadyExists no-op", r.Status, ResStatusUnchanged)
		}
	}
	if got := countContaining(progressMessages(events), "left unchanged"); got == 0 {
		t.Errorf("want a left-unchanged progress message, got %v", progressMessages(events))
	}
}

func TestApply_CreateMode_CreateErrorFailsPhase(t *testing.T) {
	// A non-AlreadyExists create error must fail the registry phase (not no-op).
	reconcilePollInterval = 0
	sim := newSimulatedClient()
	// The AgentRuntime succeeds despite the ToolRegistry failure below, so it goes
	// through reconcile — make it reconcile immediately so the only resource
	// failure this test observes is the injected ToolRegistry one.
	sim.agentRuntimeReadyOnGet = true
	sim.validProviders["claude-prod"] = true
	sim.failOn[simKey(ResTypeToolRegistry, "test-pack-tools")] = &HTTPError{
		StatusCode: httpStatusServerError, Body: "boom", Category: ErrCategoryNetwork,
	}
	p := &Provider{clientFunc: newSimulatedClientFactory(sim)}
	stateJSON, err := p.Apply(context.Background(), &deploy.PlanRequest{
		PackJSON: twoToolPackJSON, DeployConfig: twoToolDeployConfig,
	}, noopApplyCallback)
	if err == nil {
		t.Fatal("a non-AlreadyExists create error must fail the apply")
	}
	var state AdapterState
	if uerr := json.Unmarshal([]byte(stateJSON), &state); uerr != nil {
		t.Fatalf("failed to parse state: %v", uerr)
	}
	for _, r := range state.Resources {
		if r.Type == ResTypeToolRegistry && r.Status != ResStatusFailed {
			t.Errorf("tool_registry status = %q, want %q", r.Status, ResStatusFailed)
		}
	}
}

// registryHandlers extracts spec.handlers[] from a stored registry body.
func registryHandlers(t *testing.T, spec json.RawMessage) []interface{} {
	t.Helper()
	var parsed struct {
		Spec struct {
			Handlers []interface{} `json:"handlers"`
		} `json:"spec"`
	}
	if err := json.Unmarshal(spec, &parsed); err != nil {
		t.Fatalf("failed to parse registry body: %v", err)
	}
	return parsed.Spec.Handlers
}
