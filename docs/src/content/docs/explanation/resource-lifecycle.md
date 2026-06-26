---
title: Resource Lifecycle
description: Ownership, apply ordering, adopt-on-apply diffing, and destroy teardown
---

The Omnia adapter manages a small set of Kubernetes resource types. This page explains who **owns** each one, how resources are created and updated against the live cluster, and what destroy leaves behind.

## Ownership

The single most important idea: the adapter **owns** the pack-scoped resources it deploys, and the platform **operator** owns the shared infrastructure those resources bind to.

| Owner | Resources | Created by adapter? | Updated on re-apply? | Deleted on destroy? |
|---|---|---|---|---|
| **Adapter** (pack-scoped) | PromptPack, AgentRuntime, AgentPolicy | Yes | Yes | Yes |
| **Operator** (shared) | ToolRegistry | Create-only, for convenience | No | No (left in place) |
| **Operator** (shared) | Providers, SkillSources | Never (bound, not created) | No | No (left in place) |

What this means in practice:

- The **adapter-owned** resources — the **PromptPack** (and the pack content the dashboard folds into a managed ConfigMap), the **AgentRuntime** that runs it, and the derived **AgentPolicy** — are fully reconciled: the adapter creates, updates, and destroys them.
- The **ToolRegistry** is created **only as a convenience** when one does not already exist. It is **create-only** (never updated on a later apply) and is **left in place on destroy**. Once it exists it belongs to the operator, who may have completed placeholder URLs or otherwise edited it.
- **Providers** and **skill registries** (`SkillSource` CRDs) are **never** created by the adapter. It binds to ones that already exist and fails the plan if a referenced one is missing.

The ideal flow: the operator already maintains providers, skill sources, and (optionally) tool registries; the adapter simply hooks the pack up to them.

## Apply order (4 phases)

Resources are created in dependency order so that each can reference its prerequisites:

| Phase | Resource type | Owner | Depends on |
|---|---|---|---|
| 0 | PromptPack | Adapter | -- |
| 1 | ToolRegistry | Operator | -- (create mode only; create-only) |
| 2 | AgentPolicy | Adapter | -- (conditional: only if the pack defines a tool blocklist) |
| 3 | AgentRuntime | Adapter | PromptPack, ToolRegistry (optional), AgentPolicy (optional) |

The AgentRuntime is always created last because it references the PromptPack, and optionally references the ToolRegistry and AgentPolicy.

### Phase details

**Phase 0 -- PromptPack**: Creates the PromptPack CRD that records the pack version and any skill bindings, and sends the raw pack JSON as `content` (the dashboard folds it into a managed ConfigMap and sets `spec.source` itself). Named `<pack-id>`.

