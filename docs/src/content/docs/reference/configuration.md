---
title: Configuration Reference
description: Complete JSON Schema and field-by-field documentation for the Omnia adapter
---

This page documents the complete configuration schema accepted by the Omnia deploy adapter.

## JSON Schema

The adapter validates configuration against the following JSON Schema:

```json
{
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
}
```

## Field details

### `api_endpoint`

| | |
|---|---|
| **Type** | `string` (URI) |
| **Required** | Yes |
| **Example** | `"https://omnia.example.com"` |

Base URL of the Omnia Management API. The adapter constructs the full workspace URL as `<api_endpoint>/api/workspaces/<workspace>`. Trailing slashes are stripped automatically.

### `workspace`

| | |
|---|---|
| **Type** | `string` |
| **Required** | Yes |
| **Pattern** | `^[a-z0-9][a-z0-9-]*[a-z0-9]$` |
| **Example** | `"prod-workspace"` |

The Omnia workspace to deploy into. Must be a valid Kubernetes DNS subdomain: lowercase alphanumeric characters and hyphens, starting and ending with an alphanumeric character.

### `api_token`

| | |
|---|---|
| **Type** | `string` |
| **Required** | No (but token must be available via config or `OMNIA_API_TOKEN` env var) |
| **Example** | `"eyJhbGciOiJSUzI1..."` |

Bearer token for authenticating with the Omnia Management API. If omitted, the adapter reads the `OMNIA_API_TOKEN` environment variable. At least one source must provide a token; validation fails otherwise.

### `providers`

| | |
|---|---|
| **Type** | `array` of bindings **or** `object` (legacy map) |
| **Required** | Yes (at least one binding) |

Provider bindings tell each AgentRuntime which Omnia Provider CRD fulfils a given capability. The field accepts **two shapes**.

#### List form (recommended)

A list of `{name, ref, role}` bindings. Every binding is emitted to each AgentRuntime as a `NamedProviderRef`. The binding named `default` is the runtime's primary provider.

| Sub-field | Type | Required | Description |
|---|---|---|---|
| `name` | string | Yes | Logical binding name. `default` is the runtime's primary. |
| `ref` | string | Yes | Name of the Omnia `Provider` CRD to reference. |
| `role` | string | No | Capability the provider fulfils. Defaults to `llm`. |

`role` must be one of: `llm`, `embedding`, `tts`, `stt`, `image`, `inference`.

```yaml
providers:
  - name: default
    ref: claude-prod
    role: llm
  - name: embedder
    ref: text-embedding-3-large
    role: embedding
```

Binding names must be unique. A `default` binding is **not** required by validation, but it is the conventional primary.

#### Legacy map form

A `name → Provider CRD` map. Each entry is converted to a binding with role `llm` (keys are processed in sorted order for deterministic output).

```yaml
providers:
  default: claude-prod
  router: gpt4-prod
```

### `tools`

| | |
|---|---|
| **Type** | `array` of handler specs |
| **Required** | No |

Tool handlers projected verbatim into the `ToolRegistry` CRD's `spec.handlers[]`, preserving order. When `tools` is empty, **no** ToolRegistry is created. (The ToolRegistry is built from this block, not from tools embedded in the pack — inline pack tool schemas reach the runtime through the PromptPack content fold instead.)

| Sub-field | Type | Required | Description |
|---|---|---|---|
| `name` | string | Yes | Handler name; unique across handlers. Pattern `^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`. |
| `type` | string | Yes | One of `http`, `openapi`, `grpc`, `mcp`, `client`. Selects the type-specific config block. |
| `tool` | object | Conditional | Tool schema (`name`, `description`, `inputSchema`, optional `outputSchema`). Required for `http` and `grpc`. |
| `selector` | object | Conditional | Selector matching tools to this handler. For `http`/`grpc` it satisfies the config-or-selector requirement; for `openapi`/`mcp` either it or the matching config block is required. |
| `httpConfig` | object | Conditional | HTTP handler config. Required (or a `selector`) for `http`. |
| `openAPIConfig` | object | Conditional | OpenAPI handler config. Required (or a `selector`) for `openapi`. |
| `grpcConfig` | object | Conditional | gRPC handler config. Required (or a `selector`) for `grpc`. |
| `mcpConfig` | object | Conditional | MCP handler config. Required (or a `selector`) for `mcp`. |
| `clientConfig` | object | No | Client (browser) handler config. Optional for `client`. |
| `timeout` | string | No | Per-handler timeout (e.g. `"30s"`). |

Per-type requirements:

- **`http`, `grpc`** — a complete `tool` block **plus** either the matching config block (`httpConfig`/`grpcConfig`) or a `selector`.
- **`openapi`, `mcp`** — either the matching config block (`openAPIConfig`/`mcpConfig`) or a `selector`. No `tool` block required.
- **`client`** — `clientConfig` is optional; no hard requirement.

The adapter validates only structure (name pattern, uniqueness, type enum, and the tool/config-or-selector presence rule per type). The omnia CRD/controller validates the inner config fields (endpoints, URLs, etc.) deeply.

