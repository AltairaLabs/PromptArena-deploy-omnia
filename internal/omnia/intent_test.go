package omnia

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/AltairaLabs/PromptKit/runtime/packspec"
	"github.com/AltairaLabs/PromptKit/runtime/prompt"
	"github.com/AltairaLabs/promptarena/deploy/adaptersdk"
)

// buildIntentForTest parses the pack + config, resolves the binding from config
// alone, and returns the decoded DeployIntent the adapter would submit.
func buildIntentForTest(t *testing.T, packJSON, configJSON string) deployIntent {
	t.Helper()
	pack, err := adaptersdk.ParsePack([]byte(packJSON))
	if err != nil {
		t.Fatalf("ParsePack: %v", err)
	}
	cfg, err := parseConfig(configJSON)
	if err != nil {
		t.Fatalf("parseConfig: %v", err)
	}
	cfg.PackJSON = packJSON

	binding := dryRunToolBinding(pack, cfg)
	body, err := buildDeployIntent(pack, cfg, binding)
	if err != nil {
		t.Fatalf("buildDeployIntent: %v", err)
	}
	var intent deployIntent
	if err := json.Unmarshal(body, &intent); err != nil {
		t.Fatalf("unmarshal intent: %v", err)
	}
	return intent
}

func TestBuildDeployIntent_PackAndAgent(t *testing.T) {
	intent := buildIntentForTest(t, testPackJSON, testDeployConfig)

	if intent.APIVersion != intentAPIVersionV1 {
		t.Errorf("apiVersion = %q, want %q", intent.APIVersion, intentAPIVersionV1)
	}
	if intent.Pack.Name != "test-pack" || intent.Pack.Version != "1.0.0" {
		t.Errorf("pack = %+v, want name=test-pack version=1.0.0", intent.Pack)
	}
	if intent.Pack.Content != testPackJSON {
		t.Error("pack.content must carry the raw pack JSON verbatim")
	}
	if len(intent.Agents) != 1 || intent.Agents[0].Name != "test-pack" {
		t.Fatalf("agents = %+v, want a single agent named test-pack", intent.Agents)
	}
	agent := intent.Agents[0]
	if len(agent.Providers) != 1 || agent.Providers[0].Ref != "claude-prod" ||
		agent.Providers[0].Role != roleLLM {
		t.Errorf("providers = %+v, want claude-prod with the default llm role", agent.Providers)
	}
	if !agent.UseTools {
		t.Error("useTools must be true when the deploy binds a registry")
	}
	if len(agent.Facades) != 1 || agent.Facades[0].Type != facadeTypeWebSocket {
		t.Errorf("facades = %+v, want a single websocket facade", agent.Facades)
	}
}

func TestBuildDeployIntent_LabelsOmitResourceType(t *testing.T) {
	cfg := `{
		"api_endpoint": "https://omnia.test.com",
		"workspace": "test-ws",
		"api_token": "test-token",
		"providers": {"default": "claude-prod"},
		"labels": {"team": "platform"}
	}`
	intent := buildIntentForTest(t, testPackJSON, cfg)

	if intent.Labels[LabelManagedBy] != managedByValue {
		t.Errorf("managed-by label = %q, want %q", intent.Labels[LabelManagedBy], managedByValue)
	}
	if intent.Labels[LabelPackID] != "test-pack" || intent.Labels[LabelPackVer] != "1.0.0" {
		t.Errorf("pack labels = %+v, want pack-id/pack-version set", intent.Labels)
	}
	if intent.Labels["team"] != "platform" {
		t.Error("user labels must be carried through")
	}
	// A single deploy-wide map cannot express a per-resource value, so the
	// resource-type label is deliberately dropped on this path.
	if _, present := intent.Labels[LabelResType]; present {
		t.Errorf("resource-type label must not be sent deploy-wide, got %q", intent.Labels[LabelResType])
	}
}

