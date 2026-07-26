//go:build integration

package omnia

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/AltairaLabs/PromptKit/runtime/deploy"
)

// These tests assert on the OBJECTS a deploy actually produces in the cluster,
// read back through the Omnia API, rather than on the adapter's own event
// stream. That distinction matters for the deploy-intent path, where the server
// — not the adapter — builds the resources: an event says what the adapter was
// told, only a read-back says what landed.
//
// Each test works on BOTH deploy paths. Where the two genuinely differ (object
// naming), the assertion is driven off the returned state rather than a
// hardcoded name.

// applyTracked applies and registers teardown BEFORE the apply can fail, so a
// mid-apply failure still tears down whatever was created. (Applying first and
// registering cleanup afterwards — as the older tests do — leaks every resource
// of a failed run, because the fatal happens before the cleanup is registered.)
func applyTracked(
	t *testing.T, p *Provider, env itConfig, deployConfig string, req *deploy.PlanRequest,
) (string, []*deploy.ApplyEvent) {
	t.Helper()

	var state string
	t.Cleanup(func() {
		if state == "" {
			return
		}
		err := p.Destroy(context.Background(), &deploy.DestroyRequest{
			DeployConfig: deployConfig,
			Environment:  env.Workspace,
			PriorState:   state,
		}, func(_ *deploy.DestroyEvent) error { return nil })
		if err != nil {
			t.Logf("cleanup Destroy returned (non-fatal): %v", err)
		}
	})

	var events []*deploy.ApplyEvent
	// Apply returns the state it managed to build even on failure, so cleanup
	// still sees whatever landed.
	s, err := p.Apply(context.Background(), req, func(e *deploy.ApplyEvent) error {
		events = append(events, e)
		return nil
	})
	state = s
	if err != nil {
		t.Fatalf("Apply failed: %v", describeApplyFailure(err))
	}
	return state, events
}

// describeApplyFailure annotates known upstream defects so a red run points at
// the actual cause instead of a generic message.
func describeApplyFailure(err error) string {
	msg := err.Error()
	if strings.Contains(msg, "Operation cannot be fulfilled") {
		return msg + "\n\n  ^ This is a 409 optimistic-lock conflict from the deploy-intent API's " +
			"AgentRuntime upsert, which does Get->mutate->Update with no retry " +
			"(omnia internal/api/deploy/apply.go upsertAgentRuntime). It loses the race " +
			"against a controller actively reconciling the agent — which is exactly the " +
			"trigger-mode canary case. The adapter solved the same problem on its own path " +
			"in #44 (updateWithRetry); the server-owned path needs the equivalent."
	}
	return msg
}

// itClient builds a real API client from a deploy config, for reading resources
// back after an apply.
func itClient(t *testing.T, deployConfig string) omniaClient {
	t.Helper()
	cfg, err := parseConfig(deployConfig)
	if err != nil {
		t.Fatalf("parseConfig: %v", err)
	}
	client, err := newHTTPClient(cfg)
	if err != nil {
		t.Fatalf("newHTTPClient: %v", err)
	}
	return client
}

// itServesIntentAPI reports whether the target workspace advertises the
// deploy-intent contract. Used to gate assertions that only hold on that path.
func itServesIntentAPI(t *testing.T, deployConfig string) bool {
	t.Helper()
	profile, err := itClient(t, deployConfig).GetDeployProfile(context.Background())
	if err != nil {
		return false
	}
	return profile.supportsIntentV1()
}

// stateResourceName returns the recorded name of the first resource of the given
// type in an apply state. Both deploy paths record what they actually wrote, so
// this is how a test names an object without assuming which path ran.
func stateResourceName(t *testing.T, state, resType string) string {
	t.Helper()
	var s AdapterState
	if err := json.Unmarshal([]byte(state), &s); err != nil {
		t.Fatalf("parse apply state: %v", err)
	}
	for _, r := range s.Resources {
		if r.Type == resType {
			return r.Name
		}
	}
	t.Fatalf("no %s resource in state %s", resType, state)
	return ""
}

// specOf reads a resource back and returns its spec as a generic map.
func specOf(t *testing.T, client omniaClient, resType, name string) map[string]any {
	t.Helper()
	resp, err := client.GetResource(context.Background(), resType, name)
	if err != nil {
		t.Fatalf("get %s %q: %v", resType, name, err)
	}
	var spec map[string]any
	if err := json.Unmarshal(resp.Spec, &spec); err != nil {
		t.Fatalf("parse %s %q spec: %v", resType, name, err)
	}
	return spec
}

// handlersByToolName indexes a ToolRegistry spec's handlers by the LLM-facing
// tool name they expose (handler.tool.name).
func handlersByToolName(spec map[string]any) map[string]map[string]any {
	out := map[string]map[string]any{}
	handlers, _ := spec["handlers"].([]any)
	for _, h := range handlers {
		entry, ok := h.(map[string]any)
		if !ok {
			continue
		}
		tool, ok := entry["tool"].(map[string]any)
		if !ok {
			continue
		}
		if name, ok := tool["name"].(string); ok {
			out[name] = entry
		}
	}
	return out
}

// nestedString walks a chain of map keys and returns the string at the end, or
// "" if any hop is missing or the wrong type.
func nestedString(m map[string]any, path ...string) string {
	cur := any(m)
	for _, k := range path {
		asMap, ok := cur.(map[string]any)
		if !ok {
			return ""
		}
		cur = asMap[k]
	}
	s, _ := cur.(string)
	return s
}

// ----------------------------------------------------------------------------
// Tools
// ----------------------------------------------------------------------------

// TestIntegration_ToolRegistryMaterializes deploys in create mode and reads the
// resulting ToolRegistry back, asserting BOTH handler kinds landed: the
// explicitly configured handler (authoritative, with its real HTTP wiring) and
// the synthesized placeholder for a pack tool the config did not cover. It then
// asserts the AgentRuntime actually binds that registry — the link that makes
// the tools reachable at runtime.
func TestIntegration_ToolRegistryMaterializes(t *testing.T) {
	env := itEnv(t)
	p := NewProvider()

	packID := uniquePackID(t)
	cfg := buildDeployConfig(env, deployConfigOpts{createTools: true})

	state, _ := applyTracked(t, p, env, cfg, &deploy.PlanRequest{
		PackJSON:     buildPack(packID),
		DeployConfig: cfg,
		Environment:  env.Workspace,
	})

	client := itClient(t, cfg)
	registryName := sanitizeName(packID + "-tools")
	handlers := handlersByToolName(specOf(t, client, ResTypeToolRegistry, registryName))

	// The configured handler is authoritative: its real endpoint and method must
	// survive the round trip untouched.
	configured, ok := handlers[itToolCreateName]
	if !ok {
		t.Fatalf("registry %q has no handler for %q; handlers = %v",
			registryName, itToolCreateName, handlerToolNames(handlers))
	}
	if got := nestedString(configured, "httpConfig", "endpoint"); got != "https://api.example.com/things" {
		t.Errorf("%s endpoint = %q, want the configured URL", itToolCreateName, got)
	}
	if got := nestedString(configured, "httpConfig", "method"); got != "POST" {
		t.Errorf("%s method = %q, want POST", itToolCreateName, got)
	}

	// The uncovered pack tool still gets a handler, so the operator can complete
	// it in Omnia rather than the tool silently not existing.
	if _, ok := handlers[itToolListName]; !ok {
		t.Errorf("registry %q has no synthesized handler for the uncovered tool %q; handlers = %v",
			registryName, itToolListName, handlerToolNames(handlers))
	}

	// The agent must actually reference the registry.
	agentName := stateResourceName(t, state, ResTypeAgentRuntime)
	agentSpec := specOf(t, client, ResTypeAgentRuntime, agentName)
	if got := nestedString(agentSpec, "toolRegistryRef", "name"); got != registryName {
		t.Errorf("agent %q toolRegistryRef = %q, want %q", agentName, got, registryName)
	}
}