```yaml
tools:
  - name: weather
    type: http
    tool:
      name: get_weather
      description: Get the current weather for a city.
      inputSchema:
        type: object
        properties:
          city: { type: string }
        required: [city]
    httpConfig:
      url: "https://api.example.com/weather"
    timeout: "10s"
  - name: docs-search
    type: mcp
    mcpConfig:
      server: "docs-mcp"
```

### `skills`

| | |
|---|---|
| **Type** | `array` of skill bindings |
| **Required** | No |

Skill bindings projected onto the PromptPack's `spec.skills[]`, preserving order. Each references an Omnia `SkillSource` CRD. At apply time the adapter runs a pre-flight against each referenced source and fails if it is missing or its `status.phase` is not `Ready`.

| Sub-field | Type | Required | Description |
|---|---|---|---|
| `source` | string | Yes | SkillSource CRD name. Pattern `^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`. |
| `include` | array of string | No | Skill names to mount from the source. All skills are mounted when omitted. |
| `mountAs` | string | No | Rename the mounted skill set. |

```yaml
skills:
  - source: company-skills
    include: [refund-policy, escalation]
    mountAs: support
```

### `skillsConfig`

| | |
|---|---|
| **Type** | `object` |
| **Required** | No |

Maps to the PromptPack's `spec.skillsConfig`: how active skills are selected for a turn.

| Sub-field | Type | Required | Description |
|---|---|---|---|
| `maxActive` | integer (>= 1) | No | Maximum concurrently-active skills. |
| `selector` | string | No | Activation strategy: `model-driven`, `tag`, or `embedding`. |

```yaml
skillsConfig:
  maxActive: 3
  selector: model-driven
```

### `runtime`

| | |
|---|---|
| **Type** | `object` |
| **Required** | No |

Optional resource sizing and autoscaling applied to all AgentRuntime CRDs.

#### `runtime.replicas`

| | |
|---|---|
| **Type** | `integer` |
| **Minimum** | `1` |
| **Default** | Platform default |

Number of runtime replicas.

#### `runtime.cpu`

| | |
|---|---|
| **Type** | `string` |
| **Example** | `"500m"`, `"1"` |
| **Default** | Platform default |

CPU request/limit in Kubernetes resource quantity format.

#### `runtime.memory`

| | |
|---|---|
| **Type** | `string` |
| **Example** | `"512Mi"`, `"1Gi"` |
| **Default** | Platform default |

Memory request/limit in Kubernetes resource quantity format.

#### `runtime.autoscaling`

| | |
|---|---|
| **Type** | `object` |
| **Required** | No |

Horizontal autoscaling for the AgentRuntime. Faithful passthrough to `spec.runtime.autoscaling` — only fields you set are emitted; omitted fields fall back to the CRD defaults at admission time. An omitted block means the platform default applies (currently static replicas).

| Sub-field | Type | Description |
|---|---|---|
| `enabled` | boolean | Turn autoscaling on. When enabled, the autoscaler manages the replica count. |
| `type` | string | Autoscaler backend: `hpa` or `keda`. `keda` enables scale-to-zero but requires KEDA installed in the cluster. |
| `min_replicas` | integer (>= 0) | Minimum replicas. |
| `max_replicas` | integer (>= 1) | Maximum replicas. Must not be below `min_replicas`. |
| `target_cpu_utilization` | integer (1-100) | Target average CPU utilization percentage. |
| `target_memory_utilization` | integer (1-100) | Target average memory utilization percentage. |
| `scale_down_stabilization_seconds` | integer (0-3600) | Stabilization window before scaling down. |

```yaml
runtime:
  autoscaling:
    enabled: true
    type: hpa
    min_replicas: 1
    max_replicas: 10
    target_cpu_utilization: 70
    scale_down_stabilization_seconds: 300
```

### `labels`

| | |
|---|---|
| **Type** | `object` (string values) |
| **Required** | No |
| **Example** | `{"team": "platform", "env": "prod"}` |

Extra labels applied to all created resources. Merged with the adapter's managed labels. Cannot override managed labels (`app.kubernetes.io/managed-by`, `promptkit.altairalabs.ai/*`).

### `dry_run`

| | |
|---|---|
| **Type** | `boolean` |
| **Required** | No |
| **Default** | `false` |

When `true`, the Apply operation simulates resource creation without making API calls. All resources are returned with `planned` status.

## Validation rules

The adapter validates the config before every operation. The following checks are enforced:

1. `api_endpoint` must be non-empty.
2. `workspace` must be non-empty.
3. `providers` must contain at least one binding; each binding's `ref` must be non-empty, each `role` (if set) must be a valid role, and binding names must be unique.
4. An API token must be available from either `api_token` or `OMNIA_API_TOKEN`.
5. Each `tools` handler must have a valid name (pattern + unique), a valid `type`, and satisfy the per-type tool/config-or-selector requirements.
6. Each `skills` binding's `source` must be non-empty and match the SkillSource name pattern; `skillsConfig.selector` (if set) must be a valid selector and `skillsConfig.maxActive` (if set) must be >= 1.
7. If `runtime` is specified, `runtime.replicas` must be >= 1, and any `runtime.autoscaling` values must be within the documented ranges (`type` is `hpa`/`keda`, utilization targets 1-100, `min_replicas` <= `max_replicas`).
8. No additional properties are allowed (enforced by the JSON Schema).

Validation errors are returned as a list of human-readable strings.
