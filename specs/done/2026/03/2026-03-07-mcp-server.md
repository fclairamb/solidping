# MCP Server for SolidPing

## Goal

Provide a Model Context Protocol (MCP) endpoint within the existing SolidPing backend that exposes monitoring data and operations to AI assistants (Claude, Cursor, VS Code Copilot, etc.). The endpoint uses the existing PAT token authentication -- each client connects with their own token, which determines the user and organization context.

This enables AI assistants to directly query checks, inspect incidents, review monitoring results, and manage notification connections through natural language, without users needing to navigate the dashboard for routine operations.

## Architecture

The MCP server is **not** a separate process. It is a route handler registered in `server.go`, always active, at `/api/v1/mcp`. It reuses:

- The existing `RequireAuth` middleware for PAT token validation
- The user and organization resolved from the PAT token context (no `{org}` in URL)
- The existing service layer (checks, results, incidents, connections, etc.) for all business logic
- The existing bunrouter infrastructure, CORS, and logging middleware

This means zero new deployment steps -- any SolidPing instance automatically has MCP support.

## Authentication

Clients authenticate by passing their PAT token in the MCP request headers. The existing `RequireAuth` middleware extracts the token, validates it via `authService.ValidatePATToken()`, and sets the user + organization in the request context.

Since the MCP endpoint is at `/api/v1/mcp` (not under `/orgs/{org}`), the organization is derived from the PAT token's user, not from the URL. The handler reads the organization from the authenticated claims context:

```go
claims := base.GetClaimsFromContext(ctx)
orgSlug := claims.OrgSlug
```

```
Client -> POST /api/v1/mcp
Authorization: Bearer pat_xxxxx
```

## Technology