func TestBuildDeployIntent_CreateModeSendsHandlers(t *testing.T) {
	intent := buildIntentForTest(t, testPackJSON, testDeployConfig)

	if intent.Tools == nil || len(intent.Tools.Handlers) == 0 {
		t.Fatalf("tools = %+v, want handlers for create mode", intent.Tools)
	}
	if intent.Tools.Ref != "" {
		t.Error("create mode must not also set tools.ref (they are mutually exclusive)")
	}
	var found bool
	for _, h := range intent.Tools.Handlers {
		if h.Name != "search" {
			continue
		}
		found = true
		if h.Type != "http" {
			t.Errorf("handler type = %q, want http", h.Type)
		}
		if h.Tool == nil || !strings.Contains(string(*h.Tool), `"search"`) {
			t.Errorf("handler tool block not carried through: %v", h.Tool)
		}
		if h.HTTPConfig == nil || !strings.Contains(string(*h.HTTPConfig), "api.example.com") {
			t.Errorf("handler httpConfig not carried through: %v", h.HTTPConfig)
		}
	}
	if !found {
		t.Error("configured handler 'search' missing from the intent")
	}
}

func TestBuildDeployIntent_BindModeReferencesRegistry(t *testing.T) {
	cfg := `{
		"api_endpoint": "https://omnia.test.com",
		"workspace": "test-ws",
		"api_token": "test-token",
		"providers": {"default": "claude-prod"},
		"tool_registry_ref": "shared-tools"
	}`
	intent := buildIntentForTest(t, testPackJSON, cfg)

	if intent.Tools == nil || intent.Tools.Ref != "shared-tools" {
		t.Fatalf("tools = %+v, want ref=shared-tools", intent.Tools)
	}
	if len(intent.Tools.Handlers) != 0 {
		t.Error("bind mode must send no handlers")
	}
	if !intent.Agents[0].UseTools {
		t.Error("useTools must be true when binding an existing registry")
	}
}

func TestBuildDeployIntent_NoToolsOmitsBlock(t *testing.T) {
	cfg := `{
		"api_endpoint": "https://omnia.test.com",
		"workspace": "test-ws",
		"api_token": "test-token",
		"providers": {"default": "claude-prod"}
	}`
	const noToolsPack = `{
		"id": "bare-pack",
		"version": "2.0.0",
		"prompts": {"main": {"system": "hi", "description": "main"}}
	}`
	intent := buildIntentForTest(t, noToolsPack, cfg)

	if intent.Tools != nil {
		t.Errorf("tools = %+v, want nil when nothing is bound", intent.Tools)
	}
	if intent.Agents[0].UseTools {
		t.Error("useTools must be false when no registry is bound")
	}
}

func TestBuildDeployIntent_MultiAgentFanOut(t *testing.T) {
	cfg := `{
		"api_endpoint": "https://omnia.test.com",
		"workspace": "test-ws",
		"api_token": "test-token",
		"providers": {"default": "claude-prod"}
	}`
	intent := buildIntentForTest(t, multiAgentPackJSON, cfg)

	if len(intent.Agents) != 2 {
		t.Fatalf("want 2 agents from the multi-agent pack, got %d", len(intent.Agents))
	}
	names := map[string]bool{intent.Agents[0].Name: true, intent.Agents[1].Name: true}
	if !names["alice"] || !names["bob"] {
		t.Errorf("agent names = %v, want alice and bob", names)
	}
	for _, a := range intent.Agents {
		if a.PromptName != "" {
			t.Errorf("agent %q: multi-agent members resolve their own entry, got promptName=%q",
				a.Name, a.PromptName)
		}
	}
}

