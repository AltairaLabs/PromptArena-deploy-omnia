package omnia

import (
	"encoding/json"
	"sort"
	"testing"

	"github.com/AltairaLabs/PromptKit/runtime/deploy/adaptersdk"
)

const testMultiAgentPackJSON = `{
	"id": "multi-pack",
	"version": "1.0.0",
	"prompts": {
		"router": {"system": "Route requests", "description": "Router"},
		"worker": {"system": "Do work", "description": "Worker"}
	},
	"agents": {
		"entry": "router",
		"members": {
			"router": {"description": "Routes requests"},
			"worker": {"description": "Does work"}
		}
	}
}`

func TestBuildAgentRuntimeRequest_WithRuntime(t *testing.T) {
	pack, err := adaptersdk.ParsePack([]byte(testPackJSON))
	if err != nil {
		t.Fatalf("failed to parse pack: %v", err)
	}
	cfg := &Config{
		APIEndpoint: "https://omnia.test.com",
		Workspace:   "test-ws",
		APIToken:    "test-token",
		Providers:   map[string]string{"default": "claude-prod"},
		Runtime:     &RuntimeConfig{Replicas: 3, CPU: "500m", Memory: "512Mi"},
	}

	body, err := buildAgentRuntimeRequest(pack, "test-pack", cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var result map[string]interface{}
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	spec := result["spec"].(map[string]interface{})
	if spec["replicas"] != float64(3) {
		t.Errorf("expected replicas 3, got %v", spec["replicas"])
	}
	resources := spec["resources"].(map[string]interface{})
	if resources["cpu"] != "500m" {
		t.Errorf("expected cpu %q, got %v", "500m", resources["cpu"])
	}
	if resources["memory"] != "512Mi" {
		t.Errorf("expected memory %q, got %v", "512Mi", resources["memory"])
	}
}

func TestBuildAgentRuntimeRequest_MultiAgent(t *testing.T) {
	pack, err := adaptersdk.ParsePack([]byte(testMultiAgentPackJSON))
	if err != nil {
		t.Fatalf("failed to parse pack: %v", err)
	}
	cfg := &Config{
		Providers: map[string]string{"default": "claude-prod", "worker": "worker-model"},
	}

	body, err := buildAgentRuntimeRequest(pack, "worker", cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var result map[string]interface{}
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	spec := result["spec"].(map[string]interface{})
	if spec["agentName"] != "worker" {
		t.Errorf("expected agentName %q, got %v", "worker", spec["agentName"])
	}
	if spec["providerRef"] != "worker-model" {
		t.Errorf("expected providerRef %q, got %v", "worker-model", spec["providerRef"])
	}
}

func TestResourceTypePath(t *testing.T) {
	tests := []struct{ input, expected string }{
		{ResTypeConfigMap, "configmaps"},
		{ResTypePromptPack, "promptpacks"},
		{ResTypeAgentRuntime, "agentruntimes"},
		{ResTypeToolRegistry, "toolregistries"},
		{ResTypeAgentPolicy, "agentpolicies"},
		{"unknown", "unknown"},
	}
	for _, tt := range tests {
		if got := resourceTypePath(tt.input); got != tt.expected {
			t.Errorf("resourceTypePath(%q) = %q, want %q", tt.input, got, tt.expected)
		}
	}
}

func TestResolveProviderForAgent(t *testing.T) {
	cfg := &Config{Providers: map[string]string{"default": "base", "worker": "special"}}

	// Agent-specific mapping.
	if ref, ok := resolveProviderForAgent("worker", cfg); !ok || ref != "special" {
		t.Errorf("expected special for worker, got %q ok=%v", ref, ok)
	}

	// Falls back to default.
	if ref, ok := resolveProviderForAgent("unknown", cfg); !ok || ref != "base" {
		t.Errorf("expected base for unknown, got %q ok=%v", ref, ok)
	}

	// No providers at all.
	emptyCfg := &Config{Providers: map[string]string{}}
	if _, ok := resolveProviderForAgent("any", emptyCfg); ok {
		t.Error("expected no provider for empty map")
	}
}

func TestBuildConfigMapRequest(t *testing.T) {
	pack, err := adaptersdk.ParsePack([]byte(testPackJSON))
	if err != nil {
		t.Fatalf("failed to parse pack: %v", err)
	}
	cfg := &Config{
		APIEndpoint: "https://omnia.test.com",
		Workspace:   "test-ws",
		APIToken:    "test-token",
		Providers:   map[string]string{"default": "claude-prod"},
		PackJSON:    testPackJSON,
	}

	body, err := buildConfigMapRequest(pack, cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var result map[string]interface{}
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	if result["kind"] != "ConfigMap" {
		t.Errorf("expected kind %q, got %q", "ConfigMap", result["kind"])
	}

	metadata, ok := result["metadata"].(map[string]interface{})
	if !ok {
		t.Fatal("expected metadata to be an object")
	}
	if metadata["name"] != "test-pack-packdata" {
		t.Errorf("expected metadata.name %q, got %q", "test-pack-packdata", metadata["name"])
	}

	data, ok := result["data"].(map[string]interface{})
	if !ok {
		t.Fatal("expected data to be an object")
	}
	if _, ok := data["pack.json"]; !ok {
		t.Error("expected data to contain pack.json key")
	}
}

func TestBuildPromptPackRequest(t *testing.T) {
	pack, err := adaptersdk.ParsePack([]byte(testPackJSON))
	if err != nil {
		t.Fatalf("failed to parse pack: %v", err)
	}
	cfg := &Config{
		APIEndpoint: "https://omnia.test.com",
		Workspace:   "test-ws",
		APIToken:    "test-token",
		Providers:   map[string]string{"default": "claude-prod"},
	}

	body, err := buildPromptPackRequest(pack, cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var result map[string]interface{}
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	if result["kind"] != "PromptPack" {
		t.Errorf("expected kind %q, got %q", "PromptPack", result["kind"])
	}

	spec, ok := result["spec"].(map[string]interface{})
	if !ok {
		t.Fatal("expected spec to be an object")
	}
	if spec["packId"] != "test-pack" {
		t.Errorf("expected spec.packId %q, got %q", "test-pack", spec["packId"])
	}
	if spec["version"] != "1.0.0" {
		t.Errorf("expected spec.version %q, got %q", "1.0.0", spec["version"])
	}
	if spec["configMapRef"] != "test-pack-packdata" {
		t.Errorf("expected spec.configMapRef %q, got %q", "test-pack-packdata", spec["configMapRef"])
	}
	if spec["providerRef"] != "claude-prod" {
		t.Errorf("expected spec.providerRef %q, got %q", "claude-prod", spec["providerRef"])
	}
	if spec["description"] != "Test pack" {
		t.Errorf("expected spec.description %q, got %q", "Test pack", spec["description"])
	}
}

func TestBuildAgentRuntimeRequest(t *testing.T) {
	pack, err := adaptersdk.ParsePack([]byte(testPackJSON))
	if err != nil {
		t.Fatalf("failed to parse pack: %v", err)
	}
	cfg := &Config{
		APIEndpoint: "https://omnia.test.com",
		Workspace:   "test-ws",
		APIToken:    "test-token",
		Providers:   map[string]string{"default": "claude-prod"},
	}

	body, err := buildAgentRuntimeRequest(pack, "test-pack", cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var result map[string]interface{}
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	if result["kind"] != "AgentRuntime" {
		t.Errorf("expected kind %q, got %q", "AgentRuntime", result["kind"])
	}

	spec, ok := result["spec"].(map[string]interface{})
	if !ok {
		t.Fatal("expected spec to be an object")
	}
	if spec["promptPackRef"] != "test-pack" {
		t.Errorf("expected spec.promptPackRef %q, got %q", "test-pack", spec["promptPackRef"])
	}
	if spec["providerRef"] != "claude-prod" {
		t.Errorf("expected spec.providerRef %q, got %q", "claude-prod", spec["providerRef"])
	}
	if spec["toolRegistryRef"] != "test-pack-tools" {
		t.Errorf("expected spec.toolRegistryRef %q, got %q", "test-pack-tools", spec["toolRegistryRef"])
	}
}

func TestBuildToolRegistryRequest(t *testing.T) {
	pack, err := adaptersdk.ParsePack([]byte(testPackJSON))
	if err != nil {
		t.Fatalf("failed to parse pack: %v", err)
	}
	cfg := &Config{
		APIEndpoint: "https://omnia.test.com",
		Workspace:   "test-ws",
		APIToken:    "test-token",
		Providers:   map[string]string{"default": "claude-prod"},
	}

	body, err := buildToolRegistryRequest(pack, cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var result map[string]interface{}
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	if result["kind"] != "ToolRegistry" {
		t.Errorf("expected kind %q, got %q", "ToolRegistry", result["kind"])
	}

	spec, ok := result["spec"].(map[string]interface{})
	if !ok {
		t.Fatal("expected spec to be an object")
	}
	tools, ok := spec["tools"].([]interface{})
	if !ok {
		t.Fatal("expected spec.tools to be an array")
	}
	if len(tools) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(tools))
	}

	tool, ok := tools[0].(map[string]interface{})
	if !ok {
		t.Fatal("expected tool entry to be an object")
	}
	if tool["name"] != "search" {
		t.Errorf("expected tool name %q, got %q", "search", tool["name"])
	}
	if tool["description"] != "Search tool" {
		t.Errorf("expected tool description %q, got %q", "Search tool", tool["description"])
	}
}

func TestBuildAgentPolicyRequest(t *testing.T) {
	packWithPolicy := `{
		"id": "policy-pack",
		"version": "1.0.0",
		"prompts": {
			"main": {
				"system": "You are helpful",
				"description": "Main",
				"tool_policy": {
					"blocklist": ["dangerous_tool", "risky_tool"]
				}
			}
		}
	}`

	pack, err := adaptersdk.ParsePack([]byte(packWithPolicy))
	if err != nil {
		t.Fatalf("failed to parse pack: %v", err)
	}
	cfg := &Config{
		APIEndpoint: "https://omnia.test.com",
		Workspace:   "test-ws",
		APIToken:    "test-token",
		Providers:   map[string]string{"default": "claude-prod"},
	}

	body, err := buildAgentPolicyRequest(pack, cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var result map[string]interface{}
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	if result["kind"] != "AgentPolicy" {
		t.Errorf("expected kind %q, got %q", "AgentPolicy", result["kind"])
	}

	spec, ok := result["spec"].(map[string]interface{})
	if !ok {
		t.Fatal("expected spec to be an object")
	}
	blocklist, ok := spec["toolBlocklist"].([]interface{})
	if !ok {
		t.Fatal("expected spec.toolBlocklist to be an array")
	}
	if len(blocklist) != 2 {
		t.Fatalf("expected 2 blocked tools, got %d", len(blocklist))
	}
}

func TestAgentRuntimeNames_SingleAgent(t *testing.T) {
	pack, err := adaptersdk.ParsePack([]byte(testPackJSON))
	if err != nil {
		t.Fatalf("failed to parse pack: %v", err)
	}

	names := agentRuntimeNames(pack)
	if len(names) != 1 {
		t.Fatalf("expected 1 name, got %d", len(names))
	}
	if names[0] != "test-pack" {
		t.Errorf("expected name %q, got %q", "test-pack", names[0])
	}
}

func TestAgentRuntimeNames_MultiAgent(t *testing.T) {
	pack, err := adaptersdk.ParsePack([]byte(testMultiAgentPackJSON))
	if err != nil {
		t.Fatalf("failed to parse pack: %v", err)
	}

	names := agentRuntimeNames(pack)
	if len(names) != 2 {
		t.Fatalf("expected 2 names, got %d", len(names))
	}

	// Names should be sorted by ExtractAgents.
	sorted := make([]string, len(names))
	copy(sorted, names)
	sort.Strings(sorted)
	for i, n := range sorted {
		if names[i] != n {
			t.Errorf("expected names to be sorted, got %v", names)
			break
		}
	}

	nameSet := map[string]bool{}
	for _, n := range names {
		nameSet[n] = true
	}
	if !nameSet["router"] || !nameSet["worker"] {
		t.Errorf("expected names to include router and worker, got %v", names)
	}
}
