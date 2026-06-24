---
title: Anatomy of an Omnia Agent
description: What an AgentRuntime is, the resources it references, and how they run
---

Before the [Configuration Mapping](/explanation/configuration-mapping/) page explains *how a deploy config populates Omnia resources*, it helps to know **what an agent in Omnia actually is**. This page describes the moving parts.

## An agent *is* an `AgentRuntime`

The `AgentRuntime` is the resource you create — or that this adapter creates for you at deploy time. It is the composition root: a deployed, serving workload with a lifecycle, an address, and an identity. **Everything else is a separate resource the AgentRuntime references.** The "agent" you think of is the whole assembly: the AgentRuntime, the CRDs it points at, the pod it reconciles to, and the shared workspace services it leans on.

## What actually runs (the pod)

An `AgentRuntime` reconciles to a Pod with **two containers** (plus an optional sidecar):

- a **facade container** — the client-facing front door (`spec.facade`: `websocket` / `rest` / `a2a`), and
- a **runtime container** — the agent **framework** (`spec.framework`, default **PromptKit**) that executes the pack.
- optionally a **policy-proxy** sidecar — injected when the operator enables policy enforcement; it sits in front of the runtime and enforces the agent's policy.

That pod is not self-contained: it uses **shared workspace services** resolved from its `serviceGroup` — `session-api` (session storage) and `memory-api` (cross-session memory), plus an opt-in `eval-worker` for out-of-band evals.

An AgentRuntime also has two **modes** (`spec.mode`): `agent` (the conversational runtime) and `function` (the pack exposed as a one-shot, structured-I/O HTTP endpoint with input/output schemas).

## The moving parts

```d2
direction: down

caller: Caller — app / A2A peer / dashboard {
  shape: person
}

agent: "Agent = AgentRuntime + what it references" {
  ar: AgentRuntime {
    shape: hexagon
  }

  brain: Brain & capabilities (referenced CRDs) {
    pp: PromptPack
    cm: "ConfigMap: pack.json" {
      shape: cylinder
    }
    prov: "Provider xN — role to model"
    tr: "ToolRegistry — handlers"
    pol: "AgentPolicy — guardrails"
    sk: SkillSource

    pp -> cm
    pp -> sk: mounts {style.stroke-dash: 4}
  }

  pod: Pod (reconciled from AgentRuntime) {
    fac: "facade — ws / rest / a2a"
    rt: "runtime — PromptKit"
    pxy: policy-proxy (optional sidecar)

    fac -- rt
    rt -- pxy
  }

  ar -> brain.pp: promptPackRef
  ar -> brain.prov: providers
  ar -> brain.tr: toolRegistryRef
  ar -> brain.pol: governs {style.stroke-dash: 4}
  ar -> pod: materializes {style.bold: true}
}

ws: Workspace service group (shared) {
  sess: session-api
  mem: memory-api
  ev: eval-worker (opt-in)
}

caller -> agent.pod.fac: externalAuth gate
agent.pod.rt -> agent.brain.cm: loads {style.stroke-dash: 4}
agent.pod.rt -> agent.brain.prov: inference
agent.pod.rt -> agent.brain.tr: tool calls
agent.pod.rt -> ws.sess
agent.pod.rt -> ws.mem
agent.pod.rt -> ws.ev
agent.pod.pxy -> agent.brain.pol: enforces {style.stroke-dash: 4}
```

**Legend** — solid arrow = *references / uses*; **bold arrow** = *operator reconciles to*; dashed = *mounts / governs / enforces / loads*.

The boxes under **brain & capabilities** are *declarations* (control plane — what you author). The pod and the workspace-service edges are what happens *at request time* (data plane).

### Brain & capabilities

| Part | Resource | Role | Required? |
|------|----------|------|-----------|
| Prompts & behavior | **PromptPack** (`promptPackRef`) → a **ConfigMap** holding `pack.json` | The agent's brain — prompts, config, skill bindings | **Yes** |
| Models | **Provider**(s) (`providers[]`, `NamedProviderRef`) | Bind a logical role → a real inference backend; `default` = primary | Practically yes |
| Tools | **ToolRegistry** (`toolRegistryRef`) | Tool execution handlers | Optional |
| Guardrails | **AgentPolicy** | e.g. tool blocklist, enforced by the policy-proxy | Optional |
| Skills | **SkillSource** (mounted via the PromptPack) | Skill content the pack pulls in | Optional |

### Serving & platform

| Part | Where | Role |
|------|-------|------|
| Front door | `spec.facade` | websocket / rest / a2a; agent vs function mode |
| Data-plane auth | `spec.externalAuth` | who may call the facade; unset ⇒ dashboard-only |
| Shared services | `spec.serviceGroup` → Workspace `services[]` | session-api, memory-api, opt-in eval-worker |
| Memory | `spec.memory` | cross-session recall |
| Evals | `spec.evals` | runtime eval execution (inline / worker) |
| Infra | `spec.runtime`, `rollout`, `podOverrides` | sizing, progressive delivery, pod customization |

## Why this matters for deploy

A deployment is just the act of creating an `AgentRuntime` and the resources it references. The adapter's job is to translate your pack + `deploy.config` into exactly these CRDs — which is what [Configuration Mapping](/explanation/configuration-mapping/) walks through field by field.

## See also

- [Configuration Mapping](/explanation/configuration-mapping/) — which `deploy.config` field lands in which resource
- [Resource Types](/reference/resource-types/) — exact CRD payloads
- [Resource Lifecycle](/explanation/resource-lifecycle/) — apply/destroy ordering
- [Anatomy of a Deployment](https://promptkit.altairalabs.ai/arena/explanation/deploy/anatomy/) — the general, provider-neutral model
