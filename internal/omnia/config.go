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
        "memory": { "type": "string" }
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
	Replicas int    `json:"replicas,omitempty"`
	CPU      string `json:"cpu,omitempty"`
	Memory   string `json:"memory,omitempty"`
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

// baseURL returns the full base URL for the workspace API.
func (c *Config) baseURL() string {
	endpoint := strings.TrimRight(c.APIEndpoint, "/")
	return fmt.Sprintf("%s/api/v1/workspaces/%s", endpoint, c.Workspace)
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
	}

	return errs
}
