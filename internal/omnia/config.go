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
      "description": "Skill bindings for the PromptPack spec.skills[]. Each references a SkillSource. Optional.",
      "items": {
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
	Labels       map[string]string   `json:"labels,omitempty"`
	DryRun       bool                `json:"dry_run,omitempty"`

	// PackJSON holds the raw pack JSON content. Populated at apply-time.
	// NOT serialized — transient computed field.
	PackJSON string `json:"-"`
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

// parseConfig unmarshals JSON config into Config.
func parseConfig(raw string) (*Config, error) {
	var cfg Config
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		return nil, fmt.Errorf("invalid config JSON: %w", err)
	}
	return &cfg, nil
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
	}

	errs = append(errs, validateProviderBindings(c.Providers)...)

	errs = append(errs, validateToolHandlers(c.Tools)...)

	errs = append(errs, validateSkills(c.Skills, c.SkillsConfig)...)

	errs = append(errs, validateExternalAuth(c.ExternalAuth)...)

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
