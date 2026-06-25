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

func TestResolveToolBinding_BindMode_DynamicRegistry(t *testing.T) {
	pack := resolverPack("create_split", "list_splits")
	cfg := &Config{Workspace: "demo", ToolRegistryRef: "splitpantz-api"}
	sim := newSimulatedClient()
	// An openapi-backed registry exposes no inline tools (Dynamic): coverage can't
	// be verified statically, so there must be no per-tool "missing" warnings —
	// only a single advisory that verification was skipped.
	sim.toolRegistries = []ToolRegistrySummary{{Name: "splitpantz-api", Dynamic: true}}

	binding, warnings, err := resolveToolBinding(context.Background(), resolverProvider(sim), pack, cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if binding.Mode != toolModeBind || binding.RegistryName != "splitpantz-api" {
		t.Fatalf("want bind/splitpantz-api, got %+v", binding)
	}
	joined := strings.Join(warnings, "\n")
	if strings.Contains(joined, "does not provide pack tool") {
		t.Errorf("a dynamic registry must not produce per-tool missing warnings, got %v", warnings)
	}
	if !strings.Contains(joined, "resolves its tools dynamically") {
		t.Errorf("want a dynamic-registry advisory, got %v", warnings)
	}
}

func TestResolveToolBinding_Discover_DynamicCandidate(t *testing.T) {
	pack := resolverPack("create_split")
	cfg := &Config{Workspace: "demo"}
	sim := newSimulatedClient()
	// No static registry lists the tool, but a dynamic one may provide it — it
	// should be recommended (not auto-bound, since coverage is unverifiable).
	sim.toolRegistries = []ToolRegistrySummary{
		{Name: "other", Tools: []RegistryTool{{Name: "unrelated"}}},
		{Name: "splitpantz-api", Dynamic: true},
	}

	binding, warnings, err := resolveToolBinding(context.Background(), resolverProvider(sim), pack, cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if binding.Mode != toolModeNone {
		t.Fatalf("want none (dynamic registry not auto-bound), got %+v", binding)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "splitpantz-api") ||
		!strings.Contains(warnings[0], "resolve tools dynamically") {
		t.Errorf("want a dynamic-candidate recommendation naming splitpantz-api, got %v", warnings)
	}
}

// --- Create mode -----------------------------------------------------------

// seedToolRegistry stores a <pack>-tools ToolRegistry whose spec.handlers are
// the given maps, carrying the adapter's managed-by + pack-id labels so adopt
// reconciles it as prior state (apply then UPDATEs rather than CREATEs). The
// merge reads these handlers back and preserves operator-owned ones.
func seedToolRegistry(sim *simulatedClient, packID string, handlers []map[string]interface{}) {
	spec, _ := json.Marshal(map[string]interface{}{"handlers": handlers})
	sim.mu.Lock()
	defer sim.mu.Unlock()
	name := sanitizeName(packID + "-tools")
	sim.resources[simKey(ResTypeToolRegistry, name)] = &ResourceResponse{
		Kind: ResTypeToolRegistry,
		Metadata: ResourceMetadata{
			Name: name, UID: "uid-" + name, ResourceVersion: "1",
			Labels: map[string]string{LabelManagedBy: managedByValue, LabelPackID: packID},
		},
		Spec: spec,
	}
}

func TestResolveToolBinding_CreateMode_PlaceholderWarning(t *testing.T) {
	pack := resolverPack("issue_refund", "lookup_order")
	cfg := &Config{
		Tools: []ToolHandler{{
			Name: "issue-refund", Type: handlerTypeHTTP,
			Tool: &HandlerTool{Name: "issue_refund"},
		}},
	}
	sim := newSimulatedClient() // empty store → registry 404s → placeholder path
	binding, warnings, err := resolveToolBinding(context.Background(), resolverProvider(sim), pack, cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if binding.Mode != toolModeCreate || binding.RegistryName != "test-pack-tools" {
		t.Fatalf("want create/test-pack-tools, got %+v", binding)
	}
	// lookup_order has no handler and no existing → one created-placeholder warning.
	if len(warnings) != 1 || !strings.Contains(warnings[0], "lookup_order") ||
		!strings.Contains(warnings[0], "placeholder handler") {
		t.Errorf("want one created-placeholder warning naming lookup_order, got %v", warnings)
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
	sim := newSimulatedClient() // empty store → registry 404s
	_, warnings, err := resolveToolBinding(context.Background(), resolverProvider(sim), pack, cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if warnings != nil {
		t.Errorf("want no warnings when every tool has a handler, got %v", warnings)
	}
}

func TestResolveToolBinding_CreateMode_PreservesOperatorHandler(t *testing.T) {
	pack := resolverPack("issue_refund", "lookup_order")
	cfg := &Config{
		Tools: []ToolHandler{{
			Name: "issue-refund", Type: handlerTypeHTTP,
			Tool: &HandlerTool{Name: "issue_refund"},
		}},
	}
	sim := newSimulatedClient()
	// The operator completed lookup_order's handler in Omnia (real URL). Its
	// inputSchema matches the pack so only the preserved warning fires (no drift).
	seedToolRegistry(sim, "test-pack", []map[string]interface{}{{
		keyName: "lookup-order", keyType: handlerTypeHTTP,
		"tool": map[string]interface{}{
			keyName: "lookup_order", "inputSchema": map[string]interface{}{"type": "object"},
		},
		"httpConfig": map[string]interface{}{"endpoint": "https://real.example.com/lookup"},
	}})
	_, warnings, err := resolveToolBinding(context.Background(), resolverProvider(sim), pack, cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "preserved 1 operator-managed") {
		t.Errorf("want one preserved-handler warning, got %v", warnings)
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

// --- Source HTTP-method extraction ----------------------------------------

func TestExtractSourceToolMethods_FromToolSpecs(t *testing.T) {
	// A minimal valid ArenaConfig: tool_specs map with per-tool http.method. A
	// GET tool, a POST tool (lower-case → upper-cased), and a method-less tool
	// (omitted from the map).
	arenaConfig := `{
		"tool_specs": {
			"list_user_exercises": {"description": "list", "mode": "live", "http": {"url": "https://x/list", "method": "get"}},
			"create_workout": {"description": "create", "mode": "live", "http": {"url": "https://x/create", "method": "POST"}},
			"local_only": {"description": "no http", "mode": "mock"}
		}
	}`
	methods := extractSourceToolMethods(arenaConfig)
	if methods["list_user_exercises"] != "GET" {
		t.Errorf("list_user_exercises method = %q, want GET", methods["list_user_exercises"])
	}
	if methods["create_workout"] != "POST" {
		t.Errorf("create_workout method = %q, want POST", methods["create_workout"])
	}
	if _, ok := methods["local_only"]; ok {
		t.Errorf("a tool with no http method must be omitted, got %v", methods)
	}
}

func TestExtractSourceToolMethods_DegradesToEmptyNonNil(t *testing.T) {
	for _, in := range []string{"", "not json", "{"} {
		methods := extractSourceToolMethods(in)
		if methods == nil {
			t.Errorf("input %q: map must be non-nil for graceful degradation", in)
		}
		if len(methods) != 0 {
			t.Errorf("input %q: map must be empty, got %v", in, methods)
		}
	}
}

// --- Placeholder handler method --------------------------------------------

func TestPlaceholderHandler_UsesSourceMethod(t *testing.T) {
	packTool := &prompt.PackTool{Name: "list_user_exercises", Description: "list"}

	h := placeholderHandler(packTool, "list_user_exercises", "GET")
	hc := h[keyHTTPConfig].(map[string]interface{})
	if hc[keyMethod] != "GET" {
		t.Errorf("method with source GET = %v, want GET", hc[keyMethod])
	}
	if hc[keyEndpoint] != placeholderEndpoint+"list_user_exercises" {
		t.Errorf("endpoint placeholder must be unchanged, got %v", hc[keyEndpoint])
	}

	def := placeholderHandler(packTool, "list_user_exercises", "")
	dc := def[keyHTTPConfig].(map[string]interface{})
	if dc[keyMethod] != defaultHTTPMethod {
		t.Errorf("empty method must fall back to %q, got %v", defaultHTTPMethod, dc[keyMethod])
	}
}

// --- Merge wires the per-tool method ---------------------------------------

func TestMergeRegistryHandlers_AppliesSourceMethod(t *testing.T) {
	pack := resolverPack("list_user_exercises", "issue_refund")
	cfg := &Config{
		// list_user_exercises is a GET in the source; issue_refund is absent from
		// the map → it must default to POST.
		sourceToolMethods: map[string]string{"list_user_exercises": "GET"},
	}
	handlers, _ := mergeRegistryHandlers(pack, cfg, nil)

	byTool := map[string]map[string]interface{}{}
	for _, h := range handlers {
		byTool[handlerToolName(h)] = h
	}

	get := byTool["list_user_exercises"][keyHTTPConfig].(map[string]interface{})
	if get[keyMethod] != "GET" {
		t.Errorf("list_user_exercises placeholder method = %v, want GET", get[keyMethod])
	}
	post := byTool["issue_refund"][keyHTTPConfig].(map[string]interface{})
	if post[keyMethod] != defaultHTTPMethod {
		t.Errorf("issue_refund (absent from map) method = %v, want %q", post[keyMethod], defaultHTTPMethod)
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
