package omnia

import (
	"context"
	"encoding/json"
)

// omniaClient abstracts the Omnia Management API for testability.
type omniaClient interface {
	// Resource CRUD operations.
	CreateResource(ctx context.Context, resType, name string, body json.RawMessage) (*ResourceResponse, error)
	GetResource(ctx context.Context, resType, name string) (*ResourceResponse, error)
	UpdateResource(ctx context.Context, resType, name string, body json.RawMessage) (*ResourceResponse, error)
	DeleteResource(ctx context.Context, resType, name string) error

	// ListResources returns resources matching the label selector.
	ListResources(ctx context.Context, resType, labelSelector string) ([]ResourceResponse, error)

	// PostDeployment submits a whole deploy as a single versioned DeployIntent to
	// the server-owned deploy-intent API, which builds the CRDs itself. Returns
	// the per-resource apply outcome.
	PostDeployment(ctx context.Context, intent json.RawMessage) (*DeployResult, error)

	// GetDeployProfile returns the workspace's deploy profile, whose
	// supportedDeployIntentVersions advertises whether the deploy-intent API is
	// available on this server.
	GetDeployProfile(ctx context.Context) (*DeployProfile, error)

	// ValidateProvider checks that a Provider CRD exists.
	ValidateProvider(ctx context.Context, name string) error

	// ListProviders returns the workspace's Provider CRDs (name/type/model/role),
	// for validating refs against what's actually available and reporting it.
	ListProviders(ctx context.Context) ([]ProviderSummary, error)

	// ListToolRegistries returns the workspace's ToolRegistry CRDs reduced to the
	// LLM-facing tool names + input schemas they expose, for matching a pack's
	// declared tools against what a registry actually provides.
	ListToolRegistries(ctx context.Context) ([]ToolRegistrySummary, error)

	// ValidateSkillSource checks that a SkillSource CRD exists and is synced.
	ValidateSkillSource(ctx context.Context, name string) error

	// GetWorkspace resolves a workspace's target namespace (spec.namespace.name),
	// where tool-credential Secrets are created.
	GetWorkspace(ctx context.Context, name string) (*WorkspaceInfo, error)

	// CreateSecret creates/updates an Opaque credentials Secret in the namespace.
	CreateSecret(ctx context.Context, namespace, name string, data map[string]string) error

	// Health checks the API health endpoint.
	Health(ctx context.Context) error
}

// DeployResult is the deploy-intent API's response: a best-effort apply with a
// per-resource outcome. Succeeded is false when any resource failed; the
// resources that did apply are still reported.
type DeployResult struct {
	Succeeded bool                   `json:"succeeded"`
	Results   []DeployResourceResult `json:"results"`
}

// DeployResourceResult is the outcome for a single object the server applied.
// Kind is the Kubernetes kind (PromptPack, ConfigMap, ToolRegistry, AgentPolicy,
// AgentRuntime); Name is the object name the SERVER chose.
type DeployResourceResult struct {
	Kind   string `json:"kind"`
	Name   string `json:"name"`
	Action string `json:"action"` // created|updated|unchanged|failed
	Error  string `json:"error,omitempty"`
	// URL is the operator-facing console link Omnia returns for this resource
	// (Omnia#1978). The adapter passes it straight through and never builds one:
	// the dashboard owns its own routes and its public address, so constructing
	// a URL here would hardcode an Omnia route into this repo and make changing
	// it a coordinated release across two projects.
	//
	// Absent means "not known" — Omnia omits it for a kind with no console page,
	// a resource that failed to apply, or an unknown public address. The
	// adapter then reports no link rather than inventing one.
	URL string `json:"url,omitempty"`
}

// Deploy-intent action values reported per resource by the server.
const (
	intentActionCreated   = "created"
	intentActionUpdated   = "updated"
	intentActionUnchanged = "unchanged"
	intentActionFailed    = "failed"
)

// DeployProfile is the workspace's deploy discovery document, reduced to what
// the adapter reads. An empty SupportedDeployIntentVersions means the server
// predates the deploy-intent API and only serves the per-resource CRD routes.
type DeployProfile struct {
	SupportedDeployIntentVersions []string `json:"supportedDeployIntentVersions"`
}

// supportsIntentV1 reports whether the profile advertises the DeployIntent
// contract version this adapter emits.
func (p *DeployProfile) supportsIntentV1() bool {
	if p == nil {
		return false
	}
	for _, v := range p.SupportedDeployIntentVersions {
		if v == intentAPIVersionV1 {
			return true
		}
	}
	return false
}

// WorkspaceInfo is a workspace reduced to what the adapter needs.
type WorkspaceInfo struct {
	Namespace string // spec.namespace.name — where tool Secrets are created
}

// omniaClientFactory creates an omniaClient for the given config.
type omniaClientFactory func(cfg *Config) (omniaClient, error)

// ProviderSummary is a workspace Provider CRD reduced to the fields useful for
// validating and reporting deploy bindings.
type ProviderSummary struct {
	Name  string // the CRD name — what a binding's ref must match
	Type  string // e.g. openai, anthropic, ollama
	Model string // e.g. gpt-4o (may be empty)
	Role  string // llm, embedding, tts, …
	Phase string // status.phase: Ready, Error, Unavailable, … (empty if unknown)
}

// ToolRegistrySummary is a workspace ToolRegistry CRD reduced to the LLM-facing
// tools it exposes — what a pack's declared tool names are matched against.
type ToolRegistrySummary struct {
	Name  string         // the CRD name — what a tool_registry_ref binds to
	Tools []RegistryTool // one per spec.handlers[] that carries an inline tool block
	// Dynamic is true when the registry has a handler that resolves its tools
	// externally (an openapi specURL, an mcp server) rather than declaring them
	// inline. Such tools can't be enumerated or schema-checked statically, so
	// coverage against them is unverifiable rather than absent.
	Dynamic bool
}

// RegistryTool is one tool a ToolRegistry exposes: the LLM-facing name
// (handler.tool.name, snake_case — what the pack references) and its input
// schema (handler.tool.inputSchema), kept raw for a normalized schema compare.
type RegistryTool struct {
	Name        string
	InputSchema json.RawMessage
}

// ResourceResponse is the envelope returned by the Omnia API for a single resource.
type ResourceResponse struct {
	Kind     string           `json:"kind"`
	Metadata ResourceMetadata `json:"metadata"`
	Spec     json.RawMessage  `json:"spec,omitempty"`
	Status   *ResourceStatus  `json:"status,omitempty"`
}

// ResourceMetadata holds Kubernetes-style metadata from the API response.
type ResourceMetadata struct {
	Name            string            `json:"name"`
	Namespace       string            `json:"namespace,omitempty"`
	UID             string            `json:"uid,omitempty"`
	ResourceVersion string            `json:"resourceVersion,omitempty"`
	Labels          map[string]string `json:"labels,omitempty"`
}

// ResourceStatus holds the status section of an API response.
type ResourceStatus struct {
	Phase      string              `json:"phase,omitempty"`
	Conditions []ResourceCondition `json:"conditions,omitempty"`
}

// ResourceCondition represents a single status condition.
type ResourceCondition struct {
	Type   string `json:"type"`
	Status string `json:"status"`
}

// resourceTypePath maps adapter resource type constants to API URL path segments.
func resourceTypePath(resType string) string {
	switch resType {
	case ResTypePromptPack:
		return "promptpacks"
	case ResTypeAgentRuntime:
		return "agents"
	case ResTypeToolRegistry:
		return "toolregistries"
	case ResTypeAgentPolicy:
		return "agentpolicies"
	default:
		return resType
	}
}
