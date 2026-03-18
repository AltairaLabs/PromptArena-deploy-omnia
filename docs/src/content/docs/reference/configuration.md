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
}
```

## Field details

### `api_endpoint`

| | |
|---|---|
| **Type** | `string` (URI) |
| **Required** | Yes |
| **Example** | `"https://omnia.example.com"` |

Base URL of the Omnia Management API. The adapter constructs the full workspace URL as `<api_endpoint>/api/v1/workspaces/<workspace>`. Trailing slashes are stripped automatically.

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
| **Type** | `object` (string values) |
| **Required** | Yes |
| **Example** | `{"default": "claude-prod", "router": "gpt4-prod"}` |

Maps Arena provider names to Omnia Provider CRD names. The `default` key is used as a fallback when no agent-specific mapping exists.

For multi-agent packs, add entries keyed by agent name to assign different providers to different agents. Resolution order: exact agent name match, then `default`.

### `runtime`

| | |
|---|---|
| **Type** | `object` |
| **Required** | No |

Optional resource sizing applied to all AgentRuntime CRDs.

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
3. `providers` must contain at least one entry.
4. An API token must be available from either `api_token` or `OMNIA_API_TOKEN`.
5. If `runtime` is specified, `runtime.replicas` must be >= 1.
6. No additional properties are allowed (enforced by the JSON Schema).

Validation errors are returned as a list of human-readable strings.
