package omnia

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/AltairaLabs/PromptKit/runtime/deploy"
	"github.com/AltairaLabs/PromptKit/runtime/prompt"
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
	priorState := AdapterState{
		Resources: []ResourceState{
			{Type: ResTypePromptPack, Name: "test-pack"},
			{Type: ResTypeToolRegistry, Name: "test-pack-tools"},
			{Type: ResTypeAgentRuntime, Name: "test-pack"},
		},
		PackID:  "test-pack",
		Version: "0.9.0",
	}
	priorJSON, _ := json.Marshal(priorState)

	p, _ := newPlanTestProvider()
	resp, err := p.Plan(context.Background(), &deploy.PlanRequest{
		PackJSON:     testPackJSON,
		DeployConfig: testDeployConfig,
		PriorState:   string(priorJSON),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for _, c := range resp.Changes {
		if c.Action != deploy.ActionUpdate {
			t.Errorf("expected UPDATE action for %s %q, got %s", c.Type, c.Name, c.Action)
		}
	}
}

func TestPlan_WithDeletion(t *testing.T) {
	// Prior state has an extra resource not in the desired set.
	priorState := AdapterState{
		Resources: []ResourceState{
			{Type: ResTypePromptPack, Name: "test-pack"},
			{Type: ResTypeToolRegistry, Name: "test-pack-tools"},
			{Type: ResTypeAgentRuntime, Name: "test-pack"},
			{Type: ResTypeAgentPolicy, Name: "test-pack-policy"},
		},
		PackID:  "test-pack",
		Version: "1.0.0",
	}
	priorJSON, _ := json.Marshal(priorState)

	p, _ := newPlanTestProvider()
	resp, err := p.Plan(context.Background(), &deploy.PlanRequest{
		PackJSON:     testPackJSON,
		DeployConfig: testDeployConfig,
		PriorState:   string(priorJSON),
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

func TestToolCoverageWarnings(t *testing.T) {
	userTools := &prompt.Pack{Tools: map[string]*prompt.PackTool{"refund": {}, "lookup": {}}}

	t.Run("declared tools, no handlers warns", func(t *testing.T) {
		w := toolCoverageWarnings(userTools, &Config{})
		if len(w) != 1 || !strings.Contains(w[0], "lookup, refund") {
			t.Errorf("want one warning naming [lookup, refund], got %v", w)
		}
	})
	t.Run("handlers present, no warning", func(t *testing.T) {
		if w := toolCoverageWarnings(userTools, &Config{Tools: []ToolHandler{{}}}); w != nil {
			t.Errorf("want nil, got %v", w)
		}
	})
	t.Run("no declared tools, no warning", func(t *testing.T) {
		if w := toolCoverageWarnings(&prompt.Pack{}, &Config{}); w != nil {
			t.Errorf("want nil, got %v", w)
		}
	})
	t.Run("system tools only, no warning", func(t *testing.T) {
		sys := &prompt.Pack{Tools: map[string]*prompt.PackTool{"image__generate": {}}}
		if w := toolCoverageWarnings(sys, &Config{}); w != nil {
			t.Errorf("want nil, got %v", w)
		}
	})
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
