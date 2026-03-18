---
title: Use Dry-Run Mode
description: Preview deployments without making API calls
---

Dry-run mode lets you see exactly what the adapter would create or update without touching the Omnia cluster. This is useful for reviewing changes before applying them, testing configuration, and CI validation pipelines.

## Enable dry-run

Set `dry_run: true` in your deploy configuration:

```yaml
deploy:
  provider: omnia
  config:
    api_endpoint: "https://omnia.example.com"
    workspace: "my-workspace"
    providers:
      default: claude-prod
    dry_run: true
```

## What happens in dry-run mode

When `dry_run` is `true`, the Apply operation:

1. Parses and validates the pack JSON and deploy configuration.
2. Generates the full list of desired resources (identical to a real apply).
3. Streams progress events for each resource with `planned` status.
4. Returns adapter state with all resources marked as `planned`.
5. Makes **zero** API calls to the Omnia cluster.

No authentication is required for the dry-run itself -- the adapter does not create an HTTP client. However, the configuration is still validated, so `api_endpoint`, `workspace`, and `providers` must be set.

## Dry-run output

Each resource appears in the progress stream:

```
Planned configmap: my-pack-packdata
Planned prompt_pack: my-pack
Planned tool_registry: my-pack-tools
Planned agent_runtime: my-pack
```

The returned state contains the same resource entries, each with `"status": "planned"`.

## Plan vs dry-run

Both `plan` and dry-run `apply` preview resources, but they serve different purposes:

| | Plan | Dry-run Apply |
|---|---|---|
| Purpose | Show what *would* change | Simulate the full apply workflow |
| API calls | None | None |
| Prior state diff | Yes -- shows create/update/delete | No -- always shows create |
| Progress events | No | Yes -- streams resource events |
| State output | Change list | Full adapter state (with `planned` status) |

Use **plan** to review diffs against an existing deployment. Use **dry-run apply** to validate the complete apply workflow end-to-end without side effects.

## CI usage

Dry-run is useful in CI pipelines to validate that a pack compiles and produces valid deploy resources:

```bash
# Validate the pack produces a valid deployment plan
promptarena deploy apply --dry-run
```

The command exits with code 0 if the pack and configuration are valid, and non-zero if parsing or validation fails.
