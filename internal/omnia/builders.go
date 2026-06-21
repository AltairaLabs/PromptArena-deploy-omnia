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
	req := map[string]interface{}{
		keyMetadata: map[string]interface{}{
			keyName:   sanitizeName(pack.ID),
			keyLabels: buildResourceLabels(pack.ID, pack.Version, ResTypePromptPack, cfg.Labels),
		},
		keySpec: map[string]interface{}{
			"version": pack.Version,
		},
		"content": map[string]string{
			promptPackContentKey: cfg.PackJSON,
		},
	}
	return json.Marshal(req)
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
