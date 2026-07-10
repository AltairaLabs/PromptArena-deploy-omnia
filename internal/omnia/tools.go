package omnia

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/AltairaLabs/PromptKit/runtime/prompt"
)

// HTTP-handler config keys and the default method a handler uses when the
// source declares no method (or the tool isn't HTTP-backed).
const (
	keyEndpoint        = "endpoint"
	keyMethod          = "method"
	defaultHTTPMethod  = "POST"
	keyResponseMapping = "responseMapping"
	keyRedact          = "redact"
	keyQueryParams     = "queryParams"
	keyTimeout         = "timeout"
	keyHeaders         = "headers"
	methodGET          = "GET"
	authTypeBearer     = "bearer"
)

// extractSourceTools returns the full HTTP wiring for each arena tool, keyed by
// LLM-facing name. It delegates to parseArenaToolSources (which never fails), so
// a nil/empty ArenaConfig degrades gracefully to the placeholder defaults.
func extractSourceTools(arenaConfigJSON string) map[string]*httpToolSource {
	return parseArenaToolSources(arenaConfigJSON)
}

// ToolBinding is the deterministic decision the resolver reaches about how a
// deployment's tools should be wired. It is computed once at plan time and
// re-computed identically at apply time so apply never surprises plan.
type ToolBinding struct {
	// Mode is one of:
	//   "create" — synthesize a <pack-id>-tools ToolRegistry from cfg.Tools.
	//   "bind"   — bind an existing workspace ToolRegistry by name.
	//   "none"   — bind no registry (the AgentRuntime omits toolRegistryRef).
	Mode string
	// RegistryName is the ToolRegistry the AgentRuntime's toolRegistryRef should
	// point at. Empty when Mode is "none".
	RegistryName string
}

// Tool binding modes.
const (
	toolModeCreate = "create"
	toolModeBind   = "bind"
	toolModeNone   = "none"
)

// toolModeMock is the httpToolSource.Mode value for a pack tool declared with
// no live http block — a mock has no URL and its synthesized handler is a
// placeholder that will fail at runtime on a live deploy.
const toolModeMock = "mock"

// resolveToolBinding decides how a deployment's tools are wired and returns the
// decision plus advisory warnings. It is the single source of truth shared by
// Plan and Apply: everything here is warn-don't-block — a mismatch never errors
// (the AgentRuntime still deploys), only a transport/setup failure does, and
// even a failure to LIST registries degrades to skipping verification rather
// than blocking. The four branches:
//
//   - cfg.Tools set    → create a <pack-id>-tools registry; synthesize a
//     placeholder handler for every pack tool with no matching cfg.Tools handler
//     and merge against the live registry so operator edits are preserved.
//   - ToolRegistryRef  → bind that registry; warn for pack tools it doesn't
//     provide and for matched tools whose input schema drifts from the pack.
//   - pack has tools   → discover: auto-bind iff exactly one registry covers
//     ALL pack tools; otherwise bind none and recommend a fix.
//   - no pack tools    → bind none, no warning.
func resolveToolBinding(
	ctx context.Context, p *Provider, pack *prompt.Pack, cfg *Config,
) (ToolBinding, []string, error) {
	packTools := packToolNames(pack)

	switch {
	case len(cfg.Tools) > 0:
		return resolveCreateMode(ctx, p, pack, cfg)
	case cfg.ToolRegistryRef != "":
		return resolveBindMode(ctx, p, pack, cfg, packTools)
	case len(packTools) > 0 && hasLiveSource(pack, cfg):
		// The pack carries translatable live tools and no explicit binding: create
		// <pack-id>-tools from the arena source (create-only, like the explicit path).
		return resolveCreateMode(ctx, p, pack, cfg)
	case len(packTools) > 0:
		return resolveDiscoverMode(ctx, p, cfg, packTools)
	default:
		return ToolBinding{Mode: toolModeNone}, nil, nil
	}
}

// hasLiveSource reports whether at least one of the pack's tools has a live HTTP
// source (a non-empty URL parsed from req.ArenaConfig). It is the signal that the
// pack carries a translatable tool definition worth synthesizing a registry from;
// without it, auto-creating would only produce placeholders and would ignore a
// real covering registry, so the resolver stays in discover mode.
func hasLiveSource(pack *prompt.Pack, cfg *Config) bool {
	for _, name := range packToolNames(pack) {
		if src := cfg.sourceTools[name]; src != nil && src.URL != "" {
			return true
		}
	}
	return false
}

