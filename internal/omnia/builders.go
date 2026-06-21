package omnia

import (
	"encoding/json"
	"fmt"
	"sort"

	"github.com/AltairaLabs/PromptKit/runtime/deploy/adaptersdk"
	"github.com/AltairaLabs/PromptKit/runtime/prompt"
)

// promptPackContentKey is the ConfigMap data key the dashboard's promptpacks
// route folds pack JSON into.
const promptPackContentKey = "pack.json"

// JSON request body keys used across the resource builders.
const (
	keySpec     = "spec"
	keyLabels   = "labels"
	keyMetadata = "metadata"
	keyName     = "name"
	keyType     = "type"
)

// buildPromptPackRequest builds the JSON body for creating/updating a PromptPack.
// The dashboard's promptpacks route folds body.content into a managed
// ConfigMap and sets spec.source itself, so the adapter only sends the pack
// version in spec and the raw pack JSON in content.
func buildPromptPackRequest(pack *prompt.Pack, cfg *Config) (json.RawMessage, error) {
	spec := map[string]interface{}{
		"version": pack.Version,
	}
	if skills := buildSkillsSpec(cfg.Skills); skills != nil {
		spec["skills"] = skills
	}
	if sc := buildSkillsConfigSpec(cfg.SkillsConfig); sc != nil {
		spec["skillsConfig"] = sc
	}

	req := map[string]interface{}{
		keyMetadata: map[string]interface{}{
			keyName:   sanitizeName(pack.ID),
			keyLabels: buildResourceLabels(pack.ID, pack.Version, ResTypePromptPack, cfg.Labels),
		},
		keySpec: spec,
		"content": map[string]string{
			promptPackContentKey: cfg.PackJSON,
		},
	}
	return json.Marshal(req)
}

// buildSkillsSpec maps the deploy-config skills to spec.skills[], preserving
// order and emitting include/mountAs only when set. Returns nil when empty.
func buildSkillsSpec(skills []SkillBinding) []map[string]interface{} {
	if len(skills) == 0 {
		return nil
	}
	out := make([]map[string]interface{}, 0, len(skills))
	for _, b := range skills {
		entry := map[string]interface{}{"source": b.Source}
		if len(b.Include) > 0 {
			entry["include"] = b.Include
		}
		if b.MountAs != "" {
			entry["mountAs"] = b.MountAs
		}
		out = append(out, entry)
	}
	return out
}

// buildSkillsConfigSpec maps skillsConfig to spec.skillsConfig, emitting only
// the fields the user set. Returns nil when the block has no content.
func buildSkillsConfigSpec(sc *SkillsConfig) map[string]interface{} {
	if sc == nil || (sc.Selector == "" && sc.MaxActive == nil) {
		return nil
	}
	out := map[string]interface{}{}
	if sc.MaxActive != nil {
		out["maxActive"] = *sc.MaxActive
	}
	if sc.Selector != "" {
		out["selector"] = sc.Selector
	}
	return out
}

// buildAgentRuntimeRequest builds the JSON body for creating/updating an AgentRuntime.
func buildAgentRuntimeRequest(
	pack *prompt.Pack, agentName string, cfg *Config,
) (json.RawMessage, error) {
	spec := map[string]interface{}{
		"promptPackRef": map[string]interface{}{
			keyName: sanitizeName(pack.ID),
		},
		"facade": map[string]interface{}{
			keyType:   "websocket",
			"handler": "runtime",
		},
	}

	// Emit one NamedProviderRef per binding, preserving order. The binding
	// named "default" is the runtime's primary provider.
	if len(cfg.Providers) > 0 {
		refs := make([]map[string]interface{}, 0, len(cfg.Providers))
		for _, b := range cfg.Providers {
			role := b.Role
			if role == "" {
				role = roleLLM
			}
			refs = append(refs, map[string]interface{}{
				keyName:       b.Name,
				"providerRef": map[string]interface{}{keyName: b.Ref},
				"role":        role,
			})
		}
		spec["providers"] = refs
	}

	// Tool registry reference (CRD created by the ToolRegistry phase).
	if len(cfg.Tools) > 0 {
		spec["toolRegistryRef"] = map[string]interface{}{
			keyName: sanitizeName(pack.ID + "-tools"),
		}
	}

	if runtime := buildRuntimeSpec(cfg.Runtime); runtime != nil {
		spec["runtime"] = runtime
	}

	if ea := buildExternalAuthSpec(cfg.ExternalAuth); ea != nil {
		spec["externalAuth"] = ea
	}

	req := map[string]interface{}{
		keyMetadata: map[string]interface{}{
			keyName:   sanitizeName(agentName),
			keyLabels: buildResourceLabels(pack.ID, pack.Version, ResTypeAgentRuntime, cfg.Labels),
		},
		keySpec: spec,
	}
	return json.Marshal(req)
}

