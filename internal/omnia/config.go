package omnia

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
)

// Provider role constants. A binding's role tells the AgentRuntime which
// capability the referenced Provider CRD fulfills. An empty role defaults to
// roleLLM.
const (
	roleLLM       = "llm"
	roleEmbedding = "embedding"
	roleTTS       = "tts"
	roleSTT       = "stt"
	roleImage     = "image"
	roleInference = "inference"
)

// validProviderRoles is the set of accepted provider roles.
var validProviderRoles = map[string]bool{
	roleLLM:       true,
	roleEmbedding: true,
	roleTTS:       true,
	roleSTT:       true,
	roleImage:     true,
	roleInference: true,
}

// Handler type constants. A ToolRegistry handler's type selects which
// type-specific config block (httpConfig/openAPIConfig/...) applies.
const (
	handlerTypeHTTP    = "http"
	handlerTypeOpenAPI = "openapi"
	handlerTypeGRPC    = "grpc"
	handlerTypeMCP     = "mcp"
	handlerTypeClient  = "client"
)

// validHandlerTypes is the set of accepted handler types.
var validHandlerTypes = map[string]bool{
	handlerTypeHTTP:    true,
	handlerTypeOpenAPI: true,
	handlerTypeGRPC:    true,
	handlerTypeMCP:     true,
	handlerTypeClient:  true,
}

// handlerConfigField maps each handler type to the JSON name of its
// type-specific config block, used in validation messages.
var handlerConfigField = map[string]string{
	handlerTypeHTTP:    "httpConfig",
	handlerTypeOpenAPI: "openAPIConfig",
	handlerTypeGRPC:    "grpcConfig",
	handlerTypeMCP:     "mcpConfig",
	handlerTypeClient:  "clientConfig",
}

// handlerNamePattern is the omnia tool/handler name pattern (RFC1123-ish label).
var handlerNamePattern = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)

// Skill selector constants. A SkillsConfig selector tells the PromptPack how
// active skills are chosen for a turn.
const (
	skillSelectorModelDriven = "model-driven"
	skillSelectorTag         = "tag"
	skillSelectorEmbedding   = "embedding"
)

// validSkillSelectors is the set of accepted skill selectors.
var validSkillSelectors = map[string]bool{
	skillSelectorModelDriven: true,
	skillSelectorTag:         true,
	skillSelectorEmbedding:   true,
}

// skillSourceNamePattern is the omnia SkillSource name pattern (RFC1123 label).
var skillSourceNamePattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)

// workspaceNamePattern is the Omnia workspace slug (RFC1123 label). The deploy
// config must use the workspace's lowercase name, not its display name (e.g.
// "default", not "Default") — the display name 404s at the API.
var workspaceNamePattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)

// Memory retrieval strategy constants. They select how recall queries the
// memory store.
const (
	memoryStrategyKeyword   = "keyword"
	memoryStrategySemantic  = "semantic"
	memoryStrategyGraph     = "graph"
	memoryStrategyComposite = "composite"
)

// validMemoryStrategies is the set of accepted retrieval strategies.
var validMemoryStrategies = map[string]bool{
	memoryStrategyKeyword:   true,
	memoryStrategySemantic:  true,
	memoryStrategyGraph:     true,
	memoryStrategyComposite: true,
}

// API-key role constants. An APIKeysAuth defaultRole selects the role applied
// to keys that don't specify one; it must be one of these.
const (
	authRoleViewer = "viewer"
	authRoleEditor = "editor"
	authRoleAdmin  = "admin"
)

// validAuthRoles is the set of accepted API-key default roles.
var validAuthRoles = map[string]bool{
	authRoleViewer: true,
	authRoleEditor: true,
	authRoleAdmin:  true,
}

// SkillBinding maps a deploy-config skill entry to an Omnia SkillSource CRD.
// It mirrors the PromptPack spec.skills[] SkillRef: source is the SkillSource
// name; include narrows which skills are mounted; mountAs renames the mount.
type SkillBinding struct {
	Source  string   `json:"source"`
	Include []string `json:"include,omitempty"`
	MountAs string   `json:"mountAs,omitempty"`
}

// UnmarshalJSON accepts BOTH skill-entry shapes, mirroring Providers:
//   - the bare-string shorthand: "anthropic-skills" — the SkillSource name,
//     with no include filter or mountAs rename. This is what the Omnia
//     dashboard emits in an exported deploy profile.
//   - the full object form: {"source":..,"include":..,"mountAs":..}.
func (s *SkillBinding) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) > 0 && trimmed[0] == '"' {
		var source string
		if err := json.Unmarshal(trimmed, &source); err != nil {
			return fmt.Errorf("skill: invalid string form: %w", err)
		}
		s.Source = source
		return nil
	}
	// Alias to avoid recursing into this method on the object form.
	type skillBindingObject SkillBinding
	var obj skillBindingObject
	if err := json.Unmarshal(trimmed, &obj); err != nil {
		return fmt.Errorf("skill: invalid object form: %w", err)
	}
	*s = SkillBinding(obj)
	return nil
}

