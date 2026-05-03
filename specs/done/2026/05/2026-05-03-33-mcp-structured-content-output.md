# MCP — return structured content instead of stringified JSON

## Context

Every tool in `server/internal/mcp/tools_*.go` that returns data does it through `marshalResult` (`server/internal/mcp/tools_checks.go:249-255`):

```go
func marshalResult(v any) ToolCallResult {
    data, err := json.Marshal(v)
    if err != nil {
        return errorResult("Failed to marshal result: " + err.Error())
    }
    return textResult(string(data))
}
```

This stringifies the typed Go response into a JSON-in-text content block:

```json
{
  "content": [{"type": "text", "text": "{\"data\":[...],\"pagination\":{...}}"}],
  "isError": false
}
```

Two costs:

1. **The LLM has to re-parse JSON it could have consumed structurally.** Most modern MCP clients (Claude Desktop, Cursor, claude-mcp) understand the `structuredContent` field on `ToolCallResult` (added in MCP spec 2025-06-18 and supported on the older 2025-03-26 we currently advertise). Using it lets the client treat the response as a real object — better-typed, smaller token footprint when re-rendered, and tools like response-shape inspection work properly.
2. **Validation can't happen on the data side.** If a future tool wants to advertise an `outputSchema` (introduced 2025-06-18), the response needs to be structured, not stringified.

The `ToolCallResult` type at `server/internal/mcp/protocol.go:104-107` only has `Content` and `IsError`. We need to add `StructuredContent any` and start using it.

## Honest opinion

This is a refactor, not a feature. The win is real — it's the difference between "the LLM has to do a JSON.parse" and "the data shows up structured" — but it's the kind of cleanup that shouldn't block other work. **Land Tier 1 specs first**, then do this as a pure refactor PR with no behavioral change.

I'd ship the change as: send *both* `content` (text-form for clients that don't understand structured) and `structuredContent` (object for clients that do). That's the spec-recommended pattern for transitional periods, and there's no measurable cost to it. Drop the duplication only if a real reason emerges.

If you only have time to do half: `marshalResult` becomes a one-line shim that sets both. Tools don't change. This whole spec is a 30-line edit + tests.

## Scope

**In:**
- Add `StructuredContent any` to `ToolCallResult` in `server/internal/mcp/protocol.go:104`.
- Update `marshalResult` to populate both `Content` (stringified, for compat) and `StructuredContent` (the typed object).
- Update tests to assert both fields populated.

**Out:**
- Per-tool `outputSchema` registration. Defer; that's a separate spec if/when needed.
- Behavior changes to any individual tool.
- Dropping the text-form `Content`. Keep both for client-compat for at least one release cycle.
- Bumping `mcpProtocolVer` to `2025-06-18`. That's spec `2026-05-03-39-mcp-protocol-version-negotiation.md`. The `structuredContent` field works on `2025-03-26` clients that opt into it; don't conflate.

## Implementation

### Type change

`server/internal/mcp/protocol.go:104-107`:

```go
// ToolCallResult represents the result of a tools/call request.
type ToolCallResult struct {
    Content           []ContentBlock `json:"content"`
    StructuredContent any            `json:"structuredContent,omitempty"`
    IsError           bool           `json:"isError,omitempty"`
}
```

`omitempty` ensures we don't send `"structuredContent": null` for tools that intentionally don't have a structured form (e.g. `delete_check` which returns a confirmation string).

### `marshalResult` update

`server/internal/mcp/tools_checks.go:249-255`:

```go
func marshalResult(v any) ToolCallResult {
    data, err := json.Marshal(v)
    if err != nil {
        return errorResult("Failed to marshal result: " + err.Error())
    }
    return ToolCallResult{
        Content:           []ContentBlock{{Type: "text", Text: string(data)}},
        StructuredContent: v,
    }
}
```

Note the move from `textResult(string(data))` to constructing the result directly so we can add `StructuredContent` in the same allocation.

`textResult()` (`protocol.go:155-159`) stays unchanged — it's used by `delete_check` for plain confirmations and by error paths; those don't need `StructuredContent`.

### Verify no JSON-encode cycles

`StructuredContent: v` will be JSON-marshaled by the response writer (`writeJSON` at `handler.go:241-245`). If `v` contains anything non-serializable (channels, funcs), it'll panic at write time. The current `marshalResult` already json.Marshals `v` once before assigning, so any encoding error surfaces there — and we still keep that as a guard. So if `json.Marshal(v)` succeeded, the second pass at write time is also safe.

## Tests

Update `server/internal/mcp/protocol_test.go` and `tools_test.go`:

1. **`marshalResult` populates both fields.** Pass a struct; assert `len(result.Content) == 1`, `result.Content[0].Type == "text"`, `result.StructuredContent` deep-equals the input.
2. **`marshalResult` failure path unchanged.** Pass an unmarshalable value (e.g. a channel inside a struct); assert `IsError: true`, descriptive message in `Content[0].Text`, `StructuredContent` empty.
3. **JSON serialization of a `ToolCallResult` includes both `content` and `structuredContent` fields when populated.** Round-trip through `json.Marshal` → check both keys present.
4. **JSON serialization omits `structuredContent` when nil.** `textResult("hi")` → marshal → no `structuredContent` key in output.
5. **One `tools/call` round-trip test that asserts the response includes `structuredContent`.** Pick an existing tool (e.g. `list_regions`) and confirm.

Existing tests for individual tools should mostly keep working — `Content[0].Text` still has the JSON. Only update tests that explicitly probed the `ToolCallResult` shape.

## Verification

```bash
TOKEN=$(cat /tmp/token.txt)

# Any list call should now show structuredContent in the response
curl -s -X POST -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"list_regions","arguments":{}}}' \
  http://localhost:4000/api/v1/mcp | jq '.result | keys'
# Expect: ["content","structuredContent"]   (no "isError" since false → omitempty)

curl -s -X POST -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"list_regions","arguments":{}}}' \
  http://localhost:4000/api/v1/mcp | jq '.result.structuredContent'
# Expect: typed JSON object, not a string

# Delete confirmation should NOT have structuredContent
# (skip — destructive — but inspect tools_checks.go's deleteCheck to confirm
# it still uses textResult, not marshalResult)
```

In Claude Desktop, run any list tool and inspect the response in the dev tools — the structured form should be picked up. (Behavior should be visually identical for the user; this is plumbing.)

## Files touched

- `server/internal/mcp/protocol.go` — add `StructuredContent` field.
- `server/internal/mcp/tools_checks.go` — update `marshalResult`. (It's defined here despite being shared; consider moving to `protocol.go` or a `tools.go` helper file as a small follow-up. Not in scope.)
- `server/internal/mcp/protocol_test.go` — add field-presence tests.
- `server/internal/mcp/tools_test.go` — verify round-trip.
- `server/internal/mcp/handler_test.go` — adjust if any test asserts on `len(result)` keys.

No DB change. No new dependency. No behavioral change for tools.

## Implementation Plan

1. Add `StructuredContent any` to `ToolCallResult` with `omitempty`.
2. Update `marshalResult` to populate both fields.
3. Run existing test suite — fix any that assert exact response-shape keys.
4. Add the 5 new test cases above.
5. `make gotest` + `make lint-back` clean.
6. Smoke-test via curl, confirm `structuredContent` appears for typed responses and is absent for `textResult` outputs.
