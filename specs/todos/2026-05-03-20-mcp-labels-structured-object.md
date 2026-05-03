# MCP — replace `"k:v,k2:v2"` labels string with a structured object

## Context

`list_checks` accepts labels as a comma-colon-delimited string at `server/internal/mcp/tools_checks.go:17`:

```go
propLabels: stringProp("Label filter (key:value,key2:value2)"),
```

…and parses it at lines 40-48:

```go
if labelsParam := getStringArg(args, "labels"); labelsParam != "" {
    opts.Labels = make(map[string]string)
    for _, pair := range strings.Split(labelsParam, ",") {
        kv := strings.SplitN(pair, ":", 2)
        if len(kv) == 2 {
            opts.Labels[strings.TrimSpace(kv[0])] = strings.TrimSpace(kv[1])
        }
    }
}
```

Two problems:

1. **LLMs reliably produce JSON objects; they botch comma-colon string formats.** Common failure modes: spaces around `:` getting stripped or not (we strip, but LLMs don't know that); using `=` instead of `:`; quoting the values; producing JSON-like `"{env: prod}"` and breaking. Each of those silently produces wrong filters.
2. **Malformed pairs are silently skipped** (the `if len(kv) == 2` branch). The LLM gets back results filtered by *some subset* of what it asked for, with no error feedback. It cannot self-correct because it doesn't know anything went wrong.

Meanwhile, `create_check` (`tools_checks.go:124`) and `update_check` (`tools_checks.go:176`) already accept labels as an `objectProp` — the inconsistency is real, internal, and easy to fix.

## Honest opinion

Make `list_checks` accept the same shape as `create_check`/`update_check`: a JSON object. There's no version of this where the string format is the right answer.

I considered keeping backward compat (accept both string and object, prefer object). Skip that — the MCP surface is new enough that we're not breaking real users yet, and dual-shape parameters create their own LLM confusion ("do I send a string or an object?"). One shape, document it, move on.

If anyone is currently calling MCP `list_checks` with the string form, they'll get a clear "labels must be an object" error and can update once. That's a kinder failure mode than today's silent-skip.

## Scope

**In:**
- `list_checks` accepts `labels` as an object (`{env: "prod", team: "api"}`).
- Drop the comma-colon string parser at `tools_checks.go:40-48`.
- Update the param description.
- Tests covering: object accepted, missing labels OK, malformed (string-typed) value returns explicit error.

**Out:**
- Backward-compat dual shape. Hard cutover.
- Other tools — `create_check` and `update_check` already use the object shape correctly.
- Label-value validation (only-strings, length limits). Service layer's job.
- Filtering with operators (`env:prod OR env:staging`). Out of scope; existing service is AND-only.

## Implementation

### Update tool definition

`server/internal/mcp/tools_checks.go:11-23`:

```go
func listChecksDef() ToolDefinition {
    return ToolDefinition{
        Name:        "list_checks",
        Description: "List monitoring checks for the organization.",
        InputSchema: objectSchema(map[string]any{
            "q":               stringProp("Search query (name or slug substring)."),
            propLabels:        objectProp("Label filter as a JSON object. Returns checks that have ALL the given labels with matching values. Example: {\"env\": \"production\", \"team\": \"api\"}."),
            propCheckGroupUID: stringProp("Filter by check group UID or slug."),
            propWith:          stringProp("Comma-separated extra fields: lastResult, lastStatusChange. Example: \"lastResult,lastStatusChange\"."),
            propLimit:         intProp("Max results (1-100, default 20)."),
            propCursor:        stringProp("Pagination cursor from previous response."),
        }, nil),
    }
}
```

### Update handler

Replace `tools_checks.go:40-48` with:

```go
if v, ok := args["labels"]; ok && v != nil {
    labelsMap, isMap := v.(map[string]any)
    if !isMap {
        return errorResult("labels must be a JSON object, e.g. {\"env\":\"production\"}")
    }
    opts.Labels = make(map[string]string, len(labelsMap))
    for k, val := range labelsMap {
        strVal, isStr := val.(string)
        if !isStr {
            return errorResult(fmt.Sprintf("labels.%s must be a string", k))
        }
        opts.Labels[k] = strVal
    }
}
```

This uses `getStringMapArg` semantics but inline because we want explicit type errors rather than silent skipping. Alternatively, extend `getStringMapArg` to return `(map[string]string, error)` and use it everywhere — small refactor, makes other call sites stricter too. **Recommendation: inline for v1**, refactor `getStringMapArg` only if the same pattern shows up in other tools added by Tier 1 specs.

Check `import` block — ensure `fmt` is added.

## Tests

`server/internal/mcp/tools_checks_test.go` (new or extend):

1. **Labels absent → no filter.** `opts.Labels == nil`.
2. **Empty object `{}` → empty map filter.** `opts.Labels == map[string]string{}` (services should return all checks for empty filter — verify).
3. **Object with strings → forwarded.** `{"env": "prod", "team": "api"}` → `opts.Labels == {"env": "prod", "team": "api"}`.
4. **String value (legacy form `"env:prod"`) → error.** Asserts `errorResult` with `IsError: true` and message mentioning `"labels must be a JSON object"`.
5. **Object with non-string value → error.** `{"env": 123}` → error mentioning `"labels.env must be a string"`.
6. **Round-trip handler test.** Send via `tools/call`; assert error path returns proper JSON-RPC error envelope.

## Verification

```bash
TOKEN=$(cat /tmp/token.txt)

# Object form (new)
curl -s -X POST -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"list_checks","arguments":{"labels":{"env":"production","team":"api"}}}}' \
  http://localhost:4000/api/v1/mcp | jq

# String form (legacy) — should now error explicitly
curl -s -X POST -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"list_checks","arguments":{"labels":"env:production"}}}' \
  http://localhost:4000/api/v1/mcp | jq '.result.content[0].text'
# Expect: error message mentioning "labels must be a JSON object"
```

Then via Claude Desktop: *"Show me all checks tagged env=production."* The LLM should produce `{"labels": {"env": "production"}}` cleanly.

## Files touched

- `server/internal/mcp/tools_checks.go` — `listChecksDef` description, `toolListChecks` parsing logic, possible `fmt` import.
- `server/internal/mcp/tools_checks_test.go` — new (or extend).

No DB change. No service change. No breaking change to other tools.

## Implementation Plan

1. Update `propLabels` in `listChecksDef` from `stringProp` to `objectProp` with a concrete-example description.
2. Replace the string-split parser with the typed-object parser, returning explicit errors on type mismatch.
3. Add `fmt` to imports if needed.
4. Add tests for the 6 cases above.
5. `make gotest` + `make lint-back` clean.
6. Smoke-test via curl + Claude Desktop natural-language prompt.
