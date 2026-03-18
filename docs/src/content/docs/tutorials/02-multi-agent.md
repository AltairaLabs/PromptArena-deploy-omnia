---
title: Multi-Agent Deployment
description: Deploy a multi-agent prompt pack with per-agent provider mappings
---

This tutorial covers deploying a prompt pack that contains multiple agents. Each agent gets its own AgentRuntime CRD while sharing the pack's ConfigMap, PromptPack, ToolRegistry, and AgentPolicy resources.

## How multi-agent packs work

When the adapter detects a multi-agent pack (via `adaptersdk.IsMultiAgent`), it:

1. Creates a single ConfigMap, PromptPack, ToolRegistry, and AgentPolicy (shared across all agents).
2. Creates one AgentRuntime per agent extracted from the pack.
3. Each AgentRuntime references the shared PromptPack and includes an `agentName` field identifying which member prompt it serves.

## Prerequisites

- A compiled multi-agent prompt pack
- Omnia cluster access and API token (same as the [First Deployment](/tutorials/01-first-deployment/) tutorial)

## Step 1: Configure agent-specific providers

Multi-agent packs often need different LLM providers for different agents. The `providers` map supports agent-specific overrides:

```yaml
deploy:
  provider: omnia
  config:
    api_endpoint: "https://omnia.example.com"
    workspace: "prod-workspace"
    providers:
      default: claude-sonnet-4-20250514
      router-agent: gpt4-prod
      specialist-agent: claude-sonnet-4-20250514
```

Provider resolution follows this order:
1. If an entry matches the agent name exactly, use that provider.
2. Otherwise, fall back to the `default` entry.

In the example above, `router-agent` uses `gpt4-prod`, `specialist-agent` uses `claude-sonnet-4-20250514`, and any other agents use the `default` provider `claude-sonnet-4-20250514`.

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

The shared resources (ConfigMap, PromptPack, ToolRegistry, AgentPolicy) are created first. Then each AgentRuntime is created, referencing the shared PromptPack and its agent-specific provider.

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