- **Language**: Go -- same handler as everything else
- **No external MCP SDK**: The Streamable HTTP protocol is JSON-RPC 2.0 over HTTP POST. Go's standard library (`net/http`, `encoding/json`) handles this. The tool registry is a map of handler functions.
- **Transport**: [Streamable HTTP](https://modelcontextprotocol.io/specification/2025-03-26/basic/transports#streamable-http) (MCP spec 2025-03-26)
- **No new dependencies**

### Why no external MCP SDK

The Streamable HTTP transport for a simple tool server boils down to:
1. One HTTP endpoint accepting JSON-RPC POST requests
2. Responses as `application/json` (all tools are short-lived API calls)
3. Session management via `Mcp-Session-Id` header

The tool/resource registry is a map. Adding `mcp-go` would bring more abstraction than needed.

## Transport: Streamable HTTP

### Endpoint

`POST /api/v1/mcp` -- single endpoint for all MCP communication.

No `GET` (server-initiated SSE) or `DELETE` (session termination) needed initially. All tool calls are synchronous request/response.

### Request/Response Flow

1. Client sends JSON-RPC message as POST body with `Authorization: Bearer pat_xxx`
2. `RequireAuth` middleware validates the token, sets user + org in context
3. MCP handler dispatches the JSON-RPC method to the appropriate handler
4. Handler calls the existing service layer using the org/user from context
5. Response returned as `Content-Type: application/json`

For notifications (`notifications/initialized`), the server returns `202 Accepted`.

### Session Management

1. On `initialize` request, server generates a session ID (UUID) and returns it in `Mcp-Session-Id` response header
2. Client includes `Mcp-Session-Id` on subsequent requests
3. Sessions are stored in memory (`sync.Map`) with a configurable TTL (default: 1 hour)
4. Session stores: protocol version, client info, capabilities
5. Requests without a valid session ID (after initialization) return `400`

### Initialization Handshake

```
POST /api/v1/mcp
Authorization: Bearer pat_demo
Content-Type: application/json
Accept: application/json, text/event-stream

{
  "jsonrpc": "2.0",
  "id": 1,
  "method": "initialize",
  "params": {
    "protocolVersion": "2025-03-26",
    "capabilities": {},
    "clientInfo": { "name": "claude-code", "version": "1.0.0" }
  }
}

-> 200 OK
Mcp-Session-Id: <uuid>
Content-Type: application/json

{
  "jsonrpc": "2.0",
  "id": 1,
  "result": {
    "protocolVersion": "2025-03-26",
    "capabilities": {
      "tools": {},
      "resources": {}
    },
    "serverInfo": { "name": "solidping", "version": "0.1.0" }
  }
}
```

## Code Layout

```
back/internal/mcp/
  handler.go          -- HTTP handler, session management, JSON-RPC dispatch
  handler_test.go     -- Handler tests
  protocol.go         -- JSON-RPC types, MCP protocol structs
  tools.go            -- Tool definitions and dispatch table
  tools_checks.go     -- list_checks, get_check, create_check, update_check, delete_check
  tools_results.go    -- list_results
  tools_incidents.go  -- list_incidents, get_incident
  tools_connections.go -- list_connections, create_connection
  tools_groups.go     -- list_check_groups
  tools_regions.go    -- list_regions
  resources.go        -- Resource definitions and read handlers
```

## Route Registration

In `internal/app/server.go`, the MCP endpoint is registered under the `api` group with auth middleware:

```go
// MCP endpoint (auth via PAT token, org derived from token)
mcpHandler := mcp.NewHandler(s.services)
api.POST("/mcp", authMiddleware.RequireAuth(mcpHandler.Handle))
```

The MCP handler receives the services it needs via constructor injection, same as other handlers:

```go
func NewHandler(services *services.Registry) *Handler {
    return &Handler{services: services}
}
```

## JSON-RPC Layer

Minimal JSON-RPC 2.0 types using `encoding/json`:

```go
type Request struct {
    JSONRPC string          `json:"jsonrpc"`
    ID      any             `json:"id,omitempty"`
    Method  string          `json:"method"`
    Params  json.RawMessage `json:"params,omitempty"`
}

type Response struct {
    JSONRPC string `json:"jsonrpc"`
    ID      any    `json:"id,omitempty"`
    Result  any    `json:"result,omitempty"`
    Error   *Error `json:"error,omitempty"`
}

type Error struct {
    Code    int    `json:"code"`
    Message string `json:"message"`
    Data    any    `json:"data,omitempty"`
}
```

Standard error codes: `-32700` (parse error), `-32600` (invalid request), `-32601` (method not found), `-32602` (invalid params), `-32603` (internal error).

## Tools

All tools call the existing service layer methods. The user and organization are extracted from the request context (set by `RequireAuth`).

### `list_checks`

List monitoring checks for the organization.

| Name | Type | Required | Description |
|------|------|----------|-------------|
| `q` | string | no | Search query (name or slug substring) |
| `labels` | string | no | Label filter (`key:value,key2:value2`) |
| `checkGroupUid` | string | no | Filter by check group UID or slug |
| `with` | string | no | Include extra fields: `lastResult`, `lastStatusChange` (comma-separated) |
| `limit` | number | no | Max results (1-100, default 20) |
| `cursor` | string | no | Pagination cursor from previous response |

**Returns:** `{ data: [...], pagination: { total, cursor, limit } }`

### `get_check`

Get a single check by UID or slug.

| Name | Type | Required | Description |
|------|------|----------|-------------|
| `identifier` | string | yes | Check UID or slug |
| `with` | string | no | Include extra fields: `lastResult`, `lastStatusChange` |

**Returns:** Check object with config, labels, regions, period, enabled status.

### `create_check`

Create a new monitoring check.

| Name | Type | Required | Description |
|------|------|----------|-------------|
| `name` | string | no | Human-readable name (auto-generated from URL if omitted) |
| `slug` | string | no | URL-friendly identifier (auto-generated if omitted) |
| `type` | string | no | `http`, `tcp`, `icmp`, `dns`, `ssl`, `heartbeat`, `domain` (inferred from config if omitted) |
| `config` | object | yes | Check-specific config (e.g., `{"url": "https://example.com"}`) |
| `regions` | string[] | no | Region slugs (e.g., `["eu-west-1", "us-east-1"]`) |
| `enabled` | boolean | no | Default `true` |
| `period` | string | no | Check interval (e.g., `"00:00:30"` for 30s, default `"00:01:00"`) |
| `labels` | object | no | Key-value labels (e.g., `{"env": "production"}`) |
| `description` | string | no | Free-text description |
| `checkGroupUid` | string | no | Assign to a check group |

**Returns:** Created check object.

### `update_check`

Update an existing check by UID or slug. Only provided fields are modified (PATCH semantics).

| Name | Type | Required | Description |
|------|------|----------|-------------|
| `identifier` | string | yes | Check UID or slug |
| `name` | string | no | New name |
| `slug` | string | no | New slug |
| `config` | object | no | Updated config |
| `regions` | string[] | no | Updated regions |
| `enabled` | boolean | no | Enable/disable |
| `period` | string | no | New check interval |
| `labels` | object | no | Replace labels (empty object clears) |
| `description` | string | no | Updated description |
| `checkGroupUid` | string | no | Move to different group (empty string to ungroup) |

**Returns:** Updated check object.

### `delete_check`

Delete a check by UID or slug (soft delete).

| Name | Type | Required | Description |
|------|------|----------|-------------|
| `identifier` | string | yes | Check UID or slug |

**Returns:** Confirmation message.

**Error:** Returns error if the check has active incidents (`CHECK_HAS_ACTIVE_INCIDENTS`).

### `list_results`

Query monitoring results with flexible filtering.

| Name | Type | Required | Description |
|------|------|----------|-------------|
| `checkUid` | string | no | Comma-separated check UIDs or slugs |
| `checkType` | string | no | Comma-separated types: `http`, `dns`, `icmp`, etc. |
| `status` | string | no | Comma-separated: `up`, `down`, `unknown` |
| `region` | string | no | Comma-separated region slugs |
| `periodType` | string | no | Comma-separated: `raw`, `hour`, `day`, `month` |
| `periodStartAfter` | string | no | RFC3339 timestamp (inclusive lower bound) |
| `periodEndBefore` | string | no | RFC3339 timestamp (exclusive upper bound) |
| `with` | string | no | Extra fields: `durationMs`, `durationMinMs`, `durationMaxMs`, `region`, `metrics`, `output`, `availabilityPct`, `checkSlug`, `checkName` |
| `size` | number | no | Max results (1-100, default 20) |
| `cursor` | string | no | Pagination cursor |

**Returns:** `{ data: [...], pagination: { total, cursor, size } }`

### `list_incidents`

List incidents with filtering.

| Name | Type | Required | Description |
|------|------|----------|-------------|
| `checkUid` | string | no | Comma-separated check UIDs |
| `state` | string | no | Comma-separated: `active`, `resolved` |
| `since` | string | no | RFC3339 timestamp (started after) |
| `until` | string | no | RFC3339 timestamp (started before) |
| `with` | string | no | `check` to include check details |
| `size` | number | no | Max results (1-100, default 20) |
| `cursor` | string | no | Pagination cursor |

**Returns:** `{ data: [...], pagination: { cursor, size } }`

### `get_incident`

Get a single incident by UID.

| Name | Type | Required | Description |
|------|------|----------|-------------|
| `uid` | string | yes | Incident UID |
| `with` | string | no | `check` to include check details |

**Returns:** Incident object with check details, state, failure count, timestamps.

### `list_connections`

List notification connections (Slack, webhook, email).

| Name | Type | Required | Description |
|------|------|----------|-------------|
| `type` | string | no | Filter by type: `slack`, `webhook`, `email` |

**Returns:** `{ data: [...] }`

### `create_connection`

Create a new notification connection.

| Name | Type | Required | Description |
|------|------|----------|-------------|
| `type` | string | yes | `slack`, `webhook`, `email` |
| `name` | string | yes | Display name |
| `enabled` | boolean | no | Default `true` |
| `isDefault` | boolean | no | Auto-attach to new checks |
| `settings` | object | no | Type-specific settings (e.g., `{"webhookUrl": "..."}` for webhook) |

**Returns:** Created connection object.

### `list_check_groups`

List all check groups for the organization.

**Parameters:** None.

**Returns:** `{ data: [...] }` with uid, name, slug, description, sortOrder, checkCount.

### `list_regions`

List available monitoring regions for the organization.

**Parameters:** None.

**Returns:** `{ data: [...], defaultRegions: [...] }` with slug, emoji, name per region.

## Resources

### `solidping://organization`

Returns the current organization's metadata (slug, name).

### `solidping://regions`

Returns the full list of globally defined monitoring regions.

## Error Handling

Service layer errors are translated to JSON-RPC errors:

| Service Error | JSON-RPC Code | Message |
|--------------|---------------|---------|
| Not found | -32003 | "Resource not found" |
| Validation | -32602 | Validation details |
| Forbidden | -32002 | "Insufficient permissions" |
| Conflict | -32004 | "Resource conflict" |
| Internal | -32603 | "Internal server error" |

The error `code` and `detail` from the service layer are included in the JSON-RPC `data` field.

## MCP Client Configuration

### Claude Code (`.mcp.json`)

```json
{
  "mcpServers": {
    "solidping": {
      "type": "streamable-http",
      "url": "https://app.solidping.com/api/v1/mcp",
      "headers": {
        "Authorization": "Bearer pat_xxxxx"
      }
    }
  }
}
```

The PAT token is passed by the client. The server resolves user and organization from it.

## Example Interactions

- "What checks do I have?" -> `list_checks(with: "lastResult,lastStatusChange")`
- "Create an HTTP check for api.example.com every 30s in Europe and US" -> `create_check(config: {url: "https://api.example.com"}, period: "00:00:30", regions: ["eu-west-1", "us-east-1"])`
- "Show me all incidents from the last 24 hours" -> `list_incidents(since: "2026-03-06T12:00:00Z", with: "check")`
- "Disable the check called api-health" -> `update_check(identifier: "api-health", enabled: false)`
- "What notification connections do I have?" -> `list_connections()`
- "Show me the hourly availability for my-api over the last week" -> `list_results(checkUid: "my-api", periodType: "hour", periodStartAfter: "2026-02-28T00:00:00Z", with: "availabilityPct,durationMs")`
- "Are there any active incidents?" -> `list_incidents(state: "active", with: "check")`

## Implementation Order

1. JSON-RPC types and helpers (`internal/mcp/protocol.go`)
2. MCP handler with session management and method dispatch (`internal/mcp/handler.go`)
3. Register `POST /api/v1/mcp` route in `server.go`
4. MCP lifecycle methods: `initialize`, `notifications/initialized`, `tools/list`, `resources/list`
5. Read-only tools: `list_checks`, `get_check`, `list_results`, `list_regions`, `list_check_groups`
6. `list_incidents`, `get_incident`, `list_connections`
7. Write tools: `create_check`, `update_check`, `delete_check`, `create_connection`
8. Resources: `solidping://organization`, `solidping://regions`
9. Tests with testcontainers
10. Update CLAUDE.md with MCP endpoint info

## Future Extensions

- **SSE streaming** -- `text/event-stream` responses for real-time incident notifications
- **GET endpoint** -- server-initiated messages
- **DELETE endpoint** -- explicit session termination
- **Status page tools** -- CRUD for status pages, sections, and resources
- **Member management tools** -- list, add, update, remove org members
- **Check-connection tools** -- manage which connections are attached to which checks
- **Upsert check** -- idempotent check creation via `upsert_check`
- **Event timeline** -- `list_events` for audit trail access
- **Prompts** -- pre-built prompt templates (e.g., "incident summary", "availability report")
- **Session persistence** -- move sessions from `sync.Map` to database cache for multi-node deployments

---

**Status**: Draft | **Created**: 2026-03-07
