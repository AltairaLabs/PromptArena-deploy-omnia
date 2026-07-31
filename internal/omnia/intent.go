package omnia

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/AltairaLabs/PromptKit/runtime/prompt"
)

// intentAPIVersionV1 is the deploy-intent contract version this adapter emits.
// The server rejects an unknown apiVersion with 400 so the adapter can fall back
// to the CRD passthrough path (see isIntentUnsupported).
const intentAPIVersionV1 = "deploy.omnia.altairalabs.ai/v1"

// deployIntent is the versioned, CRD-agnostic description of a whole deploy that
// the adapter POSTs to the Omnia deploy-intent API. The SERVER owns CRD
// construction from here on — this type deliberately models intent, not CRDs, so
// a CRD schema change is no longer an adapter-breaking change (Omnia#1863).
//
// It mirrors the server's DeployIntent (omnia internal/api/deploy/types.go).
// Only the fields the adapter can populate from its config surface are modeled.
//
// The rollout block draws a deliberate line: the adapter sends rollout POLICY
// (which channel to watch, the canary steps) because that is operator intent,
// and never sends rollout STATE (the in-flight candidate, its weight, sticky
// sessions, rollback) because that belongs to the rollout controller. The server
// classifies the fields the same way, and preserves the state precisely because
// a deploy omits it.
type deployIntent struct {
	APIVersion  string             `json:"apiVersion"`
	Pack        packIntent         `json:"pack"`
	Tools       *toolsIntent       `json:"tools,omitempty"`
	Policy      *policyIntent      `json:"policy,omitempty"`
	Agents      []agentIntent      `json:"agents"`
	Labels      map[string]string  `json:"labels,omitempty"`
	Credentials *credentialsIntent `json:"credentials,omitempty"`
}

// credentialsIntent carries the tool-credential VALUES this deploy environment
// resolved, so the server can write the Secret the synthesized handlers
// reference and OWN it (Omnia#2008).
//
// This is the one thing on the deploy path that cannot be expressed as a name.
// The values come from environment variables in the deploy environment — a pack
// declares headers_from_env: [Authorization=GITHUB_TOKEN], and GITHUB_TOKEN
// lives in the CI job — which the server cannot read. Before the contract
// carried them the adapter wrote the Secret itself with an out-of-band POST
// /api/secrets, leaving it unowned and unreaped: one Secret of live tokens
// leaked per deploy.
//
// Contrast externalAuth.clientKeys, where the Secret is genuinely
// operator-owned and pre-existing — a name reference is right there, and the
// server deliberately leaves a reference whose key this does not carry alone.
type credentialsIntent struct {
	// Data maps a Secret key to its raw value, keyed by the env var name the
	// pack references. The server preserves the keys verbatim.
	Data map[string]string `json:"data"`
}

// packIntent describes the PromptPack + its content ConfigMap. Content is the
// raw pack JSON, which the server treats as opaque bytes.
type packIntent struct {
	Name         string              `json:"name"`
	Version      string              `json:"version"`
	Content      string              `json:"content"`
	Skills       []skillRefIntent    `json:"skills,omitempty"`
	SkillsConfig *skillsConfigIntent `json:"skillsConfig,omitempty"`
}

// skillRefIntent is one PromptPack skill binding.
type skillRefIntent struct {
	Source  string   `json:"source"`
	Include []string `json:"include,omitempty"`
	MountAs string   `json:"mountAs,omitempty"`
}

// skillsConfigIntent caps and selects active skills.
type skillsConfigIntent struct {
	MaxActive *int32 `json:"maxActive,omitempty"`
	Selector  string `json:"selector,omitempty"`
}

// agentIntent describes one AgentRuntime. Agents arrive already fanned-out and
// name-sanitized — the server does not parse the pack.
type agentIntent struct {
	Name         string              `json:"name"`
	PromptName   string              `json:"promptName,omitempty"`
	Providers    []providerBind      `json:"providers"`
	Runtime      *runtimeIntent      `json:"runtime,omitempty"`
	Facades      []facadeIntent      `json:"facades,omitempty"`
	UseTools     bool                `json:"useTools,omitempty"`
	ExternalAuth *externalAuthIntent `json:"externalAuth,omitempty"`
	Memory       *memoryIntent       `json:"memory,omitempty"`
	Evals        *evalsIntent        `json:"evals,omitempty"`
	Rollout      *rolloutIntent      `json:"rollout,omitempty"`
}