func TestBuildDeployIntent_MultiPromptFanOutPinsPromptName(t *testing.T) {
	cfg := `{
		"api_endpoint": "https://omnia.test.com",
		"workspace": "test-ws",
		"api_token": "test-token",
		"providers": {"default": "claude-prod"}
	}`
	const twoPromptPack = `{
		"id": "duo",
		"version": "1.0.0",
		"prompts": {
			"first": {"system": "one", "description": "first"},
			"second": {"system": "two", "description": "second"}
		}
	}`
	intent := buildIntentForTest(t, twoPromptPack, cfg)

	if len(intent.Agents) != 2 {
		t.Fatalf("want one agent per prompt, got %d", len(intent.Agents))
	}
	want := map[string]string{"duo-first": "first", "duo-second": "second"}
	for _, a := range intent.Agents {
		if want[a.Name] != a.PromptName {
			t.Errorf("agent %q promptName = %q, want %q", a.Name, a.PromptName, want[a.Name])
		}
	}
}

func TestBuildDeployIntent_PolicyFromPackBlocklist(t *testing.T) {
	const policyPack = `{
		"id": "policy-pack",
		"version": "1.0.0",
		"prompts": {
			"main": {
				"system": "hi",
				"description": "main",
				"tool_policy": {"blocklist": ["danger", "alpha", "danger"]}
			}
		}
	}`
	cfg := `{
		"api_endpoint": "https://omnia.test.com",
		"workspace": "test-ws",
		"api_token": "test-token",
		"providers": {"default": "claude-prod"},
		"tool_registry_ref": "shared-tools"
	}`
	intent := buildIntentForTest(t, policyPack, cfg)

	if intent.Policy == nil {
		t.Fatal("policy block missing for a pack declaring a blocklist")
	}
	want := []string{"alpha", "danger"}
	if len(intent.Policy.ToolBlocklist) != len(want) {
		t.Fatalf("blocklist = %v, want sorted+deduped %v", intent.Policy.ToolBlocklist, want)
	}
	for i, v := range want {
		if intent.Policy.ToolBlocklist[i] != v {
			t.Errorf("blocklist[%d] = %q, want %q", i, intent.Policy.ToolBlocklist[i], v)
		}
	}
}

// The assertions live outside the table so each one stands on its own and the
// test body stays a plain loop.

func assertNoExternalAuthIntent(t *testing.T, got *externalAuthIntent) {
	t.Helper()
	if got != nil {
		t.Errorf("want nil externalAuth, got %+v", got)
	}
}

func assertClientKeysFromAPIKeys(t *testing.T, got *externalAuthIntent) {
	t.Helper()
	if got == nil || got.ClientKeys == nil {
		t.Fatalf("want clientKeys, got %+v", got)
	}
	if got.ClientKeys.DefaultRole != "viewer" || !got.ClientKeys.TrustEndUserHeader {
		t.Errorf("clientKeys = %+v", got.ClientKeys)
	}
}

func assertOIDCClaimMapping(t *testing.T, got *externalAuthIntent) {
	t.Helper()
	if got == nil || got.OIDC == nil || got.OIDC.ClaimMapping == nil {
		t.Fatalf("want oidc claim mapping, got %+v", got)
	}
	if got.OIDC.ClaimMapping.Subject != "sub" || got.OIDC.ClaimMapping.EndUser != "eu" {
		t.Errorf("claimMapping = %+v", got.OIDC.ClaimMapping)
	}
}

func assertEmptyClaimMappingOmitted(t *testing.T, got *externalAuthIntent) {
	t.Helper()
	if got.OIDC.ClaimMapping != nil {
		t.Errorf("empty claim mapping must be omitted, got %+v", got.OIDC.ClaimMapping)
	}
}

func assertEdgeTrustHeaders(t *testing.T, got *externalAuthIntent) {
	t.Helper()
	if got.EdgeTrust == nil || got.EdgeTrust.HeaderMapping == nil {
		t.Fatalf("want edgeTrust header mapping, got %+v", got)
	}
	if got.EdgeTrust.HeaderMapping.Subject != "x-sub" ||
		got.EdgeTrust.HeaderMapping.Email != "x-mail" {
		t.Errorf("headerMapping = %+v", got.EdgeTrust.HeaderMapping)
	}
	if got.EdgeTrust.ClaimsFromHeaders["tenant"] != "x-tenant" {
		t.Errorf("claimsFromHeaders = %+v", got.EdgeTrust.ClaimsFromHeaders)
	}
}