// SkillsConfig is the deploy-config skillsConfig block. It maps faithfully to
// the PromptPack spec.skillsConfig: maxActive caps concurrently-active skills;
// selector chooses the activation strategy.
type SkillsConfig struct {
	MaxActive *int   `json:"maxActive,omitempty"`
	Selector  string `json:"selector,omitempty"` // model-driven|tag|embedding
}

// HandlerTool is the schema-only tool descriptor carried by a ToolHandler. It
// is a faithful passthrough to spec.handlers[].tool in the omnia ToolRegistry.
type HandlerTool struct {
	Name         string      `json:"name"`
	Description  string      `json:"description"`
	InputSchema  interface{} `json:"inputSchema"`
	OutputSchema interface{} `json:"outputSchema,omitempty"`
}

// ToolHandler is one entry in the deploy-config tools block. It maps faithfully
// to an omnia ToolRegistry spec.handlers[] HandlerDefinition. The type-specific
// config blocks are passed through verbatim — the adapter does not model their
// inner fields; the omnia CRD/controller validates those deeply.
type ToolHandler struct {
	Name          string                 `json:"name"`
	Type          string                 `json:"type"` // http|openapi|grpc|mcp|client
	Tool          *HandlerTool           `json:"tool,omitempty"`
	Selector      map[string]interface{} `json:"selector,omitempty"`
	HTTPConfig    map[string]interface{} `json:"httpConfig,omitempty"`
	OpenAPIConfig map[string]interface{} `json:"openAPIConfig,omitempty"`
	GRPCConfig    map[string]interface{} `json:"grpcConfig,omitempty"`
	MCPConfig     map[string]interface{} `json:"mcpConfig,omitempty"`
	ClientConfig  map[string]interface{} `json:"clientConfig,omitempty"`
	Timeout       string                 `json:"timeout,omitempty"`
}

// ProviderBinding maps a logical provider name to an Omnia Provider CRD for a
// given role. The binding named "default" is the runtime's primary provider.
type ProviderBinding struct {
	Name string `json:"name"`           // logical name; "default" = the runtime's primary
	Ref  string `json:"ref"`            // Omnia Provider CRD name
	Role string `json:"role,omitempty"` // llm|embedding|tts|stt|image|inference (default llm)
}

// Providers is the list of provider bindings for a deployment. It accepts both
// the new list form and the legacy map form on unmarshal (see UnmarshalJSON).
type Providers []ProviderBinding

// UnmarshalJSON accepts BOTH config shapes:
//   - the NEW list form: [{"name":..,"ref":..,"role":..}]
//   - the LEGACY map form: {"logicalName":"crdName"} — converted to bindings
//     with Role=roleLLM, iterating keys in sorted order for determinism.
func (p *Providers) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		return fmt.Errorf("providers: empty value")
	}

	switch trimmed[0] {
	case '[':
		var bindings []ProviderBinding
		if err := json.Unmarshal(trimmed, &bindings); err != nil {
			return fmt.Errorf("providers: invalid list form: %w", err)
		}
		*p = bindings
		return nil
	case '{':
		var legacy map[string]string
		if err := json.Unmarshal(trimmed, &legacy); err != nil {
			return fmt.Errorf("providers: invalid map form: %w", err)
		}
		names := make([]string, 0, len(legacy))
		for name := range legacy {
			names = append(names, name)
		}
		sort.Strings(names)
		bindings := make([]ProviderBinding, 0, len(names))
		for _, name := range names {
			bindings = append(bindings, ProviderBinding{
				Name: name,
				Ref:  legacy[name],
				Role: roleLLM,
			})
		}
		*p = bindings
		return nil
	default:
		return fmt.Errorf("providers: must be a list of bindings or a name→ref map")
	}
}