// handlerToolNames returns the tool names a handler index covers, for readable
// failure messages.
func handlerToolNames(m map[string]map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// ----------------------------------------------------------------------------
// Skills
// ----------------------------------------------------------------------------

// TestIntegration_SkillsBindToPromptPack deploys a pack with a skills block and
// asserts the binding lands on the PromptPack's spec.skills — including the
// mountAs rename and the skillsConfig cap, which are separate code paths from
// the bare source reference.
//
// Requires a Ready SkillSource in the target workspace: Plan validates every
// referenced source exists and is synced, so this cannot be faked. Set
// OMNIA_IT_SKILL_SOURCE to its name to run.
func TestIntegration_SkillsBindToPromptPack(t *testing.T) {
	env := itEnv(t)
	source := os.Getenv("OMNIA_IT_SKILL_SOURCE")
	if source == "" {
		t.Skip("set OMNIA_IT_SKILL_SOURCE to a Ready SkillSource in the workspace")
	}
	p := NewProvider()

	packID := uniquePackID(t)
	cfg := buildDeployConfig(env, deployConfigOpts{
		skills: []map[string]any{
			// include narrows which skills mount; it is a separate passthrough from
			// the bare source reference and the mountAs rename.
			{"source": source, "mountAs": "it-mounted", "include": []string{"pdf", "xlsx"}},
		},
		skillsConfig: map[string]any{"maxActive": 2, "selector": "tag"},
	})

	state, _ := applyTracked(t, p, env, cfg, &deploy.PlanRequest{
		PackJSON:     buildPack(packID),
		DeployConfig: cfg,
		Environment:  env.Workspace,
	})

	packName := stateResourceName(t, state, ResTypePromptPack)
	spec := specOf(t, itClient(t, cfg), ResTypePromptPack, packName)

	skills, _ := spec["skills"].([]any)
	if len(skills) != 1 {
		t.Fatalf("promptpack %q spec.skills = %v, want exactly one binding", packName, spec["skills"])
	}
	binding, _ := skills[0].(map[string]any)
	if got, _ := binding["source"].(string); got != source {
		t.Errorf("skills[0].source = %q, want %q", got, source)
	}
	if got, _ := binding["mountAs"].(string); got != "it-mounted" {
		t.Errorf("skills[0].mountAs = %q, want it-mounted", got)
	}
	include, _ := binding["include"].([]any)
	if len(include) != 2 {
		t.Errorf("skills[0].include = %v, want the 2 configured entries — a dropped "+
			"include silently mounts every skill in the source", binding["include"])
	}

	sc, _ := spec["skillsConfig"].(map[string]any)
	if sc == nil {
		t.Fatalf("promptpack %q has no spec.skillsConfig", packName)
	}
	if got, _ := sc["maxActive"].(float64); got != 2 {
		t.Errorf("skillsConfig.maxActive = %v, want 2", sc["maxActive"])
	}
	if got, _ := sc["selector"].(string); got != "tag" {
		t.Errorf("skillsConfig.selector = %q, want tag", got)
	}
}

// ----------------------------------------------------------------------------
// Rollouts
// ----------------------------------------------------------------------------

// TestIntegration_RolloutSurvivesVersionBump is the adapter's "do not clobber a
// canary" guarantee, verified against a live cluster.
//
// The adapter deliberately sends no rollout block, so an agent already in
// version-trigger mode is owned by the rollout controller: a subsequent deploy
// of a NEW pack version must leave the live PromptPack pin, the trigger, the
// steps and any in-flight candidate exactly as they were. Advancing the pin here
// would hard-swap production traffic onto an uncanaried version.
//
// Only the deploy-intent path makes this promise (the server implements the
// preservation), so the test skips when the workspace does not serve it.
//
// KNOWN FAILURE (upstream, not this adapter): the v2 apply usually fails with a
// 409 "Operation cannot be fulfilled" — measured ~4 runs in 5 against a live
// controller. The deploy-intent API's AgentRuntime upsert does
// Get->mutate->Update with no conflict retry, so it loses the race against the
// rollout controller, which reconciles continuously while a candidate is in
// flight. See describeApplyFailure. The test is left strict on purpose: it is a
// genuine detector, and it will go green when the server retries on conflict the
// way the adapter's own path does (#44).
func TestIntegration_RolloutSurvivesVersionBump(t *testing.T) {
	env := itEnv(t)
	p := NewProvider()

	packID := uniquePackID(t)
	cfg := buildDeployConfig(env, deployConfigOpts{})
	if !itServesIntentAPI(t, cfg) {
		t.Skip("workspace does not serve the deploy-intent API; rollout preservation is server-side")
	}

	// --- v1: a normal deploy ---
	state1, _ := applyTracked(t, p, env, cfg, &deploy.PlanRequest{
		PackJSON:     buildPackVersion(packID, "1.0.0"),
		DeployConfig: cfg,
		Environment:  env.Workspace,
	})

	client := itClient(t, cfg)
	agentName := stateResourceName(t, state1, ResTypeAgentRuntime)

	// --- put the agent into version-trigger mode with an in-flight candidate ---
	// This is what the rollout controller owns; the adapter must never touch it.
	putAgentRollout(t, client, agentName, map[string]any{
		"trigger": map[string]any{"promptPackChannel": "stable"},
		"steps": []any{
			map[string]any{"setWeight": 25},
			map[string]any{"pause": map[string]any{"duration": "30s"}},
		},
		"candidate": map[string]any{
			"promptPackRef": map[string]any{"name": packID, "version": "1.0.0"},
		},
	})
	pinnedBefore := specOf(t, client, ResTypeAgentRuntime, agentName)["promptPackRef"]

	// --- v2: deploy a NEW pack version at the same pack ID ---
	state2, _ := applyTracked(t, p, env, cfg, &deploy.PlanRequest{
		PackJSON:     buildPackVersion(packID, "2.0.0"),
		DeployConfig: cfg,
		Environment:  env.Workspace,
		PriorState:   state1,
	})

	after := specOf(t, client, ResTypeAgentRuntime, agentName)

	// The pin must NOT have advanced — the controller promotes, not the deploy.
	if got := jsonOf(t, after["promptPackRef"]); got != jsonOf(t, pinnedBefore) {
		t.Errorf("promptPackRef = %s, want the live pin %s preserved across the version bump",
			got, jsonOf(t, pinnedBefore))
	}

	rollout, _ := after["rollout"].(map[string]any)
	if rollout == nil {
		t.Fatalf("agent %q lost spec.rollout entirely across the deploy", agentName)
	}
	if got := nestedString(rollout, "trigger", "promptPackChannel"); got != "stable" {
		t.Errorf("rollout.trigger.promptPackChannel = %q, want stable preserved", got)
	}
	if steps, _ := rollout["steps"].([]any); len(steps) != 2 {
		t.Errorf("rollout.steps = %v, want the 2 configured steps preserved", rollout["steps"])
	}
	if rollout["candidate"] == nil {
		t.Error("rollout.candidate was dropped — an in-flight canary would be cancelled")
	}

	// The new version must still have been published as its own pack object, so
	// the trigger controller has something to canary TO.
	if newPack := stateResourceName(t, state2, ResTypePromptPack); newPack == "" {
		t.Error("v2 deploy recorded no PromptPack object")
	}
}

// putAgentRollout reads an AgentRuntime back, sets spec.rollout, and writes the
// whole object again — simulating the rollout controller (or an operator)
// putting the agent into trigger mode out of band.
func putAgentRollout(t *testing.T, client omniaClient, agentName string, rollout map[string]any) {
	t.Helper()
	spec := specOf(t, client, ResTypeAgentRuntime, agentName)
	spec["rollout"] = rollout

	body, err := json.Marshal(map[string]any{
		"metadata": map[string]any{"name": agentName},
		"spec":     spec,
	})
	if err != nil {
		t.Fatalf("marshal agent update: %v", err)
	}
	if _, err := client.UpdateResource(
		context.Background(), ResTypeAgentRuntime, agentName, body,
	); err != nil {
		t.Fatalf("put agent %q into trigger mode: %v", agentName, err)
	}
}

// jsonOf renders a value as compact JSON for order-insensitive comparison in
// failure messages and equality checks.
func jsonOf(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(b)
}

// ----------------------------------------------------------------------------
// Full config surface
// ----------------------------------------------------------------------------

// TestIntegration_ConfigSurfaceReachesAgentRuntime is the round-trip guard for
// every optional block the adapter can express. On the deploy-intent path the
// SERVER owns the intent->CRD translation, so nothing here is covered by the
// adapter's own CRD contract tests: a block the server forgets to map would be
// silently dropped, and the deploy would still report success.
//
// The OIDC issuer is a real, reachable provider on purpose — the controller
// fetches {issuer}/.well-known/openid-configuration, so a fake issuer would fail
// reconcile for reasons that have nothing to do with the mapping under test.
func TestIntegration_ConfigSurfaceReachesAgentRuntime(t *testing.T) {
	env := itEnv(t)
	p := NewProvider()

	packID := uniquePackID(t)
	cfg := buildDeployConfig(env, deployConfigOpts{
		runtime: map[string]any{"replicas": 2, "cpu": "100m", "memory": "128Mi"},
		externalAuth: map[string]any{
			"allowManagementPlane": false,
			"apiKeys":              map[string]any{"defaultRole": "editor", "trustEndUserHeader": true},
			"oidc": map[string]any{
				"issuer":       "https://accounts.google.com",
				"audience":     "it-audience",
				"claimMapping": map[string]any{"subject": "sub", "endUser": "email"},
			},
			"edgeTrust": map[string]any{
				"headerMapping":     map[string]any{"subject": "x-sub", "email": "x-mail"},
				"claimsFromHeaders": map[string]any{"tenant": "x-tenant"},
			},
		},
		memory: map[string]any{
			"enabled": true,
			"retrieval": map[string]any{
				"strategy":     "keyword",
				"limit":        7,
				"accessFilter": map[string]any{"denyCEL": "identity.role == 'guest'"},
			},
		},
		evals: map[string]any{
			"enabled": true,
			"inline":  map[string]any{"groups": []string{"fast-running"}},
			"worker":  map[string]any{"groups": []string{"slow-running"}},
		},
	})

	state, _ := applyTracked(t, p, env, cfg, &deploy.PlanRequest{
		PackJSON:     buildPack(packID),
		DeployConfig: cfg,
		Environment:  env.Workspace,
	})

	agentName := stateResourceName(t, state, ResTypeAgentRuntime)
	spec := specOf(t, itClient(t, cfg), ResTypeAgentRuntime, agentName)

	assertRuntimeSizing(t, spec)
	assertExternalAuth(t, spec)
	assertMemory(t, spec)
	assertEvals(t, spec)
	assertFacadeManagementPlane(t, spec)
}

func assertRuntimeSizing(t *testing.T, spec map[string]any) {
	t.Helper()
	runtime, _ := spec["runtime"].(map[string]any)
	if runtime == nil {
		t.Fatal("spec.runtime missing — replicas and resource requests were dropped")
	}
	if got, _ := runtime["replicas"].(float64); got != 2 {
		t.Errorf("runtime.replicas = %v, want 2", runtime["replicas"])
	}
	if got := nestedString(runtime, "resources", "requests", "cpu"); got != "100m" {
		t.Errorf("runtime.resources.requests.cpu = %q, want 100m", got)
	}
	if got := nestedString(runtime, "resources", "requests", "memory"); got != "128Mi" {
		t.Errorf("runtime.resources.requests.memory = %q, want 128Mi", got)
	}
}

func assertExternalAuth(t *testing.T, spec map[string]any) {
	t.Helper()
	ea, _ := spec["externalAuth"].(map[string]any)
	if ea == nil {
		t.Fatal("spec.externalAuth missing — the whole auth block was dropped")
	}
	// The adapter's apiKeys block is the CRD's clientKeys.
	if got := nestedString(ea, "clientKeys", "defaultRole"); got != "editor" {
		t.Errorf("externalAuth.clientKeys.defaultRole = %q, want editor", got)
	}
	if got := nestedString(ea, "oidc", "issuer"); got != "https://accounts.google.com" {
		t.Errorf("externalAuth.oidc.issuer = %q", got)
	}
	if got := nestedString(ea, "oidc", "audience"); got != "it-audience" {
		t.Errorf("externalAuth.oidc.audience = %q, want it-audience", got)
	}
	if got := nestedString(ea, "oidc", "claimMapping", "endUser"); got != "email" {
		t.Errorf("externalAuth.oidc.claimMapping.endUser = %q, want email", got)
	}
	if got := nestedString(ea, "edgeTrust", "headerMapping", "email"); got != "x-mail" {
		t.Errorf("externalAuth.edgeTrust.headerMapping.email = %q, want x-mail", got)
	}
	if got := nestedString(ea, "edgeTrust", "claimsFromHeaders", "tenant"); got != "x-tenant" {
		t.Errorf("externalAuth.edgeTrust.claimsFromHeaders.tenant = %q, want x-tenant", got)
	}
}

func assertMemory(t *testing.T, spec map[string]any) {
	t.Helper()
	mem, _ := spec["memory"].(map[string]any)
	if mem == nil {
		t.Fatal("spec.memory missing — the memory block was dropped")
	}
	if enabled, _ := mem["enabled"].(bool); !enabled {
		t.Error("memory.enabled = false, want true")
	}
	if got := nestedString(mem, "retrieval", "strategy"); got != "keyword" {
		t.Errorf("memory.retrieval.strategy = %q, want keyword", got)
	}
	retrieval, _ := mem["retrieval"].(map[string]any)
	if got, _ := retrieval["limit"].(float64); got != 7 {
		t.Errorf("memory.retrieval.limit = %v, want 7", retrieval["limit"])
	}
	if got := nestedString(mem, "retrieval", "accessFilter", "denyCEL"); got != "identity.role == 'guest'" {
		t.Errorf("memory.retrieval.accessFilter.denyCEL = %q, want the configured CEL", got)
	}
}

func assertEvals(t *testing.T, spec map[string]any) {
	t.Helper()
	evals, _ := spec["evals"].(map[string]any)
	if evals == nil {
		t.Fatal("spec.evals missing — the evals block was dropped")
	}
	if enabled, _ := evals["enabled"].(bool); !enabled {
		t.Error("evals.enabled = false, want true")
	}
	for path, want := range map[string]string{"inline": "fast-running", "worker": "slow-running"} {
		block, _ := evals[path].(map[string]any)
		groups, _ := block["groups"].([]any)
		if len(groups) != 1 {
			t.Errorf("evals.%s.groups = %v, want exactly [%s]", path, block["groups"], want)
			continue
		}
		if got, _ := groups[0].(string); got != want {
			t.Errorf("evals.%s.groups[0] = %q, want %q", path, got, want)
		}
	}
}

func assertFacadeManagementPlane(t *testing.T, spec map[string]any) {
	t.Helper()
	facades, _ := spec["facades"].([]any)
	if len(facades) == 0 {
		t.Fatal("spec.facades is empty — the agent has no facade to reach it on")
	}
	facade, _ := facades[0].(map[string]any)
	mp, present := facade["managementPlane"]
	if !present {
		t.Fatal("facades[0].managementPlane missing — allowManagementPlane was not projected")
	}
	if enabled, _ := mp.(bool); enabled {
		t.Error("facades[0].managementPlane = true, want the configured false")
	}
}

// ----------------------------------------------------------------------------
// Fan-out and policy
// ----------------------------------------------------------------------------

// TestIntegration_MultiPromptFanOut deploys a plain pack with two prompts, which
// the adapter fans out into one AgentRuntime per prompt, each pinned to its own
// entry prompt. The pin travels as the intent's promptName and must arrive as an
// OMNIA_PROMPT_NAME env override — without it both agents would run the
// operator's default prompt and be indistinguishable.
func TestIntegration_MultiPromptFanOut(t *testing.T) {
	env := itEnv(t)
	p := NewProvider()

	packID := uniquePackID(t)
	cfg := buildDeployConfig(env, deployConfigOpts{})

	state, _ := applyTracked(t, p, env, cfg, &deploy.PlanRequest{
		PackJSON:     buildMultiPromptPack(packID, "triage", "billing"),
		DeployConfig: cfg,
		Environment:  env.Workspace,
	})

	client := itClient(t, cfg)
	for prompt, agent := range map[string]string{
		"triage":  sanitizeName(packID + "-triage"),
		"billing": sanitizeName(packID + "-billing"),
	} {
		spec := specOf(t, client, ResTypeAgentRuntime, agent)
		if got := promptNameEnv(spec); got != prompt {
			t.Errorf("agent %q OMNIA_PROMPT_NAME = %q, want %q", agent, got, prompt)
		}
	}

	_ = state
}

// promptNameEnv returns the OMNIA_PROMPT_NAME value from spec.runtime.extraEnv,
// or "" when absent. Duplicate entries are last-wins (K8s env semantics), which
// is how the adapter guarantees its override beats the operator's default.
func promptNameEnv(spec map[string]any) string {
	runtime, _ := spec["runtime"].(map[string]any)
	env, _ := runtime["extraEnv"].([]any)
	value := ""
	for _, e := range env {
		entry, _ := e.(map[string]any)
		if name, _ := entry["name"].(string); name == envOmniaPromptName {
			value, _ = entry["value"].(string)
		}
	}
	return value
}

// TestIntegration_AgentPolicyFromBlocklist deploys a pack whose prompt declares
// a tool blocklist and asserts the AgentPolicy actually materializes carrying
// the blocked tool. A dropped policy fails open — the agent would keep full
// access to a tool the pack author explicitly denied — so this must be asserted
// against the real object, not the plan.
func TestIntegration_AgentPolicyFromBlocklist(t *testing.T) {
	env := itEnv(t)
	p := NewProvider()

	packID := uniquePackID(t)
	cfg := buildDeployConfig(env, deployConfigOpts{createTools: true})

	state, _ := applyTracked(t, p, env, cfg, &deploy.PlanRequest{
		PackJSON:     buildPackWithBlocklist(packID, itToolListName),
		DeployConfig: cfg,
		Environment:  env.Workspace,
	})

	policyName := stateResourceName(t, state, ResTypeAgentPolicy)
	spec := specOf(t, itClient(t, cfg), ResTypeAgentPolicy, policyName)

	// Assert on the rendered spec rather than a fixed shape: the server builds
	// toolAccess{denylist, rules[]} and the exact nesting is its business — what
	// must not happen is the blocked tool going missing.
	rendered := jsonOf(t, spec)
	if !strings.Contains(rendered, itToolListName) {
		t.Errorf("agent policy %q does not deny %q; spec = %s",
			policyName, itToolListName, rendered)
	}
}

// TestIntegration_BindModeReusesRegistry covers the intent's OTHER tools branch:
// tools.ref, which points the agent at an existing registry instead of supplying
// handlers to create. It deploys a create-mode pack to produce a registry, then
// deploys a second pack that binds it by name, and asserts the second deploy
// referenced the registry WITHOUT creating one of its own — binding must never
// fork a duplicate registry, and must never mutate the one it borrows.
func TestIntegration_BindModeReusesRegistry(t *testing.T) {
	env := itEnv(t)
	p := NewProvider()

	// Deploy #1 produces the registry the second deploy will borrow.
	ownerID := uniquePackID(t)
	ownerCfg := buildDeployConfig(env, deployConfigOpts{createTools: true})
	applyTracked(t, p, env, ownerCfg, &deploy.PlanRequest{
		PackJSON:     buildPack(ownerID),
		DeployConfig: ownerCfg,
		Environment:  env.Workspace,
	})

	registryName := sanitizeName(ownerID + "-tools")
	client := itClient(t, ownerCfg)
	before := jsonOf(t, specOf(t, client, ResTypeToolRegistry, registryName))

	// Deploy #2 binds that registry by name.
	borrowerID := uniquePackID(t)
	borrowerCfg := buildDeployConfig(env, deployConfigOpts{bindRegistry: registryName})
	state, _ := applyTracked(t, p, env, borrowerCfg, &deploy.PlanRequest{
		PackJSON:     buildPack(borrowerID),
		DeployConfig: borrowerCfg,
		Environment:  env.Workspace,
	})

	agentName := stateResourceName(t, state, ResTypeAgentRuntime)
	agentSpec := specOf(t, client, ResTypeAgentRuntime, agentName)
	if got := nestedString(agentSpec, "toolRegistryRef", "name"); got != registryName {
		t.Errorf("borrower agent toolRegistryRef = %q, want the bound %q", got, registryName)
	}

	// Binding must not fork a registry of the borrower's own.
	forked := sanitizeName(borrowerID + "-tools")
	if _, err := client.GetResource(context.Background(), ResTypeToolRegistry, forked); err == nil {
		t.Errorf("bind mode created a second registry %q; it must reuse %q", forked, registryName)
	}

	// Nor mutate the registry it borrowed.
	if after := jsonOf(t, specOf(t, client, ResTypeToolRegistry, registryName)); after != before {
		t.Errorf("bind mode mutated the borrowed registry\nbefore = %s\nafter  = %s", before, after)
	}
}

// TestIntegration_TrackModeSurvivesDeploy covers the OTHER auto-update mode an
// AgentRuntime can be in. spec.promptPackRef.track ("stable" / "prerelease")
// makes the agent follow a release channel instead of pinning a version — the
// controller moves the stable pods to the newest version on that channel.
//
// track is mutually exclusive with rollout.trigger, so a track-mode agent has NO
// rollout block. That matters because the deploy-intent API's preservation is
// keyed exclusively on trigger mode:
//
//	triggerMode := live.Spec.Rollout != nil && live.Spec.Rollout.Trigger != nil
//
// A track-mode agent therefore takes the unguarded path (live.Spec =
// desired.Spec), and the adapter always sends a version-pinned promptPackRef.
// If that overwrites the track, a routine deploy silently converts a
// channel-following agent into a pinned one and its auto-update stops — with the
// deploy still reporting success.
func TestIntegration_TrackModeSurvivesDeploy(t *testing.T) {
	env := itEnv(t)
	p := NewProvider()

	packID := uniquePackID(t)
	cfg := buildDeployConfig(env, deployConfigOpts{})
	if !itServesIntentAPI(t, cfg) {
		t.Skip("workspace does not serve the deploy-intent API")
	}

	state1, _ := applyTracked(t, p, env, cfg, &deploy.PlanRequest{
		PackJSON:     buildPackVersion(packID, "1.0.0"),
		DeployConfig: cfg,
		Environment:  env.Workspace,
	})

	client := itClient(t, cfg)
	agentName := stateResourceName(t, state1, ResTypeAgentRuntime)

	// Put the agent on the stable channel, as a dashboard or GitOps config would.
	// version and track are mutually exclusive, so the pin must be dropped.
	spec := specOf(t, client, ResTypeAgentRuntime, agentName)
	ref, _ := spec["promptPackRef"].(map[string]any)
	delete(ref, "version")
	ref["track"] = "stable"
	spec["promptPackRef"] = ref
	putAgentSpec(t, client, agentName, spec)

	if got := nestedString(specOf(t, client, ResTypeAgentRuntime, agentName),
		"promptPackRef", "track"); got != "stable" {
		t.Fatalf("setup failed: agent track = %q, want stable before the deploy", got)
	}

	// A routine deploy of a new pack version.
	applyTracked(t, p, env, cfg, &deploy.PlanRequest{
		PackJSON:     buildPackVersion(packID, "2.0.0"),
		DeployConfig: cfg,
		Environment:  env.Workspace,
		PriorState:   state1,
	})

	after, _ := specOf(t, client, ResTypeAgentRuntime, agentName)["promptPackRef"].(map[string]any)
	if got, _ := after["track"].(string); got != "stable" {
		t.Errorf("promptPackRef.track = %q after the deploy, want stable preserved — "+
			"the agent was silently converted from channel-following to version-pinned "+
			"(full ref = %s)", got, jsonOf(t, after))
	}
	if v, pinned := after["version"]; pinned {
		t.Errorf("promptPackRef.version = %v — a pin was written onto a track-mode agent, "+
			"which the CRD documents as mutually exclusive with track", v)
	}
}

// putAgentSpec writes a whole AgentRuntime spec back, standing in for whatever
// out-of-band actor (dashboard, GitOps, controller) owns that field.
func putAgentSpec(t *testing.T, client omniaClient, agentName string, spec map[string]any) {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"metadata": map[string]any{"name": agentName},
		"spec":     spec,
	})
	if err != nil {
		t.Fatalf("marshal agent update: %v", err)
	}
	if _, err := client.UpdateResource(
		context.Background(), ResTypeAgentRuntime, agentName, body,
	); err != nil {
		t.Fatalf("update agent %q: %v", agentName, err)
	}
}

