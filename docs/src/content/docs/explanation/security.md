---
title: Security Model
description: Authentication, workspace isolation, and resource ownership
---

The Omnia adapter's security model is built on three pillars: API token authentication, workspace isolation, and label-based resource ownership.

## API token authentication

All communication with the Omnia Management API uses bearer token authentication. Every HTTP request includes an `Authorization: Bearer <token>` header.

The token can be provided in two ways, in order of precedence:

1. **Config field**: `api_token` in the deploy configuration.
2. **Environment variable**: `OMNIA_API_TOKEN`.

The adapter validates that a token is available before any API operation. If neither source provides a token, configuration validation fails with a clear error message.

### Token handling

- The token is resolved once when the HTTP client is created and stored in memory for the duration of the operation.
- The token is never written to the adapter state or logged.
- The `api_token` config field is marked `omitempty` in JSON serialization so it does not appear in state snapshots.

### Best practices

- Use the `OMNIA_API_TOKEN` environment variable rather than the `api_token` config field to avoid committing secrets to source control.
- Rotate tokens regularly and use short-lived tokens in CI/CD pipelines.
- Use workspace-scoped tokens with the minimum permissions required (create, update, delete resources within the target workspace).

## Workspace isolation

Every API call is scoped to a single workspace. The adapter constructs URLs in the form:

```
<api_endpoint>/api/v1/workspaces/<workspace>/<resource-type>
```

This means:

- A deployment can only create, read, update, or delete resources within its configured workspace.
- Different environments (staging, production) should use separate workspaces.
- The workspace name is validated against the pattern `^[a-z0-9][a-z0-9-]*[a-z0-9]$` to ensure it is a valid Kubernetes DNS subdomain.

The Omnia Management API enforces workspace boundaries server-side. Even if a token has access to multiple workspaces, each adapter invocation operates on exactly one.

## Label-based resource ownership

The adapter uses Kubernetes labels to track which resources it manages. Every resource created by the adapter carries four managed labels:

| Label | Purpose |
|---|---|
| `app.kubernetes.io/managed-by=promptarena` | Identifies the adapter as the resource owner |
| `promptkit.altairalabs.ai/pack-id=<id>` | Links the resource to a specific pack |
| `promptkit.altairalabs.ai/pack-version=<ver>` | Records which pack version created the resource |
| `promptkit.altairalabs.ai/resource-type=<type>` | Classifies the resource type |

These labels serve multiple security functions:

- **Ownership verification**: The Status operation queries resources by label to verify they still exist and are healthy.
- **Scope enforcement**: Destroy only deletes resources that are tracked in the adapter state, preventing accidental deletion of unrelated resources.
- **Audit trail**: Labels provide a clear record of which tool created each resource and when.

### Label protection

User-supplied custom labels cannot override managed labels. The adapter applies managed labels after user labels, ensuring they always have the correct values. This prevents accidental or intentional tampering with ownership metadata.

## Error classification

The adapter classifies API errors into categories that help diagnose security-related issues:

| Category | HTTP status | Remediation |
|---|---|---|
| `permission` | 401, 403 | Verify the API token has sufficient permissions for the workspace |
| `not_found` | 404 | Verify the resource exists and the workspace/name are correct |
| `conflict` | 409 | Resource already exists; consider updating instead of creating |
| `network` | 5xx | Omnia API server error; retry after a short wait |

Error messages from the adapter include `[hint: ...]` suffixes with actionable remediation guidance.
