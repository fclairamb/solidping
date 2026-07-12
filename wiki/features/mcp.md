# Model Context Protocol (MCP)

A first-party MCP server lets an LLM client (Claude Desktop, the
Anthropic SDK, custom agents) talk to SolidPing as a *tool*. The LLM
can list checks, read incidents, propose changes, and run diagnostic
prompts — all over a single HTTP endpoint with the same auth and org
isolation as the REST API.

## Endpoint

Mounted at `/api/v1/mcp`
([`server/internal/app/server.go`](../../server/internal/app/server.go)).
JSON-RPC traffic goes over **POST**, behind `RequireMCPAuth`. The transport
is **MCP Streamable HTTP** (the spec's HTTP+SSE binding); requests carry a
JSON-RPC envelope and the server responds with either a single JSON-RPC
reply or an SSE stream depending on the request type.

The other methods are handled per the transport spec
([`mcp/handler.go`](../../server/internal/mcp/handler.go)):

- **GET** (unauthenticated): a client probing with
  `Accept: text/event-stream` gets `405` + `Allow` (we don't serve
  server-initiated SSE streams); anything else — a human in a browser —
  gets a `302` to the dashboard's MCP setup page (`/dash0/mcp?from=get`),
  which shows a "you opened the API endpoint in a browser" hint.
- **DELETE** (behind `RequireMCPAuth`): explicit session termination —
  deletes the session named by `Mcp-Session-Id` (404 if unknown or
  belonging to another org, 204 on success).

Unmatched paths under `/api/` never fall through to the SPA shell — they
answer the standard JSON error shape (`NOT_FOUND`).

The supported protocol version is **`2025-03-26`**
([`mcp/handler.go:39`](../../server/internal/mcp/handler.go)). Newer
versions (e.g. `2025-06-18` once `structuredContent` / `outputSchema`
are wired) get added to the front of the supported list as they're
adopted.

Protocol negotiation per MCP spec: if the client requests a version we
support, we echo it back; otherwise we return our latest. The client
is responsible for disconnecting if it can't speak what we returned.

## Authentication & scopes

MCP requests carry the same JWT or PAT as any other API call. Sessions
inherit the org from the token; the `orgSlug` is implicit.

Two custom scopes gate access:

| Scope | Permission |
|---|---|
| `mcp` | All tools (read + write). |
| `mcp:read` | Read-only tools only. The server denies any tool name beginning with `create_`, `update_`, `delete_`, or `set_` ([`scope.go:67`](../../server/internal/mcp/scope.go)). |

A token with **no scopes at all** (a full dashboard JWT) is treated as
having full access — back-compat for the dashboard's own MCP-over-PAT
flow that pre-dates the scope split. The deny-list (rather than
per-tool annotations) is intentional: every new write tool naturally
falls under one of the four prefixes, and a stray miss is a reviewable
oversight rather than a silent privilege escalation.

PAT tokens can be issued with `mcp:read` for safer agent embedding —
"let the assistant browse our infra without letting it create or
delete anything".

## Available tools

Tools are static, registered once at startup
([`mcp/tools.go`](../../server/internal/mcp/tools.go)). Current
inventory (this list goes stale; the source of truth is `registerTools()`):

**Checks**
- `list_checks`, `get_check`, `create_check`, `update_check`,
  `delete_check`
- `validate_check` — dry-run a config without persisting
- `diagnose_check` — fetch recent results, dependencies, recent
  incidents in one call for an LLM to reason over
- `list_check_types`, `get_check_type_samples` — discoverability
  surface so an LLM can pick a type and request a starter config

**Results**
- `list_results` — time-bounded; the server forces a default window
  rather than letting an LLM ask for "all results" and pull a year of
  rows

**Incidents**
- `list_incidents`, `get_incident`

**Connections (channels)**
- `list_connections`, `create_connection`

**Check groups**
- `list_check_groups`

**Regions**
- `list_regions`

**Status pages** (full CRUD)
- pages: `list_status_pages`, `get_status_page`, `create_status_page`,
  `update_status_page`, `delete_status_page`
- sections: `list_status_page_sections`, `create_status_page_section`,
  `update_status_page_section`, `delete_status_page_section`
- resources: `list_status_page_resources`,
  `create_status_page_resource`, `update_status_page_resource`,
  `delete_status_page_resource`

**Maintenance windows**
- `list_maintenance_windows`, `get_maintenance_window`,
  `create_maintenance_window`, `update_maintenance_window`,
  `delete_maintenance_window`, `set_maintenance_window_checks`

Tool descriptions are LLM-facing prose — they explain *when* to pick a
tool, not just what it does. Pagination (`limit` / `cursor`) is
exposed where relevant; `with` flags surface eager-load options.

## Prompts

The server registers three named prompts
([`mcp/prompts.go:34`](../../server/internal/mcp/prompts.go)). A prompt is
a parameterized message-template the client can request by name and
hand to the LLM; they're shortcuts for common multi-tool workflows.

| Prompt | Args | What it produces |
|---|---|---|
| `triage_incident` | `incidentUid` | Pulls the incident, its event timeline, and the affected check's recent results. Asks the model to summarize what's happening, the likely cause, and what to check next. |
| `summarize_org_health` | (none) | One-paragraph summary of the org's monitoring posture. |
| `draft_status_update` | `incidentUid`, optional `tone` | Drafts a customer-facing status-page update for human review. Tone is `technical` or `non-technical`. |

Prompts are not auto-published — they're seeds for a conversation
between the client and the LLM. The output never bypasses human
review.

## Sessions

Each successful `initialize` call mints a session ID
(`session.id`). Subsequent calls echo it in the `Mcp-Session-Id`
header. Sessions carry the negotiated protocol version, the client
info, the org slug, and timestamps.

Sessions expire after **1 hour of inactivity** (`sessionTTL`,
[`handler.go:33`](../../server/internal/mcp/handler.go)) and a cleanup
loop sweeps every 5 minutes. After expiry the client must re-initialize.

## Adding a tool

1. **Define the tool**: add a `…Def()` function in the matching
   `tools_<area>.go` file returning a `ToolDefinition` with `Name`,
   `Description`, and `InputSchema`. Use the schema helpers in
   `tools.go` (`objectSchema`, `stringProp`, `intProp`,
   `objectProp`).
2. **Implement the handler**: a method on `*Handler` with the
   signature `func(ctx context.Context, orgSlug string, args map[string]any) ToolCallResult`.
3. **Register it**: add a `{def, fn}` entry to the slice in
   `registerTools()` ([`tools.go:9`](../../server/internal/mcp/tools.go)).
4. **Pick the right name prefix** for the scope gate:
   - `list_…` / `get_…` / read verbs → mutation prefix detection skips it; `mcp:read` allowed.
   - `create_…` / `update_…` / `delete_…` / `set_…` → blocked from `mcp:read`.
5. **Use `objectProp("description")` for nested-object args** — the
   LLM will pass JSON; `stringProp` would force the model to stringify
   first.
6. **Limit response size**. Tools that can return many rows must accept
   `limit` (and ideally `cursor`) and clamp to a reasonable max
   (existing tools cap at 100). LLMs degrade fast on huge responses.
7. **Test**: add an entry to the matching `tools_<area>_test.go`. The
   test framework spins up the handler with a fake DB and invokes the
   tool over the JSON-RPC surface — same shape the LLM client uses.

## Caveats

- **Org isolation is per-token**. There is no "list other orgs" tool;
  the session's org is fixed at initialize time. To work in two orgs,
  open two sessions with two PATs.
- **No streaming partial results** today. A `list_*` tool returns the
  full slice (capped by `limit`); the SSE binding is used for the
  protocol envelope, not for chunked tool output.
- **`structuredContent` not yet wired.** Tools today return
  `Content` (textual). Once the next protocol version's
  `structuredContent` / `outputSchema` lands, the tools that already
  return structured shapes (counts, JSON results) can declare them
  and skip the JSON.stringify round-trip on the client side.

## Where to look in the code

| Concern | File |
|---|---|
| HTTP entrypoint, session lifecycle | [`server/internal/mcp/handler.go`](../../server/internal/mcp/handler.go) |
| JSON-RPC envelope and protocol types | [`server/internal/mcp/protocol.go`](../../server/internal/mcp/protocol.go) |
| Protocol version negotiation | [`server/internal/mcp/handler.go:45`](../../server/internal/mcp/handler.go) |
| Scope gating | [`server/internal/mcp/scope.go`](../../server/internal/mcp/scope.go) |
| Tool registry | [`server/internal/mcp/tools.go`](../../server/internal/mcp/tools.go) |
| Tool implementations | [`server/internal/mcp/tools_*.go`](../../server/internal/mcp/) |
| Prompts | [`server/internal/mcp/prompts.go`](../../server/internal/mcp/prompts.go) |

## Origin

The MCP surface shipped across more than a dozen specs in May 2026.
Highlights:

- [`2026-05-03-25-mcp-diagnose-check.md`](../../specs/done/2026/05/2026-05-03-25-mcp-diagnose-check.md) — the diagnose tool
- [`2026-05-03-26-mcp-incident-events.md`](../../specs/done/2026/05/2026-05-03-26-mcp-incident-events.md) — incident timeline access
- [`2026-05-03-27-mcp-status-pages.md`](../../specs/done/2026/05/2026-05-03-27-mcp-status-pages.md) — status-page CRUD
- [`2026-05-03-28-mcp-maintenance-windows.md`](../../specs/done/2026/05/2026-05-03-28-mcp-maintenance-windows.md)
- [`2026-05-03-29-mcp-check-types-samples-validate.md`](../../specs/done/2026/05/2026-05-03-29-mcp-check-types-samples-validate.md)
- [`2026-05-03-30-mcp-tighten-tool-descriptions.md`](../../specs/done/2026/05/2026-05-03-30-mcp-tighten-tool-descriptions.md)
- [`2026-05-03-34-mcp-prompts.md`](../../specs/done/2026/05/2026-05-03-34-mcp-prompts.md)
- [`2026-05-03-38-mcp-scoped-tokens.md`](../../specs/done/2026/05/2026-05-03-38-mcp-scoped-tokens.md) — `mcp:read` scope
- [`2026-05-03-39-mcp-protocol-version-negotiation.md`](../../specs/done/2026/05/2026-05-03-39-mcp-protocol-version-negotiation.md)