// TestIntegration_OutOfBandSpecSurvivesDeploy generalizes the track-mode finding.
//
// The deploy-intent translation populates only part of AgentRuntimeSpec
// (promptPackRef, providers, runtime, facades, externalAuth, memory, evals,
// rollout, toolRegistryRef). The apply then assigns the whole spec —
// live.Spec = desired.Spec — guarded ONLY for trigger-mode rollouts. Every other
// field an operator set out of band (dashboard, GitOps, another controller) is
// therefore zeroed by a routine deploy, silently, with the deploy reporting
// success.
//
// This asserts two concrete examples: extraPodAnnotations (free-form ops
// metadata) and runtime.autoscaling — the latter especially, because the adapter
// cannot express autoscaling in intent v1 at all, so a deploy can only ever
// destroy it, never restate it.
func TestIntegration_OutOfBandSpecSurvivesDeploy(t *testing.T) {
	env := itEnv(t)
	p := NewProvider()

	packID := uniquePackID(t)
	cfg := buildDeployConfig(env, deployConfigOpts{})
	if !itServesIntentAPI(t, cfg) {
		t.Skip("workspace does not serve the deploy-intent API")
	}

	state1, _ := applyTracked(t, p, env, cfg, &deploy.PlanRequest{
		PackJSON:     buildPackVersion(packID, "1.0.0"),
		DeployConfig: cfg,
		Environment:  env.Workspace,
	})

	client := itClient(t, cfg)
	agentName := stateResourceName(t, state1, ResTypeAgentRuntime)

	// Configure the agent out of band, as an operator would.
	spec := specOf(t, client, ResTypeAgentRuntime, agentName)
	spec["extraPodAnnotations"] = map[string]any{"ops.example.com/oncall": "platform"}
	runtime, _ := spec["runtime"].(map[string]any)
	if runtime == nil {
		runtime = map[string]any{}
	}
	runtime["autoscaling"] = map[string]any{
		"enabled": true, "minReplicas": 2, "maxReplicas": 5,
	}
	spec["runtime"] = runtime
	putAgentSpec(t, client, agentName, spec)

	// A routine deploy that says nothing about either field.
	applyTracked(t, p, env, cfg, &deploy.PlanRequest{
		PackJSON:     buildPackVersion(packID, "2.0.0"),
		DeployConfig: cfg,
		Environment:  env.Workspace,
		PriorState:   state1,
	})

	after := specOf(t, client, ResTypeAgentRuntime, agentName)

	if got := nestedString(after, "extraPodAnnotations", "ops.example.com/oncall"); got != "platform" {
		t.Errorf("extraPodAnnotations lost: oncall = %q, want platform (spec.extraPodAnnotations = %v)",
			got, after["extraPodAnnotations"])
	}

	afterRuntime, _ := after["runtime"].(map[string]any)
	autoscaling, _ := afterRuntime["autoscaling"].(map[string]any)
	if autoscaling == nil {
		t.Errorf("runtime.autoscaling was zeroed by the deploy — the adapter cannot express "+
			"autoscaling in intent v1, so a deploy can only destroy it (runtime = %s)",
			jsonOf(t, afterRuntime))
		return
	}
	if enabled, _ := autoscaling["enabled"].(bool); !enabled {
		t.Errorf("runtime.autoscaling.enabled = false after deploy, want the configured true")
	}
}

