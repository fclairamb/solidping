# MCP — expose maintenance windows (CRUD + check assignment)

## Context

Maintenance windows are the second of the two "AI-drives-on-behalf-of-human" workflows the MCP audit identified — *"schedule a 30-minute maintenance window on these checks tonight at 22:00 UTC"*. The handler exists at `server/internal/handlers/maintenancewindows/service.go` with full CRUD plus per-window check assignment (functions at lines 96, 123, 151, 172, 232, 251, 287). REST routes are at `/api/v1/orgs/$org/maintenance-windows` per the project CLAUDE.md.

The MCP surface today (`server/internal/mcp/tools.go:9-22`) exposes none of this.

## Honest opinion

This one is more constrained than status pages — the request shape is small and stable (name, start, end, recurrence, optional check list). One round of CRUD plus the check-assignment endpoint and we're done. Six tools total.

The interesting design call: should `create_maintenance_window` accept the check list inline, or is creating a window then attaching checks separately better? **Recommendation: accept inline on create.** It matches how a human thinks about the operation ("schedule maintenance on X and Y"), saves a round-trip, and the underlying service supports it. The separate `set_maintenance_window_checks` tool is for the edit case ("add another check to this scheduled window").

I'd also push back on exposing the recurrence DSL in tool descriptions verbatim — most LLMs will get it wrong on first try and silently produce broken windows. Either (a) keep recurrence-string handling but document it concretely with examples, or (b) accept structured `{frequency: "weekly", dayOfWeek: ...}` and translate inside the tool. **Recommendation: (a) for v1** — matches REST, keeps the diff small. Revisit if recurrence-typo bug reports show up.

## Scope

**In:**
- 6 MCP tools wrapping the existing `maintenancewindows.Service`:
  - `list_maintenance_windows`, `get_maintenance_window`, `create_maintenance_window`, `update_maintenance_window`, `delete_maintenance_window`
  - `set_maintenance_window_checks` (replaces the assigned check list for a window)
- Wire `maintenancewindows.Service` into `Handler` constructor.
- New file `server/internal/mcp/tools_maintenance.go`.
- Tests for happy paths + recurrence validation error.

**Out:**
- `list_maintenance_window_checks` as a separate tool. The check list comes back inside `get_maintenance_window` response (verify in service); no need for a second call.
- Bulk create / "schedule maintenance for all checks with label X" — too easy to misuse, defer.
- Calendar-style listing (`get_maintenance_calendar`) — REST doesn't have this either.

## Tool surface

| Tool | Required args | Optional args |
|---|---|---|
| `list_maintenance_windows` | — | `state` (`upcoming`\|`active`\|`past`), `since`, `until`, `size`, `cursor` |
| `get_maintenance_window` | `uid` | — |
| `create_maintenance_window` | `name`, `startsAt` (RFC3339), `endsAt` (RFC3339) | `description`, `recurrence` (e.g. `"FREQ=WEEKLY;BYDAY=MO"`), `checkUids` (array of strings) |
| `update_maintenance_window` | `uid` | any of the create fields (PATCH) |
| `delete_maintenance_window` | `uid` | — |
| `set_maintenance_window_checks` | `uid`, `checkUids` (array — replaces, not appends) | — |

Pull exact field names from `CreateRequest` / `UpdateRequest` in `server/internal/handlers/maintenancewindows/service.go` near the methods.

### Critical description hygiene

Tool/param descriptions must include working examples or LLMs will fumble:

- `recurrence`: `"iCalendar RRULE string. Examples: 'FREQ=WEEKLY;BYDAY=MO,TU,WE,TH,FR' for weekdays, 'FREQ=MONTHLY;BYMONTHDAY=1' for the 1st of each month. Omit for one-off windows."`
- `startsAt` / `endsAt`: `"RFC3339 timestamp, e.g. '2026-05-03T22:00:00Z'. Both required and endsAt must be after startsAt."`
- `checkUids`: `"Array of check UIDs to apply maintenance to. Example: ['uid1','uid2']. Pass empty array to maintain a window with no associated checks."`

This is a recurring lesson: the LLM picks the right tool based on description quality. Spec `2026-05-03-30-mcp-tighten-tool-descriptions.md` formalizes this across all tools, but apply it from the start here.

