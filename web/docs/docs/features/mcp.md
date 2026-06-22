---
sidebar_position: 7
title: MCP Server (AI Access)
---

# MCP Server

SolidPing ships a built-in **Model Context Protocol (MCP)** server, letting AI assistants and LLM agents read and manage your monitoring data through a standard tool interface. Point an MCP-capable client (such as Claude) at SolidPing and it can list checks, inspect incidents, build status pages, and schedule maintenance — all scoped to a single organization.

## Endpoint

| Property | Value |
|----------|-------|
| URL | `POST {SP_BASE_URL}/api/v1/mcp` |
| Transport | JSON-RPC 2.0 over HTTP (Streamable HTTP) |
| Protocol version | `2025-03-26` |
| Authentication | Bearer token in the `Authorization` header |

## Authentication & Scopes

The MCP endpoint requires an authenticated token. Create an API token from the dashboard or with the [CLI](/cli) (`sp tokens create`), and grant it one of:

| Scope | Access |
|-------|--------|
| `mcp` | Full access — read and mutate |
| `mcp:read` | Read-only — list/get tools only; mutation tools are refused |

A token without either scope is rejected with a `403`.

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

Read-only (`mcp:read`) tokens can use every listing and inspection tool; tools that change state require the full `mcp` scope.

## Example Client Configuration

Most MCP clients accept an HTTP server URL and a bearer token. For example:

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

Replace the URL with your `SP_BASE_URL` and supply a token carrying the `mcp` or `mcp:read` scope.