// configSchema is the JSON Schema for the omnia provider config.
const configSchema = `{
  "$schema": "http://json-schema.org/draft-07/schema#",
  "type": "object",
  "required": ["api_endpoint", "workspace", "providers"],
  "properties": {
    "api_endpoint": {
      "type": "string",
      "format": "uri",
      "description": "Omnia Management API base URL"
    },
    "workspace": {
      "type": "string",
      "pattern": "^[a-z0-9][a-z0-9-]*[a-z0-9]$",
      "description": "Omnia workspace name"
    },
    "api_token": {
      "type": "string",
      "description": "API bearer token (or set OMNIA_API_TOKEN env var)"
    },
    "providers": {
      "description": "Provider bindings: a list of {name,ref,role} or the legacy name-to-CRD map (role llm).",
      "oneOf": [
        {
          "type": "array",
          "items": {
            "type": "object",
            "required": ["name", "ref"],
            "properties": {
              "name": { "type": "string" },
              "ref": { "type": "string" },
              "role": {
                "type": "string",
                "enum": ["llm", "embedding", "tts", "stt", "image", "inference"]
              }
            },
            "additionalProperties": false
          }
        },
        {
          "type": "object",
          "additionalProperties": { "type": "string" }
        }
      ]
    },
    "tools": {
      "type": "array",
      "description": "Tool handlers for the omnia ToolRegistry spec.handlers[]. Optional.",
      "items": {
        "type": "object",
        "required": ["name", "type"],
        "properties": {
          "name": {
            "type": "string",
            "pattern": "^[a-z0-9]([-a-z0-9]*[a-z0-9])?$",
            "description": "Handler name (unique across handlers)"
          },
          "type": {
            "type": "string",
            "enum": ["http", "openapi", "grpc", "mcp", "client"],
            "description": "Handler type; selects the type-specific config block"
          },
          "tool": {
            "type": "object",
            "description": "Tool schema (required for http and grpc handlers).",
            "required": ["name", "description", "inputSchema"],
            "properties": {
              "name": { "type": "string" },
              "description": { "type": "string" },
              "inputSchema": { "type": "object" },
              "outputSchema": { "type": "object" }
            },
            "additionalProperties": false
          },
          "selector": { "type": "object" },
          "httpConfig": { "type": "object" },
          "openAPIConfig": { "type": "object" },
          "grpcConfig": { "type": "object" },
          "mcpConfig": { "type": "object" },
          "clientConfig": { "type": "object" },
          "timeout": { "type": "string" }
        },
        "additionalProperties": false
      }
    },
    "skills": {
      "type": "array",
      "description": "Skill bindings: each item is a bare SkillSource name or a {source,include,mountAs} object.",
      "items": {
        "oneOf": [
          {
            "type": "string",
            "pattern": "^[a-z0-9]([a-z0-9-]*[a-z0-9])?$",
            "description": "SkillSource CRD name (shorthand for {source: <name>})"
          },
          {
            "type": "object",
            "required": ["source"],
            "properties": {
              "source": {
                "type": "string",
                "pattern": "^[a-z0-9]([a-z0-9-]*[a-z0-9])?$",
                "description": "SkillSource CRD name"
              },
              "include": {
                "type": "array",
                "items": { "type": "string" },
                "description": "Skill names to include from the source (all when omitted)"
              },
              "mountAs": { "type": "string", "description": "Rename the mounted skill set" }
            },
            "additionalProperties": false
          }
        ]
      }
    },
    "skillsConfig": {
      "type": "object",
      "description": "PromptPack spec.skillsConfig: active-skill cap and selection strategy. Optional.",
      "properties": {
        "maxActive": {
          "type": "integer",
          "minimum": 1,
          "description": "Maximum concurrently-active skills"
        },
        "selector": {
          "type": "string",
          "enum": ["model-driven", "tag", "embedding"],
          "description": "Skill activation strategy"
        }
      },
      "additionalProperties": false
    },
    "runtime": {
      "type": "object",
      "properties": {
        "replicas": { "type": "integer", "minimum": 1 },
        "cpu": { "type": "string" },
        "memory": { "type": "string" },
        "autoscaling": {
          "type": "object",
          "description": "Horizontal autoscaling. When enabled, the autoscaler manages replica count.",
          "properties": {
            "enabled": { "type": "boolean" },
            "type": {
              "type": "string",
              "enum": ["hpa", "keda"],
              "description": "Autoscaler backend; 'keda' enables scale-to-zero but needs KEDA installed."
            },
            "min_replicas": { "type": "integer", "minimum": 0 },
            "max_replicas": { "type": "integer", "minimum": 1 },
            "target_cpu_utilization": { "type": "integer", "minimum": 1, "maximum": 100 },
            "target_memory_utilization": { "type": "integer", "minimum": 1, "maximum": 100 },
            "scale_down_stabilization_seconds": { "type": "integer", "minimum": 0, "maximum": 3600 }
          },
          "additionalProperties": false
        }
      },
      "additionalProperties": false
    },
    "externalAuth": {
      "type": "object",
      "description": "Data-plane auth validators (AgentRuntime spec.externalAuth). Each is independent (OR). Optional.",
      "properties": {
        "allowManagementPlane": {
          "type": "boolean",
          "description": "Accept dashboard-minted management-plane tokens (debug view). Defaults true at the CRD."
        },
        "sharedToken": {
          "type": "object",
          "description": "Single shared bearer token in a Secret.",
          "required": ["secretRef"],
          "properties": {
            "secretRef": { "type": "string", "description": "Name of the Secret holding the token (key 'token')" },
            "trustEndUserHeader": { "type": "boolean" }
          },
          "additionalProperties": false
        },
        "apiKeys": {
          "type": "object",
          "description": "Per-caller API keys (key list lives in Secrets, not here).",
          "properties": {
            "defaultRole": { "type": "string", "enum": ["viewer", "editor", "admin"] },
            "trustEndUserHeader": { "type": "boolean" }
          },
          "additionalProperties": false
        },
        "oidc": {
          "type": "object",
          "description": "Validate customer-issued JWTs against an OIDC discovery document.",
          "required": ["issuer", "audience"],
          "properties": {
            "issuer": { "type": "string", "minLength": 1 },
            "audience": { "type": "string", "minLength": 1 },
            "claimMapping": {
              "type": "object",
              "properties": {
                "subject": { "type": "string" },
                "role": { "type": "string" },
                "endUser": { "type": "string" }
              },
              "additionalProperties": false
            }
          },
          "additionalProperties": false
        },
        "edgeTrust": {
          "type": "object",
          "description": "Trust claim-headers injected by an upstream edge (Istio/API gateway).",
          "properties": {
            "headerMapping": {
              "type": "object",
              "properties": {
                "subject": { "type": "string" },
                "role": { "type": "string" },
                "endUser": { "type": "string" },
                "email": { "type": "string" }
              },
              "additionalProperties": false
            },
            "claimsFromHeaders": {
              "type": "object",
              "additionalProperties": { "type": "string" }
            }
          },
          "additionalProperties": false
        }
      },
      "additionalProperties": false
    },
    "memory": {
      "type": "object",
      "description": "Cross-session memory / RAG (AgentRuntime spec.memory). Enabled is the on/off switch. Optional.",
      "properties": {
        "enabled": { "type": "boolean", "description": "Turn cross-session memory on for the agent." },
        "retrieval": {
          "type": "object",
          "description": "Recall strategy, result limit, and access filter.",
          "properties": {
            "strategy": {
              "type": "string",
              "enum": ["keyword", "semantic", "graph", "composite"]
            },
            "limit": { "type": "integer", "minimum": 1, "maximum": 50 },
            "accessFilter": {
              "type": "object",
              "properties": { "denyCEL": { "type": "string" } },
              "additionalProperties": false
            }
          },
          "additionalProperties": false
        }
      },
      "additionalProperties": false
    },
    "evals": {
      "type": "object",
      "description": "Runtime evals (spec.evals). enabled is on/off; inline/worker route eval groups. Optional.",
      "properties": {
        "enabled": { "type": "boolean", "description": "Turn evals on for the agent." },
        "inline": {
          "type": "object",
          "description": "Eval groups run synchronously in the runtime (default: fast-running).",
          "properties": {
            "groups": { "type": "array", "items": { "type": "string" } }
          },
          "additionalProperties": false
        },
        "worker": {
          "type": "object",
          "description": "Eval groups run async in the per-service-group worker (default: long-running, external).",
          "properties": {
            "groups": { "type": "array", "items": { "type": "string" } }
          },
          "additionalProperties": false
        }
      },
      "additionalProperties": false
    },
    "labels": {
      "type": "object",
      "additionalProperties": { "type": "string" },
      "description": "Extra labels to apply to all created resources"
    },
    "dry_run": {
      "type": "boolean",
      "description": "When true, Apply simulates resource creation without API calls"
    }
  },
  "additionalProperties": false
}`

