package omnia

// Resource type constants used across plan, apply, destroy, and status.
const (
	ResTypePromptPack   = "prompt_pack"
	ResTypeAgentRuntime = "agent_runtime"
	ResTypeToolRegistry = "tool_registry"
	ResTypeAgentPolicy  = "agent_policy"
)

// Resource lifecycle status constants used in ResourceState.Status.
const (
	ResStatusCreated   = "created"
	ResStatusUpdated   = "updated"
	ResStatusFailed    = "failed"
	ResStatusPlanned   = "planned"
	ResStatusDeleted   = "deleted"
	ResStatusUnchanged = "unchanged"
)

// Health status constants returned by resource checks.
const (
	StatusHealthy   = "healthy"
	StatusUnhealthy = "unhealthy"
	StatusMissing   = "missing"
)

// AdapterState holds resource info from previous deploys. It is serialized
// as the opaque "prior_state" string exchanged between Plan, Apply, and Status.
type AdapterState struct {
	Resources  []ResourceState `json:"resources"`
	PackID     string          `json:"pack_id,omitempty"`
	Version    string          `json:"version,omitempty"`
	DeployedAt string          `json:"deployed_at,omitempty"`
}

// ResourceState describes a single deployed resource.
type ResourceState struct {
	Type            string            `json:"type"`
	Name            string            `json:"name"`
	UID             string            `json:"uid,omitempty"`
	ResourceVersion string            `json:"resource_version,omitempty"`
	Status          string            `json:"status,omitempty"`
	Metadata        map[string]string `json:"metadata,omitempty"`
}
