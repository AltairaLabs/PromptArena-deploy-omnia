package omnia

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/AltairaLabs/PromptKit/runtime/deploy"
)

const testPackJSON = `{
	"id": "test-pack",
	"version": "1.0.0",
	"description": "Test pack",
	"prompts": {
		"main": {
			"system": "You are a helpful assistant",
			"description": "Main prompt"
		}
	},
	"tools": {
		"search": {
			"name": "search",
			"description": "Search tool",
			"parameters": {"type": "object"}
		}
	}
}`

const testDeployConfig = `{
	"api_endpoint": "https://omnia.test.com",
	"workspace": "test-ws",
	"api_token": "test-token",
	"providers": {"default": "claude-prod"},
	"tools": [
		{
			"name": "search",
			"type": "http",
			"tool": {
				"name": "search",
				"description": "Search tool",
				"inputSchema": {"type": "object"}
			},
			"httpConfig": {"endpoint": "https://api.example.com/search"}
		}
	]
}`

func newPlanTestProvider() (*Provider, *simulatedClient) {
	sim := newSimulatedClient()
	sim.validProviders["claude-prod"] = true
	return &Provider{clientFunc: newSimulatedClientFactory(sim)}, sim
}

// seedManagedResource stores a resource in the simulated client carrying the
// adapter's managed-by + pack-id labels, so adoptPriorState lists it as part of
// the named pack (the cluster is the source of truth for plan/apply).
func seedManagedResource(sim *simulatedClient, resType, name, packID string) {
	sim.mu.Lock()
	defer sim.mu.Unlock()
	sim.resources[simKey(resType, name)] = &ResourceResponse{
		Kind: resType,
		Metadata: ResourceMetadata{
			Name:            name,
			UID:             "uid-" + name,
			ResourceVersion: "1",
			Labels: map[string]string{
				LabelManagedBy: managedByValue,
				LabelPackID:    packID,
			},
		},
	}
}