// registryNameFor returns the create-mode ToolRegistry name for a pack
// (<pack-id>-tools, sanitized). It is the single source of that derivation so
// the resolver, existence check, and builder never drift.
func registryNameFor(pack *prompt.Pack) string {
	return sanitizeName(pack.ID + "-tools")
}

// resolveCreateMode handles cfg.Tools being non-empty: a <pack-id>-tools
// registry is synthesized — but CREATE-ONLY. The registry is written exactly
// once, when it does not yet exist; an existing one is operator-owned and never
// updated. So the resolver only checks existence:
//
//   - registry absent → return the create-time handlers' "created N
//     placeholder(s)" warnings (apply will CREATE it).
//   - registry present → return a single "left unchanged" advisory; apply
//     skips it. The binding still names the registry (the AgentRuntime
//     references it regardless).
//
// A client-build failure or an existence-check error degrades to the
// unchanged note when we can't prove absence, or — for a build failure where we
// never reached the API — to the create-warnings path (apply won't update
// either way, so this is purely advisory).
func resolveCreateMode(
	ctx context.Context, p *Provider, pack *prompt.Pack, cfg *Config,
) (ToolBinding, []string, error) {
	binding := ToolBinding{Mode: toolModeCreate, RegistryName: registryNameFor(pack)}

	client, cerr := p.clientFunc(cfg)
	if cerr != nil {
		// Can't reach the workspace to check existence — degrade to the
		// create-time placeholder warnings (apply won't update an existing one).
		_, warnings := buildCreateRegistryHandlers(pack, cfg)
		return binding, warnings, nil
	}
	exists, eerr := registryExists(ctx, client, pack)
	if eerr != nil || exists {
		// Either it already exists, or we couldn't tell — either way apply will
		// not update it, so emit the operator-owned advisory.
		return binding, []string{fmt.Sprintf(registryUnchangedWarningFmt, binding.RegistryName)}, nil
	}
	_, warnings := buildCreateRegistryHandlers(pack, cfg)
	return binding, warnings, nil
}

// registryUnchangedWarningFmt is the advisory emitted when the create-mode
// registry already exists — it is operator-owned and never updated.
const registryUnchangedWarningFmt = "tool registry %q already exists — left unchanged (operator-owned); " +
	"delete it to re-seed"

// buildCreateRegistryHandlers computes the CREATE-ONLY registry's handlers and
// the advisory warning, given the pack and deploy config. It is only ever used
// to seed a registry that does not yet exist (an existing one is operator-owned
// and never touched), so it has no notion of preserve/remove/drift:
//
//   - Every cfg.Tools handler is authoritative and always included.
//   - Each non-system pack tool NOT covered by cfg.Tools (compared by LLM-facing
//     tool name) is synthesized from its arena source definition: a live tool
//     (one whose source declares a URL) is wired straight to that real URL; a
//     tool with no source URL (mock / no http block) gets a placeholder the
//     operator completes in Omnia.
//
// It returns up to three advisories (via createRegistryWarnings) — one for the
// source-wired handlers, one for placeholders with no source at all, and one
// for tools explicitly declared mock-mode in the pack — so the deploy output
// is explicit about each and a live deploy doesn't silently ship a mock
// placeholder that fails at runtime.
func buildCreateRegistryHandlers(
	pack *prompt.Pack, cfg *Config,
) (handlers []map[string]interface{}, warnings []string) {
	configured := make(map[string]bool, len(cfg.Tools))
	handlers = make([]map[string]interface{}, 0, len(cfg.Tools))
	for i := range cfg.Tools {
		handlers = append(handlers, buildHandlerEntry(&cfg.Tools[i]))
		if t := cfg.Tools[i].Tool; t != nil && t.Name != "" {
			configured[t.Name] = true
		}
	}

	secretName, _ := collectToolCredentials(pack, cfg)

	var sourceWired, placeholders, mockTools []string
	for _, name := range packToolNames(pack) {
		if configured[name] {
			continue // a configured handler is authoritative; skip synthesis
		}
		src := cfg.sourceTools[name]
		handlers = append(handlers, synthesizeHandler(pack.Tools[name], name, src, secretName))
		switch {
		case src != nil && src.URL != "":
			sourceWired = append(sourceWired, name)
		case src != nil && src.Mode == toolModeMock:
			mockTools = append(mockTools, name)
		default:
			placeholders = append(placeholders, name)
		}
	}

	warnings = append(warnings, createRegistryWarnings(sourceWired, placeholders, mockTools)...)
	warnings = append(warnings, headerEnvWarnings(pack, cfg)...)
	return handlers, warnings
}