// buildRuntimeSpec maps the adapter's runtime config to spec.runtime, or nil
// when nothing is configured (so the platform default applies).
func buildRuntimeSpec(rc *RuntimeConfig) map[string]interface{} {
	if rc == nil {
		return nil
	}
	runtime := map[string]interface{}{}
	if rc.Replicas > 0 {
		runtime["replicas"] = rc.Replicas
	}
	if requests := buildResourceRequests(rc); requests != nil {
		runtime["resources"] = map[string]interface{}{"requests": requests}
	}
	if as := buildAutoscalingSpec(rc.Autoscaling); as != nil {
		runtime["autoscaling"] = as
	}
	if len(runtime) == 0 {
		return nil
	}
	return runtime
}

// buildResourceRequests maps cpu/memory into a k8s resource requests map.
func buildResourceRequests(rc *RuntimeConfig) map[string]interface{} {
	requests := map[string]interface{}{}
	if rc.CPU != "" {
		requests["cpu"] = rc.CPU
	}
	if rc.Memory != "" {
		requests["memory"] = rc.Memory
	}
	if len(requests) == 0 {
		return nil
	}
	return requests
}

// buildAutoscalingSpec maps the adapter's autoscaling config to
// spec.runtime.autoscaling (camelCase CRD keys). It is a faithful passthrough:
// only fields the user set are emitted, so unset fields fall back to CRD
// defaults at admission time.
func buildAutoscalingSpec(a *AutoscalingConfig) map[string]interface{} {
	if a == nil {
		return nil
	}
	as := map[string]interface{}{"enabled": a.Enabled}
	if a.Type != "" {
		as["type"] = a.Type
	}
	if a.MinReplicas != nil {
		as["minReplicas"] = *a.MinReplicas
	}
	if a.MaxReplicas != nil {
		as["maxReplicas"] = *a.MaxReplicas
	}
	if a.TargetCPUUtilization != nil {
		as["targetCPUUtilizationPercentage"] = *a.TargetCPUUtilization
	}
	if a.TargetMemoryUtilization != nil {
		as["targetMemoryUtilizationPercentage"] = *a.TargetMemoryUtilization
	}
	if a.ScaleDownStabilizationSeconds != nil {
		as["scaleDownStabilizationSeconds"] = *a.ScaleDownStabilizationSeconds
	}
	return as
}

