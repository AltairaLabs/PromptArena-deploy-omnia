---
title: Configure the Adapter
description: All configuration options for the Omnia deploy adapter
---

The Omnia adapter is configured through the `deploy.config` section of your arena configuration file. This guide covers every available option.

## Minimal configuration

```yaml
deploy:
  provider: omnia
  config:
    api_endpoint: "https://omnia.example.com"
    workspace: "my-workspace"
    providers:
      default: claude-prod
```

## Full configuration

```yaml
deploy:
  provider: omnia
  config:
    api_endpoint: "https://omnia.example.com"
    workspace: "my-workspace"
    api_token: "optional-inline-token"
    providers:
      - name: default
        ref: claude-sonnet-4-20250514
        role: llm
      - name: embedder
        ref: text-embedding-3-large
        role: embedding
    tools:
      - name: weather
        type: http
        tool:
          name: get_weather
          description: Get the current weather for a city.
          inputSchema:
            type: object
            properties:
              city: { type: string }
            required: [city]
        httpConfig:
          url: "https://api.example.com/weather"
        timeout: "10s"
    skills:
      - source: company-skills
        include: [refund-policy, escalation]
        mountAs: support
    skillsConfig:
      maxActive: 3
      selector: model-driven
    runtime:
      replicas: 2
      cpu: "1000m"
      memory: "1Gi"
      autoscaling:
        enabled: true
        type: hpa
        min_replicas: 1
        max_replicas: 10
        target_cpu_utilization: 70
    externalAuth:
      oidc:
        issuer: "https://auth.example.com/"
        audience: "omnia-agents"
        claimMapping:
          subject: sub
          role: groups
      sharedToken:
        secretRef: agent-shared-token
        trustEndUserHeader: true
    labels:
      team: platform
      environment: production
    dry_run: false
```

## Field reference

### `api_endpoint` (required)

The base URL of the Omnia Management API. The adapter appends `/api/workspaces/<workspace>` to form the full API path.

```yaml
api_endpoint: "https://omnia.example.com"
```

Must be a valid URI. Trailing slashes are stripped automatically.

### `workspace` (required)

The Omnia workspace to deploy into. Must be a valid Kubernetes DNS subdomain: lowercase alphanumeric characters and hyphens, starting and ending with an alphanumeric character.

```yaml
workspace: "prod-workspace"
```

Pattern: `^[a-z0-9][a-z0-9-]*[a-z0-9]$`

### `api_token` (optional)

Bearer token for authenticating with the Omnia Management API. If omitted, the adapter reads the `OMNIA_API_TOKEN` environment variable.

```yaml
api_token: "your-token"
```

The environment variable approach is strongly recommended for CI/CD pipelines and shared configurations to avoid committing secrets:

```bash
export OMNIA_API_TOKEN="your-token"
```

One of `api_token` or `OMNIA_API_TOKEN` must be set. Validation fails if neither is provided.

### `providers` (required)

Provider bindings tell each AgentRuntime which Omnia `Provider` CRD fulfils a given capability. Every binding is emitted to each runtime; the binding named `default` is the runtime's primary provider. The field accepts two shapes.

**List form (recommended)** — a list of `{name, ref, role}` bindings. `role` defaults to `llm` and must be one of `llm`, `embedding`, `tts`, `stt`, `image`, `inference`:

```yaml
providers:
  - name: default
    ref: claude-sonnet-4-20250514
    role: llm
  - name: embedder
    ref: text-embedding-3-large
    role: embedding
  - name: reranker
    ref: bge-reranker
    role: inference
```

Binding names must be unique, and each `ref` must name an Omnia `Provider` CRD.

**Legacy map form** — a `name → Provider CRD` map. Each entry becomes a binding with role `llm`:

```yaml
providers:
  default: claude-sonnet-4-20250514
  router: gpt4-prod
```

### `tools` (optional)

Tool handlers projected into the `ToolRegistry` CRD's `spec.handlers[]`, in order. When `tools` is empty, no ToolRegistry is created.

