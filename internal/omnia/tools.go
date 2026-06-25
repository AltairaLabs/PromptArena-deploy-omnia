package omnia

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/AltairaLabs/PromptKit/runtime/prompt"
)

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

// resolveToolBinding decides how a deployment's tools are wired and returns the
// decision plus advisory warnings. It is the single source of truth shared by
// Plan and Apply: everything here is warn-don't-block — a mismatch never errors
// (the AgentRuntime still deploys), only a transport/setup failure does, and
// even a failure to LIST registries degrades to skipping verification rather
// than blocking. The four branches:
//
//   - cfg.Tools set    → create a <pack-id>-tools registry; warn for pack tools
//     with no matching handler (scaffold stubs the operator must complete).
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
		return resolveCreateMode(pack, cfg, packTools)
	case cfg.ToolRegistryRef != "":
		return resolveBindMode(ctx, p, pack, cfg, packTools)
	case len(packTools) > 0:
		return resolveDiscoverMode(ctx, p, cfg, packTools)
	default:
		return ToolBinding{Mode: toolModeNone}, nil, nil
	}
}

// resolveCreateMode handles cfg.Tools being non-empty: a <pack-id>-tools
// registry is synthesized. Each pack tool with no matching handler in cfg.Tools
// gets a scaffold warning — the create-mode body carries the schema, but the
// handler wiring is a stub the operator must complete in Omnia.
func resolveCreateMode(pack *prompt.Pack, cfg *Config, packTools []string) (ToolBinding, []string, error) {
	provided := make(map[string]bool, len(cfg.Tools))
	for i := range cfg.Tools {
		if t := cfg.Tools[i].Tool; t != nil && t.Name != "" {
			provided[t.Name] = true
		}
	}
	var warnings []string
	for _, name := range packTools {
		if !provided[name] {
			warnings = append(warnings, fmt.Sprintf(
				"create-mode scaffold: pack tool %q has no handler in the tools block — "+
					"the agent won't call it until its handler is completed in Omnia", name))
		}
	}
	return ToolBinding{Mode: toolModeCreate, RegistryName: sanitizeName(pack.ID + "-tools")}, warnings, nil
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
			warnings = append(warnings, fmt.Sprintf(
				"tool_registry_ref %q does not provide pack tool %q — the agent won't be able to call it",
				cfg.ToolRegistryRef, name))
			continue
		}
		if schemaDrifts(pack.Tools[name], rt.InputSchema) {
			warnings = append(warnings, fmt.Sprintf(
				"tool %q in registry %q has a different input schema than the pack was tested against — "+
					"calls may fail at runtime", name, cfg.ToolRegistryRef))
		}
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
	best, bestCount := "", 0
	for _, reg := range registries {
		c := coverageCount(reg, packTools)
		if c > bestCount {
			best, bestCount = reg.Name, c
		}
	}

	missing := strings.Join(packTools, ", ")
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
