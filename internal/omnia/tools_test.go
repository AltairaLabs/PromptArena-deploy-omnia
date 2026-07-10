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

// --- Auto-create from live source ------------------------------------------

func TestResolveToolBinding_AutoCreateFromLiveSource(t *testing.T) {
	pack := resolverPack("do_it")
	// A pack with a live tool source and NO explicit tools:/tool_registry_ref must
	// auto-create <pack>-tools from the source (create-only), not fall to discover.
	cfg := &Config{
		Workspace:   "demo",
		sourceTools: map[string]*httpToolSource{"do_it": {Mode: "live", Method: "POST", URL: "https://api/do"}},
	}
	sim := newSimulatedClient() // no existing registries
	binding, _, err := resolveToolBinding(context.Background(), resolverProvider(sim), pack, cfg)
	if err != nil {
		t.Fatalf("resolveToolBinding: %v", err)
	}
	if binding.Mode != toolModeCreate || binding.RegistryName != sanitizeName("test-pack-tools") {
		t.Errorf("want create mode test-pack-tools, got %+v", binding)
	}
}

func TestResolveToolBinding_NoLiveSource_KeepsDiscover(t *testing.T) {
	pack := resolverPack("do_it")
	// No sourceTools (no live URL) → must NOT auto-create; falls to discover and
	// auto-binds the covering registry, preserving the prior behavior.
	cfg := &Config{Workspace: "demo"}
	sim := newSimulatedClient()
	sim.toolRegistries = []ToolRegistrySummary{{Name: "covering", Tools: []RegistryTool{{Name: "do_it"}}}}
	binding, _, err := resolveToolBinding(context.Background(), resolverProvider(sim), pack, cfg)
	if err != nil {
		t.Fatalf("resolveToolBinding: %v", err)
	}
	if binding.Mode != toolModeBind || binding.RegistryName != "covering" {
		t.Errorf("no-live-source must keep discover/auto-bind, got %+v", binding)
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

func TestResolveToolBinding_CreateMode_ExistingRegistryLeftUnchanged(t *testing.T) {
	pack := resolverPack("issue_refund", "lookup_order")
	cfg := &Config{
		Tools: []ToolHandler{{
			Name: "issue-refund", Type: handlerTypeHTTP,
			Tool: &HandlerTool{Name: "issue_refund"},
		}},
	}
	sim := newSimulatedClient()
	// The registry already exists (operator-owned). CREATE-ONLY: the resolver
	// must NOT emit placeholder warnings — only the single left-unchanged advisory.
	seedToolRegistry(sim, "test-pack", []map[string]interface{}{{
		keyName: "lookup-order", keyType: handlerTypeHTTP,
		"tool": map[string]interface{}{
			keyName: "lookup_order", "inputSchema": map[string]interface{}{"type": "object"},
		},
		"httpConfig": map[string]interface{}{"endpoint": "https://real.example.com/lookup"},
	}})
	binding, warnings, err := resolveToolBinding(context.Background(), resolverProvider(sim), pack, cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// The AgentRuntime still references the registry by name.
	if binding.Mode != toolModeCreate || binding.RegistryName != "test-pack-tools" {
		t.Fatalf("want create/test-pack-tools, got %+v", binding)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "already exists") ||
		!strings.Contains(warnings[0], "left unchanged") {
		t.Errorf("want one left-unchanged advisory, got %v", warnings)
	}
}

func TestResolveToolBinding_CreateMode_ExistenceErrorDegradesToUnchanged(t *testing.T) {
	pack := resolverPack("issue_refund", "lookup_order")
	cfg := &Config{Tools: []ToolHandler{{
		Name: "issue-refund", Type: handlerTypeHTTP, Tool: &HandlerTool{Name: "issue_refund"},
	}}}
	sim := newSimulatedClient()
	// A non-404 existence-check error must NOT block and must NOT emit placeholder
	// warnings (apply won't update either way) — it degrades to the unchanged note.
	sim.failOn[simKey(ResTypeToolRegistry, "test-pack-tools")] = &HTTPError{
		StatusCode: httpStatusForbidden, Body: "forbidden", Category: ErrCategoryPermission,
	}
	binding, warnings, err := resolveToolBinding(context.Background(), resolverProvider(sim), pack, cfg)
	if err != nil {
		t.Fatalf("existence-check error must not hard-fail, got %v", err)
	}
	if binding.Mode != toolModeCreate {
		t.Fatalf("want create mode, got %+v", binding)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "left unchanged") {
		t.Errorf("want one left-unchanged advisory on existence error, got %v", warnings)
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

// --- Source HTTP method + URL extraction ----------------------------------

func TestExtractSourceTools_FromToolSpecs(t *testing.T) {
	// A minimal valid ArenaConfig: tool_specs map with per-tool http.{url,method}.
	// A GET tool with a URL, a POST tool (lower-case method → upper-cased) with a
	// URL, and a tool with no http block (a mock tool) — now PRESENT with an empty
	// url/method and its mode carried, which Task 5 relies on.
	arenaConfig := `{
		"tool_specs": {
			"list_user_exercises": {"description": "list", "mode": "live", "http": {"url": "https://x/list", "method": "get"}},
			"create_workout": {"description": "create", "mode": "live", "http": {"url": "https://x/create", "method": "POST"}},
			"local_only": {"description": "no http", "mode": "mock"}
		}
	}`
	tools := extractSourceTools(arenaConfig)
	if got := tools["list_user_exercises"]; got.Method != "GET" || got.URL != "https://x/list" {
		t.Errorf("list_user_exercises = %+v, want {GET https://x/list}", got)
	}
	if got := tools["create_workout"]; got.Method != "POST" || got.URL != "https://x/create" {
		t.Errorf("create_workout = %+v, want {POST https://x/create}", got)
	}
	// A no-http (mock) tool is now present with an empty url/method and its mode
	// carried — Task 5 (mock detection) relies on it; deploy output is unchanged
	// because call sites treat an empty URL as a placeholder, exactly as before.
	if got := tools["local_only"]; got == nil || got.URL != "" || got.Method != "" || got.Mode != "mock" {
		t.Errorf("local_only must be present with empty url/method and mode=mock, got %+v", got)
	}
}

func TestExtractSourceTools_DegradesToEmptyNonNil(t *testing.T) {
	for _, in := range []string{"", "not json", "{"} {
		tools := extractSourceTools(in)
		if tools == nil {
			t.Errorf("input %q: map must be non-nil for graceful degradation", in)
		}
		if len(tools) != 0 {
			t.Errorf("input %q: map must be empty, got %v", in, tools)
		}
	}
}

// --- synthesizeHandler: real URL for live tools, placeholder otherwise ------

func TestSynthesizeHandler_UsesSourceURLAndMethod(t *testing.T) {
	packTool := &prompt.PackTool{Name: "list_user_exercises", Description: "list"}

	// Source carries a real URL + method → both wired straight through.
	h := synthesizeHandler(packTool, "list_user_exercises", &httpToolSource{Method: "GET", URL: "https://x/list"}, "")
	hc := h[keyHTTPConfig].(map[string]interface{})
	if hc[keyMethod] != "GET" {
		t.Errorf("method with source GET = %v, want GET", hc[keyMethod])
	}
	if hc[keyEndpoint] != "https://x/list" {
		t.Errorf("endpoint with source URL = %v, want https://x/list", hc[keyEndpoint])
	}

	// Empty source → placeholder URL + POST default.
	def := synthesizeHandler(packTool, "list_user_exercises", &httpToolSource{}, "")
	dc := def[keyHTTPConfig].(map[string]interface{})
	if dc[keyMethod] != defaultHTTPMethod {
		t.Errorf("empty source method must fall back to %q, got %v", defaultHTTPMethod, dc[keyMethod])
	}
	if dc[keyEndpoint] != placeholderEndpoint+"list_user_exercises" {
		t.Errorf("empty source URL must fall back to placeholder, got %v", dc[keyEndpoint])
	}

	// URL empty but method set → placeholder URL with that method.
	mo := synthesizeHandler(packTool, "list_user_exercises", &httpToolSource{Method: "DELETE"}, "")
	mc := mo[keyHTTPConfig].(map[string]interface{})
	if mc[keyMethod] != "DELETE" {
		t.Errorf("method-only source must keep its method, got %v", mc[keyMethod])
	}
	if mc[keyEndpoint] != placeholderEndpoint+"list_user_exercises" {
		t.Errorf("method-only source must still use the placeholder URL, got %v", mc[keyEndpoint])
	}
}

// --- Create-build wires source URLs + emits both advisories ----------------

func TestBuildCreateRegistryHandlers_SourceWiredAndPlaceholder(t *testing.T) {
	pack := resolverPack("list_user_exercises", "issue_refund")
	cfg := &Config{
		// list_user_exercises is a live GET (has a URL) → source-wired; issue_refund
		// is absent from the map → placeholder + POST default.
		sourceTools: map[string]*httpToolSource{
			"list_user_exercises": {Method: "GET", URL: "https://x/list"},
		},
	}
	handlers, warnings := buildCreateRegistryHandlers(pack, cfg)

	byTool := map[string]map[string]interface{}{}
	for _, h := range handlers {
		byTool[handlerToolName(h)] = h
	}

	live := byTool["list_user_exercises"][keyHTTPConfig].(map[string]interface{})
	if live[keyMethod] != "GET" || live[keyEndpoint] != "https://x/list" {
		t.Errorf("list_user_exercises handler = %v, want GET https://x/list", live)
	}
	ph := byTool["issue_refund"][keyHTTPConfig].(map[string]interface{})
	if ph[keyMethod] != defaultHTTPMethod || ph[keyEndpoint] != placeholderEndpoint+"issue_refund" {
		t.Errorf("issue_refund handler = %v, want POST placeholder", ph)
	}

	if len(warnings) != 2 {
		t.Fatalf("want 2 advisories (source-wired + placeholder), got %d: %v", len(warnings), warnings)
	}
	if !strings.Contains(warnings[0], "wired 1 handler(s) from the arena tool definitions (list_user_exercises)") {
		t.Errorf("source-wired advisory = %q", warnings[0])
	}
	if !strings.Contains(warnings[1], "created 1 placeholder handler(s) with no source URL — set real URLs in Omnia: issue_refund") {
		t.Errorf("placeholder advisory = %q", warnings[1])
	}
}

func TestBuildCreateRegistryHandlers_AllConfiguredNoAdvisories(t *testing.T) {
	pack := resolverPack("search")
	cfg := &Config{Tools: []ToolHandler{{
		Name: "search", Type: handlerTypeHTTP,
		Tool:       &HandlerTool{Name: "search", Description: "Search", InputSchema: map[string]interface{}{"type": "object"}},
		HTTPConfig: map[string]interface{}{keyEndpoint: "https://api.example.com/search"},
	}}}
	_, warnings := buildCreateRegistryHandlers(pack, cfg)
	if len(warnings) != 0 {
		t.Errorf("all-configured must emit no advisories, got %v", warnings)
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

func TestSynthesizeHandler_RichHTTPConfig(t *testing.T) {
	src := &httpToolSource{
		Mode: "live", Method: "POST",
		URL:                 "https://api.splitpantz.com/api/v1/workouts",
		TimeoutMs:           15000,
		Redact:              []string{"user_id"},
		ResponseBodyMapping: "{id: id, name: name}",
	}
	h := synthesizeHandler(nil, "create_workout", src, "")
	cfg, _ := h[keyHTTPConfig].(map[string]interface{})
	if cfg["responseMapping"] != "{id: id, name: name}" {
		t.Errorf("responseMapping = %v", cfg["responseMapping"])
	}
	if cfg["timeout"] != nil {
		t.Errorf("timeout must be handler-level, not in httpConfig")
	}
	if h["timeout"] != "15000ms" {
		t.Errorf("handler timeout = %v, want 15000ms", h["timeout"])
	}
	redact, _ := cfg["redact"].([]string)
	if len(redact) != 1 || redact[0] != "user_id" {
		t.Errorf("redact = %v", cfg["redact"])
	}
}

func TestSynthesizeHandler_GETQueryParamsInferred(t *testing.T) {
	packTool := &prompt.PackTool{
		Name: "list_user_exercises",
		Parameters: map[string]interface{}{
			"type":       "object",
			"properties": map[string]interface{}{"search": map[string]interface{}{"type": "string"}},
		},
	}
	src := &httpToolSource{Mode: "live", Method: "GET", URL: "https://api.splitpantz.com/api/v1/exercises"}
	h := synthesizeHandler(packTool, "list_user_exercises", src, "")
	cfg := h[keyHTTPConfig].(map[string]interface{})
	qp, _ := cfg[keyQueryParams].([]string)
	if len(qp) != 1 || qp[0] != "search" {
		t.Errorf("queryParams = %v, want [search]", cfg[keyQueryParams])
	}
}

func TestSynthesizeHandler_POSTNoQueryParams(t *testing.T) {
	packTool := &prompt.PackTool{Parameters: map[string]interface{}{
		"properties": map[string]interface{}{"name": map[string]interface{}{"type": "string"}}}}
	src := &httpToolSource{Method: "POST", URL: "https://x/y"}
	h := synthesizeHandler(packTool, "create_x", src, "")
	if _, ok := h[keyHTTPConfig].(map[string]interface{})[keyQueryParams]; ok {
		t.Errorf("POST must not infer queryParams")
	}
}

func TestSynthesizeHandler_GETExplicitQueryParamsWin(t *testing.T) {
	packTool := &prompt.PackTool{Parameters: map[string]interface{}{
		"properties": map[string]interface{}{"a": map[string]interface{}{}, "b": map[string]interface{}{}}}}
	src := &httpToolSource{Method: "GET", URL: "https://x", QueryParams: []string{"a"}}
	h := synthesizeHandler(packTool, "t", src, "")
	qp := h[keyHTTPConfig].(map[string]interface{})[keyQueryParams].([]string)
	if len(qp) != 1 || qp[0] != "a" {
		t.Errorf("explicit queryParams must win, got %v", qp)
	}
}

func TestSynthesizeHandler_RequestMappings(t *testing.T) {
	src := &httpToolSource{
		Method:             "POST",
		URL:                "https://x/y",
		RequestBodyMapping: "{payload: input}",
		HeaderParams:       map[string]string{"X-Tenant": "tenant_id"},
		StaticQuery:        map[string]string{"v": "2"},
	}
	cfg := synthesizeHandler(nil, "create_x", src, "")[keyHTTPConfig].(map[string]interface{})
	if cfg["bodyMapping"] != "{payload: input}" {
		t.Errorf("bodyMapping = %v", cfg["bodyMapping"])
	}
	hp, _ := cfg["headerParams"].(map[string]string)
	if hp["X-Tenant"] != "tenant_id" {
		t.Errorf("headerParams = %v", cfg["headerParams"])
	}
	sq, _ := cfg["staticQuery"].(map[string]string)
	if sq["v"] != "2" {
		t.Errorf("staticQuery = %v", cfg["staticQuery"])
	}
}

func TestSynthesizeHandler_EmitsBearerAuthStanza(t *testing.T) {
	src := &httpToolSource{Mode: "live", Method: "GET", URL: "https://api.github.com/rate_limit",
		HeadersFromEnv: []string{"Authorization=GITHUB_TOKEN"}}
	h := synthesizeHandler(nil, "github_rate_limit", src, "p-tool-credentials")
	auth, ok := h["auth"].(map[string]interface{})
	if !ok {
		t.Fatalf("no auth stanza: %v", h)
	}
	if auth["type"] != "bearer" {
		t.Errorf("auth type = %v, want bearer", auth["type"])
	}
	ref, _ := auth["secretRef"].(map[string]interface{})
	if ref["name"] != "p-tool-credentials" || ref["key"] != "GITHUB_TOKEN" {
		t.Errorf("secretRef = %v", ref)
	}
	// A tool with no Authorization header emits no auth stanza.
	h2 := synthesizeHandler(nil, "no_auth", &httpToolSource{URL: "https://x"}, "p-tool-credentials")
	if _, has := h2["auth"]; has {
		t.Errorf("no-auth tool must not emit an auth stanza: %v", h2)
	}
}

func TestSynthesizeHandler_EmitsStaticHeaderFromEnv(t *testing.T) {
	t.Setenv("WORKOUT_ACT_AS_USER", "user-42")
	src := &httpToolSource{Mode: "live", Method: "POST", URL: "https://x/y",
		HeadersFromEnv: []string{"Authorization=SPLITZ_AUTH", "X-Act-As-User=WORKOUT_ACT_AS_USER"}}
	h := synthesizeHandler(nil, "create_workout", src, "p-tool-credentials")
	hc := h[keyHTTPConfig].(map[string]interface{})
	headers, _ := hc["headers"].(map[string]string)
	if headers["X-Act-As-User"] != "user-42" {
		t.Errorf("static header not emitted from env: %v", hc["headers"])
	}
	// Authorization stays the auth stanza, never a static header.
	if _, ok := headers["Authorization"]; ok {
		t.Errorf("Authorization must not be static: %v", headers)
	}
	if _, ok := h["auth"]; !ok {
		t.Error("Authorization must still produce an auth stanza")
	}
}

// --- Mock-mode pack tools get a distinct plan-time warning ------------------

func TestBuildCreateRegistryHandlers_MockToolWarning(t *testing.T) {
	pack := &prompt.Pack{ID: "p", Tools: map[string]*prompt.PackTool{
		"search_shared_workouts": {Name: "search_shared_workouts", Description: "d",
			Parameters: map[string]interface{}{}},
	}}
	cfg := &Config{sourceTools: map[string]*httpToolSource{
		"search_shared_workouts": {Mode: "mock"}, // no URL, mock mode
	}}
	_, warnings := buildCreateRegistryHandlers(pack, cfg)
	// NOTE: not using a hasWarningContaining helper here — an identical one
	// already exists in integration_test.go, but it's gated behind the
	// "integration" build tag; redefining it in this (untagged) file would
	// collide when CI builds with -tags=integration.
	joined := strings.Join(warnings, "\n")
	if !strings.Contains(joined, "mock") || !strings.Contains(joined, "search_shared_workouts") {
		t.Errorf("expected a mock-mode warning naming the tool, got %v", warnings)
	}
}