// rolloutIntent carries the rollout POLICY — which channel to watch and the
// canary steps. It deliberately has no candidate/stickySession/rollback: that is
// live state owned by the rollout controller, and the server preserves it
// precisely because a deploy does not send it. Omitting the block entirely
// leaves an existing rollout, in flight or not, untouched.
type rolloutIntent struct {
	Trigger *rolloutTriggerIntent `json:"trigger,omitempty"`
	Steps   []rolloutStepIntent   `json:"steps,omitempty"`
}

// rolloutTriggerIntent opts the agent into version-triggered canaries.
type rolloutTriggerIntent struct {
	PromptPackChannel string `json:"promptPackChannel"`
}

// rolloutStepIntent is one canary step: shift traffic, or pause.
type rolloutStepIntent struct {
	SetWeight     *int32 `json:"setWeight,omitempty"`
	PauseDuration string `json:"pauseDuration,omitempty"`
}

// providerBind is one provider binding: logical slot, Provider CRD name, role.
type providerBind struct {
	Name string `json:"name"`
	Ref  string `json:"ref"`
	Role string `json:"role,omitempty"`
}

// runtimeIntent carries replica count, resource requests and autoscaling.
type runtimeIntent struct {
	Replicas    *int32             `json:"replicas,omitempty"`
	CPU         string             `json:"cpu,omitempty"`
	Memory      string             `json:"memory,omitempty"`
	Autoscaling *autoscalingIntent `json:"autoscaling,omitempty"`
}

// autoscalingIntent maps to spec.runtime.autoscaling. The adapter's config
// exposes the HPA knobs; KEDA scalers are configured out of band, so the
// contract's keda block is deliberately not surfaced here.
type autoscalingIntent struct {
	Enabled                           bool   `json:"enabled,omitempty"`
	Type                              string `json:"type,omitempty"` // hpa|keda
	MinReplicas                       *int32 `json:"minReplicas,omitempty"`
	MaxReplicas                       *int32 `json:"maxReplicas,omitempty"`
	TargetCPUUtilizationPercentage    *int32 `json:"targetCPUUtilizationPercentage,omitempty"`
	TargetMemoryUtilizationPercentage *int32 `json:"targetMemoryUtilizationPercentage,omitempty"`
	ScaleDownStabilizationSeconds     *int32 `json:"scaleDownStabilizationSeconds,omitempty"`
}

// facadeIntent is one agent facade (the websocket runtime facade).
type facadeIntent struct {
	Type            string `json:"type"`
	ManagementPlane *bool  `json:"managementPlane,omitempty"`
}

// externalAuthIntent expresses agent auth abstractly, so a CRD-side rename is
// absorbed server-side. The adapter's apiKeys block maps onto clientKeys.
type externalAuthIntent struct {
	ClientKeys *clientKeysIntent `json:"clientKeys,omitempty"`
	OIDC       *oidcIntent       `json:"oidc,omitempty"`
	EdgeTrust  *edgeTrustIntent  `json:"edgeTrust,omitempty"`
}

// clientKeysIntent toggles per-caller key validation.
type clientKeysIntent struct {
	DefaultRole        string `json:"defaultRole,omitempty"`
	TrustEndUserHeader bool   `json:"trustEndUserHeader,omitempty"`
}

// oidcIntent validates customer-issued JWTs.
type oidcIntent struct {
	Issuer       string             `json:"issuer"`
	Audience     string             `json:"audience"`
	ClaimMapping *oidcMappingIntent `json:"claimMapping,omitempty"`
}

// oidcMappingIntent overrides the JWT claim names read by the validator.
type oidcMappingIntent struct {
	Subject string `json:"subject,omitempty"`
	EndUser string `json:"endUser,omitempty"`
}