## Implementation

### Wire service

`server/internal/mcp/handler.go:42-79`:

```go
type Handler struct {
    // ... existing fields
    maintenanceSvc *maintenancewindows.Service
}

// in NewHandler:
maintenanceSvc: maintenancewindows.NewService(dbService),
```

`maintenancewindows.NewService(dbService)` is at `server/internal/handlers/maintenancewindows/service.go:44`.

### Tool file

`server/internal/mcp/tools_maintenance.go` — same pattern as `tools_checks.go`. For `create_maintenance_window`, parse `startsAt` / `endsAt` via `time.Parse(time.RFC3339, ...)` (mirror `tools_results.go:62-64`).

For `checkUids`, use the existing `getStringSliceArg` helper (`handler.go:288-307`).

### Recurrence validation

The service already validates recurrence (`isValidRecurrence` at `maintenancewindows/service.go:29`). Don't duplicate — let the service return the error, surface as `errorResult(err.Error())`. The error message will tell the LLM what went wrong; it can correct on retry.

## Tests

`server/internal/mcp/tools_maintenance_test.go`:

1. **`list_maintenance_windows` happy path** — stub returns 2 windows, all forwarded.
2. **`create_maintenance_window` happy path** — minimal args (name, startsAt, endsAt). Stub asserts the `CreateRequest` it receives matches.
3. **`create_maintenance_window` with `checkUids`** — array properly forwarded as `[]string`.
4. **`create_maintenance_window` invalid recurrence** — stub returns recurrence-validation error; asserted as `errorResult` with `IsError: true`.
5. **`update_maintenance_window` PATCH semantics** — only `description` provided; assert other fields nil/zero in the `UpdateRequest`.
6. **`delete_maintenance_window` happy path** — text result confirmation.
7. **`set_maintenance_window_checks` empty array** — explicitly tests the "clear all" path.

## Verification

`make dev-test` on port 4000.

```bash
TOKEN=$(cat /tmp/token.txt)

# Create a one-off window
curl -s -X POST -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"create_maintenance_window","arguments":{"name":"DB upgrade","startsAt":"2026-05-04T22:00:00Z","endsAt":"2026-05-04T23:00:00Z","checkUids":["api-prod","api-staging"]}}}' \
  http://localhost:4000/api/v1/mcp | jq

# List upcoming
curl -s -X POST -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"list_maintenance_windows","arguments":{"state":"upcoming"}}}' \
  http://localhost:4000/api/v1/mcp | jq

# Recurring weekly
curl -s -X POST -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"create_maintenance_window","arguments":{"name":"Weekly patching","startsAt":"2026-05-04T02:00:00Z","endsAt":"2026-05-04T03:00:00Z","recurrence":"FREQ=WEEKLY;BYDAY=MO"}}}' \
  http://localhost:4000/api/v1/mcp | jq
```

Then via Claude Desktop: *"Schedule a 30-minute maintenance window on api-prod tonight at 22:00 UTC."* Confirm the LLM converts "tonight" + "22:00 UTC" + "30 minutes" into the right `startsAt`/`endsAt` and calls `create_maintenance_window` with `checkUids: ["api-prod"]`.

## Files touched

- `server/internal/mcp/handler.go` — add `maintenanceSvc` field + wire in `NewHandler`.
- `server/internal/mcp/tools_maintenance.go` — new (~250 lines for 6 tools).
- `server/internal/mcp/tools.go` — register the 6 tools.
- `server/internal/mcp/tools_maintenance_test.go` — new.

No DB change. No new dependency.

## Implementation Plan

1. Read `server/internal/handlers/maintenancewindows/service.go` to confirm exact request/response field names.
2. Add `maintenanceSvc *maintenancewindows.Service` to `Handler`; construct in `NewHandler`.
3. Write `tools_maintenance.go` with all 6 tools. Pay extra attention to the `recurrence`, `startsAt`, `endsAt`, `checkUids` descriptions — concrete examples, not vague hints.
4. Register tools in `tools.go`.
5. Tests for the 7 cases above.
6. `make gotest` + `make lint-back` clean.
7. Smoke-test via curl + a natural-language Claude Desktop session.
