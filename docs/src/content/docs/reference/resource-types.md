---
title: Resource Types
description: The five managed Kubernetes resource types created by the Omnia adapter
---

The Omnia adapter manages five Kubernetes resource types. This page documents each type, its naming convention, when it is created, and the API operations used.

## Overview

| Resource type | Adapter constant | API path segment | Conditional |
|---|---|---|---|
| ConfigMap | `configmap` | `configmaps` | No -- always created |
| PromptPack | `prompt_pack` | `promptpacks` | No -- always created |
| ToolRegistry | `tool_registry` | `toolregistries` | Yes -- only if the deploy-config `tools` block is non-empty |
| AgentPolicy | `agent_policy` | `agentpolicies` | Yes -- only if pack defines a tool blocklist |
| AgentRuntime | `agent_runtime` | `agentruntimes` | No -- always created (one per agent) |

## ConfigMap

### What it represents

A Kubernetes ConfigMap that stores the raw compiled pack JSON. This is the data source that the PromptPack CRD references.

### Naming convention

`<pack-id>-packdata`

All names are sanitized to valid Kubernetes DNS subdomain names: lowercased, underscores and spaces replaced with hyphens, invalid characters stripped, repeated hyphens collapsed, and truncated to 253 characters.

### When created

Always. Every deployment creates exactly one ConfigMap.

### Payload structure

```json
{
  "kind": "ConfigMap",
  "metadata": {
    "name": "<pack-id>-packdata",
    "labels": { ... }
  },
  "data": {
    "pack.json": "<raw pack JSON>"
  }
}
```

### API operations

| Operation | HTTP method | URL |
|---|---|---|
| Create | `POST` | `/api/workspaces/<ws>/configmaps` |
| Update | `PUT` | `/api/workspaces/<ws>/configmaps/<name>` |
| Get | `GET` | `/api/workspaces/<ws>/configmaps/<name>` |
| Delete | `DELETE` | `/api/workspaces/<ws>/configmaps/<name>` |

---

## PromptPack

### What it represents

A custom resource that identifies the prompt pack and records its version and skill bindings. The raw pack JSON is sent in the request `content`; the Omnia dashboard's promptpacks route folds that into a managed ConfigMap and sets `spec.source` itself.

### Naming convention

`<pack-id>`

### When created

Always. Every deployment creates exactly one PromptPack.

### Payload structure

```json
{
  "metadata": {
    "name": "<pack-id>",
    "labels": { ... }
  },
  "spec": {
    "version": "<pack-version>",
    "skills": [
      {
        "source": "company-skills",
        "include": ["refund-policy", "escalation"],
        "mountAs": "support"
      }
    ],
    "skillsConfig": {
      "maxActive": 3,
      "selector": "model-driven"
    }
  },
  "content": {
    "pack.json": "<raw pack JSON>"
  }
}
```

`spec.skills` is emitted only when the deploy-config `skills` block is non-empty; each entry's `include`/`mountAs` appear only when set. `spec.skillsConfig` is emitted only when `skillsConfig` sets `maxActive` and/or `selector`. The provider bindings are recorded on the AgentRuntime, not the PromptPack.

### API operations

| Operation | HTTP method | URL |
|---|---|---|
| Create | `POST` | `/api/workspaces/<ws>/promptpacks` |
| Update | `PUT` | `/api/workspaces/<ws>/promptpacks/<name>` |
| Get | `GET` | `/api/workspaces/<ws>/promptpacks/<name>` |
| Delete | `DELETE` | `/api/workspaces/<ws>/promptpacks/<name>` |

---

## ToolRegistry

### What it represents

A custom resource that lists the tool handlers available to the agent runtime. Handlers are a faithful passthrough of the deploy-config `tools` block to `spec.handlers[]` — they are **not** derived from tools embedded in the pack (inline pack tool schemas reach the runtime via the PromptPack content fold instead).

### Naming convention

`<pack-id>-tools`

### When created

Conditional. Only created when the deploy-config `tools` block is non-empty (`len(cfg.Tools) > 0`).

### Payload structure

Each handler carries its `name` and `type` plus the type-specific blocks that were set. The shape varies by `type`:

```json
{
  "metadata": {
    "name": "<pack-id>-tools",
    "labels": { ... }
  },
  "spec": {
    "handlers": [
      {
        "name": "weather",
        "type": "http",
        "tool": {
          "name": "get_weather",
          "description": "Get the current weather for a city.",
          "inputSchema": { ... },
          "outputSchema": { ... }
        },
        "httpConfig": { ... },
        "timeout": "10s"
      },
      {
        "name": "docs-search",
        "type": "mcp",
        "mcpConfig": { ... }
      },
      {
        "name": "browser-action",
        "type": "client",
        "clientConfig": { ... }
      }
    ]
  }
}
```

Per-type shape:

