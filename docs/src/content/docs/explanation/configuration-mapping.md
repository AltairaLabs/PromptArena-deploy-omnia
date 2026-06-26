---
title: Configuration Mapping
description: How the pack and the deploy config become Omnia resources
---

The Omnia adapter turns **two inputs** into Kubernetes resources:

- the **compiled pack** (`pack.json`) — the portable artifact you tested in Arena, and
- the **`deploy.config`** block — the Omnia-specific environment binding.

For the general, provider-neutral model — *pack = portable contract, deploy config = environment binding, adapter = translator* — see PromptKit's [Anatomy of a Deployment](https://promptkit.altairalabs.ai/arena/explanation/deploy/anatomy/). This page is the Omnia-specific concretion: exactly which field lands in which CRD, and why.

## The resources

A deployment works with up to four resource types. The PromptPack and AgentRuntime(s) are always created; the AgentPolicy is conditional; the ToolRegistry is a create-only convenience. The adapter **owns** the PromptPack, AgentRuntime, and AgentPolicy; the ToolRegistry (and the Providers and SkillSources it binds to) are **operator-owned** — see [Resource Lifecycle § Ownership](/explanation/resource-lifecycle/#ownership).

| Resource | Owner | Created | Built from |
|----------|-------|---------|-----------|
| PromptPack | Adapter | Always | Pack version + `skills` / `skillsConfig` (pack content folded dashboard-side into a managed ConfigMap) |
| ToolRegistry | Operator | Create-only, in create mode (`config.tools` set) when absent | `config.tools` handlers + synthesized handlers for uncovered pack tools |
| AgentPolicy | Adapter | When a prompt defines a tool policy | The **pack's** tool blocklist |
| AgentRuntime (one per agent) | Adapter | Always | `providers`, `runtime`, `externalAuth`, `memory`, `evals` + refs |

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

`role` defaults to `llm` and is asserted at reconcile against the Provider's real `spec.role` (a mismatch puts the AgentRuntime in `Phase=Error`). Each `ref` must name a `Provider` CRD that already exists in the workspace — validated at **plan** time, so a missing provider (or a token without permission to read it) fails the plan before anything is created.

#### `name` is a logical key, and `default` is the primary

The two fields play different roles, and confusing them is the most common mistake:

- **`name`** is the *logical name the pack looks the provider up by* (`default`, `judge`, `embeddings`, …) — a contract between the pack and the deployment.
- **`ref`** is the *Provider CRD* that fulfils it.

The runtime's **primary** provider is whichever binding has **`name: default`** — not `ref: default`. So to make `ollama` the primary you write `{ name: default, ref: ollama, role: llm }`. The other bindings are available to the pack under their own logical names (a pack that uses a separate judge or embedder asks for `name: judge` / `name: embeddings`).

:::danger[A binding must be named `default`, or the agent has no primary]
If **no** binding is named `default`, the AgentRuntime can still reconcile to `healthy` (the providers exist and their roles match), but the runtime has **no primary LLM** and fails the moment it's invoked. Beware exported profiles that set `name` equal to the CRD name for every entry (`name: ollama`, `name: rag-hero-baseline`, …): none is `default`, so there is no primary, and those logical names only resolve if the pack happens to reference providers by exactly those names. Rename the primary binding's `name` to `default` (keeping its `ref`), and give the rest the logical names the pack actually expects.
:::

The pack carries **no** provider bindings — that is deliberate, so the same pack is portable across workspaces. The providers you wire into `arena.yaml` to run scenarios are test fixtures and do **not** deploy.

### `tools` → split across PromptPack and ToolRegistry

A tool has two halves, and they come from two different inputs:

- The **schema** — the tool's name/description/input-output contract, i.e. what the model is allowed to call — is compiled into the pack and reaches the runtime inside the **PromptPack content fold**. You do not restate it in `deploy.config`.
- The **handler** — how that tool actually executes (an HTTP endpoint, an MCP server, a gRPC target, …) — is supplied by the ToolRegistry the AgentRuntime is wired to.

How that registry is resolved is decided in one of **three modes** — `tools` (create), `tool_registry_ref` (bind), or discovery — the same way at plan and apply. The full decision tree is in [Tool-registry resolution](/explanation/resource-lifecycle/#tool-registry-resolution-3-modes); the short version:

- **`config.tools` set (create)** — the adapter synthesizes a `<pack-id>-tools` registry. It is **operator-owned and create-only**: written once when absent, then never updated and left in place on destroy. Beyond your declared handlers, it synthesizes a handler for each uncovered pack tool — a `mode: live` tool wired to its real URL from the arena source, a `mode: mock`/no-URL tool to a `placeholder.invalid` endpoint the operator completes in Omnia.
- **`tool_registry_ref` set (bind)** — the AgentRuntime is pointed at an existing registry by name; the adapter never creates or updates it.
- **neither set (discover)** — the adapter auto-binds an existing registry iff exactly one covers all the pack's tools, else binds none and advises.

The AgentRuntime gets a `toolRegistryRef` whenever a registry is resolved (create, bind, or auto-discover); it is omitted only when no registry resolves. A pack tool that ends up with no handler is visible to the model but has nothing to execute it server-side (unless it is a `client`-type or system tool).

:::note[The adapter does cross-check coverage — as advisories]
Unlike a blind passthrough, the resolver **does** compare the pack's tools against the registry: in bind mode it warns for pack tools the registry doesn't provide and for input-schema drift; in create mode it reports how many handlers were configured vs source-wired vs placeholder. These are **warnings, not errors** — a mismatch never blocks the deploy. The pack's tool **schemas** still stay in the pack (they ride into the runtime via the PromptPack content fold); the handler is matched to a tool by **name** or via a `selector`. (For `http`/`grpc` you *may* restate the schema in the handler's `tool` block, but that duplicates what is already in the pack.)
:::

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
| `config.tools` | `ToolRegistry.spec.handlers[]` (create mode; create-only) |
| `config.tool_registry_ref` | `AgentRuntime.spec.toolRegistryRef` (bind mode) |
| `config.skills` / `skillsConfig` | `PromptPack.spec.skills[]` / `spec.skillsConfig` |
| `config.runtime` / `externalAuth` / `memory` / `evals` | `AgentRuntime.spec.*` |
| Pack content (prompts, **tool schemas**, eval defs) | `PromptPack.content` (folded dashboard-side into a managed ConfigMap) |
| Pack tool policy | `AgentPolicy.spec.toolBlocklist` |
| Pack agents | One `AgentRuntime` each |

## See also

- [Configure the Adapter](/how-to/configure/) — every `deploy.config` field
- [Resource Types](/reference/resource-types/) — exact CRD payloads
- [Resource Lifecycle](/explanation/resource-lifecycle/) — apply/destroy ordering and diffing
- [Anatomy of a Deployment](https://promptkit.altairalabs.ai/arena/explanation/deploy/anatomy/) — the general, provider-neutral model
