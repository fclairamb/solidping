# MCP — tighten tool and parameter descriptions with concrete examples

## Context

Tool and parameter descriptions are how an MCP-connected LLM picks the right tool and fills the right arguments. They are read on every `tools/list`, parsed by the model, and become the de-facto API contract from the LLM's perspective. The audit of `server/internal/mcp/tools_*.go` found that:

- Many parameter descriptions are terse — `"Comma-separated check UIDs or slugs"` (`tools_results.go:16`) doesn't tell the LLM what a UID or slug looks like.
- Some types in enum-like params list valid values but no examples — `"Comma-separated: up, down, unknown"` (`tools_results.go:18`).
- RFC3339 fields say `"RFC3339 timestamp (inclusive lower bound)"` (`tools_results.go:21`) without showing one — LLMs sometimes produce non-RFC3339 strings (e.g. SQL datetimes) for these.
- The `with` flag in `list_results` is documented in two places (the property string at `tools_results.go:23-26` and… nowhere else) and bundles 9 fields with no example combination.
- `list_checks` "labels" param documents the format inline (`"key:value,key2:value2"` at `tools_checks.go:17`) but the `create_check` "labels" param expects an *object* (`tools_checks.go:124`) with the same name. Inconsistency invites mistakes.

This isn't bikeshedding — wrong tool calls are token-expensive and user-facing. A 30-line description sweep is the cheapest improvement on the entire MCP roadmap by ROI.

## Honest opinion

This is a 1-PR sweep, not 12 individual fixes. Doing it as a single coordinated pass also surfaces the inconsistencies (like the labels-format split) that you can't see when you only look at one tool at a time.

The bar for *every* parameter description should be: **a mid-tier LLM with no other context can produce a valid value on the first try**. If the description doesn't get you there, add an example. If two tools accept the same logical concept (a check identifier, a region slug, a timestamp), they should describe it the same way.

I'd resist the urge to also add JSON Schema `examples` arrays — the spec supports them but most clients render only `description`, and duplicating the example in description vs `examples` is more drift surface. Keep the example in the description, in plain English.

## Scope

**In:**
- Sweep every tool definition in `server/internal/mcp/tools_*.go` and rewrite descriptions per the rules below.
- Standardize cross-cutting concepts: check identifier, region slug, label format, RFC3339 timestamps, `with` field lists.
- One small test (`tools_test.go`) that asserts every parameter has a description >= 20 chars (catches future regressions).