// createRegistryWarnings builds the advisories for buildCreateRegistryHandlers:
// one for source-wired handlers, one for no-source placeholders, and a distinct
// one for pack tools that are explicitly mock-mode (no live http) — a live
// deploy would otherwise silently ship a placeholder that fails at runtime.
func createRegistryWarnings(sourceWired, placeholders, mockTools []string) []string {
	var warnings []string
	if len(sourceWired) > 0 {
		warnings = append(warnings, fmt.Sprintf(
			"tool registry: wired %d handler(s) from the arena tool definitions (%s) — "+
				"applied only because the registry doesn't already exist",
			len(sourceWired), strings.Join(sourceWired, ", ")))
	}
	if len(placeholders) > 0 {
		warnings = append(warnings, fmt.Sprintf(
			"created %d placeholder handler(s) with no source URL — set real URLs in Omnia: %s",
			len(placeholders), strings.Join(placeholders, ", ")))
	}
	if len(mockTools) > 0 {
		warnings = append(warnings, fmt.Sprintf(
			"tool(s) [%s] are mock-mode in the pack (no live http) — the created placeholder "+
				"handler(s) will fail at runtime on a live deploy; provide live http in the pack "+
				"or a tools: override with real endpoints", strings.Join(mockTools, ", ")))
	}
	return warnings
}

// synthesizeHandler builds an http handler for a non-configured pack tool from
// its arena source definition. When the source declares a URL (a live tool) the
// handler is wired straight to it; otherwise the endpoint is an RFC-2606
// .invalid placeholder that never resolves and fails loudly, so the operator
// knows to set the real URL in Omnia. The method comes from the source too,
// falling back to defaultHTTPMethod when none was declared. The pack tool's
// schema is carried through.
func synthesizeHandler(
	packTool *prompt.PackTool, toolName string, src *httpToolSource, secretName string,
) map[string]interface{} {
	tool := map[string]interface{}{keyName: toolName}
	if packTool != nil {
		tool["description"] = packTool.Description
		tool["inputSchema"] = packTool.Parameters
	}
	entry := map[string]interface{}{
		keyName:       sanitizeName(toolName),
		keyType:       handlerTypeHTTP,
		"tool":        tool,
		keyHTTPConfig: buildSynthHTTPConfig(toolName, src, packTool),
	}
	if src != nil && src.TimeoutMs > 0 {
		entry[keyTimeout] = fmt.Sprintf("%dms", src.TimeoutMs)
	}
	if auth := buildAuthStanza(src, secretName); auth != nil {
		entry["auth"] = auth
	}
	return entry
}

// buildAuthStanza returns the handler-level auth block when the tool declares an
// Authorization header sourced from an env var (headers_from_env
// "Authorization=<ENV>"): a bearer credential resolved from secretName/<ENV>. The
// secret holds the RAW token; the executor prepends "Bearer ". Returns nil when
// the tool declares no Authorization header.
func buildAuthStanza(src *httpToolSource, secretName string) map[string]interface{} {
	if src == nil {
		return nil
	}
	authEnv, _ := parseAuthEnv(src.HeadersFromEnv)
	if authEnv == "" {
		return nil
	}
	return map[string]interface{}{
		keyType: authTypeBearer,
		keySecretRef: map[string]interface{}{
			keyName: secretName,
			"key":   authEnv,
		},
	}
}

// buildSynthHTTPConfig maps a tool's arena HTTP source to an Omnia httpConfig
// block: the real endpoint+method when live (placeholder otherwise), the response
// reshape, redact list, and request mappings the source declares, plus inferred
// GET query params.
func buildSynthHTTPConfig(
	toolName string, src *httpToolSource, packTool *prompt.PackTool,
) map[string]interface{} {
	endpoint := placeholderEndpoint + toolName
	httpMethod := defaultHTTPMethod
	cfg := map[string]interface{}{}
	if src != nil {
		if src.URL != "" {
			endpoint = src.URL
		}
		if src.Method != "" {
			httpMethod = src.Method
		}
		addSourceHTTPMappings(cfg, src)
	}
	cfg[keyEndpoint] = endpoint
	cfg[keyMethod] = httpMethod
	if qp := resolveQueryParams(src, httpMethod, packTool); len(qp) > 0 {
		cfg[keyQueryParams] = qp
	}
	return cfg
}

