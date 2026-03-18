# promptarena-deploy-omnia

<!-- Badges -->
<!-- ![CI](https://github.com/AltairaLabs/promptarena-deploy-omnia/actions/workflows/ci.yml/badge.svg) -->
<!-- ![Go Version](https://img.shields.io/github/go-mod/go-version/AltairaLabs/promptarena-deploy-omnia) -->
<!-- ![License](https://img.shields.io/github/license/AltairaLabs/promptarena-deploy-omnia) -->

An Omnia Kubernetes deploy adapter for [PromptKit](https://github.com/AltairaLabs/PromptKit). This adapter translates PromptKit pack definitions into Omnia CRD resources (PromptPack, AgentRuntime, ToolRegistry, AgentPolicy) and manages their lifecycle via the Omnia Management API. It runs as a JSON-RPC 2.0 subprocess that PromptKit discovers and invokes automatically.

## Quick Start

### Install

```bash
go install github.com/AltairaLabs/promptarena-deploy-omnia@latest
```

Or build from source:

```bash
make build
```

### Configure

Add a `deploy` section to your `arena.yaml`:

```yaml
deploy:
  adapter: omnia
  config:
    api_endpoint: https://omnia.example.com
    workspace: my-workspace
    api_token: ${OMNIA_API_TOKEN}
    providers:
      default: my-omnia-provider
    runtime:
      replicas: 2
      cpu: "500m"
      memory: "512Mi"
    labels:
      team: platform
```

Alternatively, set the API token via environment variable:

```bash
export OMNIA_API_TOKEN=your-token-here
```

### Deploy

```bash
# Plan (preview changes)
promptarena deploy plan

# Apply (create/update resources)
promptarena deploy apply

# Check status
promptarena deploy status

# Tear down
promptarena deploy destroy
```

## Configuration Reference

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `api_endpoint` | `string` (URI) | Yes | Omnia Management API base URL |
| `workspace` | `string` | Yes | Omnia workspace name (lowercase alphanumeric + hyphens) |
| `api_token` | `string` | No | API bearer token (or set `OMNIA_API_TOKEN` env var) |
| `providers` | `map[string]string` | Yes | Arena provider name to Omnia Provider CRD name mapping |
| `runtime.replicas` | `integer` | No | Number of agent runtime replicas (default: 1) |
| `runtime.cpu` | `string` | No | CPU resource request (K8s quantity, e.g., `"500m"`) |
| `runtime.memory` | `string` | No | Memory resource request (K8s quantity, e.g., `"512Mi"`) |
| `labels` | `map[string]string` | No | Extra labels applied to all created resources |
| `dry_run` | `boolean` | No | Simulate apply without making API calls |

## Resource Mapping

| Pack Concept | Omnia Resource | Adapter Type |
|-------------|----------------|--------------|
| Pack JSON data | ConfigMap | `configmap` |
| Pack definition | PromptPack CRD | `prompt_pack` |
| Pack tools | ToolRegistry CRD | `tool_registry` |
| Tool policy / blocklist | AgentPolicy CRD | `agent_policy` |
| Agent (single or multi) | AgentRuntime CRD | `agent_runtime` |

## Architecture

The adapter implements PromptKit's `deploy.Provider` interface and communicates via JSON-RPC 2.0 over stdio. PromptKit launches the adapter as a subprocess and exchanges structured requests/responses for plan, apply, destroy, and status operations.

```
PromptKit CLI
    │
    ├── JSON-RPC 2.0 (stdio) ──► promptarena-deploy-omnia
    │                                  │
    │                                  ├── HTTP REST ──► Omnia Management API
    │                                  │                     │
    │                                  │                     └── K8s CRDs
    │                                  │
    │                                  └── adaptersdk (protocol handling)
    │
    └── arena.yaml (config)
```

### Apply Phase Order

Resources are created in strict dependency order so that each resource can reference its prerequisites:

| Phase | Resource | Condition |
|-------|----------|-----------|
| 0 | ConfigMap (pack data) | Always |
| 1 | PromptPack | Always |
| 2 | ToolRegistry | Only if pack defines tools |
| 3 | AgentPolicy | Only if pack defines a tool blocklist |
| 4 | AgentRuntime(s) | Always (one per agent in multi-agent packs) |

Destroy runs in reverse order: AgentRuntime → AgentPolicy → ToolRegistry → PromptPack → ConfigMap.

### Kubernetes Labels

Every resource is tagged with managed labels for identification and lifecycle tracking:

| Label | Value |
|-------|-------|
| `app.kubernetes.io/managed-by` | `promptarena` |
| `promptkit.altairalabs.ai/pack-id` | Pack ID |
| `promptkit.altairalabs.ai/pack-version` | Pack version |
| `promptkit.altairalabs.ai/resource-type` | Resource type constant |

User-supplied labels from the `labels` config field are merged, but managed labels always take precedence.

## Development

### Prerequisites

- Go 1.25+
- [golangci-lint](https://golangci-lint.run/usage/install/)
- [goimports](https://pkg.go.dev/golang.org/x/tools/cmd/goimports) (`go install golang.org/x/tools/cmd/goimports@latest`)
- Sibling [PromptKit](https://github.com/AltairaLabs/PromptKit) checkout at `../promptkit`

### Build

```bash
make build
```

### Test

```bash
make test
```

### Lint

```bash
make lint
```

### Full Check (format + lint + test + build)

```bash
make check
```

## License

MIT

## Links

- [PromptKit](https://github.com/AltairaLabs/PromptKit)
- [PromptKit Documentation](https://promptkit.altairalabs.ai)
