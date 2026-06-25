package omnia

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/AltairaLabs/PromptKit/runtime/deploy"
	"github.com/AltairaLabs/PromptKit/runtime/prompt"
)

// twoToolPackJSON declares two non-system tools; only `search` is configured in
// twoToolDeployConfig, so `lookup_order` exercises the placeholder/preserve path.
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
// tool block carrying the given inputSchema and an http endpoint.
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

func TestMergeRegistryHandlers_ConfigOnly(t *testing.T) {
	// The single pack tool IS configured → only config handlers, no extras.
	pack := packWithTool("search", map[string]interface{}{"type": "object"})
	cfg := &Config{Tools: []ToolHandler{{
		Name: "search", Type: handlerTypeHTTP,
		Tool:       &HandlerTool{Name: "search", InputSchema: map[string]interface{}{"type": "object"}},
		HTTPConfig: map[string]interface{}{"endpoint": "https://api.example.com"},
	}}}

	handlers, warnings := mergeRegistryHandlers(pack, cfg, nil)
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

func TestMergeRegistryHandlers_PlaceholderForUnconfigured(t *testing.T) {
	pack := packWithTool("lookup_order", map[string]interface{}{"type": "object"})
	cfg := &Config{} // no configured tools at all

	handlers, warnings := mergeRegistryHandlers(pack, cfg, nil)
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

func TestMergeRegistryHandlers_PreservesExistingOperatorHandler(t *testing.T) {
	pack := packWithTool("lookup_order", map[string]interface{}{"type": "object"})
	cfg := &Config{}
	existing := []map[string]interface{}{
		handlerFor("lookup_order", "https://real.example.com/lookup",
			map[string]interface{}{"type": "object"}),
	}

	handlers, warnings := mergeRegistryHandlers(pack, cfg, existing)
	if len(handlers) != 1 {
		t.Fatalf("want one handler, got %d", len(handlers))
	}
	hc := handlers[0]["httpConfig"].(map[string]interface{})
	if hc["endpoint"] != "https://real.example.com/lookup" {
		t.Errorf("operator handler URL must be preserved verbatim, got %v", hc["endpoint"])
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "preserved 1 operator-managed") {
		t.Errorf("want one preserved warning, got %v", warnings)
	}
}

func TestMergeRegistryHandlers_RemovesDroppedTool(t *testing.T) {
	// The pack no longer declares old_tool, but the live registry still has a
	// handler for it → drop it and warn.
	pack := packWithTool("search", map[string]interface{}{"type": "object"})
	cfg := &Config{Tools: []ToolHandler{{
		Name: "search", Type: handlerTypeHTTP,
		Tool: &HandlerTool{Name: "search", InputSchema: map[string]interface{}{"type": "object"}},
	}}}
	existing := []map[string]interface{}{
		handlerFor("old_tool", "https://real.example.com/old", map[string]interface{}{"type": "object"}),
	}

	handlers, warnings := mergeRegistryHandlers(pack, cfg, existing)
	for _, h := range handlers {
		if handlerToolName(h) == "old_tool" {
			t.Errorf("dropped tool old_tool must not appear in handlers, got %v", h)
		}
	}
	joined := strings.Join(warnings, "\n")
	if !strings.Contains(joined, "removed 1 handler(s)") || !strings.Contains(joined, "old_tool") {
		t.Errorf("want a removal warning naming old_tool, got %v", warnings)
	}
}

func TestMergeRegistryHandlers_SystemToolExcluded(t *testing.T) {
	// A system __ tool needs no handler — it must be neither placeholdered nor
	// counted, even with no config tools.
	pack := &prompt.Pack{ID: "test-pack", Tools: map[string]*prompt.PackTool{
		"image__generate": {Name: "image__generate"},
	}}
	handlers, warnings := mergeRegistryHandlers(pack, &Config{}, nil)
	if len(handlers) != 0 {
		t.Errorf("system tool must not be synthesized, got %d handlers", len(handlers))
	}
	if warnings != nil {
		t.Errorf("want no warnings for system-only pack, got %v", warnings)
	}
}

func TestMergeRegistryHandlers_SchemaDriftOnPreservedWarns(t *testing.T) {
	// The pack tool's schema changed, but the operator's handler is preserved.
	pack := packWithTool("lookup_order", map[string]interface{}{
		"type": "object", "properties": map[string]interface{}{"id": map[string]interface{}{"type": "string"}},
	})
	cfg := &Config{}
	existing := []map[string]interface{}{
		handlerFor("lookup_order", "https://real.example.com/lookup",
			map[string]interface{}{"type": "object"}), // old/drifted schema
	}

	handlers, warnings := mergeRegistryHandlers(pack, cfg, existing)
	hc := handlers[0]["httpConfig"].(map[string]interface{})
	if hc["endpoint"] != "https://real.example.com/lookup" {
		t.Errorf("preserved handler must be untouched despite drift, got %v", hc["endpoint"])
	}
	joined := strings.Join(warnings, "\n")
	if !strings.Contains(joined, "preserved 1 operator-managed") {
		t.Errorf("want preserved warning, got %v", warnings)
	}
	if !strings.Contains(joined, "schema changed") || !strings.Contains(joined, "lookup_order") {
		t.Errorf("want a drift warning naming lookup_order, got %v", warnings)
	}
}

func TestExistingHandlerSchemaDrifts_NoToolBlock(t *testing.T) {
	// A handler with no inline tool block can't drift (nothing to compare).
	if existingHandlerSchemaDrifts(
		&prompt.PackTool{Parameters: map[string]interface{}{"type": "object"}},
		map[string]interface{}{keyName: "h", keyType: handlerTypeHTTP},
	) {
		t.Error("a handler with no tool block must not register drift")
	}
}

// --- currentRegistryHandlers ----------------------------------------------

func TestCurrentRegistryHandlers_404(t *testing.T) {
	sim := newSimulatedClient() // empty store → GetResource returns a typed 404
	p := resolverProvider(sim)
	handlers, err := p.currentRegistryHandlers(context.Background(), sim, &prompt.Pack{ID: "test-pack"})
	if err != nil {
		t.Fatalf("404 must yield nil error, got %v", err)
	}
	if handlers != nil {
		t.Errorf("404 must yield nil handlers, got %v", handlers)
	}
}

func TestCurrentRegistryHandlers_OtherError(t *testing.T) {
	sim := newSimulatedClient()
	sim.failOn[simKey(ResTypeToolRegistry, "test-pack-tools")] = fmt.Errorf("boom")
	p := resolverProvider(sim)
	_, err := p.currentRegistryHandlers(context.Background(), sim, &prompt.Pack{ID: "test-pack"})
	if err == nil {
		t.Fatal("a non-404 error must propagate")
	}
}

func TestCurrentRegistryHandlers_Success(t *testing.T) {
	sim := newSimulatedClient()
	seedToolRegistry(sim, "test-pack", []map[string]interface{}{
		handlerFor("lookup_order", "https://real.example.com/lookup",
			map[string]interface{}{"type": "object"}),
	})
	p := resolverProvider(sim)
	handlers, err := p.currentRegistryHandlers(context.Background(), sim, &prompt.Pack{ID: "test-pack"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(handlers) != 1 || handlerToolName(handlers[0]) != "lookup_order" {
		t.Errorf("want the seeded handler parsed back, got %v", handlers)
	}
}

func TestCurrentRegistryHandlers_EmptySpec(t *testing.T) {
	sim := newSimulatedClient()
	sim.mu.Lock()
	sim.resources[simKey(ResTypeToolRegistry, "test-pack-tools")] = &ResourceResponse{
		Kind:     ResTypeToolRegistry,
		Metadata: ResourceMetadata{Name: "test-pack-tools"},
	}
	sim.mu.Unlock()
	p := resolverProvider(sim)
	handlers, err := p.currentRegistryHandlers(context.Background(), sim, &prompt.Pack{ID: "test-pack"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if handlers != nil {
		t.Errorf("empty spec must yield nil handlers, got %v", handlers)
	}
}

func TestCurrentRegistryHandlers_MalformedSpec(t *testing.T) {
	sim := newSimulatedClient()
	sim.mu.Lock()
	sim.resources[simKey(ResTypeToolRegistry, "test-pack-tools")] = &ResourceResponse{
		Kind:     ResTypeToolRegistry,
		Metadata: ResourceMetadata{Name: "test-pack-tools"},
		Spec:     json.RawMessage(`{"handlers": "not-an-array"}`),
	}
	sim.mu.Unlock()
	p := resolverProvider(sim)
	_, err := p.currentRegistryHandlers(context.Background(), sim, &prompt.Pack{ID: "test-pack"})
	if err == nil {
		t.Fatal("malformed handlers must produce a parse error")
	}
}

// --- Plan create-mode merge warnings --------------------------------------

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
}

func TestPlan_CreateMode_PreservedAndRemovedWarnings(t *testing.T) {
	sim := newSimulatedClient()
	sim.validProviders["claude-prod"] = true
	// Live registry: an operator-completed lookup_order + a stale handler for a
	// tool no longer in the pack → preserve one, remove the other.
	seedToolRegistry(sim, "test-pack", []map[string]interface{}{
		handlerFor("lookup_order", "https://real.example.com/lookup", map[string]interface{}{"type": "object"}),
		handlerFor("gone_tool", "https://real.example.com/gone", map[string]interface{}{"type": "object"}),
	})
	p := &Provider{clientFunc: newSimulatedClientFactory(sim)}

	resp, err := p.Plan(context.Background(), &deploy.PlanRequest{
		PackJSON: twoToolPackJSON, DeployConfig: twoToolDeployConfig,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	joined := strings.Join(resp.Warnings, "\n")
	if !strings.Contains(joined, "preserved 1 operator-managed") {
		t.Errorf("want a preserved warning, got %v", resp.Warnings)
	}
	if !strings.Contains(joined, "removed 1 handler(s)") || !strings.Contains(joined, "gone_tool") {
		t.Errorf("want a removed warning naming gone_tool, got %v", resp.Warnings)
	}
}

// --- Apply create-mode merge body -----------------------------------------

func TestApply_CreateMode_BodyHasConfigAndPlaceholder(t *testing.T) {
	sim := newSimulatedClient() // empty store → registry created fresh
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

func TestApply_CreateMode_PreservesOperatorHandler(t *testing.T) {
	sim := newSimulatedClient()
	sim.validProviders["claude-prod"] = true
	// Operator completed lookup_order before re-apply.
	seedToolRegistry(sim, "test-pack", []map[string]interface{}{
		handlerFor("lookup_order", "https://real.example.com/lookup", map[string]interface{}{"type": "object"}),
	})
	p := &Provider{clientFunc: newSimulatedClientFactory(sim)}

	_, err := p.Apply(context.Background(), &deploy.PlanRequest{
		PackJSON: twoToolPackJSON, DeployConfig: twoToolDeployConfig,
	}, noopApplyCallback)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	reg := sim.resources[simKey(ResTypeToolRegistry, "test-pack-tools")]
	if reg == nil {
		t.Fatal("expected the ToolRegistry to exist")
	}
	if !strings.Contains(string(reg.Spec), "https://real.example.com/lookup") {
		t.Errorf("operator handler URL must survive re-apply, got %s", reg.Spec)
	}
	if strings.Contains(string(reg.Spec), "placeholder.invalid/lookup_order") {
		t.Errorf("a preserved operator handler must not be reverted to a placeholder, got %s", reg.Spec)
	}
}

func TestApply_CreateMode_ReadErrorFailsPhase(t *testing.T) {
	sim := newSimulatedClient()
	sim.validProviders["claude-prod"] = true
	// A non-404 GetResource failure on the registry must fail the phase — the
	// adapter must never clobber operator edits it couldn't read.
	sim.failOn[simKey(ResTypeToolRegistry, "test-pack-tools")] = &HTTPError{
		StatusCode: httpStatusForbidden, Body: "forbidden", Category: ErrCategoryPermission,
	}
	p := &Provider{clientFunc: newSimulatedClientFactory(sim)}

	stateJSON, err := p.Apply(context.Background(), &deploy.PlanRequest{
		PackJSON: twoToolPackJSON, DeployConfig: twoToolDeployConfig,
	}, noopApplyCallback)
	if err == nil {
		t.Fatal("a non-404 read error must fail the apply")
	}
	var state AdapterState
	if uerr := json.Unmarshal([]byte(stateJSON), &state); uerr != nil {
		t.Fatalf("failed to parse state: %v", uerr)
	}
	for _, r := range state.Resources {
		if r.Type == ResTypeToolRegistry && r.Status != ResStatusFailed {
			t.Errorf("ToolRegistry must be marked failed, got %q", r.Status)
		}
	}
}

// twoToolArenaConfig is a valid arena source config declaring lookup_order as a
// GET tool, so the create-mode placeholder for it must carry method GET.
const twoToolArenaConfig = `{
	"tool_specs": {
		"lookup_order": {"description": "Lookup", "mode": "live", "http": {"url": "https://x/lookup", "method": "get"}}
	}
}`

// placeholderMethodFor finds the create-mode placeholder for toolName in a
// stored registry body and returns its httpConfig.method.
func placeholderMethodFor(t *testing.T, spec json.RawMessage, toolName string) string {
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
		m, _ := hc[keyMethod].(string)
		return m
	}
	t.Fatalf("no handler found for tool %q", toolName)
	return ""
}

func TestApply_CreateMode_PlaceholderUsesSourceMethod(t *testing.T) {
	sim := newSimulatedClient() // empty store → registry created fresh
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
	// lookup_order is GET in the arena source → its placeholder must be GET, not
	// the hardcoded POST default.
	if m := placeholderMethodFor(t, reg.Spec, "lookup_order"); m != "GET" {
		t.Errorf("lookup_order placeholder method = %q, want GET", m)
	}
}

func TestApply_CreateMode_PlaceholderDefaultsPOSTWithoutArenaConfig(t *testing.T) {
	sim := newSimulatedClient()
	sim.validProviders["claude-prod"] = true
	p := &Provider{clientFunc: newSimulatedClientFactory(sim)}

	// No ArenaConfig → sourceToolMethods is empty → placeholder keeps POST.
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
