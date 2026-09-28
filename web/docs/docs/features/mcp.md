---
sidebar_position: 7
title: MCP Server (AI Access)
---

# MCP Server

SolidPing ships a built-in **Model Context Protocol (MCP)** server, letting AI assistants and LLM agents read and manage your monitoring data through a standard tool interface. Point an MCP-capable client (such as Claude) at SolidPing and it can list checks, inspect incidents, build status pages, and schedule maintenance — all scoped to a single organization.

If your dashboard is running, the fastest path is the **AI assistants** page — `/d/orgs/<org>/mcp`, linked from the main sidebar (opening the MCP endpoint URL in a browser also lands there). It has copy-paste snippets and one-click install buttons pre-filled with your instance's URL. This page covers the same ground for reference, plus the underlying protocol details.

## Connect with just the URL

SolidPing's MCP server supports the full **OAuth 2.1** authorization flow, including metadata discovery ([RFC 9728](https://datatracker.ietf.org/doc/html/rfc9728), [RFC 8414](https://datatracker.ietf.org/doc/html/rfc8414)) and dynamic client registration. In practice this means an OAuth-capable MCP client needs **only the server URL** — no manual token, no config file to hand-edit:

| Property | Value |
|----------|-------|
| URL | `{SP_BASE_URL}/api/v1/mcp` |
| Transport | JSON-RPC 2.0 over HTTP (Streamable HTTP) |
| Protocol version | `2025-06-18` (falls back to `2025-03-26` on request) |

Paste that URL into your client, log in when prompted, and approve the requested scope — that's the entire setup.

### Claude (claude.ai / desktop)

Claude doesn't yet have a public one-click install link for custom connectors, so add it manually:

1. Settings → Connectors → Add custom connector
2. Paste the URL above
3. Log in and approve the requested scope

### Claude Code

```bash
claude mcp add --transport http solidping {SP_BASE_URL}/api/v1/mcp
```

### Cursor

Cursor supports a one-click install deep link:

```
cursor://anysphere.cursor-deeplink/mcp/install?name=solidping&config=<base64 of {"url": "{SP_BASE_URL}/api/v1/mcp"}>
```

The dashboard's AI assistants page renders this as a ready-to-click button with your instance's URL already encoded.

### VS Code

VS Code supports a similar one-click install deep link (an `-insiders` variant exists for VS Code Insiders):

```
vscode:mcp/install?<url-encoded {"name": "solidping", "type": "http", "url": "{SP_BASE_URL}/api/v1/mcp"}>
```

Again, the dashboard's AI assistants page gives you a pre-filled button for both.

### Any other MCP client

Most remaining clients accept a `mcpServers` JSON block with just a URL — no headers, no token:

```json
{
  "mcpServers": {
    "solidping": {
      "url": "https://monitoring.example.com/api/v1/mcp"
    }
  }
}
```

Replace the URL with your `SP_BASE_URL`. The client discovers the OAuth endpoints itself (see below) and walks you through login.

## How the URL-only flow works

A remote-MCP-aware client resolves everything it needs from the server URL alone, via these discovery endpoints:

