---
title: First Omnia Deployment
description: Deploy a single-agent prompt pack to the Omnia Kubernetes platform
---

This tutorial walks through deploying a single-agent prompt pack to an Omnia cluster. By the end you will have a running AgentRuntime backed by your compiled pack.

## Prerequisites

- Access to an Omnia cluster with a configured workspace
- An API token with permission to create resources in the workspace
- The PromptArena CLI installed (`promptarena` in your PATH)
- A compiled prompt pack (the output of `promptarena build` or `packc`)

## Step 1: Configure the deploy provider

Create or update your `arena.yaml` to include the Omnia deploy configuration:

```yaml
deploy:
  provider: omnia
  config:
    api_endpoint: "https://omnia.example.com"
    workspace: "dev-workspace"
    providers:
      default: claude-sonnet-4-20250514
    runtime:
      replicas: 1
      cpu: "500m"
      memory: "512Mi"
    labels:
      team: platform
      environment: staging
```

### Required fields

| Field | Description |
|---|---|
| `api_endpoint` | Base URL of the Omnia Management API |
| `workspace` | Target workspace name (must match the pattern `^[a-z0-9][a-z0-9-]*[a-z0-9]$`) |
| `providers` | Map of Arena provider names to Omnia Provider CRD names. At minimum, include a `default` entry. |

### Authentication

Set the API token via environment variable:

```bash
export OMNIA_API_TOKEN="your-token-here"
```

You can also set it directly in the config as `api_token`, but the environment variable is recommended to avoid committing secrets.

## Step 2: Plan the deployment

Run the plan command to preview what resources will be created:

```bash
promptarena deploy plan
```

Expected output for a single-agent pack with tools:

```
Plan: 4 to create, 0 to update, 0 to delete
  + configmap    my-pack-packdata     Create ConfigMap with pack data for my-pack
  + prompt_pack  my-pack              Create PromptPack for my-pack
  + tool_registry my-pack-tools       Create ToolRegistry with 3 tools
  + agent_runtime my-pack             Create AgentRuntime for my-pack
```

Review the output to confirm the resources match your expectations.

## Step 3: Apply the deployment

Apply the plan to create the resources:

```bash
promptarena deploy apply
```

The adapter creates resources in dependency order:

1. **ConfigMap** -- stores the raw pack JSON
2. **PromptPack** -- references the ConfigMap and sets the provider
3. **ToolRegistry** -- registers pack tool definitions (if the pack defines tools)
4. **AgentPolicy** -- enforces tool blocklists (if the pack defines a tool policy)
5. **AgentRuntime** -- the running agent, referencing all of the above

Progress events are streamed as each resource is created.

## Step 4: Check status

Verify that all resources are healthy:

```bash
promptarena deploy status
```

A healthy deployment shows `deployed` as the aggregate status with each resource reporting `healthy`:

```
Status: deployed
  configmap     my-pack-packdata    healthy
  prompt_pack   my-pack             healthy
  tool_registry my-pack-tools       healthy
  agent_runtime my-pack             healthy
```

If any resource reports `missing` or `unhealthy`, check the Omnia dashboard for details.

## Step 5: Destroy (optional)

To tear down the deployment and remove all created resources:

```bash
promptarena deploy destroy
```

Resources are deleted in reverse dependency order (AgentRuntime first, ConfigMap last) to avoid orphaned references.

## Next steps

- [Multi-Agent Deployment](/tutorials/02-multi-agent/) -- deploy a pack with multiple agents.
- [Use Dry-Run Mode](/how-to/dry-run/) -- preview deployments without making API calls.
- [Resource Labels](/how-to/labels/) -- customize labels on deployed resources.