**Phase 1 -- ToolRegistry** (create mode only): When the deploy-config `tools` block is present, the adapter synthesizes a `<pack-id>-tools` registry — but only if one does not already exist. See [Tool-registry resolution](#tool-registry-resolution-3-modes) and [Create-mode synthesis](#create-mode-synthesis). In **bind** and **discover** modes the adapter creates nothing here; it points the AgentRuntime at an existing registry instead.

**Phase 2 -- AgentPolicy** (conditional): If any prompt in the pack defines a tool blocklist, creates an AgentPolicy CRD with the deduplicated, sorted blocklist. Named `<pack-id>-policy`. Skipped if no prompts define a tool policy.

**Phase 3 -- AgentRuntime(s)**: Creates one AgentRuntime per agent. For single-agent packs, the runtime is named after the pack ID. For multi-agent packs, one runtime is created per extracted agent. Each runtime references the PromptPack and optionally the ToolRegistry and AgentPolicy.

### Progress reporting

Each phase reports progress as a fraction of the total. With 4 phases, each phase occupies a quarter of the progress bar. For multi-agent packs with multiple AgentRuntime resources, the Phase 3 progress is subdivided across the agent count.

## Tool-registry resolution (3 modes)

How the AgentRuntime is wired to a ToolRegistry is decided **once at plan time** and re-computed **identically at apply time**, so apply never surprises plan. The two config fields `tools` and `tool_registry_ref` are **mutually exclusive**. The mode is chosen as follows:

- **create** — the deploy-config `tools` block is present. The adapter synthesizes a `<pack-id>-tools` registry (create-only — see below).
- **bind** — `tool_registry_ref: <name>` is set. The adapter binds that existing registry. It warns for any pack tool the registry does not provide and for matched tools whose input schema drifts from what the pack was tested against. Verification is **skipped for dynamic registries** (OpenAPI/MCP) that resolve their tools externally — the adapter can't enumerate them statically, so it advises rather than asserts.
- **discover** — the pack declares tools but **neither** `tools` nor `tool_registry_ref` is set. The adapter lists the workspace registries and **auto-binds iff exactly one** registry covers **all** of the pack's tools. Otherwise it binds none and recommends a concrete fix (the closest registry, or adding a `tools:` block). **It never creates a registry in discover mode.**

All of this is **warn-don't-block**: a mismatch never fails the deploy (the AgentRuntime still deploys), only a transport/setup error does. Even a failure to *list* registries degrades to skipping verification rather than blocking.

### Create-mode synthesis

Create mode synthesizes **one handler per pack tool** (system-namespaced tools like `image__generate` are runtime-provided and excluded):

- A pack tool with a matching `deploy.config.tools` handler → **that handler** is authoritative and used as-is.
- An uncovered tool whose arena source declares it `mode: live` → a handler wired to the tool's **real URL and HTTP method, pulled from the arena source** (the adapter reads these from the arena config the CLI threads through). A `GET` tool stays `GET`.
- An uncovered `mode: mock` / no-URL tool → an `https://placeholder.invalid/<tool>` endpoint that never resolves, so a forgotten handler fails loudly. The operator completes the real URL in Omnia.

The plan summarizes the registry as `N handlers (C configured, S from source, P placeholder)`, with advisories naming the source-wired and placeholder tools.

**Create-only.** The synthesized registry is written **exactly once**, when it does not yet exist. If it already exists the adapter **leaves it untouched** — the operator owns it and may have completed placeholder URLs — and emits an advisory (`tool registry "<name>" already exists — left unchanged (operator-owned); delete it to re-seed`). The AgentRuntime still references it regardless.

## Create vs update: adopt-on-apply

The **cluster is the source of truth**. Both plan and apply begin by listing **this pack's** managed resources from the cluster — filtered by label (`promptkit.altairalabs.ai/pack-id` plus `app.kubernetes.io/managed-by`) and double-checked client-side — and reconcile the desired set against **that live state**. This adopted state supersedes the local state file passed in, so a lost, stale, or empty state file (or an out-of-band deploy) can't cause a blind create of a resource that already exists. Only if the listing fails does the adapter fall back to the passed-in local state.

The diff is key-based, where the key is `<resource-type>/<resource-name>`:

- **Resource in desired and live**: `update` (HTTP PUT). *Exception:* the create-only ToolRegistry emits **no change** when it already exists — apply skips it.
- **Resource in desired but not live**: `create` (HTTP POST).
- **Resource in live but not desired**: `delete` (HTTP DELETE).

The adapter does not compare resource payloads — if an adapter-owned resource exists in both sets, it is updated.

### Conflict handling

Because the cluster is reconciled live, two 409 cases are handled transparently:

- A **409 Conflict** on update (a controller mutated the resource between the server's read and write — a stale `resourceVersion`) is **retried**, re-issuing the update so the server reads the latest version.
- A **409 AlreadyExists** on create (the adopt-list raced the write, or adoption failed and apply fell back to a state file that didn't know the resource existed) is transparently **converted to an update** — except for the create-only ToolRegistry, where AlreadyExists is a **no-op**.

### Example: adding a tool policy

If a pack initially had no tool blocklist but a new version adds one (the registry already exists in create mode):

```
Live state:    prompt_pack/my-pack, tool_registry/my-pack-tools, agent_runtime/my-pack
Desired state: prompt_pack/my-pack, tool_registry/my-pack-tools, agent_policy/my-pack-policy, agent_runtime/my-pack

Plan:
  ~ prompt_pack    my-pack            Update
    tool_registry  my-pack-tools      (no change — create-only, operator-owned)
  + agent_policy   my-pack-policy      Create
  ~ agent_runtime  my-pack            Update
```

### Example: removing an agent

If a multi-agent pack drops an agent in a new version:

```
Live state:    ..., agent_runtime/agent-a, agent_runtime/agent-b
Desired state: ..., agent_runtime/agent-a

Plan:
  ...
  ~ agent_runtime agent-a   Update
  - agent_runtime agent-b   Delete
```

## Destroy

Destroy tears down only the **adapter-owned** pack-scoped resources, in **reverse dependency order**:

| Step | Resource type |
|---|---|
| 1 | AgentRuntime |
| 2 | AgentPolicy |
| 3 | PromptPack |

The PromptPack's managed content ConfigMap is cleaned up dashboard-side when the PromptPack is deleted, so the adapter does not track or delete it separately.

### Operator-owned resources are left in place

Destroy does **not** delete the ToolRegistry, providers, or skill sources. For each operator-owned resource recorded in state it emits an advisory:

```
Left tool_registry "my-pack-tools" in place (operator-owned)
```

This is deliberate: the adapter created the ToolRegistry only as a convenience, and the operator may have completed its placeholder handlers. Tearing it down would discard that work and could orphan other agents that bind to it.

### Unordered resources

If the adapter state contains resource types not in the standard destroy order (which could happen with future resource types), those are cleaned up after the ordered phases complete — unless they are marked operator-owned, in which case they are left in place.

### Error handling during destroy

Destroy is best-effort. If deleting a resource fails, the adapter records the error and continues with the remaining resources. This ensures that a single API failure does not prevent cleanup of other resources.

## State management

The adapter tracks deployed resources in an opaque state string (JSON-serialized `AdapterState`), passed between Plan, Apply, Status, and Destroy operations. Because plan and apply adopt the live cluster state, this string is a convenience cache, not the authority — the cluster is.

Each resource in the state records:

| Field | Description |
|---|---|
| `type` | Resource type constant (e.g., `prompt_pack`, `agent_runtime`) |
| `name` | Kubernetes resource name |
| `uid` | Kubernetes UID returned by the API |
| `resource_version` | Kubernetes resource version for optimistic concurrency |
| `status` | Lifecycle status: `created`, `updated`, `unchanged`, `failed`, or `planned` |

The state also records the pack ID and version at the top level for quick reference.
