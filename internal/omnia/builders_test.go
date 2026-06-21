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
		Providers:   Providers{{Name: "default", Ref: "claude-prod", Role: "llm"}},
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
		Providers: Providers{{Name: "default", Ref: "claude-prod", Role: "llm"}},
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
		Providers: Providers{
			{Name: "default", Ref: "claude-prod", Role: "llm"},
			{Name: "worker", Ref: "worker-model", Role: "llm"},
		},
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
	// All bindings are emitted, in order — agentName no longer selects one.
	if len(providers) != 2 {
		t.Fatalf("expected 2 providers, got %d", len(providers))
	}
	p0 := providers[0].(map[string]interface{})
	if p0["name"] != "default" {
		t.Errorf("expected primary provider name %q, got %v", "default", p0["name"])
	}
	if p0["role"] != "llm" {
		t.Errorf("expected provider role %q, got %v", "llm", p0["role"])
	}
	ref := p0["providerRef"].(map[string]interface{})
	if ref["name"] != "claude-prod" {
		t.Errorf("expected providerRef.name %q, got %v", "claude-prod", ref["name"])
	}
	p1 := providers[1].(map[string]interface{})
	if p1["name"] != "worker" {
		t.Errorf("expected second provider name %q, got %v", "worker", p1["name"])
	}
}