- **`http` / `grpc`** — a `tool` block (`name`, `description`, `inputSchema`, optional `outputSchema`) plus `httpConfig`/`grpcConfig` or a `selector`.
- **`openapi`** — `openAPIConfig` or a `selector`; no `tool` block.
- **`mcp`** — `mcpConfig` or a `selector`; no `tool` block.
- **`client`** — optional `clientConfig`; no hard requirement.

Optional blocks (`tool`, `selector`, the `*Config` blocks, `timeout`) are emitted only when present. Handlers preserve the order of the deploy-config `tools` list.

### API operations

| Operation | HTTP method | URL |
|---|---|---|
| Create | `POST` | `/api/workspaces/<ws>/toolregistries` |
| Update | `PUT` | `/api/workspaces/<ws>/toolregistries/<name>` |
| Get | `GET` | `/api/workspaces/<ws>/toolregistries/<name>` |
| Delete | `DELETE` | `/api/workspaces/<ws>/toolregistries/<name>` |

---

## AgentPolicy

### What it represents

A custom resource that enforces tool usage policies. Currently supports tool blocklists -- tools that the agent is not allowed to call.

### Naming convention

`<pack-id>-policy`

### When created

Conditional. Only created when at least one prompt in the pack defines a tool policy with a non-empty blocklist.

### Payload structure

```json
{
  "kind": "AgentPolicy",
  "metadata": {
    "name": "<pack-id>-policy",
    "labels": { ... }
  },
  "spec": {
    "toolBlocklist": ["blocked-tool-a", "blocked-tool-b"]
  }
}
```

The blocklist is the deduplicated, sorted union of all blocklists across all prompts in the pack.

### API operations

| Operation | HTTP method | URL |
|---|---|---|
| Create | `POST` | `/api/workspaces/<ws>/agentpolicies` |
| Update | `PUT` | `/api/workspaces/<ws>/agentpolicies/<name>` |
| Get | `GET` | `/api/workspaces/<ws>/agentpolicies/<name>` |
| Delete | `DELETE` | `/api/workspaces/<ws>/agentpolicies/<name>` |

---

## AgentRuntime

### What it represents

The running agent instance. References the PromptPack, lists its provider bindings, and optionally references the ToolRegistry. Supports resource sizing and autoscaling via the `runtime` config.

### Naming convention

- **Single-agent packs**: `<pack-id>`
- **Multi-agent packs**: the agent's name as extracted from the pack (one AgentRuntime per agent)

### When created

Always. Single-agent packs produce one AgentRuntime. Multi-agent packs produce one per extracted agent.

### Payload structure

```json
{
  "metadata": {
    "name": "<agent-name>",
    "labels": { ... }
  },
  "spec": {
    "promptPackRef": { "name": "<pack-id>" },
    "facade": { "type": "websocket", "handler": "runtime" },
    "providers": [
      {
        "name": "default",
        "providerRef": { "name": "<provider-crd>" },
        "role": "llm"
      },
      {
        "name": "embedder",
        "providerRef": { "name": "<embedding-provider-crd>" },
        "role": "embedding"
      }
    ],
    "toolRegistryRef": { "name": "<pack-id>-tools" },
    "runtime": {
      "replicas": 2,
      "resources": { "requests": { "cpu": "500m", "memory": "512Mi" } },
      "autoscaling": {
        "enabled": true,
        "type": "hpa",
        "minReplicas": 1,
        "maxReplicas": 10,
        "targetCPUUtilizationPercentage": 70
      }
    }
  }
}
```

Notes:
- Every provider binding is emitted as a `NamedProviderRef` in `spec.providers`, preserving order. The binding named `default` is the runtime's primary; an empty `role` defaults to `llm`.
- `toolRegistryRef` is only set when the deploy-config `tools` block is non-empty.
- `runtime` is only set when the `runtime` config is provided; `replicas`/`resources`/`autoscaling` each appear only when their inputs are set. Autoscaling keys use camelCase CRD names (`minReplicas`, `targetCPUUtilizationPercentage`, etc.).
- The AgentRuntime does not carry an `agentPolicyRef`; the AgentPolicy CRD is created independently when the pack defines a tool blocklist.

### API operations

| Operation | HTTP method | URL |
|---|---|---|
| Create | `POST` | `/api/workspaces/<ws>/agentruntimes` |
| Update | `PUT` | `/api/workspaces/<ws>/agentruntimes/<name>` |
| Get | `GET` | `/api/workspaces/<ws>/agentruntimes/<name>` |
| Delete | `DELETE` | `/api/workspaces/<ws>/agentruntimes/<name>` |

## Name sanitization

All resource names are sanitized to valid Kubernetes DNS subdomain names:

1. Lowercased
2. Underscores and spaces replaced with hyphens
3. Characters outside `[a-z0-9-.]` stripped
4. Repeated hyphens collapsed
5. Leading/trailing hyphens and dots trimmed
6. Truncated to 253 characters (the Kubernetes maximum)
