//go:build integration

package omnia

import (
	"context"
	"encoding/json"
	"os"
	"strings"
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
			{"source": source, "mountAs": "it-mounted"},
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
