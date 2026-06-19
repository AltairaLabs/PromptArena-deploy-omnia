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
	runtime, ok := spec["runtime"].(map[string]interface{})
	if !ok {
		t.Fatal("expected spec.runtime to be an object")
	}
	if runtime["replicas"] != float64(3) {
		t.Errorf("expected runtime.replicas 3, got %v", runtime["replicas"])
	}
	resources := runtime["resources"].(map[string]interface{})
	requests := resources["requests"].(map[string]interface{})
	if requests["cpu"] != "500m" {
		t.Errorf("expected requests.cpu %q, got %v", "500m", requests["cpu"])
	}
	if requests["memory"] != "512Mi" {
		t.Errorf("expected requests.memory %q, got %v", "512Mi", requests["memory"])
	}
}

func TestBuildAgentRuntimeRequest_Autoscaling(t *testing.T) {
	pack, err := adaptersdk.ParsePack([]byte(testPackJSON))
	if err != nil {
		t.Fatalf("failed to parse pack: %v", err)
	}
	minR, maxR, cpu := 2, 10, 65
	cfg := &Config{
		Providers: map[string]string{"default": "claude-prod"},
		Runtime: &RuntimeConfig{
			Autoscaling: &AutoscalingConfig{
				Enabled:              true,
				Type:                 "keda",
				MinReplicas:          &minR,
				MaxReplicas:          &maxR,
				TargetCPUUtilization: &cpu,
			},
		},
	}

	body, err := buildAgentRuntimeRequest(pack, "test-pack", cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var result map[string]interface{}
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	runtime := result["spec"].(map[string]interface{})["runtime"].(map[string]interface{})
	as, ok := runtime["autoscaling"].(map[string]interface{})
	if !ok {
		t.Fatal("expected spec.runtime.autoscaling to be an object")
	}
	if as["enabled"] != true {
		t.Errorf("expected autoscaling.enabled true, got %v", as["enabled"])
	}
	if as["type"] != "keda" {
		t.Errorf("expected autoscaling.type keda, got %v", as["type"])
	}
	if as["minReplicas"] != float64(2) {
		t.Errorf("expected minReplicas 2, got %v", as["minReplicas"])
	}
	if as["maxReplicas"] != float64(10) {
		t.Errorf("expected maxReplicas 10, got %v", as["maxReplicas"])
	}
	if as["targetCPUUtilizationPercentage"] != float64(65) {
		t.Errorf("expected targetCPUUtilizationPercentage 65, got %v", as["targetCPUUtilizationPercentage"])
	}
	// Unset fields must be omitted so CRD defaults apply.
	if _, present := as["targetMemoryUtilizationPercentage"]; present {
		t.Error("expected unset targetMemoryUtilizationPercentage to be omitted")
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
	providers := spec["providers"].([]interface{})
	if len(providers) != 1 {
		t.Fatalf("expected 1 provider, got %d", len(providers))
	}
	p0 := providers[0].(map[string]interface{})
	if p0["name"] != "default" {
		t.Errorf("expected primary provider name %q, got %v", "default", p0["name"])
	}
	if p0["role"] != "llm" {
		t.Errorf("expected provider role %q, got %v", "llm", p0["role"])
	}
	ref := p0["providerRef"].(map[string]interface{})
	if ref["name"] != "worker-model" {
		t.Errorf("expected providerRef.name %q, got %v", "worker-model", ref["name"])
	}
}

func TestResourceTypePath(t *testing.T) {
	tests := []struct{ input, expected string }{
		{ResTypePromptPack, "promptpacks"},
		{ResTypeAgentRuntime, "agents"},
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
		PackJSON:    testPackJSON,
	}

	body, err := buildPromptPackRequest(pack, cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var result map[string]interface{}
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	metadata, ok := result["metadata"].(map[string]interface{})
	if !ok {
		t.Fatal("expected metadata to be an object")
	}
	if metadata["name"] != "test-pack" {
		t.Errorf("expected metadata.name %q, got %v", "test-pack", metadata["name"])
	}

	// spec carries only the version; the dashboard sets spec.source from content.
	spec, ok := result["spec"].(map[string]interface{})
	if !ok {
		t.Fatal("expected spec to be an object")
	}
	if spec["version"] != "1.0.0" {
		t.Errorf("expected spec.version %q, got %v", "1.0.0", spec["version"])
	}
	for _, invalid := range []string{"packId", "configMapRef", "providerRef", "description", "source"} {
		if _, present := spec[invalid]; present {
			t.Errorf("spec must not contain %q (dashboard owns it)", invalid)
		}
	}

	// content is folded into a managed ConfigMap dashboard-side.
	content, ok := result["content"].(map[string]interface{})
	if !ok {
		t.Fatal("expected content to be an object")
	}
	if content["pack.json"] != testPackJSON {
		t.Errorf("expected content[pack.json] to carry the raw pack JSON")
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

	spec, ok := result["spec"].(map[string]interface{})
	if !ok {
		t.Fatal("expected spec to be an object")
	}

	// promptPackRef is a struct, not a bare string.
	ppRef, ok := spec["promptPackRef"].(map[string]interface{})
	if !ok || ppRef["name"] != "test-pack" {
		t.Errorf("expected spec.promptPackRef.name %q, got %v", "test-pack", spec["promptPackRef"])
	}

	// facade is required by the CRD.
	facade, ok := spec["facade"].(map[string]interface{})
	if !ok {
		t.Fatal("expected required spec.facade to be an object")
	}
	if facade["type"] != "websocket" || facade["handler"] != "runtime" {
		t.Errorf("unexpected facade: %v", facade)
	}

	// providers is a list of NamedProviderRef; "default" is primary.
	providers, ok := spec["providers"].([]interface{})
	if !ok || len(providers) != 1 {
		t.Fatalf("expected 1 provider entry, got %v", spec["providers"])
	}
	ref := providers[0].(map[string]interface{})["providerRef"].(map[string]interface{})
	if ref["name"] != "claude-prod" {
		t.Errorf("expected providerRef.name %q, got %v", "claude-prod", ref["name"])
	}

	// toolRegistryRef is a struct, not a bare string.
	trRef, ok := spec["toolRegistryRef"].(map[string]interface{})
	if !ok || trRef["name"] != "test-pack-tools" {
		t.Errorf("expected spec.toolRegistryRef.name %q, got %v", "test-pack-tools", spec["toolRegistryRef"])
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
