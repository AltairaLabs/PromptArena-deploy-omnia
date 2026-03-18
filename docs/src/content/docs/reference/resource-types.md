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
| ToolRegistry | `tool_registry` | `toolregistries` | Yes -- only if pack defines tools |
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
| Create | `POST` | `/api/v1/workspaces/<ws>/configmaps` |
| Update | `PUT` | `/api/v1/workspaces/<ws>/configmaps/<name>` |
| Get | `GET` | `/api/v1/workspaces/<ws>/configmaps/<name>` |
| Delete | `DELETE` | `/api/v1/workspaces/<ws>/configmaps/<name>` |

---

## PromptPack

### What it represents

A custom resource that identifies the prompt pack, references its ConfigMap data, and records the provider mapping.

### Naming convention

`<pack-id>`

### When created

Always. Every deployment creates exactly one PromptPack.

### Payload structure

```json
{
  "kind": "PromptPack",
  "metadata": {
    "name": "<pack-id>",
    "labels": { ... }
  },
  "spec": {
    "packId": "<pack-id>",
    "version": "<pack-version>",
    "configMapRef": "<pack-id>-packdata",
    "providerRef": "<default-provider>",
    "description": "<pack description>"
  }
}
```

The `providerRef` is set from the `default` entry in the providers map. The `description` is included only if the pack has a non-empty description.

### API operations

| Operation | HTTP method | URL |
|---|---|---|
| Create | `POST` | `/api/v1/workspaces/<ws>/promptpacks` |
| Update | `PUT` | `/api/v1/workspaces/<ws>/promptpacks/<name>` |
| Get | `GET` | `/api/v1/workspaces/<ws>/promptpacks/<name>` |
| Delete | `DELETE` | `/api/v1/workspaces/<ws>/promptpacks/<name>` |

---

## ToolRegistry

### What it represents

A custom resource that lists the tools available to the agent runtime. Each tool entry includes its name, description, and JSON Schema input parameters.

### Naming convention

`<pack-id>-tools`

### When created

Conditional. Only created when the pack defines at least one tool (`len(pack.Tools) > 0`).

### Payload structure

```json
{
  "kind": "ToolRegistry",
  "metadata": {
    "name": "<pack-id>-tools",
    "labels": { ... }
  },
  "spec": {
    "tools": [
      {
        "name": "tool-name",
        "description": "Tool description",
        "inputSchema": { ... }
      }
    ]
  }
}
```

Tools are sorted alphabetically by name for deterministic output.

### API operations

| Operation | HTTP method | URL |
|---|---|---|
| Create | `POST` | `/api/v1/workspaces/<ws>/toolregistries` |
| Update | `PUT` | `/api/v1/workspaces/<ws>/toolregistries/<name>` |
| Get | `GET` | `/api/v1/workspaces/<ws>/toolregistries/<name>` |
| Delete | `DELETE` | `/api/v1/workspaces/<ws>/toolregistries/<name>` |

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
| Create | `POST` | `/api/v1/workspaces/<ws>/agentpolicies` |
| Update | `PUT` | `/api/v1/workspaces/<ws>/agentpolicies/<name>` |
| Get | `GET` | `/api/v1/workspaces/<ws>/agentpolicies/<name>` |
| Delete | `DELETE` | `/api/v1/workspaces/<ws>/agentpolicies/<name>` |

---

## AgentRuntime

### What it represents

The running agent instance. References the PromptPack and optionally the ToolRegistry and AgentPolicy. Supports resource sizing via the `runtime` config.

### Naming convention

- **Single-agent packs**: `<pack-id>`
- **Multi-agent packs**: the agent's name as extracted from the pack (one AgentRuntime per agent)

### When created

Always. Single-agent packs produce one AgentRuntime. Multi-agent packs produce one per extracted agent.

### Payload structure

```json
{
  "kind": "AgentRuntime",
  "metadata": {
    "name": "<agent-name>",
    "labels": { ... }
  },
  "spec": {
    "promptPackRef": "<pack-id>",
    "agentName": "<agent-name>",
    "providerRef": "<resolved-provider>",
    "replicas": 2,
    "resources": {
      "cpu": "500m",
      "memory": "512Mi"
    },
    "toolRegistryRef": "<pack-id>-tools",
    "agentPolicyRef": "<pack-id>-policy"
  }
}
```

Notes:
- `agentName` is only set for multi-agent packs.
- `providerRef` is resolved per-agent: first by exact agent name match in the providers map, then by the `default` entry.
- `replicas` and `resources` are only set when the `runtime` config is provided.
- `toolRegistryRef` is only set when the pack defines tools.
- `agentPolicyRef` is only set when the pack defines a tool policy.

### API operations

| Operation | HTTP method | URL |
|---|---|---|
| Create | `POST` | `/api/v1/workspaces/<ws>/agentruntimes` |
| Update | `PUT` | `/api/v1/workspaces/<ws>/agentruntimes/<name>` |
| Get | `GET` | `/api/v1/workspaces/<ws>/agentruntimes/<name>` |
| Delete | `DELETE` | `/api/v1/workspaces/<ws>/agentruntimes/<name>` |

## Name sanitization

All resource names are sanitized to valid Kubernetes DNS subdomain names:

1. Lowercased
2. Underscores and spaces replaced with hyphens
3. Characters outside `[a-z0-9-.]` stripped
4. Repeated hyphens collapsed
5. Leading/trailing hyphens and dots trimmed
6. Truncated to 253 characters (the Kubernetes maximum)
