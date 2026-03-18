---
title: Resource Labels
description: Managed labels and custom labels on deployed resources
---

Every resource created by the Omnia adapter carries a set of Kubernetes labels. These labels enable resource discovery, ownership tracking, and integration with Kubernetes tooling.

## Managed labels

The adapter automatically applies four labels to every resource. These cannot be overridden by user configuration:

| Label key | Value | Purpose |
|---|---|---|
| `app.kubernetes.io/managed-by` | `promptarena` | Identifies the tool that created the resource |
| `promptkit.altairalabs.ai/pack-id` | Pack ID from the compiled pack | Links the resource to its source pack |
| `promptkit.altairalabs.ai/pack-version` | Pack version string | Tracks which version of the pack created the resource |
| `promptkit.altairalabs.ai/resource-type` | One of `configmap`, `prompt_pack`, `agent_runtime`, `tool_registry`, `agent_policy` | Classifies the resource within the adapter's resource model |

These labels are always set last when merging with user labels, so they always win.

## Custom labels

Add your own labels via the `labels` configuration field:

```yaml
deploy:
  provider: omnia
  config:
    api_endpoint: "https://omnia.example.com"
    workspace: "my-workspace"
    providers:
      default: claude-prod
    labels:
      team: platform
      environment: production
      cost-center: eng-42
```

Custom labels are applied to **all** resources created by the adapter (ConfigMap, PromptPack, ToolRegistry, AgentPolicy, and AgentRuntime).

## Label merge behavior

When both custom and managed labels are present:

1. Custom labels are applied first.
2. Managed labels are applied second, overwriting any custom labels that use the same keys.

This means you cannot set `app.kubernetes.io/managed-by` or any `promptkit.altairalabs.ai/*` label to a custom value -- the adapter will always overwrite them.

## Using labels for resource discovery

You can use the managed labels to find all resources belonging to a specific pack:

```bash
# Find all resources for a pack
kubectl get all -l promptkit.altairalabs.ai/pack-id=my-pack

# Find all resources managed by PromptArena
kubectl get all -l app.kubernetes.io/managed-by=promptarena

# Find all AgentRuntime resources
kubectl get all -l promptkit.altairalabs.ai/resource-type=agent_runtime
```

The adapter's Status operation uses the same label-based queries to check resource health.
