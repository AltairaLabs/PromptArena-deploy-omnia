---
title: Resource Types
description: The managed Kubernetes resource types created by the Omnia adapter, and who owns each
---

The Omnia adapter works with four Kubernetes resource types. This page documents each type, **who owns it**, its naming convention, when it is created, and the API operations used.

## Overview

| Resource type | Adapter constant | API path segment | Owner | Created / Conditional |
|---|---|---|---|---|
| PromptPack | `prompt_pack` | `promptpacks` | **Adapter** | Always |
| ToolRegistry | `tool_registry` | `toolregistries` | **Operator** | Create-only convenience — synthesized only in create mode (deploy-config `tools` block present) when one doesn't already exist; never updated; left in place on destroy |
| AgentPolicy | `agent_policy` | `agentpolicies` | **Adapter** | Only if the pack defines a tool blocklist |
| AgentRuntime | `agent_runtime` | `agentruntimes` | **Adapter** | Always (one per agent) |

The **adapter-owned** resources are fully reconciled — created, updated, and destroyed. The **operator-owned** ToolRegistry is created only as a convenience and otherwise left alone. The providers and skill sources the AgentRuntime/PromptPack reference are also operator-owned, but the adapter never creates them (it binds to existing ones), so they are not in this table. See [Resource Lifecycle](/explanation/resource-lifecycle/#ownership) for the ownership model.

:::note
The raw pack JSON is no longer pushed as a standalone ConfigMap the adapter manages. The PromptPack carries the pack content in its request `content`, and the dashboard folds it into a **dashboard-managed** ConfigMap that is cleaned up when the PromptPack is deleted — the adapter does not track it.
:::

## PromptPack

**Adapter-owned.** Created, updated, and destroyed by the adapter.

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

:::caution[Operator-owned, create-only]
The adapter creates a ToolRegistry only as a **convenience** when one doesn't already exist. It is **operator-owned**: once it exists the adapter never updates it on a later apply, and **never deletes it on destroy**. The operator may have completed placeholder URLs or otherwise edited it.
:::

### What it represents

A custom resource that lists the tool handlers available to the agent runtime. It is populated only in **create mode** (the deploy-config `tools` block is present), by [create-mode synthesis](/explanation/resource-lifecycle/#create-mode-synthesis): one handler per pack tool, sourced from the configured `tools` handler, the tool's arena-source URL, or a placeholder. The handlers are **not** a copy of tool schemas embedded in the pack — inline pack tool schemas reach the runtime via the PromptPack content fold instead.

In **bind** mode (`tool_registry_ref`) and **discover** mode, the adapter binds an **existing** registry by name and creates nothing here.

### Naming convention

`<pack-id>-tools` (create mode). In bind/discover mode the AgentRuntime references whatever registry name was resolved.

### When created

Only in **create mode** (`len(cfg.Tools) > 0`) **and** only when a `<pack-id>-tools` registry does not already exist. An existing registry is left untouched. The registry is **never updated** on a re-apply and is **left in place on destroy**.

### Payload structure

Each handler carries its `name` and `type` plus the type-specific blocks that were set. Configured handlers preserve the order of the deploy-config `tools` list; synthesized handlers for uncovered pack tools follow. The shape varies by `type`:

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

**Adapter-owned.** Created, updated, and destroyed by the adapter (derived from the pack's tool blocklist).

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

**Adapter-owned.** Created, updated, and destroyed by the adapter. Its `providerRef`/`toolRegistryRef`/`skills` point at **operator-owned** resources (Providers, ToolRegistry, SkillSources) the adapter binds to but does not own.

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
    },
    "externalAuth": {
      "oidc": { "issuer": "https://auth.example.com/", "audience": "omnia-agents" }
    },
    "memory": {
      "enabled": true,
      "retrieval": { "strategy": "semantic", "limit": 10 }
    },
    "evals": {
      "enabled": true,
      "inline": { "groups": ["fast-running"] },
      "worker": { "groups": ["long-running", "external"] }
    }
  }
}
```

Notes:
- Every provider binding is emitted as a `NamedProviderRef` in `spec.providers`, preserving order. The binding named `default` is the runtime's primary; an empty `role` defaults to `llm`.
- `toolRegistryRef` is set whenever a registry was resolved — in **create** mode (`<pack-id>-tools`), **bind** mode (the `tool_registry_ref` name), or **discover** mode (an auto-bound registry). It is omitted only when no registry is resolved (the pack declares no tools, or discovery found no single covering registry).
- `runtime` is only set when the `runtime` config is provided; `replicas`/`resources`/`autoscaling` each appear only when their inputs are set. Autoscaling keys use camelCase CRD names (`minReplicas`, `targetCPUUtilizationPercentage`, etc.).
- `externalAuth`, `memory`, and `evals` are each emitted only when their deploy-config block is set, and carry only the sub-fields you provide (faithful passthrough). See the [configuration reference](./configuration/) for the full shape of each.
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