// envAPIToken is the environment variable name for the Omnia API token.
const envAPIToken = "OMNIA_API_TOKEN" //nolint:gosec // environment variable name, not a credential

// Config holds Omnia-specific configuration.
type Config struct {
	APIEndpoint  string              `json:"api_endpoint"`
	Workspace    string              `json:"workspace"`
	APIToken     string              `json:"api_token,omitempty"`
	Providers    Providers           `json:"providers"`
	Tools        []ToolHandler       `json:"tools,omitempty"`
	Skills       []SkillBinding      `json:"skills,omitempty"`
	SkillsConfig *SkillsConfig       `json:"skillsConfig,omitempty"`
	Runtime      *RuntimeConfig      `json:"runtime,omitempty"`
	ExternalAuth *ExternalAuthConfig `json:"externalAuth,omitempty"`
	Memory       *MemoryConfig       `json:"memory,omitempty"`
	Evals        *EvalsConfig        `json:"evals,omitempty"`
	Labels       map[string]string   `json:"labels,omitempty"`
	DryRun       bool                `json:"dry_run,omitempty"`

	// PackJSON holds the raw pack JSON content. Populated at apply-time.
	// NOT serialized — transient computed field.
	PackJSON string `json:"-"`

	// workspaceNormalizedFrom records the original workspace value when it was
	// case-normalized to a slug (e.g. "Default" → "default"), so the change can
	// be surfaced as a warning. NOT serialized.
	workspaceNormalizedFrom string
}

// RuntimeConfig holds optional resource sizing for agent runtimes.
type RuntimeConfig struct {
	Replicas    int                `json:"replicas,omitempty"`
	CPU         string             `json:"cpu,omitempty"`
	Memory      string             `json:"memory,omitempty"`
	Autoscaling *AutoscalingConfig `json:"autoscaling,omitempty"`
}

// AutoscalingConfig holds optional horizontal autoscaling settings. It is a
// faithful passthrough to the AgentRuntime spec.runtime.autoscaling block —
// the adapter sets no defaults of its own; an omitted block means the
// platform default applies (currently static replicas).
type AutoscalingConfig struct {
	Enabled                       bool   `json:"enabled,omitempty"`
	Type                          string `json:"type,omitempty"`
	MinReplicas                   *int   `json:"min_replicas,omitempty"`
	MaxReplicas                   *int   `json:"max_replicas,omitempty"`
	TargetCPUUtilization          *int   `json:"target_cpu_utilization,omitempty"`
	TargetMemoryUtilization       *int   `json:"target_memory_utilization,omitempty"`
	ScaleDownStabilizationSeconds *int   `json:"scale_down_stabilization_seconds,omitempty"`
}