// outOfBandSpecFields are AgentRuntime spec blocks an operator can set that the
// deploy-intent translation never populates. Values are deliberately simple and
// free-form so the apiserver accepts them without extra CRDs existing; anything
// it rejects is dropped by the calibration step below rather than failing the
// test, so this stays honest about what it actually measured.
func outOfBandSpecFields() map[string]any {
	return map[string]any{
		"extraPodAnnotations": map[string]any{"ops.example.com/oncall": "platform"},
		"podOverrides": map[string]any{
			"labels":      map[string]any{"ops.example.com/tier": "gold"},
			"annotations": map[string]any{"ops.example.com/owner": "platform"},
		},
		"console":      map[string]any{"maxFiles": 3},
		"media":        map[string]any{"basePath": "/media"},
		"serviceGroup": "default",
		// inputSchema/outputSchema are deliberately absent: the CRD rejects them
		// unless spec.mode is "function", and switching mode would change the
		// agent's whole shape rather than measure field preservation.
	}
}

// TestIntegration_SpecClobberBlastRadius measures exactly which operator-set
// AgentRuntime fields a routine deploy destroys, rather than inferring it from
// the translation source.
//
// It is self-calibrating: it writes the fields out of band, re-reads to see
// which ones the apiserver actually kept, and only then deploys. Fields the
// cluster refused are excluded from the verdict, so a green run means "these
// fields genuinely survive" and a red run names only real losses.
func TestIntegration_SpecClobberBlastRadius(t *testing.T) {
	env := itEnv(t)
	p := NewProvider()

	packID := uniquePackID(t)
	cfg := buildDeployConfig(env, deployConfigOpts{})
	if !itServesIntentAPI(t, cfg) {
		t.Skip("workspace does not serve the deploy-intent API")
	}

	state1, _ := applyTracked(t, p, env, cfg, &deploy.PlanRequest{
		PackJSON:     buildPackVersion(packID, "1.0.0"),
		DeployConfig: cfg,
		Environment:  env.Workspace,
	})

	client := itClient(t, cfg)
	agentName := stateResourceName(t, state1, ResTypeAgentRuntime)

	// Write every candidate field out of band.
	spec := specOf(t, client, ResTypeAgentRuntime, agentName)
	for key, value := range outOfBandSpecFields() {
		spec[key] = value
	}
	putAgentSpec(t, client, agentName, spec)

	// Calibrate: keep only what the cluster actually stored.
	settled := specOf(t, client, ResTypeAgentRuntime, agentName)
	established := map[string]string{}
	for key := range outOfBandSpecFields() {
		if v, ok := settled[key]; ok && v != nil {
			established[key] = jsonOf(t, v)
		}
	}
	if len(established) == 0 {
		t.Fatal("no out-of-band field was accepted by the cluster; nothing to measure")
	}
	t.Logf("measuring %d operator-set fields: %v", len(established), sortedKeys(established))

	// A routine deploy that mentions none of them.
	applyTracked(t, p, env, cfg, &deploy.PlanRequest{
		PackJSON:     buildPackVersion(packID, "2.0.0"),
		DeployConfig: cfg,
		Environment:  env.Workspace,
		PriorState:   state1,
	})

	after := specOf(t, client, ResTypeAgentRuntime, agentName)
	var lost, mutated []string
	for key, before := range established {
		v, present := after[key]
		switch {
		case !present || v == nil:
			lost = append(lost, key)
		case jsonOf(t, v) != before:
			mutated = append(mutated, key)
		}
	}
	sortStrings(lost)
	sortStrings(mutated)

	if len(lost) > 0 {
		t.Errorf("deploy DESTROYED %d operator-set spec field(s): %v\n"+
			"  These were set out of band (dashboard/GitOps) and the deploy did not mention them.\n"+
			"  Cause: reconcileAgentRuntimeSpec assigns live.Spec = desired.Spec wholesale.",
			len(lost), lost)
	}
	if len(mutated) > 0 {
		t.Errorf("deploy MUTATED %d operator-set spec field(s): %v", len(mutated), mutated)
	}
}

