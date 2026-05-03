# MCP — expose status pages (CRUD + sections + resources)

## Context

Status pages are one of the two REST surfaces an AI assistant would most naturally drive on a human's behalf — *"post a status update saying we're investigating elevated error rates on the API"*. The handler exists at `server/internal/handlers/statuspages/service.go` with a full CRUD surface (pages, sections, resources — see top-level functions at lines 198, 245, 288, 326, 376, 393, 415, 451, 468, 514, 533, 560). REST routes are wired at `/api/v1/orgs/$org/status-pages` per the project CLAUDE.md.

The MCP surface today (`server/internal/mcp/tools.go:9-22`) exposes none of this. So the value story stops at read-only triage — the LLM can find out what's broken but can't help draft the customer-facing communication.

## Honest opinion

Three design questions worth being explicit about:

1. **Tool granularity.** Status pages have nested sections and per-section resources. Should we expose 9 tools (page CRUD × 3 + section CRUD × 3 + resource CRUD × 3), or fewer "fat" tools that take nested payloads? **Recommendation: expose them flat (9 tools)** — matches REST shape, mirrors `list_checks` / `create_check` style, and gives the LLM clear single-purpose handles. Fat nested tools sound elegant but make the schema unreadable to the LLM.

2. **Publish workflow.** The "post a status update" use case really wants two things: (a) create/update a status page section narrative, (b) confirm it's visible publicly. Visibility is determined by `enabled` flags on the page. We don't need a special "publish" tool — `update_status_page_section` with the right body covers it. Keep it simple.

3. **Don't add a "draft narrative" prompt here.** That's the job of the prompts spec (`2026-05-03-34-mcp-prompts.md`). This spec is just the data-access plumbing.

## Scope

**In:**
- 9 MCP tools wrapping the existing `statuspages.Service`:
  - `list_status_pages`, `get_status_page`, `create_status_page`, `update_status_page`, `delete_status_page`
  - `list_status_page_sections`, `create_status_page_section`, `update_status_page_section`, `delete_status_page_section`
  - `list_status_page_resources`, `create_status_page_resource`, `update_status_page_resource`, `delete_status_page_resource`

  *(Counted as "9 tools" rhetorically; actual count is 13 with all CRUD slots. If trimming for v1, drop the `get_*` variants for sections and resources since `list_*` is enough — bringing the surface to 11 tools. This spec assumes the trimmed 11.)*

- Wire `statuspages.Service` into `Handler` constructor.
- New file `server/internal/mcp/tools_statuspages.go` containing all definitions and handlers.
- Tests covering each tool's happy path and a representative error path.

**Out:**
- Status page rendering / preview generation. REST already returns the structured data; rendering is the dashboard's concern.
- Bulk operations (e.g. "set all sections to operational"). YAGNI.
- Webhook/notification side-effects on update — those are already handled by the service layer.

## Tool surface

Naming convention: `{verb}_status_page[_section|_resource]`. Mirrors `list_checks` / `create_check` style.

### Pages

| Tool | Required args | Optional args |
|---|---|---|
| `list_status_pages` | — | (none — small list per org) |
| `get_status_page` | `identifier` (UID or slug) | — |
| `create_status_page` | `slug`, `name` | `description`, `enabled`, `visibility` (`public`\|`private`), `customDomain`, ... (mirror `CreateStatusPageRequest` from `statuspages/service.go`) |
| `update_status_page` | `identifier` | any of the create fields (PATCH semantics) |
| `delete_status_page` | `identifier` | — |

### Sections

| Tool | Required args | Optional args |
|---|---|---|
| `list_status_page_sections` | `pageIdentifier` | — |
| `create_status_page_section` | `pageIdentifier`, `name` | `description`, `position`, `slug` |
| `update_status_page_section` | `pageIdentifier`, `sectionUid` | `name`, `description`, `position` |
| `delete_status_page_section` | `pageIdentifier`, `sectionUid` | — |

### Resources (the per-section list of monitored items)

| Tool | Required args | Optional args |
|---|---|---|
| `list_status_page_resources` | `pageIdentifier`, `sectionUid` | — |
| `create_status_page_resource` | `pageIdentifier`, `sectionUid`, `checkUid` | `displayName`, `position` |
| `update_status_page_resource` | `pageIdentifier`, `sectionUid`, `resourceUid` | `displayName`, `position` |
| `delete_status_page_resource` | `pageIdentifier`, `sectionUid`, `resourceUid` | — |