| Endpoint | Purpose |
|----------|---------|
| `/.well-known/oauth-protected-resource` | RFC 9728 — tells the client which authorization server protects this resource |
| `/.well-known/oauth-authorization-server` | RFC 8414 — the authorization server's metadata (endpoints, supported flows) |
| `/.well-known/openid-configuration` | Alias of the above, for clients that only probe the OIDC-style path |
| `/api/v1/oauth/register` | Dynamic client registration ([RFC 7591](https://datatracker.ietf.org/doc/html/rfc7591)) — the client registers itself, no pre-shared client ID needed |

The client then runs a standard OAuth 2.1 authorization-code flow with PKCE, ending on the dashboard's consent screen, where you approve the requested scope (`mcp` or `mcp:read` — see below).

### Confidential clients (client_secret)

Every native and browser-based MCP client above registers as a **public** client (`token_endpoint_auth_method: none`) — PKCE is its only authentication, and that is unaffected by anything below.

A client that registers with `token_endpoint_auth_method` set to `client_secret_post` or `client_secret_basic` is **confidential**: registration returns a `client_secret` once, and every subsequent call to the token endpoint must present it, either as a `client_secret` body field or as an HTTP Basic header (`client_id` as username, secret as password). A confidential client with a missing or wrong secret gets a `401 invalid_client` — the same response an unregistered `client_id` gets, so the token endpoint can't be used to enumerate registered clients.

:::warning Breaking change
Earlier versions minted and stored a secret for confidential clients but never checked it at the token endpoint. If you registered a confidential client before this changed, it now needs to actually send its secret. An operator who needs a short migration window can set the `oauth.enforce_client_secret` system parameter to `false`: a bad secret is then only logged (once per client, at WARN) and the token is still issued. Turn it back to `true` (the default) once every confidential client has been updated.
:::

## Authentication & Scopes

Every call needs a token, with one deliberate exception: the MCP **handshake**
(`initialize` and `notifications/initialized`) is answered without one, so MCP
directories and client autodiscovery can introspect the server before any login.
The handshake returns only public facts — the protocol version, the three
capability flags, and the server name and version — and mints no session.
Everything else, `tools/list` and `resources/list` included, answers
`401 NO_TOKEN` without a token.

Whichever path a token arrives by — OAuth consent or a manual PAT (below) — it must carry one of:

| Scope | Access |
|-------|--------|
| `mcp` | Full access — read and mutate |
| `mcp:read` | Read-only — list/get tools only; mutation tools are refused |

A token without either scope is rejected with a `403`. Read-only (`mcp:read`) tokens can use every listing and inspection tool; tools that change state require the full `mcp` scope. The OAuth consent screen shows which one a connecting client is requesting and renders a distinct read-only variant when it's `mcp:read`.

### Your role gates mutations too

Scope is only half the gate. A mutating tool also requires **at least the `user` role** in the organization you are calling — the same read-only floor the REST API applies. See [Member roles](/configuration/authentication#member-roles). A member with the `viewer` role can read everything and change nothing, so their tokens list and inspect fine but are refused on every create/update/delete/set tool, **even with the full `mcp` scope**:

```json
{
  "jsonrpc": "2.0",
  "id": 1,
  "error": {
    "code": -32002,
    "message": "Tool create_check requires the user role in this organization"
  }
}
```

Two consequences worth knowing:

- **Granting the `mcp` scope does not grant write access.** If your client reports this error while the consent screen clearly asked for full `mcp`, the token is fine — your role is the limit. Ask an org admin to raise it to `user`.
- **The role is read from your membership, not from the token.** Demoting someone to `viewer` disarms their existing MCP tokens on the very next tool call; there is nothing to revoke or re-issue.

So a refusal on a mutating tool means one of two things: the token carries only `mcp:read` (fix the token), or the person behind it is a `viewer` (fix the role). The message says which.

## Capabilities

The server exposes tools covering the core SolidPing surface, including:

- **Checks** — list, get, create, update, delete
- **Results** — list and filter check results
- **Incidents** — list and get
- **Integrations** — list and create notification connections
- **Status pages** — manage pages, sections, and resources
- **Maintenance windows** — list, create, update, and attach checks
- **Check groups & regions** — list
- **Check types** — list types, fetch sample configs, and validate a config
- **Diagnostics** — diagnose a check and inspect incident notifications

Every tool also declares MCP **tool annotations** (`readOnlyHint`,
`destructiveHint`, `idempotentHint`, `openWorldHint`) and an
**output schema**, so a client can tell reads from writes and knows
what a tool returns before calling it. List results come back as
`{data: [...]}` (with a `pagination` block where the tool pages),
matching the REST API.

## Headless agents / CI

The OAuth flow above needs an interactive login — fine for a person connecting their own client, not for a CI job or an unattended agent. For those, mint a **personal access token (PAT)** instead and pass it as a bearer token:

Create an API token from the dashboard or with the [CLI](/cli) (`sp tokens create`), then configure your client with an `Authorization` header:

```json
{
  "mcpServers": {
    "solidping": {
      "url": "https://monitoring.example.com/api/v1/mcp",
      "headers": {
        "Authorization": "Bearer <your-token>"
      }
    }
  }
}
```

Replace the URL with your `SP_BASE_URL` and supply a token carrying the `mcp` or `mcp:read` scope. The API itself accepts a `scopes` field on token creation (`mcp:read` for read-only); the dashboard's token page does not yet expose a scope picker in its UI, so a scoped token currently needs to be created via the API or CLI.

## Clients that run a command (stdio)

Some MCP clients only launch a command and talk to it over stdin/stdout. The [CLI](/cli) covers them: `sp mcp` relays each message to your instance's `/api/v1/mcp` endpoint with your credential and writes the replies back.

```json
{
  "mcpServers": {
    "solidping": {
      "command": "sp",
      "args": ["mcp"],
      "env": {
        "SOLIDPING_URL": "https://monitoring.example.com",
        "SP_TOKEN": "<your-token>"
      }
    }
  }
}
```

`SP_TOKEN` is a personal access token (see above). Leave it out to use the login saved by `sp auth login`, which `sp mcp` refreshes on its own when it expires. The server applies the same scopes and roles as over HTTP. Logs go to stderr, so they never mix with the protocol.

### Without a running server (`solidping mcp --stdio`)

The server binary can also be the MCP server itself, on its own database, with no HTTP listener and no token. This is what MCP directories that run a command in a container (Glama, `mcp-proxy`) need:

```json
["solidping", "mcp", "--stdio"]
```

It starts everything `solidping serve` starts except the HTTP listener: database, migrations, background jobs and check workers. Checks you create over MCP run and produce results. It reads the same configuration as `serve` (`SP_DB_TYPE`, `SP_DB_DIR`, `SP_DB_URL`, ...). Stdout carries only JSON-RPC, logs go to stderr, and the command exits when stdin closes.

**Who the session acts as.** There is no token: whoever can run the command already holds the database credentials. The session acts as the **owner of the default organization**. Two flags change that:

- `--org <slug>` picks another organization (its owner, the oldest one if there are several).
- `--user <email or uid>` acts as that member instead of the owner.

An unknown organization or user, a user who is not a member of the organization, or an organization with no owner stops the command with an error on stderr and a non-zero exit. The member's role still applies: a `viewer` can read but not write. On a fresh database the session acts as the seeded `admin@solidping.io`, and the pending password change does not block it.

With the published Docker image, override the entrypoint (the image runs `solidping serve`) and skip its HTTP healthcheck:

```json
{
  "mcpServers": {
    "solidping": {
      "command": "docker",
      "args": ["run", "-i", "--rm", "--no-healthcheck",
               "--entrypoint", "/app/solidping", "-v", "solidping-data:/data",
               "ghcr.io/fclairamb/solidping", "mcp", "--stdio", "--org", "default"]
    }
  }
}
```

## See also

The **AI assistants** page in the dashboard sidebar (`/d/orgs/<org>/mcp`) has the same instructions rendered with your instance's actual URL — copy buttons and one-click install links included.