// TestIntegration_NonTriggerRolloutSurvivesDeploy covers the rollout shapes that
// are NOT version-triggered: a manually-driven candidate plus stickySession and
// rollback tuning. The preservation in reconcileAgentRuntimeSpec keys on
// rollout.trigger, so these take the unguarded path.
//
// The source documents the manual-candidate case as an accepted Plan A
// limitation. stickySession and rollback are not mentioned — losing them
// mid-rollout changes traffic routing and removes the automatic-rollback safety
// net while a rollout is live.
func TestIntegration_NonTriggerRolloutSurvivesDeploy(t *testing.T) {
	env := itEnv(t)
	p := NewProvider()

	packID := uniquePackID(t)
	cfg := buildDeployConfig(env, deployConfigOpts{})
	if !itServesIntentAPI(t, cfg) {
		t.Skip("workspace does not serve the deploy-intent API")
	}

	state1, _ := applyTracked(t, p, env, cfg, &deploy.PlanRequest{
		PackJSON:     buildPackVersion(packID, "1.0.0"),
		DeployConfig: cfg,
		Environment:  env.Workspace,
	})

	client := itClient(t, cfg)
	agentName := stateResourceName(t, state1, ResTypeAgentRuntime)

	spec := specOf(t, client, ResTypeAgentRuntime, agentName)
	spec["rollout"] = map[string]any{
		// No trigger: a manually-driven rollout.
		"steps":         []any{map[string]any{"setWeight": 10}},
		"stickySession": map[string]any{"hashOn": "session"},
	}
	putAgentSpec(t, client, agentName, spec)

	settled, _ := specOf(t, client, ResTypeAgentRuntime, agentName)["rollout"].(map[string]any)
	if settled == nil || settled["stickySession"] == nil {
		t.Skip("cluster did not accept the non-trigger rollout shape; nothing to measure")
	}

	applyTracked(t, p, env, cfg, &deploy.PlanRequest{
		PackJSON:     buildPackVersion(packID, "2.0.0"),
		DeployConfig: cfg,
		Environment:  env.Workspace,
		PriorState:   state1,
	})

	after, _ := specOf(t, client, ResTypeAgentRuntime, agentName)["rollout"].(map[string]any)
	if after == nil {
		t.Fatal("the whole non-trigger rollout block was destroyed by the deploy")
	}
	if after["stickySession"] == nil {
		t.Error("rollout.stickySession destroyed — consistent hashing stops mid-rollout, " +
			"so users can be bounced between versions")
	}
}

