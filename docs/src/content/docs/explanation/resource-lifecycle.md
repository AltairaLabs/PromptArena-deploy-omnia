---
title: Resource Lifecycle
description: Apply ordering, update diffing, and destroy teardown
---

The Omnia adapter manages five Kubernetes resource types with strict dependency ordering. This page explains how resources are created, updated, and destroyed.

## Apply order (5 phases)

Resources are created in dependency order so that each resource can reference its prerequisites:

| Phase | Resource type | Depends on |
|---|---|---|
| 0 | ConfigMap | -- |
| 1 | PromptPack | ConfigMap |
| 2 | ToolRegistry | -- (conditional: only if the deploy-config `tools` block is non-empty) |
| 3 | AgentPolicy | -- (conditional: only if pack defines a tool blocklist) |
| 4 | AgentRuntime | PromptPack, ToolRegistry (optional), AgentPolicy (optional) |

The AgentRuntime is always created last because it references the PromptPack, and optionally references the ToolRegistry and AgentPolicy.

### Phase details

**Phase 0 -- ConfigMap**: Stores the raw pack JSON in a ConfigMap under the key `pack.json`. Named `<pack-id>-packdata`.

**Phase 1 -- PromptPack**: Creates the PromptPack CRD that records the pack version and any skill bindings, and sends the raw pack JSON as `content` (the dashboard folds it into a managed ConfigMap). Named `<pack-id>`.

**Phase 2 -- ToolRegistry** (conditional): If the deploy-config `tools` block is non-empty, creates a ToolRegistry CRD whose `spec.handlers[]` is a faithful passthrough of those handlers. Named `<pack-id>-tools`. Skipped when `tools` is empty.

**Phase 3 -- AgentPolicy** (conditional): If any prompt in the pack defines a tool blocklist, creates an AgentPolicy CRD with the deduplicated, sorted blocklist. Named `<pack-id>-policy`. Skipped if no prompts define a tool policy.

**Phase 4 -- AgentRuntime(s)**: Creates one AgentRuntime per agent. For single-agent packs, the runtime is named after the pack ID. For multi-agent packs, one runtime is created per extracted agent. Each runtime references the PromptPack and optionally the ToolRegistry and AgentPolicy.

### Progress reporting

Each phase reports progress as a fraction of the total. With 5 phases, each phase occupies 20% of the progress bar. For multi-agent packs with multiple AgentRuntime resources, the Phase 4 progress is subdivided across the agent count.

## Create vs update diffing

When prior state exists from a previous deployment, the adapter compares the desired resource set against the prior state:

- **Resource exists in desired and prior**: action is `update` (HTTP PUT instead of POST).
- **Resource exists in desired but not prior**: action is `create` (HTTP POST).
- **Resource exists in prior but not desired**: action is `delete` (HTTP DELETE).

This diffing is key-based, where the key is `<resource-type>/<resource-name>`. The adapter does not compare resource payloads -- if a resource exists in both sets, it is always updated.

### Example: adding a tool policy

If a pack initially had no tool blocklist but a new version adds one:

```
Prior state:  configmap/my-pack-packdata, prompt_pack/my-pack, agent_runtime/my-pack
Desired state: configmap/my-pack-packdata, prompt_pack/my-pack, agent_policy/my-pack-policy, agent_runtime/my-pack

Plan:
  ~ configmap     my-pack-packdata   Update
  ~ prompt_pack   my-pack            Update
  + agent_policy  my-pack-policy     Create
  ~ agent_runtime my-pack            Update
```

### Example: removing an agent

If a multi-agent pack drops an agent in a new version:

```
Prior state:  ..., agent_runtime/agent-a, agent_runtime/agent-b
Desired state: ..., agent_runtime/agent-a

Plan:
  ...
  ~ agent_runtime agent-a   Update
  - agent_runtime agent-b   Delete
```

## Destroy order

Destroy tears down resources in **reverse dependency order** to avoid orphaned references:

| Step | Resource type |
|---|---|
| 1 | AgentRuntime |
| 2 | AgentPolicy |
| 3 | ToolRegistry |
| 4 | PromptPack |
| 5 | ConfigMap |

Within each step, all resources of that type are deleted before moving to the next type.

### Unordered resources

If the adapter state contains resource types not in the standard destroy order (which could happen with future resource types), those are cleaned up after the ordered phases complete.

### Error handling during destroy

Destroy is best-effort. If deleting a resource fails, the adapter logs the error and continues with the remaining resources. This ensures that a single API failure does not prevent cleanup of other resources.

## State management

The adapter tracks deployed resources in an opaque state string (JSON-serialized `AdapterState`). This state is passed between Plan, Apply, Status, and Destroy operations.

Each resource in the state records:

| Field | Description |
|---|---|
| `type` | Resource type constant (e.g., `configmap`, `agent_runtime`) |
| `name` | Kubernetes resource name |
| `uid` | Kubernetes UID returned by the API |
| `resource_version` | Kubernetes resource version for optimistic concurrency |
| `status` | Lifecycle status: `created`, `updated`, `failed`, or `planned` |

The state also records the pack ID and version at the top level for quick reference.