// ExternalAuthConfig is the deploy-config externalAuth block. It maps
// faithfully to the AgentRuntime spec.externalAuth (AgentExternalAuth):
// each validator is independent (OR logic), and an empty block leaves the
// agent reachable only from the management plane (dashboard debug view).
// The adapter does structural validation only — Secret existence and deep
// OIDC/edge checks are the omnia controller's job.
type ExternalAuthConfig struct {
	AllowManagementPlane *bool                  `json:"allowManagementPlane,omitempty"`
	SharedToken          *SharedTokenAuthConfig `json:"sharedToken,omitempty"`
	APIKeys              *APIKeysAuthConfig     `json:"apiKeys,omitempty"`
	OIDC                 *OIDCAuthConfig        `json:"oidc,omitempty"`
	EdgeTrust            *EdgeTrustAuthConfig   `json:"edgeTrust,omitempty"`
}

// SharedTokenAuthConfig validates a single bearer token shared by all
// callers. SecretRef names a Secret (with key "token"); it emits to the CRD
// as a LocalObjectReference ({"name": ...}).
type SharedTokenAuthConfig struct {
	SecretRef          string `json:"secretRef"`
	TrustEndUserHeader bool   `json:"trustEndUserHeader,omitempty"`
}

// APIKeysAuthConfig toggles per-caller API key validation. DefaultRole, when
// set, must be one of viewer|editor|admin.
type APIKeysAuthConfig struct {
	DefaultRole        string `json:"defaultRole,omitempty"`
	TrustEndUserHeader bool   `json:"trustEndUserHeader,omitempty"`
}

// OIDCAuthConfig validates customer-issued JWTs. Issuer and Audience are both
// required when the block is present.
type OIDCAuthConfig struct {
	Issuer       string                  `json:"issuer"`
	Audience     string                  `json:"audience"`
	ClaimMapping *OIDCClaimMappingConfig `json:"claimMapping,omitempty"`
}

// OIDCClaimMappingConfig overrides the JWT claim names the OIDC validator
// reads. All fields optional — omitted fields fall back to CRD defaults.
type OIDCClaimMappingConfig struct {
	Subject string `json:"subject,omitempty"`
	Role    string `json:"role,omitempty"`
	EndUser string `json:"endUser,omitempty"`
}

// EdgeTrustAuthConfig trusts claim-headers injected by an upstream edge. It
// has no required fields.
type EdgeTrustAuthConfig struct {
	HeaderMapping     *EdgeTrustHeaderMappingConfig `json:"headerMapping,omitempty"`
	ClaimsFromHeaders map[string]string             `json:"claimsFromHeaders,omitempty"`
}

// EdgeTrustHeaderMappingConfig overrides the inbound header names the
// edgeTrust validator reads. All fields optional.
type EdgeTrustHeaderMappingConfig struct {
	Subject string `json:"subject,omitempty"`
	Role    string `json:"role,omitempty"`
	EndUser string `json:"endUser,omitempty"`
	Email   string `json:"email,omitempty"`
}

// MemoryConfig is the deploy-config memory block. It maps faithfully to the
// AgentRuntime spec.memory (MemoryConfig): cross-session memory / RAG for the
// agent. Enabled is the on/off switch; Retrieval tunes how recall queries the
// store. The adapter does structural validation only — deep retrieval checks
// are the omnia controller's job. Embedding for semantic memory is configured
// at the workspace level (the workspace service group's memory-api uses a
// configured embedding Provider), not per agent — there is no per-agent
// embedding setting.
type MemoryConfig struct {
	Enabled   bool                   `json:"enabled,omitempty"`
	Retrieval *MemoryRetrievalConfig `json:"retrieval,omitempty"`
}

// MemoryRetrievalConfig controls how memories are recalled: the strategy, the
// result limit (1–50), and an optional access filter. All fields optional.
type MemoryRetrievalConfig struct {
	Strategy     string                    `json:"strategy,omitempty"`
	Limit        *int32                    `json:"limit,omitempty"`
	AccessFilter *MemoryAccessFilterConfig `json:"accessFilter,omitempty"`
}

// MemoryAccessFilterConfig carries a CEL expression that denies recall when it
// evaluates true. Optional.
type MemoryAccessFilterConfig struct {
	DenyCEL string `json:"denyCEL,omitempty"`
}

// EvalsConfig is the deploy-config evals block. It maps to the AgentRuntime
// spec.evals: enabled is the on/off switch the runtime and the eval-worker
// reconcile gate both read; inline/worker route which eval *groups* run where.
// The eval *definitions* come from the PromptPack (pack.json "evals"), NOT this
// block — this block only turns evals on and routes group execution. The
// adapter intentionally exposes only the proven-wired knobs: sampling,
// rateLimit, sessionCompletion, and podOverrides exist on the CRD but are not
// surfaced per-agent here (unwired or platform-level). The adapter does
// structural validation only.
type EvalsConfig struct {
	Enabled bool            `json:"enabled,omitempty"`
	Inline  *EvalPathConfig `json:"inline,omitempty"`
	Worker  *EvalPathConfig `json:"worker,omitempty"`
}

