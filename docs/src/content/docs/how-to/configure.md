---
title: Configure the Adapter
description: All configuration options for the Omnia deploy adapter
---

The Omnia adapter is configured through the `deploy.config` section of your arena configuration file. This guide covers every available option.

## Minimal configuration

```yaml
deploy:
  provider: omnia
  config:
    api_endpoint: "https://omnia.example.com"
    workspace: "my-workspace"
    providers:
      default: claude-prod
```

## Full configuration

```yaml
deploy:
  provider: omnia
  config:
    api_endpoint: "https://omnia.example.com"
    workspace: "my-workspace"
    api_token: "optional-inline-token"
    providers:
      default: claude-sonnet-4-20250514
      router-agent: gpt4-prod
    runtime:
      replicas: 2
      cpu: "1000m"
      memory: "1Gi"
    labels:
      team: platform
      environment: production
    dry_run: false
```

## Field reference

### `api_endpoint` (required)

The base URL of the Omnia Management API. The adapter appends `/api/v1/workspaces/<workspace>/` to form the full API path.

```yaml
api_endpoint: "https://omnia.example.com"
```

Must be a valid URI. Trailing slashes are stripped automatically.

### `workspace` (required)

The Omnia workspace to deploy into. Must be a valid Kubernetes DNS subdomain: lowercase alphanumeric characters and hyphens, starting and ending with an alphanumeric character.

```yaml
workspace: "prod-workspace"
```

Pattern: `^[a-z0-9][a-z0-9-]*[a-z0-9]$`

### `api_token` (optional)

Bearer token for authenticating with the Omnia Management API. If omitted, the adapter reads the `OMNIA_API_TOKEN` environment variable.

```yaml
api_token: "your-token"
```

The environment variable approach is strongly recommended for CI/CD pipelines and shared configurations to avoid committing secrets:

```bash
export OMNIA_API_TOKEN="your-token"
```

One of `api_token` or `OMNIA_API_TOKEN` must be set. Validation fails if neither is provided.

### `providers` (required)

A map of Arena provider names to Omnia Provider CRD names. At minimum, include a `default` entry. For multi-agent packs, you can add agent-specific overrides keyed by agent name.

```yaml
providers:
  default: claude-sonnet-4-20250514
  router-agent: gpt4-prod
  specialist-agent: claude-sonnet-4-20250514
```

Resolution order:
1. Exact match on the agent name.
2. Fall back to the `default` entry.

### `runtime` (optional)

Resource sizing for AgentRuntime CRDs.

```yaml
runtime:
  replicas: 2
  cpu: "500m"
  memory: "512Mi"
```

| Sub-field | Type | Description |
|---|---|---|
| `replicas` | integer | Number of runtime replicas. Must be >= 1. |
| `cpu` | string | CPU request/limit in Kubernetes resource format (e.g., `"500m"`, `"1"`). |
| `memory` | string | Memory request/limit in Kubernetes resource format (e.g., `"512Mi"`, `"1Gi"`). |

If `runtime` is omitted, the Omnia platform applies its own defaults.

### `labels` (optional)

Extra labels to apply to all created resources. These are merged with the adapter's managed labels.

```yaml
labels:
  team: platform
  environment: production
  cost-center: eng-42
```

User-supplied labels cannot override the adapter's managed labels. See [Resource Labels](/how-to/labels/) for details.

### `dry_run` (optional)

When set to `true`, the Apply operation generates a deployment preview without making any API calls. Resources are planned but not created.

```yaml
dry_run: true
```

Default: `false`. See [Use Dry-Run Mode](/how-to/dry-run/) for details.

## Validation

The adapter validates the configuration before any operation. Validation checks:

- `api_endpoint` is non-empty
- `workspace` is non-empty
- `providers` contains at least one entry
- An API token is available (from config or environment)
- `runtime.replicas` is >= 1 (if runtime is specified)

Validation errors are returned as a list of human-readable messages.