Pull exact field names by reading `CreateStatusPageRequest`, `CreateSectionRequest`, `CreateResourceRequest` etc. in `server/internal/handlers/statuspages/service.go` (the request structs are defined alongside the methods they feed).

## Implementation

### Wire service

`server/internal/mcp/handler.go:42-79`:

```go
type Handler struct {
    // ... existing fields
    statusPagesSvc *statuspages.Service
}

// in NewHandler:
statusPagesSvc: statuspages.NewService(dbService),
```

`statuspages.NewService(dbService)` is at `server/internal/handlers/statuspages/service.go:58`.

### Tool file

`server/internal/mcp/tools_statuspages.go` — follow the exact pattern of `tools_checks.go`:
- one `xxxDef()` function per tool returning a `ToolDefinition`
- one `(h *Handler) toolXxx()` method per tool delegating to `h.statusPagesSvc.*`
- argument extraction via the existing `getStringArg` / `getMapArg` / `getBoolArg` / etc. helpers (`handler.go:249-333`)
- response via `marshalResult()` (or `structuredContent` once spec `2026-05-03-33-mcp-structured-content-output.md` lands).

### Registration

`server/internal/mcp/tools.go:9-22` — append all 11 tools to the `all` slice. Group with comments:

```go
// Status pages
{listStatusPagesDef(), h.toolListStatusPages},
{getStatusPageDef(), h.toolGetStatusPage},
{createStatusPageDef(), h.toolCreateStatusPage},
{updateStatusPageDef(), h.toolUpdateStatusPage},
{deleteStatusPageDef(), h.toolDeleteStatusPage},
// Status page sections
{listStatusPageSectionsDef(), h.toolListStatusPageSections},
{createStatusPageSectionDef(), h.toolCreateStatusPageSection},
{updateStatusPageSectionDef(), h.toolUpdateStatusPageSection},
{deleteStatusPageSectionDef(), h.toolDeleteStatusPageSection},
// Status page resources
{listStatusPageResourcesDef(), h.toolListStatusPageResources},
// ... etc
```

## Tests

`server/internal/mcp/tools_statuspages_test.go`:

For each tool, one happy-path test and one negative test (missing required arg or service error). Use the same stub-service pattern as `handler_test.go`. Skip exhaustive permutation — the underlying `statuspages.Service` already has full coverage in `server/internal/handlers/statuspages/service_test.go`.

Round-trip handler test: invoke `tools/list` and assert all 11 new tools appear with the expected names.

## Verification

`make dev-test` on port 4000.

```bash
TOKEN=$(cat /tmp/token.txt)

# tools/list contains the new tools
curl -s -X POST -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"jsonrpc":"2.0","id":1,"method":"tools/list"}' \
  http://localhost:4000/api/v1/mcp | jq '.result.tools[].name' | grep status_page

# Create a page
curl -s -X POST -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"create_status_page","arguments":{"slug":"public","name":"Public status","enabled":true}}}' \
  http://localhost:4000/api/v1/mcp | jq

# Add a section
curl -s -X POST -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"create_status_page_section","arguments":{"pageIdentifier":"public","name":"API"}}}' \
  http://localhost:4000/api/v1/mcp | jq
```

Then connect Claude Desktop and ask: *"Create a public status page called 'Trust' with a section for API services."* Confirm the LLM picks the right tools in the right order.

## Files touched

- `server/internal/mcp/handler.go` — add `statusPagesSvc` field + wire in `NewHandler`.
- `server/internal/mcp/tools_statuspages.go` — new (estimated 350-450 lines for all 11 tools).
- `server/internal/mcp/tools.go` — register all 11 tools.
- `server/internal/mcp/tools_statuspages_test.go` — new.

No DB change. No new dependency.

## Implementation Plan

1. Read `server/internal/handlers/statuspages/service.go` end-to-end to capture the exact `CreateStatusPageRequest` / `CreateSectionRequest` / `CreateResourceRequest` field names and types.
2. Add `statusPagesSvc` to `Handler` and wire in `NewHandler`.
3. Create `tools_statuspages.go` — pages first, then sections, then resources. Use `tools_checks.go` as a template.
4. Register all 11 tools in `tools.go`.
5. Tests: one happy + one error path per tool; one `tools/list` registration test.
6. `make gotest` + `make lint-back` clean.
7. Smoke-test the curl flow + a Claude Desktop natural-language session.