func TestBuildAgentRuntimeRequest_MultiRoleProviders(t *testing.T) {
	pack, err := adaptersdk.ParsePack([]byte(testPackJSON))
	if err != nil {
		t.Fatalf("failed to parse pack: %v", err)
	}
	cfg := &Config{
		Providers: Providers{
			{Name: "default", Ref: "claude-prod", Role: "llm"},
			{Name: "embed", Ref: "openai-embed", Role: "embedding"},
			{Name: "infer", Ref: "vllm", Role: "inference"},
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
	spec := result["spec"].(map[string]interface{})
	providers := spec["providers"].([]interface{})
	if len(providers) != 3 {
		t.Fatalf("expected 3 provider entries, got %d", len(providers))
	}

	type want struct{ name, ref, role string }
	wants := []want{
		{"default", "claude-prod", "llm"},
		{"embed", "openai-embed", "embedding"},
		{"infer", "vllm", "inference"},
	}
	for i, w := range wants {
		p := providers[i].(map[string]interface{})
		if p["name"] != w.name {
			t.Errorf("provider[%d].name = %v, want %q", i, p["name"], w.name)
		}
		if p["role"] != w.role {
			t.Errorf("provider[%d].role = %v, want %q", i, p["role"], w.role)
		}
		ref := p["providerRef"].(map[string]interface{})
		if ref["name"] != w.ref {
			t.Errorf("provider[%d].providerRef.name = %v, want %q", i, ref["name"], w.ref)
		}
	}
}

func TestBuildAgentRuntimeRequest_EmptyRoleDefaultsToLLM(t *testing.T) {
	pack, err := adaptersdk.ParsePack([]byte(testPackJSON))
	if err != nil {
		t.Fatalf("failed to parse pack: %v", err)
	}
	cfg := &Config{Providers: Providers{{Name: "default", Ref: "claude-prod"}}}

	body, err := buildAgentRuntimeRequest(pack, "test-pack", cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var result map[string]interface{}
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	providers := result["spec"].(map[string]interface{})["providers"].([]interface{})
	if providers[0].(map[string]interface{})["role"] != "llm" {
		t.Errorf("expected empty role to default to llm, got %v", providers[0])
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

func TestBuildPromptPackRequest(t *testing.T) {
	pack, err := adaptersdk.ParsePack([]byte(testPackJSON))
	if err != nil {
		t.Fatalf("failed to parse pack: %v", err)
	}
	cfg := &Config{
		APIEndpoint: "https://omnia.test.com",
		Workspace:   "test-ws",
		APIToken:    "test-token",
		Providers:   Providers{{Name: "default", Ref: "claude-prod", Role: "llm"}},
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
		Providers:   Providers{{Name: "default", Ref: "claude-prod", Role: "llm"}},
		Tools: []ToolHandler{{
			Name: "search", Type: handlerTypeHTTP,
			Tool:       &HandlerTool{Name: "search", Description: "Search tool", InputSchema: map[string]interface{}{"type": "object"}},
			HTTPConfig: map[string]interface{}{"endpoint": "https://api.example.com"},
		}},
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

// toolsCfg builds a Config carrying an http handler (with tool+httpConfig) and
// an mcp handler (mcpConfig only, no tool), order preserved.
func toolsCfg() *Config {
	return &Config{
		APIEndpoint: "https://omnia.test.com",
		Workspace:   "test-ws",
		APIToken:    "test-token",
		Providers:   Providers{{Name: "default", Ref: "claude-prod", Role: "llm"}},
		Tools: []ToolHandler{
			{
				Name: "search", Type: handlerTypeHTTP,
				Tool: &HandlerTool{
					Name: "search", Description: "Search tool",
					InputSchema: map[string]interface{}{"type": "object"},
				},
				HTTPConfig: map[string]interface{}{"endpoint": "https://api.example.com/search"},
			},
			{
				Name: "knowledge", Type: handlerTypeMCP,
				MCPConfig: map[string]interface{}{"server": "knowledge-mcp"},
			},
		},
	}
}

func TestBuildToolRegistryRequest(t *testing.T) {
	pack, err := adaptersdk.ParsePack([]byte(testPackJSON))
	if err != nil {
		t.Fatalf("failed to parse pack: %v", err)
	}

	body, err := buildToolRegistryRequest(pack, toolsCfg())
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
	if _, present := spec["tools"]; present {
		t.Error("spec must not contain a 'tools' key; handlers replaced it")
	}
	handlers, ok := spec["handlers"].([]interface{})
	if !ok {
		t.Fatal("expected spec.handlers to be an array")
	}
	if len(handlers) != 2 {
		t.Fatalf("expected 2 handlers, got %d", len(handlers))
	}

	// Order preserved: http handler first, with tool + httpConfig.
	h0 := handlers[0].(map[string]interface{})
	if h0["name"] != "search" || h0["type"] != handlerTypeHTTP {
		t.Errorf("handler[0] = %v, want name=search type=http", h0)
	}
	tool, ok := h0["tool"].(map[string]interface{})
	if !ok {
		t.Fatal("expected handler[0].tool to be an object")
	}
	if tool["name"] != "search" || tool["description"] != "Search tool" {
		t.Errorf("handler[0].tool = %v", tool)
	}
	if _, ok := h0["httpConfig"].(map[string]interface{}); !ok {
		t.Error("expected handler[0].httpConfig to be present")
	}

	// mcp handler second: mcpConfig only, no tool.
	h1 := handlers[1].(map[string]interface{})
	if h1["name"] != "knowledge" || h1["type"] != handlerTypeMCP {
		t.Errorf("handler[1] = %v, want name=knowledge type=mcp", h1)
	}
	if _, present := h1["tool"]; present {
		t.Error("expected mcp handler[1] to have no tool block")
	}
	if _, ok := h1["mcpConfig"].(map[string]interface{}); !ok {
		t.Error("expected handler[1].mcpConfig to be present")
	}
}

// TestBuildAgentRuntimeRequest_NoTools verifies that a pack with inline
// pack.Tools but NO cfg.Tools produces no toolRegistryRef — the ToolRegistry is
// now driven exclusively from the deploy-config tools block.
func TestBuildAgentRuntimeRequest_NoTools(t *testing.T) {
	pack, err := adaptersdk.ParsePack([]byte(testPackJSON))
	if err != nil {
		t.Fatalf("failed to parse pack: %v", err)
	}
	if len(pack.Tools) == 0 {
		t.Fatal("test fixture should carry inline pack.Tools")
	}
	cfg := &Config{Providers: Providers{{Name: "default", Ref: "claude-prod", Role: "llm"}}}

	body, err := buildAgentRuntimeRequest(pack, "test-pack", cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var result map[string]interface{}
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	spec := result["spec"].(map[string]interface{})
	if _, present := spec["toolRegistryRef"]; present {
		t.Error("expected no toolRegistryRef when cfg.Tools is empty")
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
		Providers:   Providers{{Name: "default", Ref: "claude-prod", Role: "llm"}},
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

func TestBuildPromptPackRequest_WithSkills(t *testing.T) {
	pack, err := adaptersdk.ParsePack([]byte(testPackJSON))
	if err != nil {
		t.Fatalf("failed to parse pack: %v", err)
	}
	maxActive := 4
	cfg := &Config{
		APIEndpoint: "https://omnia.test.com",
		Workspace:   "test-ws",
		APIToken:    "test-token",
		Providers:   Providers{{Name: "default", Ref: "claude-prod", Role: "llm"}},
		PackJSON:    testPackJSON,
		Skills: []SkillBinding{
			{Source: "shared-skills", Include: []string{"summarize", "search"}, MountAs: "tools"},
			{Source: "math-skills"},
		},
		SkillsConfig: &SkillsConfig{MaxActive: &maxActive, Selector: skillSelectorModelDriven},
	}

	body, err := buildPromptPackRequest(pack, cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var result map[string]interface{}
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	spec := result["spec"].(map[string]interface{})

	skills, ok := spec["skills"].([]interface{})
	if !ok {
		t.Fatalf("expected spec.skills to be an array, got %T", spec["skills"])
	}
	if len(skills) != 2 {
		t.Fatalf("expected 2 skills, got %d", len(skills))
	}
	first := skills[0].(map[string]interface{})
	if first["source"] != "shared-skills" {
		t.Errorf("expected skills[0].source shared-skills, got %v", first["source"])
	}
	if first["mountAs"] != "tools" {
		t.Errorf("expected skills[0].mountAs tools, got %v", first["mountAs"])
	}
	inc, ok := first["include"].([]interface{})
	if !ok || len(inc) != 2 || inc[0] != "summarize" || inc[1] != "search" {
		t.Errorf("expected skills[0].include [summarize search], got %v", first["include"])
	}
	// Source-only binding must omit include/mountAs.
	second := skills[1].(map[string]interface{})
	if second["source"] != "math-skills" {
		t.Errorf("expected skills[1].source math-skills, got %v", second["source"])
	}
	if _, present := second["include"]; present {
		t.Error("expected skills[1].include to be omitted")
	}
	if _, present := second["mountAs"]; present {
		t.Error("expected skills[1].mountAs to be omitted")
	}

	sc, ok := spec["skillsConfig"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected spec.skillsConfig to be an object, got %T", spec["skillsConfig"])
	}
	if sc["maxActive"] != float64(4) {
		t.Errorf("expected skillsConfig.maxActive 4, got %v", sc["maxActive"])
	}
	if sc["selector"] != skillSelectorModelDriven {
		t.Errorf("expected skillsConfig.selector %q, got %v", skillSelectorModelDriven, sc["selector"])
	}
}

func TestBuildPromptPackRequest_NoSkills(t *testing.T) {
	pack, err := adaptersdk.ParsePack([]byte(testPackJSON))
	if err != nil {
		t.Fatalf("failed to parse pack: %v", err)
	}
	cfg := &Config{
		APIEndpoint: "https://omnia.test.com",
		Workspace:   "test-ws",
		APIToken:    "test-token",
		Providers:   Providers{{Name: "default", Ref: "claude-prod", Role: "llm"}},
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
	spec := result["spec"].(map[string]interface{})
	if _, present := spec["skills"]; present {
		t.Error("expected spec.skills to be omitted when no skills configured")
	}
	if _, present := spec["skillsConfig"]; present {
		t.Error("expected spec.skillsConfig to be omitted when no skills configured")
	}
}

// TestBuildSkillsConfigSpec_EmptyBlock verifies an empty skillsConfig (no
// selector, no maxActive) emits nothing.
func TestBuildSkillsConfigSpec_EmptyBlock(t *testing.T) {
	if got := buildSkillsConfigSpec(&SkillsConfig{}); got != nil {
		t.Errorf("expected nil for empty skillsConfig, got %v", got)
	}
	if got := buildSkillsConfigSpec(nil); got != nil {
		t.Errorf("expected nil for nil skillsConfig, got %v", got)
	}
}