func TestIntentExternalAuth(t *testing.T) {
	trueVal := true
	tests := []struct {
		name   string
		in     *ExternalAuthConfig
		assert func(t *testing.T, got *externalAuthIntent)
	}{
		{
			name:   "nil config",
			in:     nil,
			assert: assertNoExternalAuthIntent,
		},
		{
			name: "apiKeys maps onto clientKeys",
			in: &ExternalAuthConfig{
				APIKeys: &APIKeysAuthConfig{DefaultRole: "viewer", TrustEndUserHeader: true},
			},
			assert: assertClientKeysFromAPIKeys,
		},
		{
			name: "oidc with claim mapping",
			in: &ExternalAuthConfig{OIDC: &OIDCAuthConfig{
				Issuer:       "https://issuer.example",
				Audience:     "aud",
				ClaimMapping: &OIDCClaimMappingConfig{Subject: "sub", EndUser: "eu"},
			}},
			assert: assertOIDCClaimMapping,
		},
		{
			name: "oidc without claim mapping content",
			in: &ExternalAuthConfig{OIDC: &OIDCAuthConfig{
				Issuer: "i", Audience: "a", ClaimMapping: &OIDCClaimMappingConfig{},
			}},
			assert: assertEmptyClaimMappingOmitted,
		},
		{
			name: "edgeTrust headers",
			in: &ExternalAuthConfig{EdgeTrust: &EdgeTrustAuthConfig{
				HeaderMapping:     &EdgeTrustHeaderMappingConfig{Subject: "x-sub", Email: "x-mail"},
				ClaimsFromHeaders: map[string]string{"tenant": "x-tenant"},
			}},
			assert: assertEdgeTrustHeaders,
		},
		{
			name:   "empty edgeTrust omitted",
			in:     &ExternalAuthConfig{EdgeTrust: &EdgeTrustAuthConfig{}},
			assert: assertNoExternalAuthIntent,
		},
		{
			name:   "allowManagementPlane alone is a facade concern",
			in:     &ExternalAuthConfig{AllowManagementPlane: &trueVal},
			assert: assertNoExternalAuthIntent,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.assert(t, intentExternalAuth(tt.in))
		})
	}
}

func TestIntentFacades_ProjectsManagementPlane(t *testing.T) {
	falseVal := false
	got := intentFacades(&ExternalAuthConfig{AllowManagementPlane: &falseVal})
	if len(got) != 1 || got[0].ManagementPlane == nil || *got[0].ManagementPlane {
		t.Fatalf("facades = %+v, want managementPlane=false projected onto the facade", got)
	}
	if plain := intentFacades(nil); len(plain) != 1 || plain[0].ManagementPlane != nil {
		t.Errorf("facades = %+v, want a bare websocket facade when auth is unset", plain)
	}
}

func TestIntentMemory_FlattensAccessFilter(t *testing.T) {
	limit := int32(7)
	got := intentMemory(&MemoryConfig{
		Enabled: true,
		Retrieval: &MemoryRetrievalConfig{
			Strategy:     "semantic",
			Limit:        &limit,
			AccessFilter: &MemoryAccessFilterConfig{DenyCEL: "user.role == 'guest'"},
		},
	})
	if got == nil || got.Retrieval == nil {
		t.Fatal("want a retrieval block")
	}
	if got.Retrieval.Strategy != "semantic" || *got.Retrieval.Limit != 7 {
		t.Errorf("retrieval = %+v", got.Retrieval)
	}
	if got.Retrieval.DenyCEL != "user.role == 'guest'" {
		t.Errorf("denyCEL = %q, want the nested accessFilter flattened", got.Retrieval.DenyCEL)
	}

	if bare := intentMemory(&MemoryConfig{Enabled: true}); bare == nil || bare.Retrieval != nil {
		t.Errorf("memory without retrieval = %+v, want enabled only", bare)
	}
	if intentMemory(nil) != nil {
		t.Error("nil memory config must map to nil")
	}
}

