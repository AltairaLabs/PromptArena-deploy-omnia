---
title: Log In with the Browser
description: Autoconfigure the Omnia deploy profile with promptarena deploy login
---

`promptarena deploy login` opens your browser, authenticates you against Omnia, lets you pick a workspace, and writes the resolved deploy profile back into your arena config — so you don't hand-assemble `api_endpoint`, `workspace`, providers, and skills yourself. The scoped token it issues is stored separately from the config, so no secret lands in a committed file.

## Prerequisites

- An Omnia dashboard you can sign in to (any OIDC identity provider the dashboard is wired to).
- `api_endpoint` set in your `deploy.config` so the CLI knows which Omnia to authenticate against — or the `OMNIA_DASHBOARD_URL` environment variable (see [Pointing at a different dashboard](#pointing-at-a-different-dashboard)).

A minimal starting config is enough:

```yaml
deploy:
  provider: omnia
  config:
    api_endpoint: "https://omnia.example.com"
```

## Run it

```bash
promptarena deploy login --provider omnia
```

The CLI:

1. Builds the authorize URL from your `api_endpoint` (the dashboard's `/api/cli/authorize` route) and opens it in your browser.
2. You authenticate (OIDC) and **pick a workspace** in the dashboard.
3. The dashboard redirects back to a local loopback callback with a one-time code.
4. The CLI exchanges that code (back-channel, via `/api/cli/token`) for a **scoped token** and the **assembled deploy profile**.

## What it writes, and where

| Artifact | Destination |
|---|---|
| Resolved profile — `endpoint`, `workspace`, and the workspace's `providers` and `skills` | Your arena config's `deploy.config` block |
| The scoped bearer token | `~/.promptarena/credentials` (never written into the config) |

Keeping the token out of `deploy.config` means the config stays safe to commit. The deploy commands (`plan`, `apply`, `destroy`) read the token from the credentials store automatically — you don't set `OMNIA_API_TOKEN` or `api_token` yourself after logging in.

If the returned profile lists providers but none is named `default`, the adapter promotes the first to `default` so the runtime has a deliberate primary provider. (See the [`default` is the primary](/explanation/configuration-mapping/#name-is-a-logical-key-and-default-is-the-primary) note.)

## Pointing at a different dashboard

The login routes (`/api/cli/authorize`, `/api/cli/token`) live in the **dashboard**, which may be a different origin than the management-API `api_endpoint` — for example a local dev server. Set `OMNIA_DASHBOARD_URL` to override the base used for login:

```bash
export OMNIA_DASHBOARD_URL="http://localhost:3000"
promptarena deploy login --provider omnia
```

When `OMNIA_DASHBOARD_URL` is set it takes precedence over `api_endpoint` for the login flow only. When it is unset, `api_endpoint` from `deploy.config` is used — and login fails fast if neither is available.

## After logging in

Your config now has the workspace binding filled in and your token is in the credentials store. You can go straight to a plan:

```bash
promptarena deploy plan
```

See [Configure the Adapter](/how-to/configure/) for every field the profile can populate, and [Configuration Mapping](/explanation/configuration-mapping/) for how those fields become Omnia resources.