// EvalPathConfig names the eval groups that run on a single path (inline in the
// runtime, or in the per-service-group worker). Groups are free-form names
// resolved against the PromptPack's eval definitions.
type EvalPathConfig struct {
	Groups []string `json:"groups,omitempty"`
}

// parseConfig unmarshals JSON config into Config.
func parseConfig(raw string) (*Config, error) {
	var cfg Config
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		return nil, fmt.Errorf("invalid config JSON: %w", err)
	}
	cfg.normalizeWorkspace()
	return &cfg, nil
}

// normalizeWorkspace lowercases the workspace name so the common "Default"
// (display name) vs "default" (slug) case slip just works: workspace slugs are
// always lowercase k8s names, so an uppercase input has exactly one valid
// reading. It records the original for a transparency warning. A name that is
// still not a valid slug once lowercased (e.g. it contains spaces) is left
// untouched for validate() to reject with a clear message.
func (c *Config) normalizeWorkspace() {
	if c.Workspace == "" {
		return
	}
	lower := strings.ToLower(c.Workspace)
	if lower != c.Workspace && workspaceNamePattern.MatchString(lower) {
		c.workspaceNormalizedFrom = c.Workspace
		c.Workspace = lower
	}
}

// normalizationWarnings returns a transparency advisory when the workspace name
// was case-normalized to its slug, so the user can clean up the source config.
func (c *Config) normalizationWarnings() []string {
	if c.workspaceNormalizedFrom == "" {
		return nil
	}
	return []string{fmt.Sprintf(
		"workspace %q normalized to %q — workspace names are lowercase slugs, "+
			"not display names", c.workspaceNormalizedFrom, c.Workspace)}
}

// resolveToken returns the API token from config or the environment variable.
func (c *Config) resolveToken() string {
	if c.APIToken != "" {
		return c.APIToken
	}
	return os.Getenv(envAPIToken)
}

// endpointRoot returns the API endpoint with any trailing slash trimmed.
func (c *Config) endpointRoot() string {
	return strings.TrimRight(c.APIEndpoint, "/")
}

// baseURL returns the full base URL for the workspace API.
func (c *Config) baseURL() string {
	return fmt.Sprintf("%s/api/workspaces/%s", c.endpointRoot(), c.Workspace)
}

// validate checks the config and returns any validation errors.
func (c *Config) validate() []string {
	var errs []string

	if c.APIEndpoint == "" {
		errs = append(errs, "api_endpoint is required")
	}

	if c.Workspace == "" {
		errs = append(errs, "workspace is required")
	} else if !workspaceNamePattern.MatchString(c.Workspace) {
		errs = append(errs, fmt.Sprintf(
			"workspace %q is not a valid name — use the lowercase workspace slug "+
				"(e.g. \"default\"), not the display name", c.Workspace))
	}

	errs = append(errs, validateProviderBindings(c.Providers)...)

	errs = append(errs, validateToolHandlers(c.Tools)...)

	errs = append(errs, validateSkills(c.Skills, c.SkillsConfig)...)

	errs = append(errs, validateExternalAuth(c.ExternalAuth)...)

	errs = append(errs, validateMemory(c.Memory)...)

	errs = append(errs, validateEvals(c.Evals)...)

	if c.resolveToken() == "" {
		errs = append(errs, "api_token is required (set in config or OMNIA_API_TOKEN env var)")
	}

	if c.Runtime != nil {
		if c.Runtime.Replicas < 0 {
			errs = append(errs, "runtime.replicas must be >= 1")
		}
		errs = append(errs, validateAutoscaling(c.Runtime.Autoscaling)...)
	}

	return errs
}

// validateProviderBindings checks the provider bindings: at least one is
// required, each ref must be non-empty, each role (if set) must be in the
// enum, and binding names must be unique. A "default" binding is NOT required.
func validateProviderBindings(bindings Providers) []string {
	if len(bindings) == 0 {
		errs := []string{"providers is required (at least one provider binding)"}
		return errs
	}
	var errs []string
	seen := make(map[string]bool, len(bindings))
	for _, b := range bindings {
		if b.Ref == "" {
			errs = append(errs, fmt.Sprintf("provider binding %q: ref is required", b.Name))
		}
		if b.Role != "" && !validProviderRoles[b.Role] {
			errs = append(errs, fmt.Sprintf("provider binding %q: invalid role %q", b.Name, b.Role))
		}
		if seen[b.Name] {
			errs = append(errs, fmt.Sprintf("provider binding name %q is duplicated", b.Name))
		}
		seen[b.Name] = true
	}
	return errs
}

// defaultProviderName is the logical binding name the omnia runtime treats as
// the primary provider (see defaultProviderIndex in the omnia runtime).
const defaultProviderName = "default"