func TestIntentEvals_FlattensGroups(t *testing.T) {
	got := intentEvals(&EvalsConfig{
		Enabled: true,
		Inline:  &EvalPathConfig{Groups: []string{"smoke"}},
		Worker:  &EvalPathConfig{Groups: []string{"nightly", "regression"}},
	})
	if got == nil || !got.Enabled {
		t.Fatalf("evals = %+v", got)
	}
	if len(got.Inline) != 1 || got.Inline[0] != "smoke" {
		t.Errorf("inlineGroups = %v", got.Inline)
	}
	if len(got.Worker) != 2 {
		t.Errorf("workerGroups = %v", got.Worker)
	}
	if intentEvals(nil) != nil {
		t.Error("nil evals config must map to nil")
	}
}

func TestIntentRuntime(t *testing.T) {
	got := intentRuntime(&RuntimeConfig{Replicas: 3, CPU: "500m", Memory: "1Gi"})
	if got == nil || got.Replicas == nil || *got.Replicas != 3 {
		t.Fatalf("runtime = %+v", got)
	}
	if got.CPU != "500m" || got.Memory != "1Gi" {
		t.Errorf("runtime resources = %+v", got)
	}
	if intentRuntime(nil) != nil {
		t.Error("nil runtime config must map to nil")
	}
	if empty := intentRuntime(&RuntimeConfig{}); empty != nil {
		t.Errorf("an empty runtime block must map to nil, got %+v", empty)
	}
}

func TestIntentSkillsConfig(t *testing.T) {
	maxActive := 4
	got := intentSkillsConfig(&SkillsConfig{MaxActive: &maxActive, Selector: "tag"})
	if got == nil || got.MaxActive == nil || *got.MaxActive != 4 || got.Selector != "tag" {
		t.Fatalf("skillsConfig = %+v", got)
	}
	if intentSkillsConfig(nil) != nil || intentSkillsConfig(&SkillsConfig{}) != nil {
		t.Error("an empty skillsConfig must map to nil")
	}
}

func TestIntentSkills(t *testing.T) {
	got := intentSkills([]SkillBinding{
		{Source: "anthropic-skills", Include: []string{"a"}, MountAs: "sk"},
	})
	if len(got) != 1 || got[0].Source != "anthropic-skills" || got[0].MountAs != "sk" {
		t.Fatalf("skills = %+v", got)
	}
	if intentSkills(nil) != nil {
		t.Error("no skills must map to nil")
	}
}

func TestIntentPolicy_RequiresRegistry(t *testing.T) {
	pack := &prompt.Pack{Pack: packspec.Pack{
		ID: "p",
		Prompts: map[string]*prompt.PackPrompt{
			"main": {ToolPolicy: &prompt.ToolPolicyPack{Blocklist: []string{"danger"}}},
		},
	}}
	if got := intentPolicy(pack, ToolBinding{Mode: toolModeNone}); got != nil {
		t.Errorf("a blocklist with no registry must emit no policy, got %+v", got)
	}
	got := intentPolicy(pack, ToolBinding{Mode: toolModeBind, RegistryName: "shared"})
	if got == nil || len(got.ToolBlocklist) != 1 {
		t.Errorf("policy = %+v, want the blocklist carried", got)
	}
}