func TestPlan_SingleAgent(t *testing.T) {
	p, _ := newPlanTestProvider()
	resp, err := p.Plan(context.Background(), &deploy.PlanRequest{
		PackJSON:     testPackJSON,
		DeployConfig: testDeployConfig,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	expectedTypes := map[string]bool{
		ResTypePromptPack:   false,
		ResTypeToolRegistry: false,
		ResTypeAgentRuntime: false,
	}

	for _, c := range resp.Changes {
		if _, ok := expectedTypes[c.Type]; ok {
			expectedTypes[c.Type] = true
		}
		if c.Action != deploy.ActionCreate {
			t.Errorf("expected CREATE action for %s, got %s", c.Type, c.Action)
		}
	}

	for resType, found := range expectedTypes {
		if !found {
			t.Errorf("missing expected resource type in plan: %s", resType)
		}
	}
}

func TestPlan_ValidatesProviders(t *testing.T) {
	sim := newSimulatedClient()
	sim.validProviders["claude-prod"] = true
	p := &Provider{clientFunc: newSimulatedClientFactory(sim)}

	resp, err := p.Plan(context.Background(), &deploy.PlanRequest{
		PackJSON:     testPackJSON,
		DeployConfig: testDeployConfig,
	})
	if err != nil {
		t.Fatalf("expected plan to succeed with valid provider, got: %v", err)
	}
	if len(resp.Changes) == 0 {
		t.Error("expected non-empty changes")
	}
}

func TestPlan_InvalidProvider(t *testing.T) {
	sim := newSimulatedClient()
	// Do NOT add "claude-prod" to validProviders — it should fail.
	p := &Provider{clientFunc: newSimulatedClientFactory(sim)}

	_, err := p.Plan(context.Background(), &deploy.PlanRequest{
		PackJSON:     testPackJSON,
		DeployConfig: testDeployConfig,
	})
	if err == nil {
		t.Fatal("expected error for invalid provider, got nil")
	}
	if !strings.Contains(err.Error(), "provider validation failed") {
		t.Errorf("expected provider validation error, got: %v", err)
	}
	if !strings.Contains(err.Error(), "claude-prod") {
		t.Errorf("expected error to mention provider name, got: %v", err)
	}
}

func TestPlan_SkipsProviderValidationOnDryRun(t *testing.T) {
	sim := newSimulatedClient()
	// Do NOT add any valid providers — dry-run should skip validation.
	p := &Provider{clientFunc: newSimulatedClientFactory(sim)}

	dryRunConfig := `{
		"api_endpoint": "https://omnia.test.com",
		"workspace": "test-ws",
		"api_token": "test-token",
		"providers": {"default": "nonexistent-provider"},
		"dry_run": true
	}`

	resp, err := p.Plan(context.Background(), &deploy.PlanRequest{
		PackJSON:     testPackJSON,
		DeployConfig: dryRunConfig,
	})
	if err != nil {
		t.Fatalf("expected dry-run plan to succeed, got: %v", err)
	}
	if len(resp.Changes) == 0 {
		t.Error("expected non-empty changes")
	}
}

func TestPlan_WithPriorState(t *testing.T) {
	// The cluster (adopted via labels) is the source of truth — these resources
	// already exist, so the plan must show UPDATE even though req.PriorState is
	// empty.
	p, sim := newPlanTestProvider()
	seedManagedResource(sim, ResTypePromptPack, "test-pack", "test-pack")
	seedManagedResource(sim, ResTypeToolRegistry, "test-pack-tools", "test-pack")
	seedManagedResource(sim, ResTypeAgentRuntime, "test-pack", "test-pack")

	resp, err := p.Plan(context.Background(), &deploy.PlanRequest{
		PackJSON:     testPackJSON,
		DeployConfig: testDeployConfig,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for _, c := range resp.Changes {
		// The tool_registry is CREATE-ONLY: an existing one emits no change at all,
		// so it must never appear in the diff. Every other adopted resource updates.
		if c.Type == ResTypeToolRegistry {
			t.Errorf("an existing tool_registry must emit no change, got %+v", c)
		}
		if c.Action != deploy.ActionUpdate {
			t.Errorf("expected UPDATE action for %s %q, got %s", c.Type, c.Name, c.Action)
		}
	}
}

func TestPlan_WithDeletion(t *testing.T) {
	// The cluster has an extra adopted resource not in the desired set — plan
	// must show it as a DELETE (a real cluster orphan).
	p, sim := newPlanTestProvider()
	seedManagedResource(sim, ResTypePromptPack, "test-pack", "test-pack")
	seedManagedResource(sim, ResTypeToolRegistry, "test-pack-tools", "test-pack")
	seedManagedResource(sim, ResTypeAgentRuntime, "test-pack", "test-pack")
	seedManagedResource(sim, ResTypeAgentPolicy, "test-pack-policy", "test-pack")

	resp, err := p.Plan(context.Background(), &deploy.PlanRequest{
		PackJSON:     testPackJSON,
		DeployConfig: testDeployConfig,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var deleteCount int
	for _, c := range resp.Changes {
		if c.Action == deploy.ActionDelete {
			deleteCount++
			if c.Type != ResTypeAgentPolicy {
				t.Errorf("expected deletion of %s, got deletion of %s", ResTypeAgentPolicy, c.Type)
			}
		}
	}
	if deleteCount != 1 {
		t.Errorf("expected 1 deletion, got %d", deleteCount)
	}
}

const testDeployConfigWithSkills = `{
	"api_endpoint": "https://omnia.test.com",
	"workspace": "test-ws",
	"api_token": "test-token",
	"providers": {"default": "claude-prod"},
	"skills": [
		{"source": "shared-skills"},
		{"source": "shared-skills"},
		{"source": "math-skills"}
	]
}`

func TestPlan_ValidatesSkillSources(t *testing.T) {
	sim := newSimulatedClient()
	sim.validProviders["claude-prod"] = true
	sim.validSkillSources["shared-skills"] = true
	sim.validSkillSources["math-skills"] = true
	p := &Provider{clientFunc: newSimulatedClientFactory(sim)}

	resp, err := p.Plan(context.Background(), &deploy.PlanRequest{
		PackJSON:     testPackJSON,
		DeployConfig: testDeployConfigWithSkills,
	})
	if err != nil {
		t.Fatalf("expected plan to succeed with valid skill sources, got: %v", err)
	}
	if len(resp.Changes) == 0 {
		t.Error("expected non-empty changes")
	}
}

func TestPlan_InvalidSkillSource(t *testing.T) {
	sim := newSimulatedClient()
	sim.validProviders["claude-prod"] = true
	// Only shared-skills is valid — math-skills should fail.
	sim.validSkillSources["shared-skills"] = true
	p := &Provider{clientFunc: newSimulatedClientFactory(sim)}

	_, err := p.Plan(context.Background(), &deploy.PlanRequest{
		PackJSON:     testPackJSON,
		DeployConfig: testDeployConfigWithSkills,
	})
	if err == nil {
		t.Fatal("expected error for invalid skill source, got nil")
	}
	if !strings.Contains(err.Error(), "skill validation failed") {
		t.Errorf("expected skill validation error, got: %v", err)
	}
	if !strings.Contains(err.Error(), "math-skills") {
		t.Errorf("expected error to mention the failing source, got: %v", err)
	}
}

func TestPlan_SkipsSkillValidationOnDryRun(t *testing.T) {
	sim := newSimulatedClient()
	// No valid skill sources — dry-run should skip validation.
	p := &Provider{clientFunc: newSimulatedClientFactory(sim)}

	dryRunConfig := `{
		"api_endpoint": "https://omnia.test.com",
		"workspace": "test-ws",
		"api_token": "test-token",
		"providers": {"default": "claude-prod"},
		"skills": [{"source": "missing-skills"}],
		"dry_run": true
	}`

	resp, err := p.Plan(context.Background(), &deploy.PlanRequest{
		PackJSON:     testPackJSON,
		DeployConfig: dryRunConfig,
	})
	if err != nil {
		t.Fatalf("expected dry-run plan to succeed, got: %v", err)
	}
	if len(resp.Changes) == 0 {
		t.Error("expected non-empty changes")
	}
}

func TestDescribeRefValidationError(t *testing.T) {
	notFound := &HTTPError{StatusCode: 404, Category: ErrCategoryNotFound}
	forbidden := &HTTPError{
		StatusCode:  403,
		Category:    ErrCategoryPermission,
		Remediation: "verify the API token has sufficient permissions for the workspace",
	}

	// A genuine 404 keeps the familiar wording.
	msg := describeRefValidationError("provider", "ollama", notFound)
	if !strings.Contains(msg, "not found in workspace") {
		t.Errorf("404 should read as not found, got: %q", msg)
	}

	// A 403 must NOT be mislabeled as not-found; it surfaces the real cause + hint.
	msg = describeRefValidationError("provider", "ollama", forbidden)
	if strings.Contains(msg, "not found in workspace") {
		t.Errorf("403 must not be reported as not found, got: %q", msg)
	}
	if !strings.Contains(msg, "403") || !strings.Contains(msg, "permissions") {
		t.Errorf("403 should surface status and remediation, got: %q", msg)
	}
}

func TestValidateProviders_PhaseWarning(t *testing.T) {
	sim := newSimulatedClient()
	// claude EXISTS (no error) but is not Ready → expect a non-blocking warning.
	sim.providerSummaries = []ProviderSummary{{Name: "claude", Role: "llm", Phase: "Error"}}
	p := &Provider{clientFunc: newSimulatedClientFactory(sim)}

	cfg := `{"api_endpoint":"https://x","workspace":"demo","api_token":"t",` +
		`"providers":{"default":"claude"}}`
	resp, err := p.Plan(context.Background(), &deploy.PlanRequest{PackJSON: testPackJSON, DeployConfig: cfg})
	if err != nil {
		t.Fatalf("a present-but-unready provider must not error: %v", err)
	}
	found := false
	for _, w := range resp.Warnings {
		if strings.Contains(w, "claude") && strings.Contains(w, "not ready (phase: Error)") {
			found = true
		}
	}
	if !found {
		t.Errorf("want a provider phase warning, got %v", resp.Warnings)
	}
}

func TestProviderNotFoundMessage(t *testing.T) {
	available := []ProviderSummary{
		{Name: "rag-hero-candidate", Type: "openai", Model: "gpt-4o", Role: "llm"},
		{Name: "rag-hero-embeddings", Type: "openai", Model: "text-embedding-3", Role: "embedding"},
		{Name: "ollama", Type: "ollama", Model: "llava:7b", Role: "llm"},
	}
	msg := providerNotFoundMessage("claude", "demo", "llm", available)
	for _, want := range []string{
		`"claude" not found`, `"demo"`, "Available providers",
		"rag-hero-candidate (openai/gpt-4o, role=llm)", "For role=llm try:", "ollama",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("message missing %q\n got: %s", want, msg)
		}
	}
	// embedding-only providers must not appear in the llm role hint.
	hint := msg[strings.Index(msg, "For role=llm try:"):]
	if strings.Contains(hint, "rag-hero-embeddings") {
		t.Errorf("embedding provider leaked into llm hint: %s", hint)
	}
	// no available providers -> bare message.
	if got := providerNotFoundMessage("x", "demo", "llm", nil); strings.Contains(got, "Available") {
		t.Errorf("expected bare message when nothing is available, got %s", got)
	}
}

func TestValidateProviders_ListBasedRichError(t *testing.T) {
	sim := newSimulatedClient()
	sim.providerSummaries = []ProviderSummary{
		{Name: "rag-hero-candidate", Type: "openai", Model: "gpt-4o", Role: "llm"},
	}
	p := &Provider{clientFunc: newSimulatedClientFactory(sim)}

	cfg := `{"api_endpoint":"https://x","workspace":"demo","api_token":"t",` +
		`"providers":{"default":"claude"}}`
	_, err := p.Plan(context.Background(), &deploy.PlanRequest{PackJSON: testPackJSON, DeployConfig: cfg})
	if err == nil {
		t.Fatal("expected provider validation error")
	}
	if !strings.Contains(err.Error(), "rag-hero-candidate") ||
		!strings.Contains(err.Error(), "Available providers") {
		t.Errorf("expected rich error listing available providers, got: %v", err)
	}
}

func TestValidateProviders_FallbackOnListError(t *testing.T) {
	sim := newSimulatedClient()
	sim.listProvidersErr = fmt.Errorf("forbidden")
	sim.validProviders["claude-prod"] = true
	p := &Provider{clientFunc: newSimulatedClientFactory(sim)}

	_, err := p.Plan(context.Background(), &deploy.PlanRequest{
		PackJSON: testPackJSON, DeployConfig: testDeployConfig,
	})
	if err != nil {
		t.Fatalf("expected fallback per-ref validation to succeed, got: %v", err)
	}
}

func TestValidateProviders_FallbackError(t *testing.T) {
	sim := newSimulatedClient()
	sim.listProvidersErr = fmt.Errorf("forbidden")
	// validProviders is empty -> the per-ref fallback also fails.
	p := &Provider{clientFunc: newSimulatedClientFactory(sim)}

	_, err := p.Plan(context.Background(), &deploy.PlanRequest{
		PackJSON: testPackJSON, DeployConfig: testDeployConfig,
	})
	if err == nil || !strings.Contains(err.Error(), "provider validation failed") {
		t.Fatalf("expected fallback provider validation error, got: %v", err)
	}
}

// packNoConfigTools is a pack declaring one tool, with a deploy config that has
// neither a tools block nor a tool_registry_ref — exercising discover mode.
const discoverPackJSON = `{
	"id": "test-pack",
	"version": "1.0.0",
	"prompts": {"main": {"system": "hi", "description": "main"}},
	"tools": {"refund": {"name": "refund", "description": "Refund", "parameters": {"type": "object"}}}
}`

const discoverDeployConfig = `{
	"api_endpoint": "https://omnia.test.com",
	"workspace": "test-ws",
	"api_token": "test-token",
	"providers": {"default": "claude-prod"}
}`

func TestPlan_BindMode_NoToolRegistryCreated(t *testing.T) {
	sim := newSimulatedClient()
	sim.validProviders["claude-prod"] = true
	sim.toolRegistries = []ToolRegistrySummary{{
		Name:  "shared-tools",
		Tools: []RegistryTool{{Name: "refund", InputSchema: json.RawMessage(`{"type":"object"}`)}},
	}}
	p := &Provider{clientFunc: newSimulatedClientFactory(sim)}

	cfg := `{
		"api_endpoint": "https://omnia.test.com",
		"workspace": "test-ws",
		"api_token": "test-token",
		"providers": {"default": "claude-prod"},
		"tool_registry_ref": "shared-tools"
	}`
	resp, err := p.Plan(context.Background(), &deploy.PlanRequest{
		PackJSON: discoverPackJSON, DeployConfig: cfg,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, c := range resp.Changes {
		if c.Type == ResTypeToolRegistry {
			t.Errorf("bind mode must NOT create a ToolRegistry, got change %+v", c)
		}
	}
	// Full coverage + matching schema → no tool warnings.
	for _, w := range resp.Warnings {
		if strings.Contains(w, "does not provide") || strings.Contains(w, "different input schema") {
			t.Errorf("unexpected tool warning on full coverage: %q", w)
		}
	}
}

func TestPlan_DiscoverMode_AutoBindWarning(t *testing.T) {
	sim := newSimulatedClient()
	sim.validProviders["claude-prod"] = true
	sim.toolRegistries = []ToolRegistrySummary{{
		Name: "refunds", Tools: []RegistryTool{{Name: "refund"}},
	}}
	p := &Provider{clientFunc: newSimulatedClientFactory(sim)}

	resp, err := p.Plan(context.Background(), &deploy.PlanRequest{
		PackJSON: discoverPackJSON, DeployConfig: discoverDeployConfig,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var found bool
	for _, w := range resp.Warnings {
		if strings.Contains(w, "auto-bound tools to registry \"refunds\"") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected auto-bind warning in plan warnings, got %v", resp.Warnings)
	}
	// Auto-bind must not create a registry either.
	for _, c := range resp.Changes {
		if c.Type == ResTypeToolRegistry {
			t.Errorf("auto-bind must NOT create a ToolRegistry, got %+v", c)
		}
	}
}

func TestPlan_DryRun_SkipsToolDiscovery(t *testing.T) {
	sim := newSimulatedClient()
	// A list error would surface if dry-run called ListToolRegistries.
	sim.listToolRegistriesErr = fmt.Errorf("should not be called")
	p := &Provider{clientFunc: newSimulatedClientFactory(sim)}

	cfg := `{
		"api_endpoint": "https://omnia.test.com",
		"workspace": "test-ws",
		"api_token": "test-token",
		"providers": {"default": "claude-prod"},
		"dry_run": true
	}`
	if _, err := p.Plan(context.Background(), &deploy.PlanRequest{
		PackJSON: discoverPackJSON, DeployConfig: cfg,
	}); err != nil {
		t.Fatalf("dry-run plan must not touch the API, got: %v", err)
	}
}

func TestBuildSummary(t *testing.T) {
	changes := []deploy.ResourceChange{
		{Action: deploy.ActionCreate},
		{Action: deploy.ActionCreate},
		{Action: deploy.ActionUpdate},
		{Action: deploy.ActionDelete},
	}
	summary := buildSummary(changes)
	if summary != "Plan: 2 to create, 1 to update, 1 to delete" {
		t.Errorf("unexpected summary: %q", summary)
	}

	// Verify NO_CHANGE and DRIFT are not tallied.
	changes = append(changes,
		deploy.ResourceChange{Action: deploy.ActionNoChange},
		deploy.ResourceChange{Action: deploy.ActionDrift},
	)
	summary = buildSummary(changes)
	if !strings.Contains(summary, "2 to create") {
		t.Errorf("NO_CHANGE/DRIFT should not be tallied, got: %q", summary)
	}
}