// edgeTrustIntent trusts claim headers injected by an upstream edge.
type edgeTrustIntent struct {
	HeaderMapping     *edgeTrustHeaderIntent `json:"headerMapping,omitempty"`
	ClaimsFromHeaders map[string]string      `json:"claimsFromHeaders,omitempty"`
}

// edgeTrustHeaderIntent overrides the inbound header names.
type edgeTrustHeaderIntent struct {
	Subject string `json:"subject,omitempty"`
	EndUser string `json:"endUser,omitempty"`
	Email   string `json:"email,omitempty"`
}

// memoryIntent toggles and tunes cross-session memory.
type memoryIntent struct {
	Enabled   bool                   `json:"enabled,omitempty"`
	Retrieval *memoryRetrievalIntent `json:"retrieval,omitempty"`
}

// memoryRetrievalIntent tunes recall. The adapter's nested
// retrieval.accessFilter.denyCEL flattens onto retrieval.denyCEL here.
type memoryRetrievalIntent struct {
	Strategy string `json:"strategy,omitempty"`
	Limit    *int32 `json:"limit,omitempty"`
	DenyCEL  string `json:"denyCEL,omitempty"`
}

// evalsIntent toggles evals and routes eval groups. The adapter's nested
// inline.groups / worker.groups flatten onto inlineGroups / workerGroups here.
type evalsIntent struct {
	Enabled bool     `json:"enabled,omitempty"`
	Inline  []string `json:"inlineGroups,omitempty"`
	Worker  []string `json:"workerGroups,omitempty"`
}

// toolsIntent either references an existing registry (Ref) or supplies the
// contents of one to create (Handlers). The two are mutually exclusive.
type toolsIntent struct {
	Ref      string          `json:"ref,omitempty"`
	Handlers []handlerIntent `json:"handlers,omitempty"`
}

// handlerIntent mirrors the stable surface of a ToolRegistry handler. The
// per-executor config blocks stay raw JSON so the intent tracks CRD growth
// without the adapter re-typing every executor.
type handlerIntent struct {
	Name          string           `json:"name"`
	Type          string           `json:"type"`
	Tool          *json.RawMessage `json:"tool,omitempty"`
	HTTPConfig    *json.RawMessage `json:"httpConfig,omitempty"`
	OpenAPIConfig *json.RawMessage `json:"openAPIConfig,omitempty"`
	GRPCConfig    *json.RawMessage `json:"grpcConfig,omitempty"`
	MCPConfig     *json.RawMessage `json:"mcpConfig,omitempty"`
	ClientConfig  *json.RawMessage `json:"clientConfig,omitempty"`
	Auth          *json.RawMessage `json:"auth,omitempty"`
	Timeout       string           `json:"timeout,omitempty"`
}

// policyIntent is the flat list of tool names to deny. The server builds the
// AgentPolicy denylist rule against the deploy's registry.
type policyIntent struct {
	ToolBlocklist []string `json:"toolBlocklist,omitempty"`
}

// buildDeployIntent translates the parsed pack, deploy config, and the resolved
// tool binding into a single DeployIntent body. It is only ever called after
// preflightIntentV1 reports no blockers, so every field it reads is known to be
// expressible in intent v1.
func buildDeployIntent(pack *prompt.Pack, cfg *Config, binding ToolBinding) (json.RawMessage, error) {
	packName := sanitizeName(pack.ID)

	intent := deployIntent{
		APIVersion: intentAPIVersionV1,
		Pack: packIntent{
			Name:         packName,
			Version:      pack.Version,
			Content:      cfg.PackJSON,
			Skills:       intentSkills(cfg.Skills),
			SkillsConfig: intentSkillsConfig(cfg.SkillsConfig),
		},
		Tools:  intentTools(pack, cfg, binding),
		Policy: intentPolicy(pack, binding),
		Agents: intentAgents(pack, cfg, binding),
		Labels: intentLabels(pack, cfg),
	}
	intent.Credentials = intentCredentials(pack, cfg, intent.Tools)
	return json.Marshal(intent)
}

