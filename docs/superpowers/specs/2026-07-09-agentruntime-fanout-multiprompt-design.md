# Per-prompt AgentRuntime fan-out for plain multi-prompt packs

- **Date:** 2026-07-09
- **Status:** Approved design, pending implementation plan
- **Repo:** `promptarena-deploy-omnia`
- **Related:** Omnia#1595, Omnia#1597 (entry resolution), PR #56 (prompt warning — to be closed), PR #57 (`spec.facades[]` migration — sequencing dependency)

## Problem

A PromptPack that is a **plain pack** (no `workflow`, no `agents`) with **more than one
top-level prompt** currently deploys as a **single** AgentRuntime named `pack.ID`. The
Omnia runtime resolves that agent's entry via `ResolvePackEntry`, which for a plain
multi-prompt pack falls back to the prompt literally named `default`. If no such prompt
exists, the agent fails its first request unless the caller names a prompt per request.

The maintainers author plain packs where **each top-level prompt is intended to be its own
agent** (e.g. `prompts: [triage, billing, tech]` → three agents). There is currently no
way to express that through the adapter: the pack collapses to one agent with an ambiguous
entry.

## Key facts that shape the design

1. **A plain-pack prompt is fully self-contained.** `prompt.PackPrompt` carries its own
   system template, variables, tools, tool policy, media, parameters, validators, and model
   overrides, and references **no other prompt**. Composition/orchestration is what
   `workflow` (states) and `agents` (members) are for. Therefore, in a plain pack, **every
   top-level prompt is an independent agent entry** — there are no helper sub-prompts, so
   fanning out one agent per prompt cannot produce a bogus agent.

2. **No new CRD field is needed** (consistent with the Omnia#1597 decision to resolve the
   entry from the pack rather than add a CRD field). The AgentRuntime already exposes a
   generic lever: `spec.runtime.extraEnv` is appended to the runtime container **after** the
   operator's hardcoded `OMNIA_PROMPT_NAME=default` (`internal/controller/deployment_builder_env.go`).
   Kubernetes takes the last value for a duplicate env name, so
   `spec.runtime.extraEnv: [{name: OMNIA_PROMPT_NAME, value: <prompt>}]` pins the entry.

3. **The adapter already fans out for multi-agent packs.** `agentRuntimeNames(pack)` returns
   one name per `agents.member`; apply's phase 4 and plan both loop over it. This feature
   extends the same pattern to plain multi-prompt packs.

## Design

### Scope of fan-out

Only the **AgentRuntime** fans out. The PromptPack, ConfigMap, ToolRegistry, and AgentPolicy
stay single and shared; all N runtimes reference them. This already matches the apply phase
structure (phases 0–3 build shared resources once; only phase 4 loops agent names).

### Name + entry derivation

`agentRuntimeNames` is the single decision point. It changes from returning `[]string` to
returning a small struct carrying **name** and an optional **entry prompt**:

| Pack shape | Agents created | Entry override |
|---|---|---|
| Multi-agent (`agents.members`) | one per member — **unchanged** | none — existing multi-agent behavior is untouched by this feature |
| Plain, exactly 1 prompt | `[pack.ID]` — **unchanged** | none (runtime auto-resolves the sole prompt) |
| Plain, N > 1 prompts | one per prompt: `sanitizeName(pack.ID + "-" + promptName)` | `OMNIA_PROMPT_NAME=<promptName>` |

Naming is `<pack.ID>-<prompt>` (sanitized to the 253-char DNS limit via the existing
`sanitizeName`). Prompt order is sorted for deterministic planning.

### Entry pinning

`buildAgentRuntimeRequest` gains the per-agent entry prompt as an input. When set (plain
multi-prompt fan-out only), it emits:

```yaml
spec:
  runtime:
    extraEnv:
      - name: OMNIA_PROMPT_NAME
        value: <promptName>
```

merged with any `spec.runtime` the user's config already produces (`buildRuntimeSpec`). The
adapter's `OMNIA_PROMPT_NAME` override is appended **last** so it wins over both the
operator default and any user-supplied `extraEnv` collision. No generic `runtime.extraEnv`
config field is exposed in this feature (YAGNI); the override is internal to the adapter.

### Plan / destroy

No structural change: plan already lists one `agent_runtime` change per `agentRuntimeNames`
entry, and destroy reverses. Extending `agentRuntimeNames` covers all three phases.

### Migration (accepted)

An existing plain multi-prompt deployment has one agent named `pack.ID`. After this change,
the next plan (adopt-on-apply diffs against the live cluster) shows:

```
- agent_runtime.<pack>           (delete)
+ agent_runtime.<pack>-<promptA> (create)
+ agent_runtime.<pack>-<promptB> (create)
```

i.e. a **destroy+recreate of the runtimes**; the shared PromptPack/ConfigMap/ToolRegistry/
AgentPolicy are untouched. This is accepted for alpha (no installed base to protect) and
will be called out in the changelog/PR.

### Supersedes PR #56

With fan-out, a plain multi-prompt pack no longer has an unresolved-entry problem — every
prompt gets its own resolving agent. `promptWarnings` (and its `defaultEntryName` constant,
comment block, and tests) is therefore **deleted** as part of this feature, and **PR #56 is
closed unmerged**. Single-prompt plain packs already resolve automatically and produce no
warning, so nothing is lost.

## Components touched

| File | Change |
|---|---|
| `internal/omnia/builders.go` | `agentRuntimeNames` returns name+entry; `buildAgentRuntimeRequest` accepts and emits the `OMNIA_PROMPT_NAME` override via `spec.runtime.extraEnv` |
| `internal/omnia/apply.go` | phase-4 loop threads the per-agent entry into the builder |
| `internal/omnia/plan.go` | agent-runtime change list uses the new return type; **delete** `promptWarnings`, `defaultEntryName`, and their wiring |
| `internal/omnia/prompt_warnings_test.go` | deleted |
| `internal/omnia/builders_test.go` | table-driven `agentRuntimeNames` cases; assert `runtime.extraEnv` override present on fanned agents and absent for single/multi-agent |
| `internal/omnia/apply_test.go` | event-ordering test: N runtimes over one shared pack |
| docs (`how-to/configure.md` / `reference/configuration.md`) | document per-prompt fan-out behavior and naming |

## Sequencing dependency

This edits `buildAgentRuntimeRequest` and `buildRuntimeSpec`, which **PR #57** (`spec.facade`
→ `spec.facades[]`) also edits. Implement this on top of PR #57 (branch from it or rebase
after it merges) to avoid conflicts and to build against the correct `facades[]` shape.

## Testing strategy

- **`agentRuntimeNames`** — table-driven: multi-agent (per member), plain single (pack.ID,
  no entry), plain multi (pack.ID-prompt per prompt, sorted, with entry).
- **`buildAgentRuntimeRequest`** — fanned agent emits `spec.runtime.extraEnv` with
  `OMNIA_PROMPT_NAME=<prompt>` merged after user runtime config; single/multi-agent emit no
  such override; override wins over a colliding user value.
- **apply** — a plain multi-prompt pack produces one shared PromptPack (+ tools/policy) and N
  AgentRuntimes in phase 4, in deterministic order.
- **Naming** — `<pack.ID>-<prompt>` sanitized within the 253-char limit.

## Out of scope

- A generic user-facing `runtime.extraEnv` config field.
- Any opt-out / single-agent-from-multi-prompt mode (can be added later if a use case
  appears).
- Any Omnia-side change (the extraEnv lever already exists).