// buildExternalAuthSpec maps the adapter's externalAuth config to
// spec.externalAuth (AgentExternalAuth), a faithful passthrough that omits any
// unset/empty validator block. Each validator is independent. sharedToken's
// secretRef emits as a LocalObjectReference ({"name": ...}), not a bare
// string. Returns nil when nothing is configured (the agent stays
// management-plane-only).
func buildExternalAuthSpec(ea *ExternalAuthConfig) map[string]interface{} {
	if ea == nil {
		return nil
	}
	out := map[string]interface{}{}
	if ea.AllowManagementPlane != nil {
		out["allowManagementPlane"] = *ea.AllowManagementPlane
	}
	if st := buildSharedTokenSpec(ea.SharedToken); st != nil {
		out["sharedToken"] = st
	}
	if ak := buildAPIKeysSpec(ea.APIKeys); ak != nil {
		out["apiKeys"] = ak
	}
	if oidc := buildOIDCSpec(ea.OIDC); oidc != nil {
		out["oidc"] = oidc
	}
	if et := buildEdgeTrustSpec(ea.EdgeTrust); et != nil {
		out["edgeTrust"] = et
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// buildSharedTokenSpec maps the sharedToken block, emitting secretRef as a
// LocalObjectReference. Returns nil when unset.
func buildSharedTokenSpec(st *SharedTokenAuthConfig) map[string]interface{} {
	if st == nil {
		return nil
	}
	out := map[string]interface{}{
		"secretRef": map[string]interface{}{keyName: st.SecretRef},
	}
	if st.TrustEndUserHeader {
		out["trustEndUserHeader"] = true
	}
	return out
}

// buildAPIKeysSpec maps the apiKeys block, emitting only set fields. Returns
// nil when unset (an empty struct still emits {} so the facade treats keys
// labeled for this agent as valid).
func buildAPIKeysSpec(ak *APIKeysAuthConfig) map[string]interface{} {
	if ak == nil {
		return nil
	}
	out := map[string]interface{}{}
	if ak.DefaultRole != "" {
		out["defaultRole"] = ak.DefaultRole
	}
	if ak.TrustEndUserHeader {
		out["trustEndUserHeader"] = true
	}
	return out
}

// buildOIDCSpec maps the oidc block (issuer+audience required upstream) and
// the optional claimMapping. Returns nil when unset.
func buildOIDCSpec(o *OIDCAuthConfig) map[string]interface{} {
	if o == nil {
		return nil
	}
	out := map[string]interface{}{
		"issuer":   o.Issuer,
		"audience": o.Audience,
	}
	if cm := buildClaimMappingSpec(o.ClaimMapping); cm != nil {
		out["claimMapping"] = cm
	}
	return out
}

// buildClaimMappingSpec maps the optional OIDC claimMapping, emitting only set
// fields. Returns nil when the block has no content.
func buildClaimMappingSpec(cm *OIDCClaimMappingConfig) map[string]interface{} {
	if cm == nil {
		return nil
	}
	out := map[string]interface{}{}
	addStr(out, "subject", cm.Subject)
	addStr(out, "role", cm.Role)
	addStr(out, "endUser", cm.EndUser)
	if len(out) == 0 {
		return nil
	}
	return out
}

// buildEdgeTrustSpec maps the edgeTrust block (headerMapping + claimsFromHeaders),
// emitting only set sub-blocks. Returns nil when the block has no content.
func buildEdgeTrustSpec(et *EdgeTrustAuthConfig) map[string]interface{} {
	if et == nil {
		return nil
	}
	out := map[string]interface{}{}
	if hm := buildHeaderMappingSpec(et.HeaderMapping); hm != nil {
		out["headerMapping"] = hm
	}
	if len(et.ClaimsFromHeaders) > 0 {
		out["claimsFromHeaders"] = et.ClaimsFromHeaders
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// buildHeaderMappingSpec maps the optional edgeTrust headerMapping, emitting
// only set fields. Returns nil when the block has no content.
func buildHeaderMappingSpec(hm *EdgeTrustHeaderMappingConfig) map[string]interface{} {
	if hm == nil {
		return nil
	}
	out := map[string]interface{}{}
	addStr(out, "subject", hm.Subject)
	addStr(out, "role", hm.Role)
	addStr(out, "endUser", hm.EndUser)
	addStr(out, "email", hm.Email)
	if len(out) == 0 {
		return nil
	}
	return out
}

// addStr sets key=v on m only when v is non-empty.
func addStr(m map[string]interface{}, key, v string) {
	if v != "" {
		m[key] = v
	}
}

// buildToolRegistryRequest builds the JSON body for creating/updating a
// ToolRegistry CRD. The handlers are a faithful passthrough of the explicit
// deploy-config tools block to spec.handlers[], preserving order; the inline
// pack.Tools schemas reach the runtime via the PromptPack content fold instead.
func buildToolRegistryRequest(pack *prompt.Pack, cfg *Config) (json.RawMessage, error) {
	handlers := make([]map[string]interface{}, 0, len(cfg.Tools))
	for i := range cfg.Tools {
		handlers = append(handlers, buildHandlerEntry(&cfg.Tools[i]))
	}

	req := map[string]interface{}{
		keyMetadata: map[string]interface{}{
			keyName:   sanitizeName(pack.ID + "-tools"),
			keyLabels: buildResourceLabels(pack.ID, pack.Version, ResTypeToolRegistry, cfg.Labels),
		},
		keySpec: map[string]interface{}{
			"handlers": handlers,
		},
	}
	return json.Marshal(req)
}

// buildHandlerEntry maps one ToolHandler to a spec.handlers[] entry, emitting
// only the present optional blocks.
func buildHandlerEntry(h *ToolHandler) map[string]interface{} {
	entry := map[string]interface{}{
		keyName: h.Name,
		keyType: h.Type,
	}
	if h.Tool != nil {
		tool := map[string]interface{}{
			keyName:       h.Tool.Name,
			"description": h.Tool.Description,
			"inputSchema": h.Tool.InputSchema,
		}
		if h.Tool.OutputSchema != nil {
			tool["outputSchema"] = h.Tool.OutputSchema
		}
		entry["tool"] = tool
	}
	addIfPresent(entry, "selector", h.Selector)
	addIfPresent(entry, "httpConfig", h.HTTPConfig)
	addIfPresent(entry, "openAPIConfig", h.OpenAPIConfig)
	addIfPresent(entry, "grpcConfig", h.GRPCConfig)
	addIfPresent(entry, "mcpConfig", h.MCPConfig)
	addIfPresent(entry, "clientConfig", h.ClientConfig)
	if h.Timeout != "" {
		entry["timeout"] = h.Timeout
	}
	return entry
}

// addIfPresent sets key=m on entry only when m is non-empty.
func addIfPresent(entry map[string]interface{}, key string, m map[string]interface{}) {
	if len(m) > 0 {
		entry[key] = m
	}
}

// buildAgentPolicyRequest builds the JSON body for creating/updating an AgentPolicy CRD.
func buildAgentPolicyRequest(pack *prompt.Pack, cfg *Config) (json.RawMessage, error) {
	var blocklist []string
	for _, p := range pack.Prompts {
		if p != nil && p.ToolPolicy != nil && len(p.ToolPolicy.Blocklist) > 0 {
			blocklist = append(blocklist, p.ToolPolicy.Blocklist...)
		}
	}
	sort.Strings(blocklist)
	// Deduplicate.
	blocklist = dedup(blocklist)

	spec := map[string]interface{}{}
	if len(blocklist) > 0 {
		spec["toolBlocklist"] = blocklist
	}

	req := map[string]interface{}{
		keyMetadata: map[string]interface{}{
			keyName:   sanitizeName(pack.ID + "-policy"),
			keyLabels: buildResourceLabels(pack.ID, pack.Version, ResTypeAgentPolicy, cfg.Labels),
		},
		keySpec: spec,
	}
	return json.Marshal(req)
}

// hasToolPolicy returns true if any prompt in the pack defines a tool policy.
func hasToolPolicy(pack *prompt.Pack) bool {
	for _, p := range pack.Prompts {
		if p != nil && p.ToolPolicy != nil && len(p.ToolPolicy.Blocklist) > 0 {
			return true
		}
	}
	return false
}

// dedup removes duplicate strings from a sorted slice.
func dedup(sorted []string) []string {
	if len(sorted) == 0 {
		return sorted
	}
	result := make([]string, 0, len(sorted))
	result = append(result, sorted[0])
	for i := 1; i < len(sorted); i++ {
		if sorted[i] != sorted[i-1] {
			result = append(result, sorted[i])
		}
	}
	return result
}

// agentRuntimeNames returns the list of agent runtime names to create.
func agentRuntimeNames(pack *prompt.Pack) []string {
	if adaptersdk.IsMultiAgent(pack) {
		agents := adaptersdk.ExtractAgents(pack)
		names := make([]string, len(agents))
		for i, a := range agents {
			names[i] = a.Name
		}
		return names
	}
	name := pack.ID
	if name == "" {
		name = "default"
	}
	return []string{name}
}

// resourceKey returns a unique key for a resource type+name pair.
func resourceKey(typ, name string) string {
	return fmt.Sprintf("%s/%s", typ, name)
}