// intentCredentials resolves the pack's tool-credential env vars into the
// values the server writes to its Secret.
//
// Only ever set alongside synthesized handlers. In bind mode the registry is
// operator-owned and the server never rewrites its Secret references, so
// credentials there would write a Secret nothing points at — the server rejects
// that combination outright rather than silently no-op'ing, so the adapter must
// not send it.
//
// An unset env var contributes no key: writing an empty value would produce a
// Secret key that resolves to nothing, which is indistinguishable at call time
// from a header that was never configured. headerEnvWarnings already tells the
// operator which ones are missing.
func intentCredentials(pack *prompt.Pack, cfg *Config, tools *toolsIntent) *credentialsIntent {
	if tools == nil || len(tools.Handlers) == 0 {
		return nil
	}
	_, envVars := collectToolCredentials(pack, cfg)
	data := make(map[string]string, len(envVars))
	for _, env := range envVars {
		if v := os.Getenv(env); v != "" {
			data[env] = v
		}
	}
	if len(data) == 0 {
		return nil
	}
	return &credentialsIntent{Data: data}
}

// intentLabels builds the deploy-wide label map the server overlays on every
// object it creates. It reuses the adapter's managed labels so adoption, status,
// and destroy keep finding intent-created resources — minus the per-resource
// resource-type label, which a single deploy-wide map cannot express (the
// adapter writes that label but never reads it).
func intentLabels(pack *prompt.Pack, cfg *Config) map[string]string {
	labels := buildResourceLabels(pack.ID, pack.Version, "", cfg.Labels)
	delete(labels, LabelResType)
	return labels
}

// intentSkills maps the deploy-config skill bindings to the intent form.
func intentSkills(skills []SkillBinding) []skillRefIntent {
	if len(skills) == 0 {
		return nil
	}
	// SkillBinding and skillRefIntent are field-identical: the deploy-config skill
	// shape already matches what the intent carries.
	out := make([]skillRefIntent, 0, len(skills))
	for _, b := range skills {
		out = append(out, skillRefIntent(b))
	}
	return out
}

// intentSkillsConfig maps skillsConfig, returning nil when it has no content.
func intentSkillsConfig(sc *SkillsConfig) *skillsConfigIntent {
	if sc == nil || (sc.Selector == "" && sc.MaxActive == nil) {
		return nil
	}
	out := &skillsConfigIntent{Selector: sc.Selector}
	if sc.MaxActive != nil {
		v := int32(*sc.MaxActive) //nolint:gosec // maxActive is a small operator-set cap
		out.MaxActive = &v
	}
	return out
}

// intentTools maps the resolver's binding decision onto the intent's tools
// block: create mode supplies the handlers to materialize, bind/discovered modes
// reference an existing registry by name, and none mode sends nothing.
func intentTools(pack *prompt.Pack, cfg *Config, binding ToolBinding) *toolsIntent {
	switch binding.Mode {
	case toolModeCreate:
		// true: the deploy-intent API postdates headersFromSecret, so the value
		// never has to enter the CRD.
		handlers, _ := buildCreateRegistryHandlers(pack, cfg, true)
		if len(handlers) == 0 {
			return nil
		}
		return &toolsIntent{Handlers: intentHandlers(handlers)}
	case toolModeBind:
		if binding.RegistryName == "" {
			return nil
		}
		return &toolsIntent{Ref: binding.RegistryName}
	default:
		return nil
	}
}

// intentHandlers converts the shared registry-handler builder's CRD-shaped maps
// into handlerIntent entries. The per-executor config blocks are re-marshaled
// verbatim as raw JSON — the adapter does not model their inner fields.
func intentHandlers(handlers []map[string]interface{}) []handlerIntent {
	out := make([]handlerIntent, 0, len(handlers))
	for _, h := range handlers {
		entry := handlerIntent{
			Name:    stringField(h, keyName),
			Type:    stringField(h, keyType),
			Timeout: stringField(h, keyTimeout),
		}
		entry.Tool = rawField(h, "tool")
		entry.HTTPConfig = rawField(h, keyHTTPConfig)
		entry.OpenAPIConfig = rawField(h, "openAPIConfig")
		entry.GRPCConfig = rawField(h, "grpcConfig")
		entry.MCPConfig = rawField(h, "mcpConfig")
		entry.ClientConfig = rawField(h, "clientConfig")
		entry.Auth = rawField(h, "auth")
		out = append(out, entry)
	}
	return out
}

