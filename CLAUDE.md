# Omnia Deploy Adapter - Claude Code Project Instructions

## Project Overview

This is the Omnia Kubernetes deploy adapter for PromptKit. It implements the `deploy.Provider` interface as a JSON-RPC 2.0 subprocess that PromptKit discovers and invokes to manage Omnia CRD resources (PromptPack, AgentRuntime, ToolRegistry, AgentPolicy, ConfigMap) in a Kubernetes cluster via the Omnia Management API.

## Git Workflow

- **Never push directly to main** — use feature branches.
- Branch naming: `feat/<description>`, `fix/<description>`, or `feature/<issue-number>-<short-description>`.
- Standard flow: branch → commit → push with `-u` → create PR via `gh pr create` → monitor CI → merge via `gh pr merge --squash`.
- Use conventional commits (`feat:`, `fix:`, `chore:`, `ci:`, `docs:`).
- When continuing a previous session, check `git status`, `git log --oneline -5`, and any existing plan files before taking action.

## Build & Test Commands

All commands use `GOWORK=off` because the module depends on a sibling PromptKit checkout via `replace` directives.

```bash
# Format code with goimports
make fmt

# Run golangci-lint
make lint

# Run tests with race detector
make test

# Build binary
make build

# Run all quality checks (fmt + lint + test + build)
make check

# Manual equivalents
GOWORK=off goimports -w -local github.com/AltairaLabs/promptarena-deploy-omnia .
GOWORK=off golangci-lint run ./...
GOWORK=off go test ./... -race -count=1
GOWORK=off go build -o promptarena-deploy-omnia .

# Run the adapter (JSON-RPC over stdio)
echo '{"jsonrpc":"2.0","method":"get_provider_info","id":1}' | ./promptarena-deploy-omnia
```

## Project Structure

| Path | Purpose |
|------|---------|
| `main.go` | Entry point — thin wrapper calling `adaptersdk.Serve(provider)` |
| `internal/omnia/provider.go` | `Provider`, factories, `GetProviderInfo`, `ValidateConfig`, `Import` |
| `internal/omnia/config.go` | Config parsing, validation, JSON Schema definition |
| `internal/omnia/plan.go` | Plan generation — diffs desired resources vs prior state |
| `internal/omnia/apply.go` | Apply — creates resources in 5-phase dependency order |
| `internal/omnia/destroy.go` | Destroy — teardown in reverse dependency order |
| `internal/omnia/status.go` | Status — health checks via Omnia API |
| `internal/omnia/builders.go` | Request body builders for each CRD type |
| `internal/omnia/client.go` | `omniaClient` interface and response types |
| `internal/omnia/client_real.go` | Real HTTP implementation of `omniaClient` |
| `internal/omnia/client_simulated_test.go` | Simulated client for unit tests |
| `internal/omnia/state.go` | `AdapterState` and `ResourceState` type definitions |
| `internal/omnia/naming.go` | K8s DNS name sanitization (253-char limit) |
| `internal/omnia/labels.go` | Kubernetes label generation (managed-by, pack metadata) |
| `internal/omnia/errors.go` | `DeployError` with automatic HTTP/message classification |
| `internal/omnia/version.go` | Build-time version variables (ldflags) |
| `.golangci.yml` | Linter configuration (25 linters) |
| `Makefile` | Build targets: fmt, lint, test, build, check |

## Go Code Standards

- **Cognitive complexity**: Keep functions below **15** (enforced by `gocognit` linter). Proactively extract helper functions.
- **Line length**: Max 120 characters (golangci-lint `lll`).
- **Magic numbers**: The `mnd` linter flags magic numbers in arguments, cases, conditions, operations, returns, and assignments. Extract to named constants.
- **Duplicated strings**: Extract string literals used 2+ times into constants (`goconst` min-occurrences: 2).
- **Formatting**: `gofmt` and `goimports` are enforced (local prefix: `github.com/AltairaLabs/promptarena-deploy-omnia`).
- **Test exclusions**: The `.golangci.yml` relaxes `mnd`, `gocognit`, `goconst`, `gosec`, `lll`, `gocritic`, `gochecknoinits`, `unused`, `errcheck`, `staticcheck`, `govet`, `whitespace`, and `revive` for `*_test.go` files.
- **Test coverage**: All changed files must have >= 80% coverage. Write tests for error paths and edge cases, not just happy paths.

## Key Architecture Patterns

### Factory Injection

The `Provider` struct holds an `omniaClientFactory` function field (`clientFunc`) that creates `omniaClient` instances. Production uses `newHTTPClient`; tests inject simulated clients via the factory. This avoids interface mocking and keeps test setup simple.

### adaptersdk Integration

The adapter is a JSON-RPC 2.0 subprocess, not a CLI tool. `main.go` calls `adaptersdk.Serve(provider)` which handles the JSON-RPC protocol over stdio. The `Provider` implements `deploy.Provider` with methods: `GetProviderInfo`, `ValidateConfig`, `Plan`, `Apply`, `Destroy`, `Status`, `Import`.

### 5-Phase Apply

Apply creates resources in strict dependency order:

| Phase | Step | Resource Type | Description |
|-------|------|---------------|-------------|
| 0 | ConfigMap | `configmap` | Pack JSON data stored in a ConfigMap |
| 1 | PromptPack | `prompt_pack` | PromptPack CRD referencing the ConfigMap |
| 2 | ToolRegistry | `tool_registry` | Tool definitions (only if pack has tools) |
| 3 | AgentPolicy | `agent_policy` | Tool blocklist policy (only if pack has tool policy) |
| 4 | AgentRuntime | `agent_runtime` | Agent runtime(s) referencing pack, tools, and policy |

Destroy runs in reverse order: `agent_runtime` → `agent_policy` → `tool_registry` → `prompt_pack` → `configmap`.

### Resource Naming

All resource names are sanitized to valid K8s DNS subdomain names (lowercase, hyphens, max 253 chars) via `sanitizeName()`.

### Kubernetes Labels

Every resource gets four managed labels (`app.kubernetes.io/managed-by`, `promptkit.altairalabs.ai/pack-id`, `pack-version`, `resource-type`) plus optional user-supplied labels from config. Managed labels always take precedence.

## Testing Patterns

- **Unit tests**: Use simulated `omniaClient` injection via `clientFunc` factory on `Provider`.
- **Table-driven tests** for config validation, naming sanitization, and resource planning.
- **Event-driven tests** for Apply/Destroy — collect callback events, assert ordering and content.
- **JSON-RPC protocol tests**: Use `adaptersdk.ServeIO()` to run the adapter in-process.

## Pre-commit / CI

CI mirrors what `make lint` and `make test` enforce. Run `make check` before committing to catch all issues locally. **Never use `--no-verify`** to skip pre-commit hooks.

## SonarCloud Quality Gate (CI)

SonarCloud runs on every PR and enforces quality on **new code only**:

| Metric | Threshold |
|--------|-----------|
| Coverage | >= 80% on new/changed lines |
| Duplicated lines | <= 3% |
| Reliability rating | A (no new bugs) |
| Security rating | A (no new vulnerabilities) |
| Maintainability rating | A (no new code smells, includes cognitive complexity) |

## Dependencies: Sibling PromptKit Checkout

This repo depends on `github.com/AltairaLabs/PromptKit/runtime` and `github.com/AltairaLabs/PromptKit/pkg` via `replace` directives in `go.mod` pointing to `../promptkit/runtime` and `../promptkit/pkg`. Ensure the sibling checkout exists:

```bash
git clone git@github.com:AltairaLabs/PromptKit.git ../promptkit
```
