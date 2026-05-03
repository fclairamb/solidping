# MCP — expose incident events on `get_incident`

## Context

`get_incident` (`server/internal/mcp/tools_incidents.go:72-104`) returns an incident's metadata but **not the timeline of events** that happened during it: status transitions, notifications sent, manual notes, group-membership changes. The REST API exposes these at `GET /api/v1/orgs/$org/incidents/$uid/events` (the `events` package — `server/internal/handlers/events/service.go`), and they are *the* thing an operator looks at when trying to understand what happened during an outage.

Without events in MCP, an LLM doing post-mortem or live triage is blind to the timeline. It can know "incident started at T, resolved at T+5m" but not "notification fired at T+30s, second failure at T+1m, recovery at T+4m45s". That's a hole in the most-used MCP workflow (incident review).

## Honest opinion

Two ways to expose events:

1. **New tool `list_incident_events`** — mirrors REST cleanly, separate concern.
2. **Add `events` to the existing `with` flag on `get_incident`** — mirrors the existing pattern (`with: ["check"]` already accepted, `tools_incidents.go:90-95`).

I'd do **option 2**. Here's why: in practice, when an LLM wants events, it always also wants the incident itself. Two calls to fetch (incident, events) when one would do is exactly the round-trip waste this audit is trying to fix. The `with` pattern is already in the codebase — extending it is consistent and zero-friction for existing callers.

The one tradeoff: response size grows when `with: ["events"]` is set. Mitigate with a hard cap (e.g. 50 most recent events) and document it.

## Scope

**In:**
- Extend `get_incident` to accept `with: ["check","events"]` and include event timeline when requested.
- Wire the existing `events.Service` into the MCP `Handler` constructor (`server/internal/mcp/handler.go:60-79`).
- Cap events at 50 per response; document the cap in the parameter description.
- Tests for: events excluded by default, events present with `with=events`, cap enforcement, `with=check,events` returns both.

**Out:**
- Standalone `list_incident_events` tool. Add later only if a use case emerges that wants events without the incident.
- Org-wide `list_events` tool (the audit identified this as a separate gap; defer).
- Pagination for events on a single incident — 50 is a hard ceiling for v1; if real incidents commonly exceed 50 events we can reconsider.
- Filtering events by `eventType` within the response — keep the surface minimal.

## Tool change

`get_incident` schema becomes:

```
inputSchema:
  type: object
  required: [uid]
  properties:
    uid:
      type: string
      description: Incident UID.
    with:
      type: string
      description:
        Comma-separated extra fields. Supported: "check" (include the
        underlying check), "events" (include up to 50 most recent timeline
        events: status transitions, notifications, manual notes).
        Example "check,events".
```

Response gains an optional `events` array when requested:

```json
{
  "uid": "...",
  "title": "API production is down",
  "startedAt": "...",
  "resolvedAt": "...",
  "check": { ... },           // present when with includes "check"
  "events": [                 // present when with includes "events"
    {
      "uid": "...",
      "eventType": "incident.created",
      "actorType": "system",
      "createdAt": "2026-05-03T10:14:22Z",
      "payload": { ... }
    },
    ...
  ]
}
```

## Implementation

### Wire `events.Service` into MCP `Handler`

`server/internal/mcp/handler.go:42-79`:

```go
type Handler struct {
    // ... existing fields
    eventsSvc *events.Service
}

func NewHandler(
    dbService db.Service,
    eventNotifier notifier.EventNotifier,
    jobSvc jobsvc.Service,
) *Handler {
    handler := &Handler{
        // ... existing
        eventsSvc: events.NewService(dbService),
    }
    handler.registerTools()
    return handler
}
```

`events.NewService(dbService)` is at `server/internal/handlers/events/service.go:25`.

### Update `tools_incidents.go:72-104`

Parse a new `withEvents` flag from the existing `with` string parser at lines 90-95:

```go
opts := &incidents.GetIncidentOptions{}
withEvents := false
if v := getStringArg(args, "with"); v != "" {
    for _, part := range strings.Split(v, ",") {
        switch strings.TrimSpace(part) {
        case "check":
            opts.WithCheck = true
        case "events":
            withEvents = true
        }
    }
}

incident, err := h.incidentsSvc.GetIncident(ctx, orgSlug, uid, opts)
if err != nil { return errorResult(err.Error()) }

if !withEvents {
    return marshalResult(incident)
}

evtList, err := h.eventsSvc.ListEvents(ctx, orgSlug, &events.ListEventsOptions{
    IncidentUID: &uid,
    Size:        50,
})
if err != nil { return errorResult(err.Error()) }

// Wrap in a small struct that embeds the existing incident response
return marshalResult(struct {
    *incidents.IncidentResponse
    Events []events.EventResponse `json:"events"`
}{incident, evtList.Data})
```

(Verify the exact incident response type name from `incidents/service.go`; embed accordingly.)

Update `getIncidentDef()` description at `tools_incidents.go:72-81` to mention the new `events` value.

### Cap behavior

`Size: 50` is hard-coded; this is intentional. The audit flagged "unbounded result sets" as a quality issue; events on a single incident is a place to enforce a sensible default rather than expose the knob. If `len(evtList.Data) == 50` we can optionally include `"eventsCapped": true` so the LLM knows there may be more — defer that polish unless real incidents bump the cap regularly.

## Tests

`server/internal/mcp/tools_incidents_test.go` (new, or extend `handler_test.go` patterns):

1. **`with` empty → no events field in response.** Stub `eventsSvc` to fail loudly if called, assert it isn't.
2. **`with=events` → events field present, capped at 50.** Stub events service to return 50 items; assert all returned.
3. **`with=events` → service returns 100, only 50 reach the response.** Pre-asserted by `Size: 50` passed into `ListEventsOptions`; verify the captured options.
4. **`with=check,events` → both fields present.** Stub both services; assert both invoked.
5. **`eventsSvc.ListEvents` returns error → tool returns `errorResult` with message.**

## Verification

```bash
TOKEN=$(cat /tmp/token.txt)

# Pick an incident UID
INC_UID=$(curl -s -X POST -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"list_incidents","arguments":{"size":1}}}' \
  http://localhost:4000/api/v1/mcp | jq -r '.result.content[0].text' | jq -r '.data[0].uid')

# Without events
curl -s -X POST -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d "{\"jsonrpc\":\"2.0\",\"id\":2,\"method\":\"tools/call\",\"params\":{\"name\":\"get_incident\",\"arguments\":{\"uid\":\"$INC_UID\"}}}" \
  http://localhost:4000/api/v1/mcp | jq -r '.result.content[0].text' | jq 'keys'
# Expect: ["check","resolvedAt","startedAt","title","uid",...]  -- no "events"

# With events
curl -s -X POST -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d "{\"jsonrpc\":\"2.0\",\"id\":3,\"method\":\"tools/call\",\"params\":{\"name\":\"get_incident\",\"arguments\":{\"uid\":\"$INC_UID\",\"with\":\"events\"}}}" \
  http://localhost:4000/api/v1/mcp | jq -r '.result.content[0].text' | jq '.events | length'
# Expect: integer >= 1, <= 50

# Combined
curl -s -X POST -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d "{\"jsonrpc\":\"2.0\",\"id\":4,\"method\":\"tools/call\",\"params\":{\"name\":\"get_incident\",\"arguments\":{\"uid\":\"$INC_UID\",\"with\":\"check,events\"}}}" \
  http://localhost:4000/api/v1/mcp | jq -r '.result.content[0].text' | jq 'has("events"), has("check")'
# Expect: true, true
```

## Files touched

- `server/internal/mcp/handler.go` — add `eventsSvc` field + constructor wiring.
- `server/internal/mcp/tools_incidents.go` — extend `getIncidentDef()` description and `toolGetIncident()` logic.
- `server/internal/mcp/tools_incidents_test.go` — new tests (or extend existing test file).

No DB change. Reuses `events.NewService(dbService)` already used by REST.

## Implementation Plan

1. Add `eventsSvc *events.Service` field to `Handler` and construct it in `NewHandler` (`handler.go:42-79`).
2. Update `getIncidentDef()` description to document `with: "events"`.
3. Update `toolGetIncident()` to parse `events` from `with`, fetch via `eventsSvc.ListEvents` with `Size: 50` and `IncidentUID: &uid`, embed in the response struct.
4. Add tests covering the 5 cases listed above.
5. `make gotest` + `make lint-back` clean.
6. Smoke-test via the curl flow above against `make dev-test`.