// addSourceHTTPMappings copies the response/redact/request mappings a source
// declares into an httpConfig map (only non-empty ones).
func addSourceHTTPMappings(cfg map[string]interface{}, src *httpToolSource) {
	if src.ResponseBodyMapping != "" {
		cfg[keyResponseMapping] = src.ResponseBodyMapping
	}
	if len(src.Redact) > 0 {
		cfg[keyRedact] = src.Redact
	}
	if src.RequestBodyMapping != "" {
		cfg["bodyMapping"] = src.RequestBodyMapping
	}
	if len(src.HeaderParams) > 0 {
		cfg["headerParams"] = src.HeaderParams
	}
	if len(src.StaticQuery) > 0 {
		cfg["staticQuery"] = src.StaticQuery
	}
	// Non-Authorization headers_from_env become static headers with their env
	// value resolved at deploy time (Authorization is handled by the auth stanza).
	if headers, _ := buildStaticHeaders(src); len(headers) > 0 {
		cfg[keyHeaders] = headers
	}
}

// resolveQueryParams returns the arg names an httpConfig should send as query
// parameters: the source's explicit request.query_params when set, else — for a
// GET with none declared — every top-level input-schema property (sorted), so GET
// args are sent as query string rather than an absent request body.
func resolveQueryParams(src *httpToolSource, method string, packTool *prompt.PackTool) []string {
	if src != nil && len(src.QueryParams) > 0 {
		return src.QueryParams
	}
	if method != methodGET || packTool == nil {
		return nil
	}
	return inputSchemaPropertyNames(packTool.Parameters)
}