// providerWarnings returns non-blocking advisories about the provider bindings.
// When no binding is named "default", the runtime falls back to the
// lexicographically-first binding as the primary — which is rarely deliberate —
// so we warn and name the exact binding the runtime would pick, mirroring
// defaultProviderIndex in the omnia runtime.
func providerWarnings(bindings Providers) []string {
	if len(bindings) == 0 {
		return nil
	}
	first := bindings[0]
	for _, b := range bindings {
		if b.Name == defaultProviderName {
			return nil
		}
		if b.Name < first.Name {
			first = b
		}
	}
	return []string{fmt.Sprintf(
		"no provider binding is named %q — the runtime will use the "+
			"alphabetically-first binding (%q → ref %q) as the primary. "+
			"Set name: default on the binding you intend as primary to choose deliberately.",
		defaultProviderName, first.Name, first.Ref,
	)}
}

// validateSkills checks the optional skills block and skillsConfig. Skills are
// optional (zero is fine). Each binding's source must be non-empty and match
// the SkillSource name pattern. If skillsConfig is set, the selector (when
// non-empty) must be in the enum and maxActive (when set) must be >= 1.
// Light/structural only — the omnia PromptPack CRD validates deeply.
func validateSkills(skills []SkillBinding, sc *SkillsConfig) []string {
	var errs []string
	for _, b := range skills {
		switch {
		case b.Source == "":
			errs = append(errs, "skill binding: source is required")
		case !skillSourceNamePattern.MatchString(b.Source):
			errs = append(errs, fmt.Sprintf(
				"skill binding %q: source must match %s", b.Source, skillSourceNamePattern.String()))
		}
	}

	if sc != nil {
		if sc.Selector != "" && !validSkillSelectors[sc.Selector] {
			errs = append(errs, fmt.Sprintf("skillsConfig: invalid selector %q", sc.Selector))
		}
		if sc.MaxActive != nil && *sc.MaxActive < 1 {
			errs = append(errs, "skillsConfig.maxActive must be >= 1")
		}
	}

	return errs
}

// validateExternalAuth checks the optional externalAuth block. Structural
// only — the adapter rejects only what the AgentExternalAuth CRD would also
// reject, so a valid adapter config produces an admissible spec. It does NOT
// validate Secret existence or fetch OIDC discovery; that is the controller's
// job. Each validator is independent; an empty block is valid (the agent
// stays management-plane-only). edgeTrust has no required fields.
func validateExternalAuth(ea *ExternalAuthConfig) []string {
	if ea == nil {
		return nil
	}
	var errs []string

	if ea.SharedToken != nil && ea.SharedToken.SecretRef == "" {
		errs = append(errs, "externalAuth.sharedToken.secretRef is required")
	}

	if ea.APIKeys != nil && ea.APIKeys.DefaultRole != "" && !validAuthRoles[ea.APIKeys.DefaultRole] {
		errs = append(errs, fmt.Sprintf(
			"externalAuth.apiKeys.defaultRole %q must be one of viewer, editor, admin",
			ea.APIKeys.DefaultRole))
	}

	if ea.OIDC != nil {
		if ea.OIDC.Issuer == "" {
			errs = append(errs, "externalAuth.oidc.issuer is required")
		}
		if ea.OIDC.Audience == "" {
			errs = append(errs, "externalAuth.oidc.audience is required")
		}
	}

	return errs
}

// validateMemory checks the optional memory block. Structural only — the
// adapter rejects only what the AgentRuntime spec.memory CRD would also reject,
// so a valid adapter config produces an admissible spec. A nil block is valid
// (no memory is configured). Any set retrieval enum/limit must be within its
// allowed set/range.
func validateMemory(m *MemoryConfig) []string {
	if m == nil {
		return nil
	}
	var errs []string

	if r := m.Retrieval; r != nil {
		if r.Strategy != "" && !validMemoryStrategies[r.Strategy] {
			errs = append(errs, fmt.Sprintf(
				"memory.retrieval.strategy %q must be one of keyword, semantic, graph, composite", r.Strategy))
		}
		if r.Limit != nil && (*r.Limit < 1 || *r.Limit > 50) {
			errs = append(errs, "memory.retrieval.limit must be between 1 and 50")
		}
	}

	return errs
}

// validateEvals checks the optional evals block. Structural only — group names
// are free-form (resolved against the PromptPack's eval definitions), so the
// only genuine structural issue is an empty-string group entry. A nil block is
// valid (no evals configured).
func validateEvals(e *EvalsConfig) []string {
	if e == nil {
		return nil
	}
	var errs []string
	errs = append(errs, validateEvalGroups("evals.inline.groups", e.Inline)...)
	errs = append(errs, validateEvalGroups("evals.worker.groups", e.Worker)...)
	return errs
}

// validateEvalGroups rejects empty-string entries in a path's group list.
func validateEvalGroups(field string, p *EvalPathConfig) []string {
	if p == nil {
		return nil
	}
	for _, g := range p.Groups {
		if strings.TrimSpace(g) == "" {
			return []string{field + " must not contain empty group names"}
		}
	}
	return nil
}