func TestPreflightIntentV1(t *testing.T) {
	packWithBlocklist := &prompt.Pack{Pack: packspec.Pack{
		ID: "p",
		Prompts: map[string]*prompt.PackPrompt{
			"main": {ToolPolicy: &prompt.ToolPolicyPack{Blocklist: []string{"danger"}}},
		},
	}}
	plainPack := &prompt.Pack{Pack: packspec.Pack{ID: "p", Prompts: map[string]*prompt.PackPrompt{"main": {}}}}
	boundRegistry := ToolBinding{Mode: toolModeBind, RegistryName: "shared"}

	tests := []struct {
		name    string
		pack    *prompt.Pack
		cfg     *Config
		binding ToolBinding
		want    string // substring the single expected reason must contain ("" = no reasons)
	}{
		{name: "clean config", pack: plainPack, cfg: &Config{}, binding: boundRegistry},
		{
			// omnia#1916 added autoscaling to the contract, so it no longer
			// diverts — it is sent like any other runtime setting.
			name: "autoscaling does not divert",
			pack: plainPack, binding: boundRegistry,
			cfg: &Config{Runtime: &RuntimeConfig{Autoscaling: &AutoscalingConfig{Enabled: true}}},
		},
		{
			// Omnia removed sharedToken, so the per-resource path cannot deliver it
			// either — diverting would buy an identical silent drop. It is migrated
			// onto client keys and reported by removedFieldWarnings instead.
			name: "shared token does not divert",
			pack: plainPack, binding: boundRegistry,
			cfg: &Config{ExternalAuth: &ExternalAuthConfig{
				SharedToken: &SharedTokenAuthConfig{SecretRef: "s"},
			}},
		},
		{
			name: "oidc role claim does not divert",
			pack: plainPack, binding: boundRegistry,
			cfg: &Config{ExternalAuth: &ExternalAuthConfig{OIDC: &OIDCAuthConfig{
				Issuer: "i", Audience: "a",
				ClaimMapping: &OIDCClaimMappingConfig{Role: "roles"},
			}}},
		},
		{
			name: "edge trust role header does not divert",
			pack: plainPack, binding: boundRegistry,
			cfg: &Config{ExternalAuth: &ExternalAuthConfig{EdgeTrust: &EdgeTrustAuthConfig{
				HeaderMapping: &EdgeTrustHeaderMappingConfig{Role: "x-role"},
			}}},
		},
		{
			name: "handler selector does not divert",
			pack: plainPack, binding: boundRegistry,
			cfg: &Config{Tools: []ToolHandler{
				{Name: "search", Selector: map[string]interface{}{"matchLabels": "x"}},
			}},
		},
		{
			name: "blocklist without a registry",
			pack: packWithBlocklist, cfg: &Config{}, binding: ToolBinding{Mode: toolModeNone},
			want: "tool blocklist",
		},
		{
			name: "registry name the server would not reproduce",
			pack: plainPack, cfg: &Config{},
			binding: ToolBinding{Mode: toolModeCreate, RegistryName: "hand-picked-name"},
			want:    "does not match",
		},
		{
			name: "matching create-mode registry name",
			pack: plainPack, cfg: &Config{},
			binding: ToolBinding{Mode: toolModeCreate, RegistryName: "p-tools"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := preflightIntentV1(tt.pack, tt.cfg, tt.binding)
			if tt.want == "" {
				if len(got) != 0 {
					t.Fatalf("want no blockers, got %v", got)
				}
				return
			}
			if len(got) != 1 {
				t.Fatalf("want exactly one blocker containing %q, got %v", tt.want, got)
			}
			if !strings.Contains(got[0], tt.want) {
				t.Errorf("blocker = %q, want it to name %q", got[0], tt.want)
			}
		})
	}
}