// TestIntegration_AgentPolicyRulesSurviveDeploy checks the same clobber class on
// the AgentPolicy, which the server updates in place (unlike the create-only
// ToolRegistry). The adapter sends only a flat toolBlocklist, so any rule an
// operator added by hand rides on a spec the deploy rewrites.
//
// A lost policy rule fails OPEN — access the operator denied silently returns.
func TestIntegration_AgentPolicyRulesSurviveDeploy(t *testing.T) {
	env := itEnv(t)
	p := NewProvider()

	packID := uniquePackID(t)
	cfg := buildDeployConfig(env, deployConfigOpts{createTools: true})

	state1, _ := applyTracked(t, p, env, cfg, &deploy.PlanRequest{
		PackJSON:     buildPackWithBlocklist(packID, itToolListName),
		DeployConfig: cfg,
		Environment:  env.Workspace,
	})

	client := itClient(t, cfg)
	policyName := stateResourceName(t, state1, ResTypeAgentPolicy)

	// An operator tightens the policy out of band: permissive -> enforce, and
	// deny on evaluation failure. Both are real AgentPolicySpec fields the
	// adapter never sends, so they ride on a spec the deploy rewrites.
	spec := specOf(t, client, ResTypeAgentPolicy, policyName)
	spec["mode"] = "enforce"
	spec["onFailure"] = "deny"
	body, err := json.Marshal(map[string]any{
		"metadata": map[string]any{"name": policyName},
		"spec":     spec,
	})
	if err != nil {
		t.Fatalf("marshal policy: %v", err)
	}
	if _, uerr := client.UpdateResource(
		context.Background(), ResTypeAgentPolicy, policyName, body,
	); uerr != nil {
		t.Skipf("cluster rejected the out-of-band policy edit, nothing to measure: %v", uerr)
	}
	if got, _ := specOf(t, client, ResTypeAgentPolicy, policyName)["mode"].(string); got != "enforce" {
		t.Skipf("cluster did not persist the out-of-band policy mode (got %q); nothing to measure", got)
	}

	applyTracked(t, p, env, cfg, &deploy.PlanRequest{
		PackJSON:     buildPackWithBlocklist(packID, itToolListName),
		DeployConfig: cfg,
		Environment:  env.Workspace,
		PriorState:   state1,
	})

	settled := specOf(t, client, ResTypeAgentPolicy, policyName)
	if got, _ := settled["mode"].(string); got != "enforce" {
		t.Errorf("AgentPolicy mode = %q after deploy, want the operator-set enforce — "+
			"a policy downgraded to permissive stops blocking anything", got)
	}
	if got, _ := settled["onFailure"].(string); got != "deny" {
		t.Errorf("AgentPolicy onFailure = %q after deploy, want the operator-set deny — "+
			"the policy now fails OPEN when evaluation errors", got)
	}
}

// TestIntegration_SameVersionContentChange pins what happens when a pack's
// CONTENT changes but its version does not — the everyday inner-loop mistake.
//
// Pack objects are immutable and per-version, so the server answers AlreadyExists
// and reports the pack unchanged. The edited prompt never reaches the cluster.
// That is defensible versioning, but the deploy currently reports success with
// nothing in the output saying the new content was discarded, so the operator
// has no signal that their edit did not ship.
func TestIntegration_SameVersionContentChange(t *testing.T) {
	env := itEnv(t)
	p := NewProvider()

	packID := uniquePackID(t)
	cfg := buildDeployConfig(env, deployConfigOpts{})
	if !itServesIntentAPI(t, cfg) {
		t.Skip("workspace does not serve the deploy-intent API")
	}

	state1, _ := applyTracked(t, p, env, cfg, &deploy.PlanRequest{
		PackJSON:     buildPackVersion(packID, "1.0.0"),
		DeployConfig: cfg,
		Environment:  env.Workspace,
	})

	// Same version, different prompt content.
	edited := basePackDoc(packID, "1.0.0")
	prompts, _ := edited["prompts"].(map[string]any)
	main, _ := prompts["main"].(map[string]any)
	main["system_template"] = "You are a COMPLETELY DIFFERENT agent."

	_, events := applyTracked(t, p, env, cfg, &deploy.PlanRequest{
		PackJSON:     mustMarshal(edited),
		DeployConfig: cfg,
		Environment:  env.Workspace,
		PriorState:   state1,
	})

	// The pack must be reported unchanged — proving the edit did not ship.
	unchanged := false
	for _, e := range events {
		if e.Resource != nil && e.Resource.Type == ResTypePromptPack &&
			e.Resource.Status == ResStatusUnchanged {
			unchanged = true
		}
	}
	if !unchanged {
		t.Skip("pack was not reported unchanged; immutable-version semantics may have changed")
	}

	// ...and the operator must be told. Any message naming the version or saying
	// the content was not republished would do.
	var messages []string
	for _, e := range events {
		messages = append(messages, e.Message)
	}
	warned := countContaining(messages, "version") > 0 || countContaining(messages, "unchanged") > 0
	if !warned {
		t.Errorf("a same-version content change shipped nothing and said nothing — "+
			"the deploy reported success with no indication the edited pack was discarded.\n"+
			"  messages: %v", messages)
	}
}