```yaml
tools:
  - name: weather
    type: http
    tool:
      name: get_weather
      description: Get the current weather for a city.
      inputSchema:
        type: object
        properties:
          city: { type: string }
        required: [city]
    httpConfig:
      url: "https://api.example.com/weather"
    timeout: "10s"
  - name: docs-search
    type: mcp
    mcpConfig:
      server: "docs-mcp"
```

| Sub-field | Type | Description |
|---|---|---|
| `name` | string | Handler name; unique across handlers. Pattern `^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`. |
| `type` | string | One of `http`, `openapi`, `grpc`, `mcp`, `client`. Selects the type-specific config block. |
| `tool` | object | Tool schema (`name`, `description`, `inputSchema`, optional `outputSchema`). Required for `http` and `grpc`. |
| `selector` | object | Selector matching tools to this handler. |
| `httpConfig` / `openAPIConfig` / `grpcConfig` / `mcpConfig` / `clientConfig` | object | Type-specific config; passed through verbatim. |
| `timeout` | string | Per-handler timeout (e.g. `"30s"`). |

Per-type requirements: `http`/`grpc` need a `tool` block **plus** the matching config block or a `selector`; `openapi`/`mcp` need the matching config block or a `selector`; `client` has no hard requirement. The omnia CRD validates the inner config fields deeply.

### `skills` (optional)

Skill bindings projected onto the PromptPack's `spec.skills[]`. Each references an Omnia `SkillSource` CRD. At apply time the adapter pre-flights each source and fails if it is missing or its `status.phase` is not `Ready`.

```yaml
skills:
  - source: company-skills
    include: [refund-policy, escalation]
    mountAs: support
```

| Sub-field | Type | Description |
|---|---|---|
| `source` | string | SkillSource CRD name. Pattern `^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`. |
| `include` | array of string | Skill names to mount from the source. All skills mounted when omitted. |
| `mountAs` | string | Rename the mounted skill set. |

### `skillsConfig` (optional)

Maps to the PromptPack's `spec.skillsConfig`: how active skills are selected for a turn.

```yaml
skillsConfig:
  maxActive: 3
  selector: model-driven
```

| Sub-field | Type | Description |
|---|---|---|
| `maxActive` | integer | Maximum concurrently-active skills. Must be >= 1. |
| `selector` | string | Activation strategy: `model-driven`, `tag`, or `embedding`. |

### `externalAuth` (optional)

Data-plane authentication for the deployed agent, projected onto the AgentRuntime's `spec.externalAuth`. It holds up to four **independent** validators — `sharedToken`, `apiKeys`, `oidc`, and `edgeTrust` — evaluated with **OR** logic: a request is admitted if **any** configured validator accepts it.

**Dashboard-only by default.** When `externalAuth` is omitted — or present but with no validator configured — the agent is reachable **only** from the Omnia dashboard (the management plane) and serves no external traffic. To accept external traffic you must configure **at least one** validator.

```yaml
externalAuth:
  allowManagementPlane: true
  oidc:
    issuer: "https://auth.example.com/"
    audience: "omnia-agents"
    claimMapping:
      subject: sub
      role: groups
  sharedToken:
    secretRef: agent-shared-token
    trustEndUserHeader: true
```

| Sub-field | Type | Description |
|---|---|---|
| `allowManagementPlane` | boolean | Accept dashboard-minted management-plane tokens (the debug view). Defaults to `true` at the CRD. |
| `sharedToken` | object | Single shared bearer token. `secretRef` (required) names a workspace `Secret` holding the token under key `token`; `trustEndUserHeader` (boolean) optionally trusts an end-user identity header. |
| `apiKeys` | object | Per-caller API keys. `defaultRole` (one of `viewer`/`editor`/`admin`) and `trustEndUserHeader` (boolean). The key list lives in Secrets, not here. |
| `oidc` | object | Validate customer-issued JWTs. `issuer` and `audience` are both required; optional `claimMapping` overrides the `subject`/`role`/`endUser` claim names. |
| `edgeTrust` | object | Trust claim-headers injected by an upstream edge. Optional `headerMapping` (`subject`/`role`/`endUser`/`email`) and `claimsFromHeaders` (claim → header map). No required fields. |

