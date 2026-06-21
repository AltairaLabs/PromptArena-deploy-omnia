---
title: Multi-Agent Deployment
description: Deploy a multi-agent prompt pack with shared provider bindings
---

This tutorial covers deploying a prompt pack that contains multiple agents. Each agent gets its own AgentRuntime CRD while sharing the pack's ConfigMap, PromptPack, ToolRegistry, and AgentPolicy resources.

## How multi-agent packs work

When the adapter detects a multi-agent pack (via `adaptersdk.IsMultiAgent`), it:

1. Creates a single ConfigMap, PromptPack, ToolRegistry, and AgentPolicy (shared across all agents).
2. Creates one AgentRuntime per agent extracted from the pack.
3. Each AgentRuntime references the shared PromptPack and receives the **same** set of provider bindings.

## Prerequisites

- A compiled multi-agent prompt pack
- Omnia cluster access and API token (same as the [First Deployment](/tutorials/01-first-deployment/) tutorial)

## Step 1: Configure providers

Every binding in `providers` is emitted to **each** AgentRuntime — bindings are shared across all agents, not assigned per agent. The binding named `default` is each runtime's primary provider; the others are available by role.

Use the list form to give each runtime an LLM plus, say, an embedding provider:

```yaml
deploy:
  provider: omnia
  config:
    api_endpoint: "https://omnia.example.com"
    workspace: "prod-workspace"
    providers:
      - name: default
        ref: claude-sonnet-4-20250514
        role: llm
      - name: embedder
        ref: text-embedding-3-large
        role: embedding
```

The legacy map form is also accepted (each entry binds with role `llm`):

```yaml
    providers:
      default: claude-sonnet-4-20250514
      router: gpt4-prod
```

There is no per-agent provider override keyed by agent name. All bindings go to every runtime; `default` is the primary. Selecting a non-primary provider for a given agent is driven by role within the pack, not by the deploy config.

## Step 2: Plan and review

```bash
promptarena deploy plan
```

Expected output for a two-agent pack:

```
Plan: 5 to create, 0 to update, 0 to delete
  + configmap      support-pack-packdata   Create ConfigMap with pack data for support-pack
  + prompt_pack    support-pack            Create PromptPack for support-pack
  + tool_registry  support-pack-tools      Create ToolRegistry with 5 tools
  + agent_runtime  router-agent            Create AgentRuntime for router-agent
  + agent_runtime  specialist-agent        Create AgentRuntime for specialist-agent
```

Note the two separate `agent_runtime` entries, one per agent.

## Step 3: Apply

```bash
promptarena deploy apply
```

The shared resources (ConfigMap, PromptPack, ToolRegistry, AgentPolicy) are created first. Then each AgentRuntime is created, referencing the shared PromptPack and the shared provider bindings.

## Step 4: Verify

```bash
promptarena deploy status
```

Each AgentRuntime is checked independently. The aggregate status is `deployed` only when all resources are healthy. If any single agent is unhealthy, the aggregate status is `degraded`.

## Updating a multi-agent deployment

When you re-deploy an updated pack, the adapter diffs the desired resources against the prior state:

- **Existing resources** are updated (PUT instead of POST).
- **New agents** added to the pack result in new AgentRuntime create actions.
- **Removed agents** result in delete actions for their AgentRuntime resources.

This diffing ensures that only the necessary changes are applied.

## Next steps

- [Configure the Adapter](/how-to/configure/) -- all configuration options in detail.
- [Resource Lifecycle](/explanation/resource-lifecycle/) -- understand the 5-phase apply order.
- [Resource Types Reference](/reference/resource-types/) -- details on each managed resource type.
