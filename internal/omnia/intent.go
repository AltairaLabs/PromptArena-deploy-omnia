package omnia

import (
	"encoding/json"
	"fmt"

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
// It mirrors the server's DeployIntent (omnia internal/api/deploy/types.go). Only
// the fields the adapter can populate from its config surface are modeled; the
// server's rollout block is server/controller-driven and is deliberately not sent
// (an existing trigger-mode agent's canary is preserved server-side).
type deployIntent struct {
	APIVersion string            `json:"apiVersion"`
	Pack       packIntent        `json:"pack"`
	Tools      *toolsIntent      `json:"tools,omitempty"`
	Policy     *policyIntent     `json:"policy,omitempty"`
	Agents     []agentIntent     `json:"agents"`
	Labels     map[string]string `json:"labels,omitempty"`
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
}

// providerBind is one provider binding: logical slot, Provider CRD name, role.
type providerBind struct {
	Name string `json:"name"`
	Ref  string `json:"ref"`
	Role string `json:"role,omitempty"`
}

// runtimeIntent carries replica count and resource requests.
type runtimeIntent struct {
	Replicas *int32 `json:"replicas,omitempty"`
	CPU      string `json:"cpu,omitempty"`
	Memory   string `json:"memory,omitempty"`
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
	return json.Marshal(intent)
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
		handlers, _ := buildCreateRegistryHandlers(pack, cfg)
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
	out := &runtimeIntent{CPU: rc.CPU, Memory: rc.Memory}
	if rc.Replicas > 0 {
		v := int32(rc.Replicas) //nolint:gosec // replicas is a small operator-set count
		out.Replicas = &v
	}
	if out.Replicas == nil && out.CPU == "" && out.Memory == "" {
		return nil
	}
	return out
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
func preflightIntentV1(pack *prompt.Pack, cfg *Config, binding ToolBinding) []string {
	var reasons []string
	reasons = append(reasons, preflightRuntimeReasons(cfg.Runtime)...)
	reasons = append(reasons, preflightToolReasons(pack, binding)...)
	return reasons
}

// preflightRuntimeReasons reports runtime fields intent v1 cannot carry.
func preflightRuntimeReasons(rc *RuntimeConfig) []string {
	if rc == nil || rc.Autoscaling == nil {
		return nil
	}
	return []string{"runtime.autoscaling — the deploy-intent contract carries replicas and " +
		"resource requests only"}
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