// inputSchemaPropertyNames extracts the sorted top-level property names from a
// JSON-Schema document decoded as interface{} (map[string]interface{}). Returns
// nil when there is no properties object.
func inputSchemaPropertyNames(schema interface{}) []string {
	m, ok := schema.(map[string]interface{})
	if !ok {
		return nil
	}
	props, ok := m["properties"].(map[string]interface{})
	if !ok {
		return nil
	}
	names := make([]string, 0, len(props))
	for name := range props {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// placeholderEndpoint is the RFC-2606 .invalid base URL a placeholder handler
// points at; it never resolves, so a forgotten handler fails loudly.
const placeholderEndpoint = "https://placeholder.invalid/"

// countSynthesizedPackTools returns how many non-system pack tools create mode
// synthesizes beyond the configured ones, split by how they're wired: sourceWired
// is the count whose arena source declares a URL (wired to the real endpoint),
// placeholder is the count with no source URL (an Omnia placeholder). Used for an
// accurate plan summary.
func countSynthesizedPackTools(pack *prompt.Pack, cfg *Config) (sourceWired, placeholder int) {
	configured := make(map[string]bool, len(cfg.Tools))
	for i := range cfg.Tools {
		if t := cfg.Tools[i].Tool; t != nil && t.Name != "" {
			configured[t.Name] = true
		}
	}
	for _, name := range packToolNames(pack) {
		if configured[name] {
			continue
		}
		if src := cfg.sourceTools[name]; src != nil && src.URL != "" {
			sourceWired++
		} else {
			placeholder++
		}
	}
	return sourceWired, placeholder
}

// registryExists reports whether the create-mode <pack-id>-tools ToolRegistry
// already exists in the workspace. It is the gate for the CREATE-ONLY rule: an
// existing registry is operator-owned and never updated. A typed 404 from
// GetResource yields (false, nil); a successful fetch yields (true, nil); any
// other error is returned so the caller can decide how to degrade (it never
// blocks the deploy, since apply won't update an existing registry anyway).
func registryExists(ctx context.Context, client omniaClient, pack *prompt.Pack) (bool, error) {
	_, err := client.GetResource(ctx, ResTypeToolRegistry, registryNameFor(pack))
	if err != nil {
		var he *HTTPError
		if errors.As(err, &he) && he.StatusCode == httpStatusNotFound {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// resolveBindMode handles an explicit tool_registry_ref: the named registry is
// bound. If the workspace registries can be listed, each pack tool the registry
// doesn't provide is warned, and each matched tool whose input schema drifts
// from the pack's is warned. A list failure skips verification (no hard error).
func resolveBindMode(
	ctx context.Context, p *Provider, pack *prompt.Pack, cfg *Config, packTools []string,
) (ToolBinding, []string, error) {
	binding := ToolBinding{Mode: toolModeBind, RegistryName: cfg.ToolRegistryRef}

	client, err := p.clientFunc(cfg)
	if err != nil {
		return binding, nil, fmt.Errorf("omnia: failed to create client for tool binding: %w", err)
	}
	registries, listErr := client.ListToolRegistries(ctx)
	if listErr != nil {
		// Degrade open: can't verify, but the bind is still the decision.
		return binding, nil, nil
	}

	reg, found := findRegistry(registries, cfg.ToolRegistryRef)
	if !found {
		return binding, []string{fmt.Sprintf(
			"tool_registry_ref %q not found in workspace %q — the agent will deploy but "+
				"its tools have no execution wiring until the registry exists",
			cfg.ToolRegistryRef, cfg.Workspace)}, nil
	}

	byName := make(map[string]RegistryTool, len(reg.Tools))
	for _, t := range reg.Tools {
		byName[t.Name] = t
	}

	var warnings []string
	for _, name := range packTools {
		rt, ok := byName[name]
		if !ok {
			// A dynamic registry resolves tools externally (openapi/mcp), so a
			// statically-absent tool may still be provided — don't warn per-tool
			// (the dynamic advisory below covers it). Only a fully-static registry
			// genuinely lacks the tool.
			if !reg.Dynamic {
				warnings = append(warnings, fmt.Sprintf(
					"tool_registry_ref %q does not provide pack tool %q — the agent won't be able to call it",
					cfg.ToolRegistryRef, name))
			}
			continue
		}
		if schemaDrifts(pack.Tools[name], rt.InputSchema) {
			warnings = append(warnings, fmt.Sprintf(
				"tool %q in registry %q has a different input schema than the pack was tested against — "+
					"calls may fail at runtime", name, cfg.ToolRegistryRef))
		}
	}
	if reg.Dynamic {
		warnings = append(warnings, fmt.Sprintf(
			"registry %q resolves its tools dynamically (e.g. an OpenAPI spec or MCP server), so the "+
				"adapter can't statically verify it provides the pack's tools — ensure the source exposes them",
			cfg.ToolRegistryRef))
	}
	return binding, warnings, nil
}

// resolveDiscoverMode handles a pack with tools but neither tools nor
// tool_registry_ref in config. It lists the workspace registries and auto-binds
// iff exactly one covers ALL pack tools (single toolRegistryRef means we can't
// span several). Otherwise it binds none and recommends a concrete fix. A list
// failure degrades to none with no error.
func resolveDiscoverMode(
	ctx context.Context, p *Provider, cfg *Config, packTools []string,
) (ToolBinding, []string, error) {
	client, err := p.clientFunc(cfg)
	if err != nil {
		return ToolBinding{Mode: toolModeNone}, nil, fmt.Errorf(
			"omnia: failed to create client for tool discovery: %w", err)
	}
	registries, listErr := client.ListToolRegistries(ctx)
	if listErr != nil {
		// Can't discover — fall back to the no-handler warning so the operator
		// still learns the pack's tools won't run.
		return ToolBinding{Mode: toolModeNone}, unprovidedToolsWarning(packTools, ""), nil
	}

	var covering []ToolRegistrySummary
	for _, reg := range registries {
		if registryCovers(reg, packTools) {
			covering = append(covering, reg)
		}
	}

	if len(covering) == 1 {
		name := covering[0].Name
		return ToolBinding{Mode: toolModeBind, RegistryName: name}, []string{fmt.Sprintf(
			"auto-bound tools to registry %q — it provides all of the pack's declared tools. "+
				"Set tool_registry_ref: %s to make this explicit.", name, name)}, nil
	}

	// None or several cover all: recommend the best candidate or a tools block.
	return ToolBinding{Mode: toolModeNone}, discoverNoneWarning(registries, packTools), nil
}

// findRegistry returns the registry with the given name.
func findRegistry(registries []ToolRegistrySummary, name string) (ToolRegistrySummary, bool) {
	for _, reg := range registries {
		if reg.Name == name {
			return reg, true
		}
	}
	return ToolRegistrySummary{}, false
}

// registryCovers reports whether the registry provides every name in want.
func registryCovers(reg ToolRegistrySummary, want []string) bool {
	have := make(map[string]bool, len(reg.Tools))
	for _, t := range reg.Tools {
		have[t.Name] = true
	}
	for _, name := range want {
		if !have[name] {
			return false
		}
	}
	return true
}

// discoverNoneWarning builds the advisory for the discover branch when no single
// registry covers all pack tools. It names the registry with the best (highest)
// coverage as the recommended tool_registry_ref, falling back to a tools block.
func discoverNoneWarning(registries []ToolRegistrySummary, packTools []string) []string {
	missing := strings.Join(packTools, ", ")

	var dynamic []string
	best, bestCount := "", 0
	for _, reg := range registries {
		if reg.Dynamic {
			dynamic = append(dynamic, reg.Name)
		}
		if c := coverageCount(reg, packTools); c > bestCount {
			best, bestCount = reg.Name, c
		}
	}

	// A dynamic registry (openapi/mcp) can't be enumerated, so it never "covers"
	// statically — but it may well provide the pack's tools. Recommend it.
	if len(dynamic) > 0 {
		return []string{fmt.Sprintf(
			"pack declares tool(s) [%s]; no registry statically lists all of them, but [%s] "+
				"resolve tools dynamically (e.g. an OpenAPI spec or MCP server) and may provide them — "+
				"set tool_registry_ref to one (coverage can't be verified statically), or add a tools: block.",
			missing, strings.Join(dynamic, ", "))}
	}

	if best == "" {
		return []string{fmt.Sprintf(
			"pack declares tool(s) [%s] but no workspace ToolRegistry provides them and "+
				"deploy.config has neither tools nor tool_registry_ref — these tools won't "+
				"execute. Add a tools: block with handlers, or a tool_registry_ref to a "+
				"registry that provides them.", missing)}
	}
	return []string{fmt.Sprintf(
		"pack declares tool(s) [%s] but no single workspace ToolRegistry provides all of them "+
			"(toolRegistryRef binds exactly one). Closest is %q — set tool_registry_ref: %s "+
			"(and complete the rest), or add a tools: block with handlers.",
		missing, best, best)}
}

// coverageCount returns how many of want the registry provides.
func coverageCount(reg ToolRegistrySummary, want []string) int {
	have := make(map[string]bool, len(reg.Tools))
	for _, t := range reg.Tools {
		have[t.Name] = true
	}
	n := 0
	for _, name := range want {
		if have[name] {
			n++
		}
	}
	return n
}

// unprovidedToolsWarning warns that the pack's tools have no execution wiring.
// registryHint, when set, names a registry to point at.
func unprovidedToolsWarning(packTools []string, registryHint string) []string {
	if len(packTools) == 0 {
		return nil
	}
	missing := strings.Join(packTools, ", ")
	if registryHint != "" {
		return []string{fmt.Sprintf(
			"pack declares tool(s) [%s] — set tool_registry_ref: %s or add a tools: block.",
			missing, registryHint)}
	}
	return []string{fmt.Sprintf(
		"pack declares tool(s) [%s] but deploy.config has no tools and the workspace "+
			"ToolRegistries could not be listed — no registry will be bound and these "+
			"tools won't execute. Add a tools: block, or a tool_registry_ref.", missing)}
}

// packToolNames returns the sorted set of the pack's non-system tool names.
// System-namespaced tools (names containing "__", e.g. image__generate) are
// runtime-provided and need no handler, so they're excluded — matching the
// philosophy of the prior toolCoverageWarnings.
func packToolNames(pack *prompt.Pack) []string {
	if pack == nil || len(pack.Tools) == 0 {
		return nil
	}
	names := make([]string, 0, len(pack.Tools))
	for name := range pack.Tools {
		if !strings.Contains(name, "__") {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

// schemaDrifts reports whether the registry tool's input schema differs from the
// pack tool's declared parameters, comparing normalized (canonical-JSON) forms
// so key ordering and formatting don't register as drift. A pack tool with no
// schema, or a registry with no schema, is treated as no-drift (nothing to
// compare against — absence isn't a mismatch we can act on).
func schemaDrifts(packTool *prompt.PackTool, registrySchema json.RawMessage) bool {
	if packTool == nil || packTool.Parameters == nil || len(registrySchema) == 0 {
		return false
	}
	packNorm, err := normalizeJSON(packTool.Parameters)
	if err != nil {
		return false
	}
	regNorm, err := normalizeRawJSON(registrySchema)
	if err != nil {
		return false
	}
	return packNorm != regNorm
}

// normalizeJSON marshals a value to canonical JSON (Go's encoder sorts map keys).
func normalizeJSON(v interface{}) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// normalizeRawJSON round-trips raw JSON through interface{} so object keys are
// re-sorted into the same canonical order normalizeJSON produces.
func normalizeRawJSON(raw json.RawMessage) (string, error) {
	var v interface{}
	if err := json.Unmarshal(raw, &v); err != nil {
		return "", err
	}
	return normalizeJSON(v)
}