// validateAutoscaling checks the optional autoscaling block. The adapter only
// rejects values the AgentRuntime CRD would also reject, so a valid adapter
// config produces a valid spec.
func validateAutoscaling(a *AutoscalingConfig) []string {
	if a == nil {
		return nil
	}
	var errs []string

	if a.Type != "" && a.Type != "hpa" && a.Type != "keda" {
		errs = append(errs, `runtime.autoscaling.type must be "hpa" or "keda"`)
	}
	if a.MinReplicas != nil && *a.MinReplicas < 0 {
		errs = append(errs, "runtime.autoscaling.min_replicas must be >= 0")
	}
	if a.MaxReplicas != nil && *a.MaxReplicas < 1 {
		errs = append(errs, "runtime.autoscaling.max_replicas must be >= 1")
	}
	if a.MinReplicas != nil && a.MaxReplicas != nil && *a.MinReplicas > *a.MaxReplicas {
		errs = append(errs, "runtime.autoscaling.min_replicas must not exceed max_replicas")
	}
	errs = append(errs, validatePercent("runtime.autoscaling.target_cpu_utilization", a.TargetCPUUtilization)...)
	errs = append(errs, validatePercent("runtime.autoscaling.target_memory_utilization", a.TargetMemoryUtilization)...)

	return errs
}

// validatePercent checks that an optional utilization target is within 1-100.
func validatePercent(field string, v *int) []string {
	if v != nil && (*v < 1 || *v > 100) {
		return []string{field + " must be between 1 and 100"}
	}
	return nil
}

// validateToolHandlers checks the optional tools block. Light structural
// validation only — name pattern/uniqueness, type enum, and the
// tool/config-or-selector presence rules per type. The omnia CRD/controller
// validates inner config fields (endpoint URLs etc.) deeply, so this mirrors
// the philosophy of validateProviderBindings: reject only what omnia would.
// An empty tools block is valid (no ToolRegistry is created).
func validateToolHandlers(handlers []ToolHandler) []string {
	var errs []string
	seen := make(map[string]bool, len(handlers))
	for i := range handlers {
		errs = append(errs, validateToolHandler(&handlers[i], seen)...)
	}
	return errs
}

// validateToolHandler validates a single handler and records its name in seen.
func validateToolHandler(h *ToolHandler, seen map[string]bool) []string {
	var errs []string

	switch {
	case h.Name == "":
		errs = append(errs, "tool handler: name is required")
	case !handlerNamePattern.MatchString(h.Name):
		errs = append(errs, fmt.Sprintf("tool handler %q: name must match %s", h.Name, handlerNamePattern.String()))
	case seen[h.Name]:
		errs = append(errs, fmt.Sprintf("tool handler name %q is duplicated", h.Name))
	}
	seen[h.Name] = true

	if !validHandlerTypes[h.Type] {
		errs = append(errs, fmt.Sprintf("tool handler %q: invalid type %q", h.Name, h.Type))
		return errs
	}

	return append(errs, validateHandlerByType(h)...)
}

// validateHandlerByType applies the per-type tool/config requirements.
func validateHandlerByType(h *ToolHandler) []string {
	switch h.Type {
	case handlerTypeHTTP, handlerTypeGRPC:
		return validateHandlerWithTool(h)
	case handlerTypeOpenAPI:
		return requireConfigOrSelector(h, h.OpenAPIConfig)
	case handlerTypeMCP:
		return requireConfigOrSelector(h, h.MCPConfig)
	default: // handlerTypeClient: clientConfig is optional, no hard requirement.
		return nil
	}
}

// validateHandlerWithTool enforces the http/grpc rules: a complete tool block
// plus either the matching config block or a selector.
func validateHandlerWithTool(h *ToolHandler) []string {
	var errs []string
	errs = append(errs, validateRequiredTool(h)...)

	var cfg map[string]interface{}
	if h.Type == handlerTypeHTTP {
		cfg = h.HTTPConfig
	} else {
		cfg = h.GRPCConfig
	}
	return append(errs, requireConfigOrSelector(h, cfg)...)
}

// validateRequiredTool enforces a non-empty tool block for handlers that need it.
func validateRequiredTool(h *ToolHandler) []string {
	if h.Tool == nil {
		return []string{fmt.Sprintf("tool handler %q: tool is required for type %q", h.Name, h.Type)}
	}
	var errs []string
	if h.Tool.Name == "" {
		errs = append(errs, fmt.Sprintf("tool handler %q: tool.name is required", h.Name))
	}
	if h.Tool.Description == "" {
		errs = append(errs, fmt.Sprintf("tool handler %q: tool.description is required", h.Name))
	}
	if h.Tool.InputSchema == nil {
		errs = append(errs, fmt.Sprintf("tool handler %q: tool.inputSchema is required", h.Name))
	}
	return errs
}

// requireConfigOrSelector requires the type's config block OR a selector.
func requireConfigOrSelector(h *ToolHandler, cfg map[string]interface{}) []string {
	if len(cfg) > 0 || len(h.Selector) > 0 {
		return nil
	}
	return []string{fmt.Sprintf(
		"tool handler %q: %s or selector is required", h.Name, handlerConfigField[h.Type])}
}
