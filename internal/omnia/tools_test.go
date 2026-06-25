package omnia

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/AltairaLabs/PromptKit/runtime/prompt"
)

// resolverPack builds a pack whose non-system tools are the given names. Each
// tool carries a simple {"type":"object"} parameter schema unless overridden.
func resolverPack(names ...string) *prompt.Pack {
	tools := make(map[string]*prompt.PackTool, len(names))
	for _, n := range names {
		tools[n] = &prompt.PackTool{
			Name:       n,
			Parameters: map[string]interface{}{"type": "object"},
		}
	}
	return &prompt.Pack{ID: "test-pack", Tools: tools}
}

func resolverProvider(sim *simulatedClient) *Provider {
	return &Provider{clientFunc: newSimulatedClientFactory(sim)}
}

func objSchema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }

// --- Create mode -----------------------------------------------------------

func TestResolveToolBinding_CreateMode_StubWarning(t *testing.T) {
	pack := resolverPack("issue_refund", "lookup_order")
	cfg := &Config{
		Tools: []ToolHandler{{
			Name: "issue-refund", Type: handlerTypeHTTP,
			Tool: &HandlerTool{Name: "issue_refund"},
		}},
	}
	sim := newSimulatedClient()
	binding, warnings, err := resolveToolBinding(context.Background(), resolverProvider(sim), pack, cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if binding.Mode != toolModeCreate || binding.RegistryName != "test-pack-tools" {
		t.Fatalf("want create/test-pack-tools, got %+v", binding)
	}
	// lookup_order has no handler → exactly one stub warning naming it.
	if len(warnings) != 1 || !strings.Contains(warnings[0], "lookup_order") ||
		!strings.Contains(warnings[0], "completed in Omnia") {
		t.Errorf("want one stub warning naming lookup_order, got %v", warnings)
	}
}

func TestResolveToolBinding_CreateMode_AllHandlersNoWarning(t *testing.T) {
	pack := resolverPack("issue_refund")
	cfg := &Config{
		Tools: []ToolHandler{{
			Name: "issue-refund", Type: handlerTypeHTTP,
			Tool: &HandlerTool{Name: "issue_refund"},
		}},
	}
	_, warnings, err := resolveToolBinding(context.Background(), resolverProvider(newSimulatedClient()), pack, cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if warnings != nil {
		t.Errorf("want no warnings when every tool has a handler, got %v", warnings)
	}
}

// --- Bind mode -------------------------------------------------------------

func TestResolveToolBinding_BindMode_MissingAndSchemaDrift(t *testing.T) {
	pack := resolverPack("issue_refund", "lookup_order")
	cfg := &Config{Workspace: "demo", ToolRegistryRef: "refunds"}
	sim := newSimulatedClient()
	sim.toolRegistries = []ToolRegistrySummary{{
		Name: "refunds",
		Tools: []RegistryTool{
			// issue_refund present but with a DIFFERENT schema → drift warning.
			{Name: "issue_refund", InputSchema: json.RawMessage(`{"type":"string"}`)},
			// lookup_order absent → missing warning.
		},
	}}
	binding, warnings, err := resolveToolBinding(context.Background(), resolverProvider(sim), pack, cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if binding.Mode != toolModeBind || binding.RegistryName != "refunds" {
		t.Fatalf("want bind/refunds, got %+v", binding)
	}
	joined := strings.Join(warnings, "\n")
	if !strings.Contains(joined, "does not provide pack tool \"lookup_order\"") {
		t.Errorf("want missing-tool warning for lookup_order, got %v", warnings)
	}
	if !strings.Contains(joined, "different input schema") || !strings.Contains(joined, "issue_refund") {
		t.Errorf("want schema-drift warning for issue_refund, got %v", warnings)
	}
}

func TestResolveToolBinding_BindMode_MatchingSchemaNoWarning(t *testing.T) {
	pack := resolverPack("issue_refund")
	cfg := &Config{Workspace: "demo", ToolRegistryRef: "refunds"}
	sim := newSimulatedClient()
	sim.toolRegistries = []ToolRegistrySummary{{
		Name:  "refunds",
		Tools: []RegistryTool{{Name: "issue_refund", InputSchema: objSchema()}},
	}}
	_, warnings, err := resolveToolBinding(context.Background(), resolverProvider(sim), pack, cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if warnings != nil {
		t.Errorf("want no warnings for full coverage + matching schema, got %v", warnings)
	}
}

func TestResolveToolBinding_BindMode_RegistryNotFound(t *testing.T) {
	pack := resolverPack("issue_refund")
	cfg := &Config{Workspace: "demo", ToolRegistryRef: "missing-registry"}
	sim := newSimulatedClient()
	sim.toolRegistries = []ToolRegistrySummary{{Name: "other"}}
	binding, warnings, err := resolveToolBinding(context.Background(), resolverProvider(sim), pack, cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Still binds the requested ref (warn-don't-block), with a not-found advisory.
	if binding.Mode != toolModeBind || binding.RegistryName != "missing-registry" {
		t.Fatalf("want bind/missing-registry, got %+v", binding)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "not found in workspace") {
		t.Errorf("want one not-found warning, got %v", warnings)
	}
}

func TestResolveToolBinding_BindMode_ListErrorSkipsVerification(t *testing.T) {
	pack := resolverPack("issue_refund")
	cfg := &Config{Workspace: "demo", ToolRegistryRef: "refunds"}
	sim := newSimulatedClient()
	sim.listToolRegistriesErr = fmt.Errorf("forbidden")
	binding, warnings, err := resolveToolBinding(context.Background(), resolverProvider(sim), pack, cfg)
	if err != nil {
		t.Fatalf("list error must NOT hard-fail, got: %v", err)
	}
	if binding.Mode != toolModeBind || binding.RegistryName != "refunds" {
		t.Fatalf("want bind/refunds, got %+v", binding)
	}
	if warnings != nil {
		t.Errorf("list error should skip verification (no warnings), got %v", warnings)
	}
}

// --- Discover mode ---------------------------------------------------------

func TestResolveToolBinding_Discover_AutoBindSingle(t *testing.T) {
	pack := resolverPack("issue_refund", "lookup_order")
	cfg := &Config{Workspace: "demo"}
	sim := newSimulatedClient()
	sim.toolRegistries = []ToolRegistrySummary{
		{Name: "refunds", Tools: []RegistryTool{
			{Name: "issue_refund"}, {Name: "lookup_order"}, {Name: "extra"},
		}},
		{Name: "partial", Tools: []RegistryTool{{Name: "issue_refund"}}},
	}
	binding, warnings, err := resolveToolBinding(context.Background(), resolverProvider(sim), pack, cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if binding.Mode != toolModeBind || binding.RegistryName != "refunds" {
		t.Fatalf("want auto-bind to refunds, got %+v", binding)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "auto-bound tools to registry \"refunds\"") {
		t.Errorf("want one auto-bind info warning, got %v", warnings)
	}
}

func TestResolveToolBinding_Discover_MultipleCoverWarns(t *testing.T) {
	pack := resolverPack("issue_refund")
	cfg := &Config{Workspace: "demo"}
	sim := newSimulatedClient()
	// Two registries each cover the single pack tool → ambiguous, bind none.
	sim.toolRegistries = []ToolRegistrySummary{
		{Name: "a", Tools: []RegistryTool{{Name: "issue_refund"}}},
		{Name: "b", Tools: []RegistryTool{{Name: "issue_refund"}}},
	}
	binding, warnings, err := resolveToolBinding(context.Background(), resolverProvider(sim), pack, cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if binding.Mode != toolModeNone || binding.RegistryName != "" {
		t.Fatalf("want none on ambiguous coverage, got %+v", binding)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "no single workspace ToolRegistry") {
		t.Errorf("want one ambiguity warning, got %v", warnings)
	}
}

func TestResolveToolBinding_Discover_NoneCoverWarns(t *testing.T) {
	pack := resolverPack("issue_refund", "lookup_order")
	cfg := &Config{Workspace: "demo"}
	sim := newSimulatedClient()
	// One registry partially covers → recommend it as the closest candidate.
	sim.toolRegistries = []ToolRegistrySummary{
		{Name: "partial", Tools: []RegistryTool{{Name: "issue_refund"}}},
	}
	binding, warnings, err := resolveToolBinding(context.Background(), resolverProvider(sim), pack, cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if binding.Mode != toolModeNone {
		t.Fatalf("want none when no registry covers all, got %+v", binding)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "Closest is \"partial\"") {
		t.Errorf("want one closest-candidate warning, got %v", warnings)
	}
}

func TestResolveToolBinding_Discover_NoRegistriesWarns(t *testing.T) {
	pack := resolverPack("issue_refund")
	cfg := &Config{Workspace: "demo"}
	sim := newSimulatedClient() // toolRegistries nil
	binding, warnings, err := resolveToolBinding(context.Background(), resolverProvider(sim), pack, cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if binding.Mode != toolModeNone {
		t.Fatalf("want none with no registries, got %+v", binding)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "no workspace ToolRegistry provides them") {
		t.Errorf("want one no-registry warning, got %v", warnings)
	}
}

func TestResolveToolBinding_Discover_ListErrorFallsBack(t *testing.T) {
	pack := resolverPack("issue_refund")
	cfg := &Config{Workspace: "demo"}
	sim := newSimulatedClient()
	sim.listToolRegistriesErr = fmt.Errorf("forbidden")
	binding, warnings, err := resolveToolBinding(context.Background(), resolverProvider(sim), pack, cfg)
	if err != nil {
		t.Fatalf("list error must NOT hard-fail, got: %v", err)
	}
	if binding.Mode != toolModeNone {
		t.Fatalf("want none on list error, got %+v", binding)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "could not be listed") {
		t.Errorf("want one list-failure warning, got %v", warnings)
	}
}

// --- No pack tools ---------------------------------------------------------

func TestResolveToolBinding_NoPackTools_None(t *testing.T) {
	pack := &prompt.Pack{ID: "test-pack"}
	binding, warnings, err := resolveToolBinding(
		context.Background(), resolverProvider(newSimulatedClient()), pack, &Config{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if binding.Mode != toolModeNone || warnings != nil {
		t.Errorf("want none/no-warnings for a pack with no tools, got %+v / %v", binding, warnings)
	}
}

func TestResolveToolBinding_SystemToolsExcluded(t *testing.T) {
	// A pack whose only tool is system-namespaced (image__generate) needs no
	// handler and must resolve to none with no warning.
	pack := &prompt.Pack{ID: "test-pack", Tools: map[string]*prompt.PackTool{
		"image__generate": {Name: "image__generate"},
	}}
	binding, warnings, err := resolveToolBinding(
		context.Background(), resolverProvider(newSimulatedClient()), pack, &Config{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if binding.Mode != toolModeNone || warnings != nil {
		t.Errorf("want none/no-warnings for system-only tools, got %+v / %v", binding, warnings)
	}
}

// --- Helpers ---------------------------------------------------------------

func TestSchemaDrifts(t *testing.T) {
	matching := &prompt.PackTool{Parameters: map[string]interface{}{
		"type": "object", "properties": map[string]interface{}{"a": map[string]interface{}{"type": "string"}},
	}}
	// Same content, different key order in the raw form → must NOT drift.
	sameDiffOrder := json.RawMessage(`{"properties":{"a":{"type":"string"}},"type":"object"}`)
	if schemaDrifts(matching, sameDiffOrder) {
		t.Error("normalized-equal schemas must not be reported as drift")
	}
	if !schemaDrifts(matching, json.RawMessage(`{"type":"string"}`)) {
		t.Error("differing schemas must be reported as drift")
	}
	// Absent schemas on either side → no actionable drift.
	if schemaDrifts(nil, objSchema()) {
		t.Error("nil pack tool must not drift")
	}
	if schemaDrifts(&prompt.PackTool{}, objSchema()) {
		t.Error("pack tool with no parameters must not drift")
	}
	if schemaDrifts(matching, nil) {
		t.Error("empty registry schema must not drift")
	}
}

func TestDryRunToolBinding(t *testing.T) {
	pack := &prompt.Pack{ID: "test-pack"}
	if b := dryRunToolBinding(pack, &Config{Tools: []ToolHandler{{}}}); b.Mode != toolModeCreate ||
		b.RegistryName != "test-pack-tools" {
		t.Errorf("tools → create/test-pack-tools, got %+v", b)
	}
	if b := dryRunToolBinding(pack, &Config{ToolRegistryRef: "shared"}); b.Mode != toolModeBind ||
		b.RegistryName != "shared" {
		t.Errorf("ref → bind/shared, got %+v", b)
	}
	if b := dryRunToolBinding(pack, &Config{}); b.Mode != toolModeNone {
		t.Errorf("neither → none, got %+v", b)
	}
}
