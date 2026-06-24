---
title: Configuration Mapping
description: How the pack and the deploy config become Omnia resources
---

The Omnia adapter turns **two inputs** into Kubernetes resources:

- the **compiled pack** (`pack.json`) — the portable artifact you tested in Arena, and
- the **`deploy.config`** block — the Omnia-specific environment binding.

For the general, provider-neutral model — *pack = portable contract, deploy config = environment binding, adapter = translator* — see PromptKit's [Anatomy of a Deployment](https://promptkit.altairalabs.ai/arena/explanation/deploy/anatomy/). This page is the Omnia-specific concretion: exactly which field lands in which CRD, and why.

## The resources

A deployment produces up to five resources. Two are always created; three are conditional.

| Resource | Created | Built from |
|----------|---------|-----------|
| ConfigMap (`<pack>-packdata`) | Always | The raw pack JSON (content fold) |
| PromptPack | Always | Pack version + `skills` / `skillsConfig` |
| ToolRegistry | When `config.tools` is non-empty | `config.tools` handlers |
| AgentPolicy | When a prompt defines a tool policy | The **pack's** tool blocklist |
| AgentRuntime (one per agent) | Always | `providers`, `runtime`, `externalAuth`, `memory`, `evals` + refs |

Apply/destroy ordering across these is covered in [Resource Lifecycle](/explanation/resource-lifecycle/); exact payloads are in [Resource Types](/reference/resource-types/).

## What comes from the pack vs the config

The dividing line is **portability**. If a fact about the agent is true no matter where it runs, it is in the pack; if it is specific to *this* Omnia workspace, it is in `deploy.config`.

| From the **pack** | From **`deploy.config`** |
|-------------------|--------------------------|
| Prompts, system templates, guardrails | Provider bindings (which `Provider` CRD fills each role) |
| Tool **schemas** (what the model may call) | Tool **handlers** (how each tool executes) |
| Tool policies (blocklist) | Skills (which `SkillSource` CRDs to mount) |
| Eval **definitions** | Eval **routing** (on/off, inline vs worker) |
| Agent topology, pack id + version | Runtime sizing, external auth, memory, labels |

## Field-by-field mapping

### `providers` → `AgentRuntime.spec.providers[]`

Each binding becomes a `NamedProviderRef` on **every** AgentRuntime, preserving order:

```
{ name, ref, role } → { name, providerRef: { name: ref }, role }
```

`role` defaults to `llm`. The binding named **`default`** is the runtime's primary provider. Each `ref` must name a `Provider` CRD that already exists in the workspace — this is validated at **plan** time, so a missing provider (or a token without permission to read it) fails the plan before anything is created.

The pack carries **no** provider bindings — that is deliberate, so the same pack is portable across workspaces. The providers you wire into `arena.yaml` to run scenarios are test fixtures and do **not** deploy.

### `tools` → split across PromptPack and ToolRegistry

A tool has two halves, and they come from two different inputs:

- The **schema** — the tool's name/description/input-output contract, i.e. what the model is allowed to call — is compiled into the pack and reaches the runtime inside the **PromptPack content fold**. You do not restate it in `deploy.config`.
- The **handler** — how that tool actually executes (an HTTP endpoint, an MCP server, a gRPC target, …) — comes from `config.tools` and becomes `ToolRegistry.spec.handlers[]`, one entry per handler, in order.

The AgentRuntime gets a `toolRegistryRef` **only** when at least one handler is declared. Consequence: a pack tool with no matching `config.tools` handler is visible to the model but has nothing to execute it server-side (unless it is a `client`-type or system tool).

### `skills` / `skillsConfig` → `PromptPack.spec.skills[]` / `spec.skillsConfig`

`config.skills` maps to `spec.skills[]` on the PromptPack. Each entry references a `SkillSource` CRD and accepts two shapes:

```yaml
skills:
  - anthropic-skills                       # bare name shorthand
  - source: company-skills                 # full form
    include: [refund-policy, escalation]
    mountAs: support
```

Each source is **pre-flighted at apply**: a missing source, or one whose `status.phase` is not `Ready`, fails the deployment. `config.skillsConfig` (`maxActive`, `selector`) maps to `spec.skillsConfig`. Skills are not part of the pack — they are bound entirely here.

### Tool policy → `AgentPolicy.spec.toolBlocklist`

This one flows the *other* way: the blocklist is read from the **pack** (each prompt's tool policy), deduplicated and sorted, into an `AgentPolicy` CRD. It is created only when a prompt defines a policy. Nothing in `deploy.config` feeds it.

### `runtime`, `externalAuth`, `memory`, `evals` → `AgentRuntime.spec.*`

These are faithful passthroughs onto the AgentRuntime — only the fields you set are emitted, so unset values fall back to CRD defaults. One subtlety: eval **definitions** live in the pack; `config.evals` only turns evals on and routes which **groups** run inline (in the runtime) versus in the per-service-group worker.

## At a glance

| Input | Destination |
|-------|-------------|
| `config.providers` | `AgentRuntime.spec.providers[]` (NamedProviderRef) |
| `config.tools` | `ToolRegistry.spec.handlers[]` |
| `config.skills` / `skillsConfig` | `PromptPack.spec.skills[]` / `spec.skillsConfig` |
| `config.runtime` / `externalAuth` / `memory` / `evals` | `AgentRuntime.spec.*` |
| Pack content (prompts, **tool schemas**, eval defs) | `PromptPack` (via the `<pack>-packdata` ConfigMap) |
| Pack tool policy | `AgentPolicy.spec.toolBlocklist` |
| Pack agents | One `AgentRuntime` each |

## See also

- [Configure the Adapter](/how-to/configure/) — every `deploy.config` field
- [Resource Types](/reference/resource-types/) — exact CRD payloads
- [Resource Lifecycle](/explanation/resource-lifecycle/) — apply/destroy ordering and diffing
- [Anatomy of a Deployment](https://promptkit.altairalabs.ai/arena/explanation/deploy/anatomy/) — the general, provider-neutral model
