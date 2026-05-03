# MCP — `diagnose_check` aggregator tool

## Context

Today, when an LLM connected via MCP gets the question *"check `api-prod` is red, what's going on?"*, it has to chain four tool calls:

1. `get_check` to confirm the check exists and grab basic metadata (`tools_checks.go:73-108`).
2. `list_results` with a hand-invented `periodStartAfter` and `checkUid` filter (`tools_results.go:11-81`).
3. `list_incidents` filtered by `checkUid` and `state=active` (`tools_incidents.go:11-70`).
4. `get_incident` for the most recent one (`tools_incidents.go:72-104`) — and even then, the timeline of incident events is *not* exposed today (see spec `2026-05-03-26-mcp-incident-events.md`).

That's 4+ round-trips, each time-window and filter is LLM-invented (often wrong on the first try), and a meaningful chunk of the LLM's context is spent on plumbing instead of reasoning. For the MCP surface to actually be useful in incident triage — the #1 use case — we need one purpose-built tool that returns the bundle the LLM needs in a single call.

## Honest opinion

This is the highest-leverage addition to the MCP surface, full stop. The "MCP as a thin REST mirror" mindset doesn't serve LLM workflows well — REST is shaped for human dashboards, not for an LLM that pays per-token to round-trip. A dedicated diagnostic tool collapses 4 calls into 1, removes the burden of inventing filter values, and shapes the response as a *triage briefing* rather than four loosely-coupled JSON blobs.

I'm deliberately *not* generalizing this into "diagnose_org" or letting the caller pick which sub-bundles to include. Either of those would re-introduce the same parameter-hunting pain. Make one tool that does one thing, well. If a multi-check version is needed later, add it then.

## Scope

**In:**
- New tool `diagnose_check` registered in `server/internal/mcp/tools.go`.
- New file `server/internal/mcp/tools_diagnose.go` containing the schema and handler.
- Reuses existing services on the `Handler` struct (`tools_checks.go`, `tools_results.go`, `tools_incidents.go`) — no new service wiring.
- Unit + handler test coverage in `server/internal/mcp/tools_diagnose_test.go`.

**Out:**
- "Diagnose all red checks" / multi-check variant. Add later if asked.
- Calling the LLM internally to summarize. The tool returns structured data; summarization is the LLM client's job.
- Result-aggregation across regions (returning per-region summaries). Single-region-aware payload is enough for v1.
- Incident events inclusion — handled by spec `2026-05-03-26-mcp-incident-events.md`. Once that lands, this tool will fold the events in via the same `with` flag.

## Tool definition

```
name: diagnose_check
description:
  Return everything an operator would want to look at to diagnose a single
  check's current state in one call: current status, the most recent raw
  results across regions, any active incident, and the most recent resolved
  incident. Use this instead of chaining list_results + list_incidents when a
  human asks "what's wrong with check X?".
inputSchema:
  type: object
  required: [identifier]
  properties:
    identifier:
      type: string
      description: Check UID or slug (e.g. "api-prod" or a UUID).
    recentResultsLimit:
      type: integer
      description:
        Number of most-recent raw results to include per region (1-20, default 5).
        Larger values consume more tokens; default is usually enough for
        pattern-spotting.
```

## Response shape

Structured JSON returned via the existing `marshalResult()` path (or `structuredContent` once spec `2026-05-03-33-mcp-structured-content-output.md` lands).

```json
{
  "check": {
    "uid": "...",
    "slug": "api-prod",
    "name": "API production",
    "type": "http",
    "enabled": true,
    "period": "00:01:00",
    "regions": ["eu-west-1", "us-east-1"],
    "labels": {"env": "prod", "team": "api"},
    "currentStatus": "down",
    "lastStatusChangeAt": "2026-05-03T10:14:22Z"
  },
  "recentResults": [
    {
      "periodStart": "2026-05-03T10:14:00Z",
      "region": "eu-west-1",
      "status": "down",
      "durationMs": 30000,
      "output": {"error": "context deadline exceeded", "httpStatus": 0}
    },
    ...
  ],
  "activeIncident": {
    "uid": "...",
    "title": "API production is down",
    "startedAt": "2026-05-03T10:14:22Z",
    "failureCount": 3,
    "lastEventAt": "2026-05-03T10:16:00Z"
  } | null,
  "lastResolvedIncident": {
    "uid": "...",
    "startedAt": "2026-05-02T08:30:00Z",
    "resolvedAt": "2026-05-02T08:34:11Z",
    "durationSeconds": 251
  } | null
}
```

Token budget: aim for the typical response to fit under ~3 KB. With `recentResultsLimit=5` across 2 regions, that's 10 results + 1 active incident + 1 resolved incident — comfortably under budget.

## Implementation

### Reuse existing service calls

| Field group | Source |
|---|---|
| `check` | `h.checksSvc.GetCheck(ctx, orgSlug, identifier, GetCheckOptions{IncludeLastStatusChange: true})` (`server/internal/handlers/checks/service.go:619`). |
| `recentResults` | `h.resultsSvc.ListResults(ctx, orgSlug, &results.ListResultsOptions{Checks: []string{checkUID}, PeriodTypes: []string{"raw"}, Size: recentResultsLimit * len(regions), With: ["region","output","durationMs"]})` (`server/internal/handlers/results/service.go`). Then group by region client-side and trim to limit per region. |
| `activeIncident` | `h.incidentsSvc.ListIncidents(ctx, orgSlug, &incidents.ListIncidentsOptions{CheckUIDs: []string{checkUID}, States: []string{"active"}, Size: 1})` (`server/internal/handlers/incidents/service.go`). |
| `lastResolvedIncident` | Same call with `States: []string{"resolved"}, Size: 1`. Sorted DESC by `startedAt` already (verify in service). |