**Out:**
- Schema structural changes (e.g. moving from string to object for labels — that's spec `2026-05-03-20-mcp-labels-structured-object.md`).
- New tools or new parameters.
- i18n / translation of descriptions. English-only for now.
- Auto-generated descriptions from Go doc comments. Manual is fine for ~12 tools.

## Rules

Each rule, applied uniformly:

1. **Every parameter description includes an example value when the type isn't self-evident from the name.**
   - `"identifier"` → `"Check UID or slug, e.g. \"api-prod\" or \"63d49e55-97e3-4e8c-b7ab-c862de7a43f3\"."`
   - `"period"` → `"Check interval as HH:MM:SS, e.g. \"00:00:30\" for 30 seconds, \"00:01:00\" for 1 minute."`
   - `"region"` (filter) → `"Comma-separated region slugs, e.g. \"eu-west-1,us-east-1\"."`

2. **RFC3339 fields show a timestamp.**
   - All instances of `"RFC3339 timestamp"` get `e.g. \"2026-05-03T10:14:22Z\""` appended.

3. **Enum-like fields list values AND show one in context.**
   - `"status"` → `"Comma-separated check statuses. Allowed: up, down, unknown. Example: \"down\" or \"down,unknown\"."`
   - `"state"` (incidents) → `"Comma-separated incident states. Allowed: active, resolved. Example: \"active\"."`

4. **`with` parameters list every supported value with a one-line meaning.**
   - For `list_results`'s `with` (`tools_results.go:23-26`):
     ```
     Comma-separated extra fields:
       durationMs        — response time in ms
       durationMinMs     — min response time in the bucket (aggregated rows only)
       durationMaxMs     — max response time in the bucket (aggregated rows only)
       region            — region the check ran in
       metrics           — per-execution metrics
       output            — full check output incl. error messages
       availabilityPct   — uptime % (aggregated rows only)
       checkSlug         — slug of the check
       checkName         — human name of the check
     Example: "durationMs,output".
     ```
     Yes this is long. It's read once per session; tokens spent here are paid back many times in correct calls.

5. **Tool-level descriptions briefly say *when to use this versus another tool*.** Particularly important for tools that overlap (`list_results` vs `diagnose_check` vs `list_incidents` once those land).
   - `list_checks` → `"List monitoring checks for the organization. Use this for browsing or filtering. For investigating a single check's current health, use diagnose_check instead."`

6. **The same logical concept gets the same description across tools.** E.g. `propIdentifier` (`server/internal/mcp/constants.go`) is used in 4 tools — the *constant's* description should be the canonical one, not redefined per tool.

7. **No vague modifiers.** `"flexible filtering"` (`tools_results.go:14`), `"with filtering"` (`tools_incidents.go:14`) — replace with what's actually filterable.

## Implementation

### Where to edit

Each tool's `*Def()` function, in the following files:

- `server/internal/mcp/tools_checks.go` — `listChecksDef`, `getCheckDef`, `createCheckDef`, `updateCheckDef`, `deleteCheckDef` (lines 11-23, 73-82, 110-129, 164-181, 226-233).
- `server/internal/mcp/tools_results.go` — `listResultsDef` (lines 11-31).
- `server/internal/mcp/tools_incidents.go` — `listIncidentsDef`, `getIncidentDef` (lines 11-25, 72-81).
- `server/internal/mcp/tools_connections.go` — both definitions.
- `server/internal/mcp/tools_groups.go` — `listCheckGroupsDef`.
- `server/internal/mcp/tools_regions.go` — `listRegionsDef`.

If specs `13-17` (Tier 1 specs) land first, also sweep their new tools. Order this spec to run *after* the new-tool specs land, so it fixes the whole surface in one pass rather than each new spec doing its own ad-hoc descriptions.

### Constants file

Open `server/internal/mcp/constants.go` and check if there's a single source of truth for prop names (e.g. `propIdentifier`, `propWith`). If yes, consider a parallel `propIdentifierDesc`, `propWithDesc` map of canonical descriptions to import from each tool def — but only if the duplication is actually painful. If there are only 4-5 reuse cases, hand-typed strings are fine; abstraction overhead isn't worth it.

### Helper function tweak

`server/internal/mcp/handler.go:249-258` (`getStringArg`) — no change needed.

## Test

`server/internal/mcp/tools_test.go` — add one new table-driven test:

```go
func TestAllToolDescriptionsMeetMinimum(t *testing.T) {
    t.Parallel()
    h := NewHandler(/* nil-safe stubs */)
    r := require.New(t)
    for _, tool := range h.tools {
        r.GreaterOrEqual(len(tool.Description), 40, "tool %q description too short", tool.Name)
        schema, ok := tool.InputSchema.(map[string]any)
        r.True(ok)
        props, ok := schema["properties"].(map[string]any)
        if !ok { continue }  // tool with no params is fine
        for name, p := range props {
            propMap, ok := p.(map[string]any)
            r.True(ok, "tool %q param %q schema malformed", tool.Name, name)
            desc, _ := propMap["description"].(string)
            r.GreaterOrEqual(len(desc), 20, "tool %q param %q description too short", tool.Name, name)
        }
    }
}
```

Thresholds (40 chars for tool, 20 chars for param) are deliberately loose — they catch one-word descriptions, not enforce a style. Adjust if there's a legitimate short description.

## Verification

```bash
TOKEN=$(cat /tmp/token.txt)

# Compare tools/list output diff before/after for one tool
curl -s -X POST -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"jsonrpc":"2.0","id":1,"method":"tools/list"}' \
  http://localhost:4000/api/v1/mcp | jq '.result.tools[] | select(.name=="list_results")'
```

Then a soft test: connect Claude Desktop, ask *"Show me the last 10 raw HTTP check failures from the last hour."* Pre-sweep, the LLM often gets `periodType` wrong (omits it, sends `"raw_data"`, etc.) or builds a malformed time string. Post-sweep, it should one-shot it.

`make gotest` runs the new description-length test.

## Files touched

- `server/internal/mcp/tools_checks.go`
- `server/internal/mcp/tools_results.go`
- `server/internal/mcp/tools_incidents.go`
- `server/internal/mcp/tools_connections.go`
- `server/internal/mcp/tools_groups.go`
- `server/internal/mcp/tools_regions.go`
- `server/internal/mcp/constants.go` (if extracted)
- `server/internal/mcp/tools_test.go` (new test)
- Any tool files added by Tier 1 specs (13-17) if they're already merged at the time of this PR.

No backend logic change. No DB change.

## Implementation Plan

1. Read every `*Def()` function across the 6 (or 9, with Tier 1) tool files.
2. List the cross-cutting concepts (identifier, region, RFC3339, label, with, status, state, period) and write *one* canonical description per concept.
3. Apply the canonical descriptions everywhere, plus per-tool tweaks per the 7 rules.
4. Add the description-length test.
5. `make gotest` + `make lint-back` clean.
6. Manual Claude Desktop probe with the failure-listing prompt above; confirm one-shot success.