// sortedKeys returns a map's keys in stable order.
func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sortStrings(out)
	return out
}

// sortStrings sorts in place (insertion sort keeps the helper dependency-free).
func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

// TestIntegration_MultiAgentPackFanOut covers the OTHER fan-out shape: a pack
// declaring agents{} members, one AgentRuntime per member. Unlike the
// multi-prompt fan-out these carry no OMNIA_PROMPT_NAME — each member resolves
// its own entry — so this asserts the agents exist under their member names and
// all share the one pack.
func TestIntegration_MultiAgentPackFanOut(t *testing.T) {
	env := itEnv(t)
	p := NewProvider()

	packID := uniquePackID(t)
	cfg := buildDeployConfig(env, deployConfigOpts{})

	state, _ := applyTracked(t, p, env, cfg, &deploy.PlanRequest{
		PackJSON:     buildMultiAgentPack(packID, "alice", "bob"),
		DeployConfig: cfg,
		Environment:  env.Workspace,
	})

	var s AdapterState
	if err := json.Unmarshal([]byte(state), &s); err != nil {
		t.Fatalf("parse state: %v", err)
	}
	agents := map[string]bool{}
	for _, r := range s.Resources {
		if r.Type == ResTypeAgentRuntime {
			agents[r.Name] = true
		}
	}
	if len(agents) != 2 {
		t.Fatalf("want one AgentRuntime per member, got %d: %v", len(agents), agents)
	}

	client := itClient(t, cfg)
	for _, member := range []string{"alice", "bob"} {
		if !agents[member] {
			t.Errorf("no AgentRuntime for member %q; got %v", member, agents)
			continue
		}
		spec := specOf(t, client, ResTypeAgentRuntime, member)
		if got := nestedString(spec, "promptPackRef", "name"); got != packID {
			t.Errorf("member %q promptPackRef.name = %q, want the shared pack %q",
				member, got, packID)
		}
		// Members resolve their own entry — a prompt pin here would override it.
		if got := promptNameEnv(spec); got != "" {
			t.Errorf("member %q carries OMNIA_PROMPT_NAME=%q; members must resolve their own entry",
				member, got)
		}
	}
}

// TestIntegration_DestroyRemovesIntentObjects verifies teardown on the
// deploy-intent path, whose object names come from the server rather than the
// adapter. A destroy that misses them leaves live agents serving traffic after
// the operator believes the deploy is gone.
//
// The content ConfigMap is deliberately not asserted: it has no ownerReference
// and no delete route (Omnia#1913), so the adapter cannot remove it. That
// omission is tracked upstream rather than papered over here.
func TestIntegration_DestroyRemovesIntentObjects(t *testing.T) {
	env := itEnv(t)
	p := NewProvider()

	packID := uniquePackID(t)
	cfg := buildDeployConfig(env, deployConfigOpts{createTools: true})

	state, _ := applyTracked(t, p, env, cfg, &deploy.PlanRequest{
		PackJSON:     buildPackWithBlocklist(packID, itToolListName),
		DeployConfig: cfg,
		Environment:  env.Workspace,
	})

	client := itClient(t, cfg)
	packName := stateResourceName(t, state, ResTypePromptPack)
	agentName := stateResourceName(t, state, ResTypeAgentRuntime)
	policyName := stateResourceName(t, state, ResTypeAgentPolicy)

	if err := p.Destroy(context.Background(), &deploy.DestroyRequest{
		DeployConfig: cfg,
		Environment:  env.Workspace,
		PriorState:   state,
	}, func(_ *deploy.DestroyEvent) error { return nil }); err != nil {
		t.Fatalf("Destroy: %v", err)
	}

	for _, target := range []struct{ resType, name string }{
		{ResTypeAgentRuntime, agentName},
		{ResTypeAgentPolicy, policyName},
		{ResTypePromptPack, packName},
	} {
		if _, err := client.GetResource(
			context.Background(), target.resType, target.name,
		); err == nil {
			t.Errorf("%s %q still exists after Destroy", target.resType, target.name)
		}
	}

	// The ToolRegistry is operator-owned: destroy must LEAVE it, since another
	// deploy may bind the same registry.
	registryName := sanitizeName(packID + "-tools")
	if _, err := client.GetResource(
		context.Background(), ResTypeToolRegistry, registryName,
	); err != nil {
		t.Errorf("ToolRegistry %q was deleted by Destroy; it is operator-owned and must survive: %v",
			registryName, err)
	} else {
		t.Cleanup(func() {
			_ = client.DeleteResource(context.Background(), ResTypeToolRegistry, registryName)
		})
	}
}

// ----------------------------------------------------------------------------
// Concurrency
// ----------------------------------------------------------------------------

// concurrentApplies runs n Applies in parallel and returns their errors in
// order. It deliberately does NOT use applyTracked: t.Fatalf is invalid from a
// non-test goroutine, and a concurrency test needs every outcome, not the first
// failure.
func concurrentApplies(n int, p *Provider, req *deploy.PlanRequest) []error {
	var wg sync.WaitGroup
	errs := make([]error, n)
	wg.Add(n)
	for i := range n {
		go func() {
			defer wg.Done()
			_, errs[i] = p.Apply(context.Background(), req,
				func(*deploy.ApplyEvent) error { return nil })
		}()
	}
	wg.Wait()
	return errs
}

// destroyByPack registers teardown for a pack deployed outside applyTracked,
// reconstructing the state from a throwaway sequential apply's return value.
func destroyByPack(t *testing.T, p *Provider, env itConfig, deployConfig, state string) {
	t.Helper()
	t.Cleanup(func() {
		if state == "" {
			return
		}
		if err := p.Destroy(context.Background(), &deploy.DestroyRequest{
			DeployConfig: deployConfig,
			Environment:  env.Workspace,
			PriorState:   state,
		}, func(_ *deploy.DestroyEvent) error { return nil }); err != nil {
			t.Logf("cleanup Destroy (non-fatal): %v", err)
		}
	})
}

// TestIntegration_ConcurrentDeploysSamePack races several deploys of the same
// pack, the way parallel CI jobs or a retried pipeline would.
//
// Every apply is idempotent in intent — same pack, same version, same config —
// so all of them should succeed. They contend on the single AgentRuntime upsert,
// which is Get->mutate->Update with no retry, so this is the same defect as the
// trigger-mode 409 but reachable without any rollout involved.
func TestIntegration_ConcurrentDeploysSamePack(t *testing.T) {
	env := itEnv(t)
	p := NewProvider()

	packID := uniquePackID(t)
	cfg := buildDeployConfig(env, deployConfigOpts{})

	// Establish the deploy first, so the race is purely on the update path.
	state, _ := applyTracked(t, p, env, cfg, &deploy.PlanRequest{
		PackJSON:     buildPackVersion(packID, "1.0.0"),
		DeployConfig: cfg,
		Environment:  env.Workspace,
	})

	// 10, not 2-3: each Apply is a full HTTP round trip through the dashboard
	// proxy, so a small racer count almost never lands inside the server's
	// Get->Update window. A high count is needed to make client-side
	// concurrency actually contend.
	const racers = 10
	errs := concurrentApplies(racers, p, &deploy.PlanRequest{
		PackJSON:     buildPackVersion(packID, "1.0.0"),
		DeployConfig: cfg,
		Environment:  env.Workspace,
		PriorState:   state,
	})

	var failed []string
	for i, err := range errs {
		if err != nil {
			failed = append(failed, fmt.Sprintf("apply %d: %v", i, err))
		}
	}
	if len(failed) > 0 {
		t.Errorf("%d of %d concurrent idempotent deploys failed:\n  %s\n\n"+
			"  Concurrent deploys of the same pack are ordinary in CI (parallel jobs, "+
			"pipeline retries). They contend on the AgentRuntime upsert, which does "+
			"Get->mutate->Update with no conflict retry, so one writer wins and the rest "+
			"error. No rollout is involved — this is the plain concurrency case.",
			len(failed), racers, strings.Join(failed, "\n  "))
	}
}