### Tool handler skeleton (`server/internal/mcp/tools_diagnose.go`)

```go
func diagnoseCheckDef() ToolDefinition {
    return ToolDefinition{
        Name: "diagnose_check",
        Description: "Return everything an operator would want to look at to diagnose " +
            "a single check's current state in one call: current status, recent raw " +
            "results across regions, any active incident, and the most recent resolved " +
            "incident. Use this instead of chaining list_results + list_incidents " +
            "when a human asks \"what's wrong with check X?\".",
        InputSchema: objectSchema(map[string]any{
            propIdentifier: stringProp("Check UID or slug (e.g. \"api-prod\" or a UUID)."),
            "recentResultsLimit": intProp("Recent raw results per region (1-20, default 5)."),
        }, []string{propIdentifier}),
    }
}

func (h *Handler) toolDiagnoseCheck(
    ctx context.Context, orgSlug string, args map[string]any,
) ToolCallResult {
    identifier := getStringArg(args, "identifier")
    if identifier == "" {
        return errorResult("identifier is required")
    }
    perRegion := getIntArg(args, "recentResultsLimit", 5)
    if perRegion < 1 { perRegion = 1 }
    if perRegion > 20 { perRegion = 20 }

    // 1. Check
    check, err := h.checksSvc.GetCheck(ctx, orgSlug, identifier,
        checks.GetCheckOptions{IncludeLastStatusChange: true})
    if err != nil { return errorResult(err.Error()) }

    // 2. Recent results (oversize then trim per-region)
    rawSize := perRegion * max(1, len(check.Regions))
    if rawSize > 100 { rawSize = 100 }
    res, err := h.resultsSvc.ListResults(ctx, orgSlug, &results.ListResultsOptions{
        Checks:      []string{check.UID},
        PeriodTypes: []string{"raw"},
        Size:        rawSize,
        With:        []string{"region", "output", "durationMs"},
    })
    if err != nil { return errorResult(err.Error()) }

    // 3. Active incident (Size=1)
    active, _ := h.incidentsSvc.ListIncidents(ctx, orgSlug, &incidents.ListIncidentsOptions{
        CheckUIDs: []string{check.UID}, States: []string{"active"}, Size: 1,
    })
    // 4. Last resolved incident (Size=1)
    resolved, _ := h.incidentsSvc.ListIncidents(ctx, orgSlug, &incidents.ListIncidentsOptions{
        CheckUIDs: []string{check.UID}, States: []string{"resolved"}, Size: 1,
    })

    return marshalResult(buildDiagnoseResponse(check, res, active, resolved, perRegion))
}
```

`buildDiagnoseResponse` is a pure shaper function — easy to unit test in isolation.

### Wiring

`server/internal/mcp/tools.go:9-22` — append to the `all` slice:

```go
{diagnoseCheckDef(), h.toolDiagnoseCheck},
```

## Tests

`server/internal/mcp/tools_diagnose_test.go`:

1. **Missing identifier → error result** with `IsError: true`.
2. **Identifier resolves, no incidents, results across 2 regions, limit=3.** Stub services to return 6 raw results (3 per region) + empty incidents. Assert response has 6 results, no active/resolvedIncident keys.
3. **Identifier resolves, active incident present.** Active incident populated, `lastResolvedIncident` null.
4. **Identifier resolves, both active and resolved present.** Both fields populated; resolved is the most recent resolved one.
5. **`recentResultsLimit` clamps:** pass `0` → coerced to 1, pass `999` → coerced to 20.
6. **Service error on `GetCheck`** → propagated as `errorResult`. Other service calls do not run (asserted via stub call counter).

Use the same test harness pattern as `handler_test.go` for the round-trip case (one test that invokes via `tools/call` JSON-RPC envelope and asserts on the response shape).

## Verification

`make dev-test` on port 4000.

```bash
TOKEN=$(cat /tmp/token.txt)

# Initialize MCP session
curl -s -X POST -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"curl","version":"1"}}}' \
  http://localhost:4000/api/v1/mcp | jq

# Call diagnose_check on a known-down check
curl -s -X POST -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"diagnose_check","arguments":{"identifier":"api-prod","recentResultsLimit":5}}}' \
  http://localhost:4000/api/v1/mcp | jq

# tools/list shows the new tool
curl -s -X POST -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"jsonrpc":"2.0","id":3,"method":"tools/list"}' \
  http://localhost:4000/api/v1/mcp | jq '.result.tools[] | select(.name=="diagnose_check")'
```

Then connect Claude Desktop / `claude mcp` to `http://localhost:4000/api/v1/mcp` and ask "what's wrong with check api-prod?" — confirm the LLM picks `diagnose_check` rather than chaining `get_check` + `list_results` + `list_incidents`.

## Files touched

- `server/internal/mcp/tools_diagnose.go` — new.
- `server/internal/mcp/tools.go` — register in `registerTools()`.
- `server/internal/mcp/tools_diagnose_test.go` — new.

No DB change. No new dependency.

## Implementation Plan

1. Add `diagnoseCheckDef()` and `toolDiagnoseCheck()` in `server/internal/mcp/tools_diagnose.go` with the response-shaping helper isolated as `buildDiagnoseResponse()`.
2. Register the tool in `tools.go:9-22`.
3. Unit-test the response shaper with table-driven cases for all 6 scenarios above.
4. Add one round-trip handler test mirroring `handler_test.go`.
5. `make gotest` + `make lint-back` clean.
6. Smoke-test against `make dev-test` with a check that has at least one active incident; verify Claude Desktop picks the new tool when prompted.
