package omnia

import (
	"encoding/json"
	"fmt"
	"sort"

	"github.com/AltairaLabs/PromptKit/runtime/deploy/adaptersdk"
	"github.com/AltairaLabs/PromptKit/runtime/prompt"
)

// configMapRequest builds the JSON body for creating/updating a ConfigMap
// that holds the raw pack JSON data.
func buildConfigMapRequest(pack *prompt.Pack, cfg *Config) (json.RawMessage, error) {
	req := map[string]interface{}{
		"kind": "ConfigMap",
		"metadata": map[string]interface{}{
			"name":   sanitizeName(pack.ID + "-packdata"),
			"labels": buildResourceLabels(pack.ID, pack.Version, ResTypeConfigMap, cfg.Labels),
		},
		"data": map[string]string{
			"pack.json": cfg.PackJSON,
		},
	}
	return json.Marshal(req)
}

// buildPromptPackRequest builds the JSON body for creating/updating a PromptPack CRD.
func buildPromptPackRequest(pack *prompt.Pack, cfg *Config) (json.RawMessage, error) {
	// Resolve provider mapping.
	providerRef := ""
	if defaultProvider, ok := cfg.Providers["default"]; ok {
		providerRef = defaultProvider
	}

	spec := map[string]interface{}{
		"packId":       pack.ID,
		"version":      pack.Version,
		"configMapRef": sanitizeName(pack.ID + "-packdata"),
	}
	if providerRef != "" {
		spec["providerRef"] = providerRef
	}
	if pack.Description != "" {
		spec["description"] = pack.Description
	}

	req := map[string]interface{}{
		"kind": "PromptPack",
		"metadata": map[string]interface{}{
			"name":   sanitizeName(pack.ID),
			"labels": buildResourceLabels(pack.ID, pack.Version, ResTypePromptPack, cfg.Labels),
		},
		"spec": spec,
	}
	return json.Marshal(req)
}

// buildAgentRuntimeRequest builds the JSON body for creating/updating an AgentRuntime CRD.
func buildAgentRuntimeRequest(
	pack *prompt.Pack, agentName string, cfg *Config,
) (json.RawMessage, error) {
	spec := map[string]interface{}{
		"promptPackRef": sanitizeName(pack.ID),
	}

	// Multi-agent: reference the specific member prompt.
	if adaptersdk.IsMultiAgent(pack) {
		spec["agentName"] = agentName
	}

	// Provider mapping for this agent.
	if providerRef, ok := resolveProviderForAgent(agentName, cfg); ok {
		spec["providerRef"] = providerRef
	}

	// Runtime sizing.
	if cfg.Runtime != nil {
		resources := map[string]interface{}{}
		if cfg.Runtime.Replicas > 0 {
			spec["replicas"] = cfg.Runtime.Replicas
		}
		if cfg.Runtime.CPU != "" {
			resources["cpu"] = cfg.Runtime.CPU
		}
		if cfg.Runtime.Memory != "" {
			resources["memory"] = cfg.Runtime.Memory
		}
		if len(resources) > 0 {
			spec["resources"] = resources
		}
	}

	// Tool registry reference.
	if len(pack.Tools) > 0 {
		spec["toolRegistryRef"] = sanitizeName(pack.ID + "-tools")
	}

	// Policy reference.
	if hasToolPolicy(pack) {
		spec["agentPolicyRef"] = sanitizeName(pack.ID + "-policy")
	}

	req := map[string]interface{}{
		"kind": "AgentRuntime",
		"metadata": map[string]interface{}{
			"name":   sanitizeName(agentName),
			"labels": buildResourceLabels(pack.ID, pack.Version, ResTypeAgentRuntime, cfg.Labels),
		},
		"spec": spec,
	}
	return json.Marshal(req)
}

// buildToolRegistryRequest builds the JSON body for creating/updating a ToolRegistry CRD.
func buildToolRegistryRequest(pack *prompt.Pack, cfg *Config) (json.RawMessage, error) {
	toolNames := make([]string, 0, len(pack.Tools))
	for name := range pack.Tools {
		toolNames = append(toolNames, name)
	}
	sort.Strings(toolNames)

	tools := make([]map[string]interface{}, 0, len(toolNames))
	for _, name := range toolNames {
		tool := pack.Tools[name]
		entry := map[string]interface{}{
			"name": name,
		}
		if tool.Description != "" {
			entry["description"] = tool.Description
		}
		if tool.Parameters != nil {
			entry["inputSchema"] = tool.Parameters
		}
		tools = append(tools, entry)
	}

	req := map[string]interface{}{
		"kind": "ToolRegistry",
		"metadata": map[string]interface{}{
			"name":   sanitizeName(pack.ID + "-tools"),
			"labels": buildResourceLabels(pack.ID, pack.Version, ResTypeToolRegistry, cfg.Labels),
		},
		"spec": map[string]interface{}{
			"tools": tools,
		},
	}
	return json.Marshal(req)
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
		"kind": "AgentPolicy",
		"metadata": map[string]interface{}{
			"name":   sanitizeName(pack.ID + "-policy"),
			"labels": buildResourceLabels(pack.ID, pack.Version, ResTypeAgentPolicy, cfg.Labels),
		},
		"spec": spec,
	}
	return json.Marshal(req)
}

// resolveProviderForAgent returns the Omnia provider CRD name for an agent.
// It first checks for agent-specific mappings, then falls back to "default".
func resolveProviderForAgent(agentName string, cfg *Config) (string, bool) {
	// Check for agent-specific provider mapping.
	if ref, ok := cfg.Providers[agentName]; ok {
		return ref, true
	}
	// Fall back to default provider.
	if ref, ok := cfg.Providers["default"]; ok {
		return ref, true
	}
	return "", false
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
