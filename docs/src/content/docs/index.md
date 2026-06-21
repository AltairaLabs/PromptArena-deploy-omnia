---
title: Omnia Kubernetes Adapter
description: Deploy prompt packs to the Omnia Kubernetes platform
---

The Omnia adapter is a PromptKit deploy provider that translates compiled prompt packs into Omnia Kubernetes custom resources. It communicates with the Omnia Management API to create, update, and destroy the Kubernetes objects that back a running agent.

## Resource mapping

The adapter maps each concept from a compiled pack to one or more Omnia Kubernetes resources:

| Pack concept | Omnia resource | Adapter resource type |
|---|---|---|
| Pack JSON | ConfigMap | `configmap` |
| Pack identity + skills | PromptPack CRD | `prompt_pack` |
| Agent + provider bindings | AgentRuntime CRD | `agent_runtime` |
| Deploy-config `tools` | ToolRegistry CRD | `tool_registry` |
| Tool blocklist | AgentPolicy CRD | `agent_policy` |

## Key features

- **Single and multi-agent packs** -- single-agent packs produce one AgentRuntime; multi-agent packs produce one per agent with shared PromptPack and ToolRegistry resources.
- **Role-aware multi-provider** -- bind one or more Omnia Provider CRDs per runtime, each tagged with a role (`llm`, `embedding`, `tts`, `stt`, `image`, `inference`). The `default` binding is the primary.
- **Tool registry** -- tool handlers declared in the deploy-config `tools` block are projected into a ToolRegistry CRD (`spec.handlers[]`) so the Omnia runtime can discover them at startup.
- **Skill bindings** -- `skills` and `skillsConfig` from the deploy config are projected onto the PromptPack, referencing Omnia SkillSource CRDs (pre-flighted at apply time).
- **Dry-run mode** -- preview every resource the adapter would create or update, without making API calls.
- **Managed resource labels** -- every resource is labelled with `app.kubernetes.io/managed-by`, pack ID, pack version, and resource type for reliable ownership tracking.
- **Agent policy** -- tool blocklists defined in the pack are enforced via an AgentPolicy CRD.
- **Create/update diffing** -- when prior state exists, the adapter diffs desired resources against the previous deployment and emits create, update, or delete actions accordingly.

## Quick start

Add an `omnia` deploy provider to your arena configuration:

```yaml
deploy:
  provider: omnia
  config:
    api_endpoint: "https://omnia.example.com"
    workspace: "my-workspace"
    providers:
      default: claude-prod
```

Set the API token via environment variable:

```bash
export OMNIA_API_TOKEN="your-token-here"
```

Then use PromptArena to plan and apply:

```bash
promptarena deploy plan
promptarena deploy apply
```

## How it works

```d2
direction: right

pack: Compiled Pack JSON {
  shape: document
}

adapter: Omnia Adapter {
  shape: hexagon
}

api: Omnia Management API {
  shape: cloud
}

k8s: Kubernetes Resources {
  cm: ConfigMap
  pp: PromptPack CRD
  tr: ToolRegistry CRD
  ap: AgentPolicy CRD
  ar: AgentRuntime CRD

  cm -> pp: references
  pp -> ar: binds
  tr -> ar: binds
  ap -> ar: binds
}

pack -> adapter: pack JSON
adapter -> api: REST calls
api -> k8s: creates/updates
```

1. PromptArena compiles the pack and passes the JSON to the adapter.
2. The adapter parses the pack, builds Kubernetes resource payloads, and resolves provider mappings.
3. Resources are applied in dependency order through the Omnia Management API.
4. The API creates or updates the corresponding Kubernetes objects in the target workspace.

## Next steps

- [First Omnia Deployment](/tutorials/01-first-deployment/) -- deploy a single-agent pack step by step.
- [Multi-Agent Deployment](/tutorials/02-multi-agent/) -- deploy a pack with multiple agents.
- [Configure the Adapter](/how-to/configure/) -- full reference for all configuration options.
- [Resource Lifecycle](/explanation/resource-lifecycle/) -- understand the apply and destroy ordering.
- [Configuration Reference](/reference/configuration/) -- JSON Schema and field-by-field documentation.
