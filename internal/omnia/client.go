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

	// ValidateProvider checks that a Provider CRD exists.
	ValidateProvider(ctx context.Context, name string) error

	// ListProviders returns the workspace's Provider CRDs (name/type/model/role),
	// for validating refs against what's actually available and reporting it.
	ListProviders(ctx context.Context) ([]ProviderSummary, error)

	// ValidateSkillSource checks that a SkillSource CRD exists and is synced.
	ValidateSkillSource(ctx context.Context, name string) error

	// Health checks the API health endpoint.
	Health(ctx context.Context) error
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