// stringField reads a string value from a builder map, defaulting to "".
func stringField(m map[string]interface{}, key string) string {
	s, _ := m[key].(string)
	return s
}

// rawField re-marshals a builder map's sub-block as raw JSON, returning nil when
// the block is absent or cannot be marshaled (the server then applies its
// default for that block rather than receiving a malformed one).
func rawField(m map[string]interface{}, key string) *json.RawMessage {
	v, ok := m[key]
	if !ok || v == nil {
		return nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	raw := json.RawMessage(b)
	return &raw
}

// intentPolicy maps the pack's tool blocklist. The server requires a registry to
// build the denylist rule against, so a blocklist with no bound registry emits
// nothing — preflightIntentV1 has already diverted that case to the passthrough
// path, where the AgentPolicy is created standalone.
func intentPolicy(pack *prompt.Pack, binding ToolBinding) *policyIntent {
	blocklist := collectToolBlocklist(pack)
	if len(blocklist) == 0 || binding.RegistryName == "" {
		return nil
	}
	return &policyIntent{ToolBlocklist: blocklist}
}

// intentAgents fans the pack out into one agentIntent per AgentRuntime, using
// the SAME fan-out the passthrough path uses so both paths deploy the same set
// of agents under the same names.
func intentAgents(pack *prompt.Pack, cfg *Config, binding ToolBinding) []agentIntent {
	targets := agentRuntimeNames(pack)
	agents := make([]agentIntent, 0, len(targets))
	for _, tgt := range targets {
		agents = append(agents, agentIntent{
			Name:         sanitizeName(tgt.name),
			PromptName:   tgt.entry,
			Providers:    intentProviders(cfg.Providers),
			Runtime:      intentRuntime(cfg.Runtime),
			Facades:      intentFacades(cfg.ExternalAuth),
			UseTools:     binding.RegistryName != "",
			ExternalAuth: intentExternalAuth(cfg.ExternalAuth),
			Memory:       intentMemory(cfg.Memory),
			Evals:        intentEvals(cfg.Evals),
			Rollout:      intentRollout(cfg.Rollout),
		})
	}
	return agents
}

// intentProviders maps the provider bindings, defaulting an unset role to llm.
func intentProviders(providers Providers) []providerBind {
	out := make([]providerBind, 0, len(providers))
	for _, b := range providers {
		role := b.Role
		if role == "" {
			role = roleLLM
		}
		out = append(out, providerBind{Name: b.Name, Ref: b.Ref, Role: role})
	}
	return out
}

// intentRuntime maps replicas and resource requests, returning nil when nothing
// is configured so the platform default applies.
func intentRuntime(rc *RuntimeConfig) *runtimeIntent {
	if rc == nil {
		return nil
	}
	out := &runtimeIntent{
		CPU:         rc.CPU,
		Memory:      rc.Memory,
		Autoscaling: intentAutoscaling(rc.Autoscaling),
	}
	if rc.Replicas > 0 {
		v := int32(rc.Replicas) //nolint:gosec // replicas is a small operator-set count
		out.Replicas = &v
	}
	if out.Replicas == nil && out.CPU == "" && out.Memory == "" && out.Autoscaling == nil {
		return nil
	}
	return out
}

// intentAutoscaling maps the autoscaling block, added to the contract by
// omnia#1916. Before that it could not be expressed, so the adapter diverted
// such deploys to the per-resource path — which by then could not create a
// PromptPack at all, making the divert a guaranteed failure.
func intentAutoscaling(a *AutoscalingConfig) *autoscalingIntent {
	if a == nil {
		return nil
	}
	return &autoscalingIntent{
		Enabled:                           a.Enabled,
		Type:                              a.Type,
		MinReplicas:                       optInt32(a.MinReplicas),
		MaxReplicas:                       optInt32(a.MaxReplicas),
		TargetCPUUtilizationPercentage:    optInt32(a.TargetCPUUtilization),
		TargetMemoryUtilizationPercentage: optInt32(a.TargetMemoryUtilization),
		ScaleDownStabilizationSeconds:     optInt32(a.ScaleDownStabilizationSeconds),
	}
}

// optInt32 narrows an optional int config value to the int32 the contract uses.
// All of these are small operator-set counts, percentages or second counts.
func optInt32(v *int) *int32 {
	if v == nil {
		return nil
	}
	n := int32(*v) //nolint:gosec // replica counts, percentages and second counts
	return &n
}

// intentFacades emits the single websocket runtime facade, projecting the
// adapter's agent-global allowManagementPlane onto the per-facade gate.
func intentFacades(ea *ExternalAuthConfig) []facadeIntent {
	facade := facadeIntent{Type: facadeTypeWebSocket}
	if ea != nil {
		facade.ManagementPlane = ea.AllowManagementPlane
	}
	return []facadeIntent{facade}
}

// intentExternalAuth maps the auth validators the intent can express. The
// adapter's apiKeys block is the same concept as the intent's clientKeys.
//
// sharedToken was removed by Omnia (#1775) with client keys named as its
// replacement, so a config still using it is migrated here rather than dropped.
// The migration is partial by nature and removedFieldWarnings says so: only
// trustEndUserHeader has a counterpart, because client keys are created in the
// dashboard and stored hashed rather than declared via a secretRef.
func intentExternalAuth(ea *ExternalAuthConfig) *externalAuthIntent {
	if ea == nil {
		return nil
	}
	out := &externalAuthIntent{
		OIDC:       intentOIDC(ea.OIDC),
		EdgeTrust:  intentEdgeTrust(ea.EdgeTrust),
		ClientKeys: intentClientKeys(ea),
	}
	if out.ClientKeys == nil && out.OIDC == nil && out.EdgeTrust == nil {
		return nil
	}
	return out
}

// intentClientKeys builds the client-keys block from an explicit apiKeys block,
// falling back to migrating a removed sharedToken. An explicit apiKeys always
// wins: it is the current vocabulary, so it must not be overridden by a
// migration of the legacy one.
func intentClientKeys(ea *ExternalAuthConfig) *clientKeysIntent {
	if ea.APIKeys != nil {
		return &clientKeysIntent{
			DefaultRole:        ea.APIKeys.DefaultRole,
			TrustEndUserHeader: ea.APIKeys.TrustEndUserHeader,
		}
	}
	if ea.SharedToken != nil {
		// secretRef has no counterpart — see sharedTokenWarning.
		return &clientKeysIntent{TrustEndUserHeader: ea.SharedToken.TrustEndUserHeader}
	}
	return nil
}

// intentOIDC maps the OIDC validator and its optional claim mapping.
func intentOIDC(o *OIDCAuthConfig) *oidcIntent {
	if o == nil {
		return nil
	}
	out := &oidcIntent{Issuer: o.Issuer, Audience: o.Audience}
	if cm := o.ClaimMapping; cm != nil && (cm.Subject != "" || cm.EndUser != "") {
		out.ClaimMapping = &oidcMappingIntent{Subject: cm.Subject, EndUser: cm.EndUser}
	}
	return out
}

// intentEdgeTrust maps the edge-trust validator, returning nil when empty.
func intentEdgeTrust(et *EdgeTrustAuthConfig) *edgeTrustIntent {
	if et == nil {
		return nil
	}
	out := &edgeTrustIntent{ClaimsFromHeaders: et.ClaimsFromHeaders}
	if hm := et.HeaderMapping; hm != nil && (hm.Subject != "" || hm.EndUser != "" || hm.Email != "") {
		out.HeaderMapping = &edgeTrustHeaderIntent{
			Subject: hm.Subject, EndUser: hm.EndUser, Email: hm.Email,
		}
	}
	if out.HeaderMapping == nil && len(out.ClaimsFromHeaders) == 0 {
		return nil
	}
	return out
}

// intentMemory maps the memory block, flattening the adapter's nested
// retrieval.accessFilter.denyCEL onto the intent's retrieval.denyCEL.
func intentMemory(m *MemoryConfig) *memoryIntent {
	if m == nil {
		return nil
	}
	out := &memoryIntent{Enabled: m.Enabled}
	r := m.Retrieval
	if r == nil {
		return out
	}
	retrieval := &memoryRetrievalIntent{Strategy: r.Strategy, Limit: r.Limit}
	if r.AccessFilter != nil {
		retrieval.DenyCEL = r.AccessFilter.DenyCEL
	}
	if retrieval.Strategy == "" && retrieval.Limit == nil && retrieval.DenyCEL == "" {
		return out
	}
	out.Retrieval = retrieval
	return out
}

// intentEvals maps the evals block, flattening inline.groups / worker.groups.
func intentEvals(e *EvalsConfig) *evalsIntent {
	if e == nil {
		return nil
	}
	out := &evalsIntent{Enabled: e.Enabled}
	if e.Inline != nil {
		out.Inline = e.Inline.Groups
	}
	if e.Worker != nil {
		out.Worker = e.Worker.Groups
	}
	return out
}

// intentRollout maps the deploy config's rollout policy. Returns nil when no
// policy is configured, which is what keeps a controller-driven rollout intact:
// the server preserves the live rollout wholesale when the intent omits it.
func intentRollout(r *RolloutConfig) *rolloutIntent {
	if r == nil || r.Channel == "" {
		return nil
	}
	out := &rolloutIntent{
		Trigger: &rolloutTriggerIntent{PromptPackChannel: r.Channel},
		Steps:   make([]rolloutStepIntent, 0, len(r.Steps)),
	}
	for _, step := range r.Steps {
		entry := rolloutStepIntent{PauseDuration: step.Pause}
		if step.SetWeight != nil {
			w := int32(*step.SetWeight) //nolint:gosec // validated to 0-100
			entry.SetWeight = &w
		}
		out.Steps = append(out.Steps, entry)
	}
	return out
}

// preflightIntentV1 reports every reason this deploy CANNOT be expressed as a
// v1 DeployIntent. A non-empty result diverts the deploy to the per-resource
// path, which is worth doing ONLY for a field that path can actually deliver.
//
// It deliberately does NOT list fields Omnia has removed (sharedToken, the role
// mappings, handler selectors). Those are undeliverable by either route — the
// per-resource path proxies raw JSON to an apiserver that prunes unknown fields
// and returns success — so diverting for them would trade a working deploy for
// an identical silent drop. They are reported by removedFieldWarnings instead,
// and sharedToken is migrated onto client keys.
//
// Each reason is user-facing: it names the config field and what would be lost.
func preflightIntentV1(pack *prompt.Pack, _ *Config, binding ToolBinding) []string {
	return preflightToolReasons(pack, binding)
}

// preflightToolReasons reports tool and policy shapes intent v1 cannot carry: a
// tool blocklist with no registry to bind it to, and a registry name the
// server's own naming would not reproduce.
//
// Handler selectors are absent by design — Omnia removed them, so neither path
// can deliver one (see removedToolWarnings).
func preflightToolReasons(pack *prompt.Pack, binding ToolBinding) []string {
	var reasons []string
	if len(collectToolBlocklist(pack)) > 0 && binding.RegistryName == "" {
		reasons = append(reasons, "the pack declares a tool blocklist but no tool registry is "+
			"bound — the deploy-intent API builds the policy against the deploy's registry")
	}
	if binding.Mode == toolModeCreate {
		if want := toolRegistryObjectName(sanitizeName(pack.ID)); want != binding.RegistryName {
			reasons = append(reasons, fmt.Sprintf(
				"the resolved tool registry %q does not match the name the deploy-intent API "+
					"would create (%q)", binding.RegistryName, want))
		}
	}
	return reasons
}