func TestPromptPackObjectName_MatchesServerScheme(t *testing.T) {
	// Locked against omnia's v1alpha1.PromptPackObjectName: sha256("name@version"),
	// first 12 hex characters, "pp-" prefixed. A drift here silently mis-names the
	// plan preview, so the expectation is pinned to a literal.
	got := promptPackObjectName("test-pack", "1.0.0")
	if !strings.HasPrefix(got, "pp-") || len(got) != len("pp-")+packObjectHashLen {
		t.Fatalf("object name %q has the wrong shape", got)
	}
	if promptPackObjectName("test-pack", "1.0.0") != got {
		t.Error("object naming must be deterministic")
	}
	if promptPackObjectName("test-pack", "1.0.1") == got {
		t.Error("a new pack version must yield a distinct object name")
	}
	if promptPackObjectName("other-pack", "1.0.0") == got {
		t.Error("a different pack must yield a distinct object name")
	}
}

func TestToolRegistryObjectName(t *testing.T) {
	if got := toolRegistryObjectName("test-pack"); got != "test-pack-tools" {
		t.Errorf("registry name = %q, want test-pack-tools", got)
	}
}

func TestDeployProfile_SupportsIntentV1(t *testing.T) {
	if (&DeployProfile{}).supportsIntentV1() {
		t.Error("an empty profile advertises no deploy-intent support")
	}
	if (*DeployProfile)(nil).supportsIntentV1() {
		t.Error("a nil profile advertises no deploy-intent support")
	}
	if (&DeployProfile{SupportedDeployIntentVersions: []string{"deploy.omnia.altairalabs.ai/v99"}}).
		supportsIntentV1() {
		t.Error("an unrelated contract version must not be treated as supported")
	}
	if !(&DeployProfile{
		SupportedDeployIntentVersions: []string{"other", intentAPIVersionV1},
	}).supportsIntentV1() {
		t.Error("the advertised v1 contract must be recognized")
	}
}

func TestIntentAutoscaling(t *testing.T) {
	minR, maxR, cpu, mem, stab := 2, 8, 70, 80, 300
	got := intentAutoscaling(&AutoscalingConfig{
		Enabled:                       true,
		Type:                          "hpa",
		MinReplicas:                   &minR,
		MaxReplicas:                   &maxR,
		TargetCPUUtilization:          &cpu,
		TargetMemoryUtilization:       &mem,
		ScaleDownStabilizationSeconds: &stab,
	})
	if got == nil {
		t.Fatal("want an autoscaling block")
	}
	if !got.Enabled || got.Type != "hpa" {
		t.Errorf("autoscaling = %+v, want enabled hpa", got)
	}
	for name, pair := range map[string][2]interface{}{
		"minReplicas": {got.MinReplicas, int32(2)},
		"maxReplicas": {got.MaxReplicas, int32(8)},
		"targetCPU":   {got.TargetCPUUtilizationPercentage, int32(70)},
		"targetMem":   {got.TargetMemoryUtilizationPercentage, int32(80)},
		"scaleDown":   {got.ScaleDownStabilizationSeconds, int32(300)},
	} {
		ptr, _ := pair[0].(*int32)
		if ptr == nil || *ptr != pair[1].(int32) {
			t.Errorf("%s = %v, want %v", name, ptr, pair[1])
		}
	}

	if intentAutoscaling(nil) != nil {
		t.Error("no autoscaling config must map to nil")
	}
}

func TestIntentRuntime_AutoscalingAloneStillEmitsRuntime(t *testing.T) {
	// Before omnia#1916 this config diverted to the per-resource path. It must
	// now produce a runtime block even with no replicas or resource requests set,
	// or the autoscaling settings would be dropped on the floor.
	got := intentRuntime(&RuntimeConfig{Autoscaling: &AutoscalingConfig{Enabled: true}})
	if got == nil || got.Autoscaling == nil {
		t.Fatalf("runtime = %+v, want an autoscaling-only runtime block", got)
	}
	if got.Replicas != nil || got.CPU != "" || got.Memory != "" {
		t.Errorf("runtime = %+v, want nothing invented beyond autoscaling", got)
	}
}