// TestIntegration_ConcurrentDeploysDifferentPacks races deploys of INDEPENDENT
// packs. These share no object, so nothing should contend: a failure here would
// mean the deploy path serializes badly or corrupts unrelated deploys, which is
// far more serious than the same-pack case.
func TestIntegration_ConcurrentDeploysDifferentPacks(t *testing.T) {
	env := itEnv(t)
	p := NewProvider()
	cfg := buildDeployConfig(env, deployConfigOpts{})

	const racers = 3
	packs := make([]string, racers)
	for i := range packs {
		packs[i] = fmt.Sprintf("%s-%d", uniquePackID(t), i)
	}

	var wg sync.WaitGroup
	errs := make([]error, racers)
	states := make([]string, racers)
	wg.Add(racers)
	for i := range racers {
		go func() {
			defer wg.Done()
			states[i], errs[i] = p.Apply(context.Background(), &deploy.PlanRequest{
				PackJSON:     buildPackVersion(packs[i], "1.0.0"),
				DeployConfig: cfg,
				Environment:  env.Workspace,
			}, func(*deploy.ApplyEvent) error { return nil })
		}()
	}
	wg.Wait()

	for i := range racers {
		destroyByPack(t, p, env, cfg, states[i])
	}
	for i, err := range errs {
		if err != nil {
			t.Errorf("independent pack %q failed while deployed concurrently with others: %v",
				packs[i], err)
		}
	}
}

// ----------------------------------------------------------------------------
// Authorization
// ----------------------------------------------------------------------------

// TestIntegration_ViewerCannotDeploy checks the deploy-intent endpoint's
// editor gate against a real workspace where the caller only has viewer.
//
// Two things must hold, and the second is the subtle one: the deploy must fail,
// AND the adapter must NOT treat the refusal as "this server has no
// deploy-intent API" and quietly retry down the per-resource path. A permission
// denial that triggered a fallback would route around the authorization gate.
//
// Set OMNIA_IT_VIEWER_WORKSPACE to a workspace where the token holds viewer.
func TestIntegration_ViewerCannotDeploy(t *testing.T) {
	env := itEnv(t)
	workspace := os.Getenv("OMNIA_IT_VIEWER_WORKSPACE")
	if workspace == "" {
		t.Skip("set OMNIA_IT_VIEWER_WORKSPACE to a workspace where the token has viewer only")
	}
	p := NewProvider()

	packID := uniquePackID(t)
	cfg := buildDeployConfig(env, deployConfigOpts{workspaceOverride: workspace})

	var events []*deploy.ApplyEvent
	_, err := p.Apply(context.Background(), &deploy.PlanRequest{
		PackJSON:     buildPack(packID),
		DeployConfig: cfg,
		Environment:  workspace,
	}, capturingCallback(&events))

	if err == nil {
		t.Fatal("a viewer-only token deployed successfully; the editor gate is not enforced")
	}
	if countContaining(progressMessages(events), "does not serve the deploy-intent API") > 0 {
		t.Error("the adapter treated a permission denial as an absent deploy-intent API and " +
			"fell back to the per-resource path — a fallback must never route around authz")
	}
	t.Logf("viewer deploy correctly refused: %v", err)
}

// TestIntegration_NoAccessWorkspaceIsRefused covers the access-denied path that a
// dashboard in anonymous auth mode makes untestable via a bogus token: a
// workspace the calling identity holds NO role in at all.
//
// This is distinct from the viewer case — there the identity has a role, just an
// insufficient one. Here it has none, which is the shape a genuinely invalid or
// foreign token produces. Plan must fail cleanly rather than mislabelling the
// refusal as a missing resource.
//
// Set OMNIA_IT_NOACCESS_WORKSPACE to a workspace the token cannot read.
func TestIntegration_NoAccessWorkspaceIsRefused(t *testing.T) {
	env := itEnv(t)
	workspace := os.Getenv("OMNIA_IT_NOACCESS_WORKSPACE")
	if workspace == "" {
		t.Skip("set OMNIA_IT_NOACCESS_WORKSPACE to a workspace the token has no role in")
	}
	p := NewProvider()

	packID := uniquePackID(t)
	cfg := buildDeployConfig(env, deployConfigOpts{workspaceOverride: workspace})

	_, err := p.Plan(context.Background(), &deploy.PlanRequest{
		PackJSON:     buildPack(packID),
		DeployConfig: cfg,
		Environment:  workspace,
	})
	if err == nil {
		t.Fatal("Plan succeeded against a workspace the token has no access to")
	}

	// An access refusal must not be dressed up as a missing resource — that sends
	// the operator hunting for a provider that exists and is readable.
	if strings.Contains(strings.ToLower(err.Error()), "not found in workspace") {
		t.Errorf("access denial mislabelled as a missing resource: %v", err)
	}
	t.Logf("no-access workspace correctly refused: %v", err)
}

// TestIntegration_MultiRoleProvidersReachAgent binds two providers in different
// roles — the default llm plus an embedding provider — and asserts both arrive
// as distinct NamedProviderRef entries with their roles intact.
//
// Roles matter at runtime: the agent resolves its LLM by role, and a binding
// whose role was dropped or defaulted to llm would give the agent two LLM
// providers and no embedder. The deploy profile only advertises llm-role
// providers, so a non-llm binding is exactly the case least likely to be
// exercised by hand.
//
// Set OMNIA_IT_EMBED_PROVIDER to a non-llm Provider in the workspace.
func TestIntegration_MultiRoleProvidersReachAgent(t *testing.T) {
	env := itEnv(t)
	embed := os.Getenv("OMNIA_IT_EMBED_PROVIDER")
	if embed == "" {
		t.Skip("set OMNIA_IT_EMBED_PROVIDER to an embedding-role Provider in the workspace")
	}
	p := NewProvider()

	packID := uniquePackID(t)
	cfg := buildDeployConfig(env, deployConfigOpts{
		extraProviders: []map[string]any{
			{"name": "embedder", "ref": embed, "role": "embedding"},
		},
	})

	state, _ := applyTracked(t, p, env, cfg, &deploy.PlanRequest{
		PackJSON:     buildPack(packID),
		DeployConfig: cfg,
		Environment:  env.Workspace,
	})

	agentName := stateResourceName(t, state, ResTypeAgentRuntime)
	spec := specOf(t, itClient(t, cfg), ResTypeAgentRuntime, agentName)

	providers, _ := spec["providers"].([]any)
	byName := map[string]map[string]any{}
	for _, entry := range providers {
		e, _ := entry.(map[string]any)
		if name, _ := e["name"].(string); name != "" {
			byName[name] = e
		}
	}
	if len(byName) != 2 {
		t.Fatalf("spec.providers = %s, want both the llm and embedding bindings",
			jsonOf(t, spec["providers"]))
	}

	llm, ok := byName[itProviderDefault]
	if !ok {
		t.Fatalf("no %q provider binding; got %v", itProviderDefault, byName)
	}
	if got, _ := llm["role"].(string); got != itRoleLLM {
		t.Errorf("default binding role = %q, want %q", got, itRoleLLM)
	}

	emb, ok := byName["embedder"]
	if !ok {
		t.Fatalf("the embedding binding was dropped; got %v", byName)
	}
	if got, _ := emb["role"].(string); got != "embedding" {
		t.Errorf("embedder role = %q, want embedding — a role silently coerced to llm "+
			"gives the agent two LLMs and no embedder", got)
	}
	if got := nestedString(emb, "providerRef", "name"); got != embed {
		t.Errorf("embedder providerRef.name = %q, want %q", got, embed)
	}
}