At least **one** validator is required to serve external traffic. Any `Secret` referenced by `sharedToken.secretRef` (and the API-key Secrets) must already exist in the workspace — the adapter does not pre-flight them at plan time. The omnia controller validates Secret existence and fetches the OIDC discovery document at reconcile time.

### `runtime` (optional)

Resource sizing and autoscaling for AgentRuntime CRDs.

```yaml
runtime:
  replicas: 2
  cpu: "500m"
  memory: "512Mi"
  autoscaling:
    enabled: true
    type: hpa
    min_replicas: 1
    max_replicas: 10
    target_cpu_utilization: 70
    scale_down_stabilization_seconds: 300
```

| Sub-field | Type | Description |
|---|---|---|
| `replicas` | integer | Number of runtime replicas. Must be >= 1. Ignored when autoscaling is enabled. |
| `cpu` | string | CPU request in Kubernetes resource format (e.g., `"500m"`, `"1"`). |
| `memory` | string | Memory request in Kubernetes resource format (e.g., `"512Mi"`, `"1Gi"`). |
| `autoscaling` | object | Horizontal autoscaling (see below). |

#### `runtime.autoscaling`

Faithful passthrough to `spec.runtime.autoscaling` — only fields you set are emitted; omitted fields fall back to CRD defaults. An omitted block means the platform default applies (currently static replicas).

| Sub-field | Type | Description |
|---|---|---|
| `enabled` | boolean | Turn autoscaling on. When enabled, the autoscaler manages replica count. |
| `type` | string | `hpa` or `keda`. `keda` enables scale-to-zero but requires KEDA installed. |
| `min_replicas` | integer (>= 0) | Minimum replicas. |
| `max_replicas` | integer (>= 1) | Maximum replicas. Must not be below `min_replicas`. |
| `target_cpu_utilization` | integer (1-100) | Target average CPU utilization percentage. |
| `target_memory_utilization` | integer (1-100) | Target average memory utilization percentage. |
| `scale_down_stabilization_seconds` | integer (0-3600) | Stabilization window before scaling down. |

If `runtime` is omitted, the Omnia platform applies its own defaults.

### `labels` (optional)

Extra labels to apply to all created resources. These are merged with the adapter's managed labels.

```yaml
labels:
  team: platform
  environment: production
  cost-center: eng-42
```

User-supplied labels cannot override the adapter's managed labels. See [Resource Labels](/how-to/labels/) for details.

### `dry_run` (optional)

When set to `true`, the Apply operation generates a deployment preview without making any API calls. Resources are planned but not created.

```yaml
dry_run: true
```

Default: `false`. See [Use Dry-Run Mode](/how-to/dry-run/) for details.

## Validation

The adapter validates the configuration before any operation. Validation checks:

- `api_endpoint` is non-empty
- `workspace` is non-empty
- `providers` contains at least one binding with a non-empty `ref`; roles are valid and binding names unique
- An API token is available (from config or environment)
- Each `tools` handler is structurally valid (name pattern/uniqueness, type enum, per-type tool/config-or-selector rules)
- Each `skills` binding's `source` matches the SkillSource pattern; `skillsConfig` selector/`maxActive` are valid
- `runtime.replicas` is >= 1 and any `runtime.autoscaling` values are within range (if runtime is specified)
- If `externalAuth` is specified, each configured validator is structurally valid (`sharedToken.secretRef` non-empty, `apiKeys.defaultRole` a valid role, `oidc.issuer`/`oidc.audience` non-empty). Secret existence and OIDC discovery are checked by the controller at reconcile time, not at plan time.

Validation errors are returned as a list of human-readable messages.
