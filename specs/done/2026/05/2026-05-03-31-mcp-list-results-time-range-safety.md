# MCP — make `list_results` time-range-safe by default

## Context

`list_results` (`server/internal/mcp/tools_results.go:11-81`) accepts `periodStartAfter`, `periodEndBefore`, `periodType`, and `size` (max 100). Today, *none* of these are required — an LLM can call `list_results` with an empty argument map and get up to 100 raw results going back to whenever they were inserted.

That sounds bounded but isn't, in two ways:

1. **Volume per call is technically capped** at 100 results, but with no time filter the 100 returned are effectively *random with respect to recency* (they're whatever the underlying paginated query returns first). The LLM thinks it has "the last 100 results" when it has "100 arbitrary results across the table's history". Wrong conclusions follow.
2. **An LLM that loops** ("get next page, get next page…") with no time bound can fetch a month of raw results and blow out its context for the rest of the session. The audit explicitly flagged this as a real failure mode.

The fix is to enforce a sane default time window when none is specified. `periodType` can also be defaulted (most LLMs picking results data want aggregated data unless they say otherwise).

## Honest opinion

Two design choices, with my pick:

- **Hard-require `periodStartAfter` OR `periodType`** — makes the LLM think about what it's asking for; ergonomic friction.
- **Default a sensible time window per `periodType` if none given** — silent helper; LLM gets useful results on first try.

I'd ship **both layers**: default `periodType` to `"hour"` if absent (aggregated, low-volume, usually-what-you-want), and *if* the LLM insists on `periodType=raw` without a time filter, default `periodStartAfter` to `now - 1h`. That way:

- Common case ("show me results for check X") → returns hourly aggregates for the last day or so → small response, useful.
- Power case ("show me raw results for check X") → returns last hour of raw data → bounded.
- Explicit case ("`periodType=raw`, `periodStartAfter=2026-04-01`") → caller knows what they're doing, give them what they asked for.

Crucially, **the response should include the effective time range and periodType used**, even when defaulted, so the LLM knows what filter actually ran. Silent-defaulting is a footgun without that.

## Scope

**In:**
- Default `periodType=["hour"]` when `periodType` is absent.
- Default `periodStartAfter` to a `periodType`-aware sliding window when absent (raw → 1h, hour → 24h, day → 30d, month → 365d).
- Include `effectiveFilter: { periodType, periodStartAfter, periodEndBefore }` in the response so the caller sees what was used.
- Update the tool description to reflect the new defaults.
- Tests for: defaults applied, defaults overridden, mixed (some specified, some not).

**Out:**
- Cap on `size` (already capped at 100).
- Required-field enforcement — defaults are friendlier and don't break existing callers.
- Time-window safety on other tools (`list_incidents` already takes `since`/`until` but is naturally bounded; `list_check_groups` returns small lists). Defer.
- Pagination cursor validation — separate concern (see audit red flag #3).

## Default windows

| `periodType` | Default `periodStartAfter` if missing |
|---|---|
| `raw` | `now - 1h` |
| `hour` | `now - 24h` |
| `day` | `now - 30d` |
| `month` | `now - 365d` |

These mirror the natural granularity of each level. If a caller passes multiple periodTypes (`"raw,hour"`), use the *finest* granularity's default (raw → 1h).

## Implementation

### Update `tools_results.go`

`server/internal/mcp/tools_results.go:33-81` — modify the option building:

```go
func (h *Handler) toolListResults(
    ctx context.Context, orgSlug string, args map[string]any,
) ToolCallResult {
    opts := &results.ListResultsOptions{
        Cursor: getStringArg(args, "cursor"),
        Size:   getIntArg(args, "size", 20),
    }
    if opts.Size < 1 { opts.Size = 1 }
    if opts.Size > 100 { opts.Size = 100 }

    if v := getStringArg(args, "checkUid"); v != "" {
        opts.Checks = strings.Split(v, ",")
    }
    if v := getStringArg(args, "checkType"); v != "" {
        opts.CheckTypes = strings.Split(v, ",")
    }
    if v := getStringArg(args, "status"); v != "" {
        opts.Statuses = strings.Split(v, ",")
    }
    if v := getStringArg(args, "region"); v != "" {
        opts.Regions = strings.Split(v, ",")
    }

    // PeriodType default
    if v := getStringArg(args, "periodType"); v != "" {
        opts.PeriodTypes = strings.Split(v, ",")
    } else {
        opts.PeriodTypes = []string{"hour"}
    }

    // Time-range defaults
    if v := getStringArg(args, "periodStartAfter"); v != "" {
        if t, err := time.Parse(time.RFC3339, v); err == nil {
            opts.PeriodStartAfter = &t
        }
    } else {
        finest := finestPeriodType(opts.PeriodTypes)
        defaultWindow := defaultWindowFor(finest)
        cutoff := time.Now().Add(-defaultWindow)
        opts.PeriodStartAfter = &cutoff
    }
    if v := getStringArg(args, "periodEndBefore"); v != "" {
        if t, err := time.Parse(time.RFC3339, v); err == nil {
            opts.PeriodEndBefore = &t
        }
    }

    if v := getStringArg(args, "with"); v != "" {
        opts.With = strings.Split(v, ",")
    }

    result, err := h.resultsSvc.ListResults(ctx, orgSlug, opts)
    if err != nil { return errorResult(err.Error()) }

    // Wrap with effective filter so caller sees what was used
    return marshalResult(struct {
        *results.ListResultsResponse
        EffectiveFilter map[string]any `json:"effectiveFilter"`
    }{
        result,
        map[string]any{
            "periodType":       opts.PeriodTypes,
            "periodStartAfter": opts.PeriodStartAfter,
            "periodEndBefore":  opts.PeriodEndBefore,
        },
    })
}

func finestPeriodType(pts []string) string {
    order := map[string]int{"raw": 0, "hour": 1, "day": 2, "month": 3}
    finest := "hour"
    finestRank := 99
    for _, p := range pts {
        if r, ok := order[p]; ok && r < finestRank {
            finest = p
            finestRank = r
        }
    }
    return finest
}

func defaultWindowFor(periodType string) time.Duration {
    switch periodType {
    case "raw":
        return time.Hour
    case "hour":
        return 24 * time.Hour
    case "day":
        return 30 * 24 * time.Hour
    case "month":
        return 365 * 24 * time.Hour
    }
    return 24 * time.Hour
}
```

(Verify the exact response type from `results.ListResultsResponse` in `server/internal/handlers/results/service.go` and embed accordingly.)

### Update tool description

`tools_results.go:11-31` — update `Description` and the `periodType` / `periodStartAfter` parameter descriptions:

```
Description: "Query monitoring results with flexible filtering. " +
    "If periodType is omitted, defaults to \"hour\". " +
    "If periodStartAfter is omitted, defaults to a sensible window for the " +
    "requested periodType (raw=1h, hour=24h, day=30d, month=365d). " +
    "Specify both for precise control. The response includes effectiveFilter " +
    "so you can see what was actually used."
```

```
"periodType": stringProp("Comma-separated: raw, hour, day, month. Default \"hour\"."),
"periodStartAfter": stringProp("RFC3339 timestamp (inclusive lower bound), e.g. \"2026-05-03T10:14:22Z\". Defaults to a window matched to periodType when omitted."),
```

## Tests

`server/internal/mcp/tools_results_test.go` (new):

1. **No args → defaults applied.** Stub records the `ListResultsOptions` it received: `PeriodTypes == ["hour"]`, `PeriodStartAfter` ≈ `now - 24h`. Response has `effectiveFilter` populated.
2. **`periodType=raw` only → window defaults to 1h.** `PeriodStartAfter` ≈ `now - 1h`.
3. **`periodType=raw,hour` → finest wins → window 1h.**
4. **Both `periodType` and `periodStartAfter` explicit → no defaulting.** Recorded options match input exactly.
5. **`periodEndBefore` only → still defaults `periodStartAfter`.** Both ranges populated.
6. **`effectiveFilter` always present in response, regardless of caller-supplied values.**

## Verification

```bash
TOKEN=$(cat /tmp/token.txt)

# No args — should return last 24h of hourly aggregates
curl -s -X POST -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"list_results","arguments":{}}}' \
  http://localhost:4000/api/v1/mcp | jq '.result.content[0].text' | jq '.effectiveFilter'

# raw without window — should return last hour
curl -s -X POST -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"list_results","arguments":{"periodType":"raw"}}}' \
  http://localhost:4000/api/v1/mcp | jq '.result.content[0].text' | jq '.effectiveFilter'

# Explicit window — defaults bypassed
curl -s -X POST -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"list_results","arguments":{"periodType":"raw","periodStartAfter":"2026-05-01T00:00:00Z"}}}' \
  http://localhost:4000/api/v1/mcp | jq '.result.content[0].text' | jq '.effectiveFilter'
```

Then via Claude Desktop ask: *"Show me how check api-prod has been doing recently."* — the LLM should call `list_results` with no time filter and get a bounded, useful response (hourly, last 24h) rather than a randomly-sliced firehose.

## Files touched

- `server/internal/mcp/tools_results.go` — defaulting logic, helper functions, response wrapping, description updates.
- `server/internal/mcp/tools_results_test.go` — new.

No DB change. No new dependency. No service change.

## Implementation Plan

1. Confirm the `results.ListResultsResponse` type in `server/internal/handlers/results/service.go`.
2. Update `toolListResults` per the implementation above. Add `finestPeriodType` and `defaultWindowFor` helpers in the same file (file-private, lowercase).
3. Update `Description`, `periodType`, and `periodStartAfter` on `listResultsDef`.
4. Add tests for the 6 cases.
5. `make gotest` + `make lint-back` clean.
6. Smoke-test via curl + Claude Desktop natural-language prompt.
