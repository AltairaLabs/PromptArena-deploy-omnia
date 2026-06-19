package omnia

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

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
      "type": "object",
      "additionalProperties": { "type": "string" },
      "description": "Arena provider name to Omnia Provider CRD name mapping"
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
	APIEndpoint string            `json:"api_endpoint"`
	Workspace   string            `json:"workspace"`
	APIToken    string            `json:"api_token,omitempty"`
	Providers   map[string]string `json:"providers"`
	Runtime     *RuntimeConfig    `json:"runtime,omitempty"`
	Labels      map[string]string `json:"labels,omitempty"`
	DryRun      bool              `json:"dry_run,omitempty"`

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

	if len(c.Providers) == 0 {
		errs = append(errs, "providers map is required (at least one arena→omnia provider mapping)")
	}

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
