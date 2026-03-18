---
title: Reference
description: API and configuration reference for the Omnia adapter
---

This section contains detailed reference documentation for the Omnia deploy adapter.

## Contents

- [Configuration Reference](/reference/configuration/) -- complete JSON Schema and field-by-field documentation for all adapter configuration options.
- [Resource Types](/reference/resource-types/) -- the five managed Kubernetes resource types, their naming conventions, payload structures, and API operations.

## Adapter metadata

The adapter reports the following metadata via the `GetProviderInfo` operation:

| Field | Value |
|---|---|
| Name | `omnia` |
| Capabilities | `plan`, `apply`, `destroy`, `status` |
| Config schema | See [Configuration Reference](/reference/configuration/) |

The `import` capability is not yet supported.
