# MCP — expose check types, samples, and validate

## Context

`create_check` (`server/internal/mcp/tools_checks.go:110-162`) accepts a `config` object but the LLM has no way to discover what shape `config` should take for a given check type. The `type` parameter description hints `"http, tcp, icmp, dns, ssl, heartbeat, domain"` — but the actual config schema differs wildly per type (HTTP needs `url` + maybe `method`/`headers`/`expectedStatus`; DNS needs `host` + `recordType`; ICMP needs `host`; SSL needs `host`/`port`; etc.).

In practice, an LLM creating a check from natural language today either invents a plausible-looking config (often subtly wrong — wrong field names, missing required fields) or refuses to try. Either is a bad UX.

The REST API solves this with three endpoints:

- `GET /api/v1/check-types` — list types and their descriptions (`server/internal/handlers/checktypes/service.go:110`).
- `GET /api/v1/check-types/samples` — return ready-made sample configs per type (`checktypes/service.go:86`).
- `POST /api/v1/orgs/$org/checks/validate` — dry-run validate a `CreateCheckRequest` without persisting (`server/internal/handlers/checks/service.go:164`).

None are exposed via MCP. Together they enable the workflow: **discover types → pick a sample → tweak → validate → create**. That's how you make "create a check from natural language" actually work.

## Honest opinion

Three tools, one workflow. The tools are individually small but their *combination* is what unlocks the use case — shipping just one of them is half a feature.

I'd resist the temptation to add a fourth "smart" tool that does everything in one shot ("create_check_from_description"). That's a wrapper over an LLM call inside our server, and it's the wrong layer for that — the LLM client is the one that should be doing the natural-language → structured-config mapping. Our job is to give it the building blocks.

## Scope

**In:**
- 3 MCP tools wrapping existing services:
  - `list_check_types` → `checktypes.Service.ListServerCheckTypes()`
  - `get_check_type_samples` → `checktypes.Service.ListSampleConfigs(type)`
  - `validate_check` → `checks.Service.ValidateCheck(...)`
- Wire `checktypes.Service` into the MCP `Handler` constructor.
- Tests for happy paths.

**Out:**
- Per-type schema introspection (`describe_check_type` returning JSON Schema for the config). REST doesn't have this; defer until samples-aren't-enough is demonstrated.
- Org-scoped check-type listing (`/api/v1/orgs/$org/check-types`). The server-wide list is enough for MCP v1; org-disabled-types filtering is a UI concern.
- Auto-validation inside `create_check` (already happens server-side; no change needed).

## Tool surface

| Tool | Required args | Optional args |
|---|---|---|
| `list_check_types` | — | — |
| `get_check_type_samples` | `type` (e.g. `"http"`) | — |
| `validate_check` | `config`, `type` | `name`, `slug`, `regions`, `period`, `enabled`, `labels`, `description`, `checkGroupUid` (mirror `create_check` shape exactly) |

### Tool descriptions (critical)

These tool descriptions are explicitly designed to *teach the LLM the workflow*. The LLM will read them when picking a tool — make the chain obvious:

- `list_check_types`: `"List all monitoring check types supported by this server (e.g. http, tcp, dns, icmp, ssl). Use this first when you don't know what type to use. Then call get_check_type_samples for the chosen type to get a starting config."`
- `get_check_type_samples`: `"Return ready-made sample configs for the given check type. Each sample is a complete, valid CreateCheckRequest you can clone and modify. Use this to learn the config shape for a type — much more reliable than guessing field names."`
- `validate_check`: `"Dry-run validate a check config without creating the check. Returns validation errors if any. Use this before create_check when you've assembled a config from scratch or modified a sample, to catch problems early."`

## Implementation

### Wire `checktypes.Service`

`checktypes.NewService` has a different signature than the others (`server/internal/handlers/checktypes/service.go:55`):

```go
func NewService(resolver *checkerdef.ActivationResolver, baseURL string) *Service
```

Trace where `checktypes.NewService` is constructed in `server/internal/app/server.go` to find the resolver and `baseURL` already wired up. Do the same wiring in `mcp.NewHandler`. If the resolver isn't easily reachable from the current `NewHandler` signature, accept a constructed `*checktypes.Service` as a new constructor parameter rather than re-creating it — caller passes both the existing REST one and the MCP one.

`server/internal/mcp/handler.go:60-79`:

```go
func NewHandler(
    dbService db.Service,
    eventNotifier notifier.EventNotifier,
    jobSvc jobsvc.Service,
    checkTypesSvc *checktypes.Service,  // new
) *Handler {
    handler := &Handler{
        // ... existing
        checkTypesSvc: checkTypesSvc,
    }
    handler.registerTools()
    return handler
}
```

The caller in `server/internal/app/server.go` will need updating to pass the existing `checkTypesSvc` instance through.

### Tool file

`server/internal/mcp/tools_checktypes.go` — three small functions. Skeletons:

```go
func validateCheckDef() ToolDefinition {
    return ToolDefinition{
        Name: "validate_check",
        Description: "Dry-run validate a check config without creating the check. " +
            "Returns validation errors if any. Use this before create_check when " +
            "you've assembled a config from scratch or modified a sample, to catch " +
            "problems early.",
        InputSchema: objectSchema(map[string]any{
            schemaKeyType:        stringProp("Check type (e.g. \"http\", \"dns\", \"tcp\", \"icmp\")."),
            schemaKeyConfig:      objectProp("Check-specific config (e.g., {\"url\": \"https://example.com\"})."),
            schemaKeyName:        stringProp("Optional name."),
            schemaKeySlug:        stringProp("Optional slug."),
            "regions":            arrayOfStringsProp("Optional region slugs."),
            schemaKeyEnabled:     boolProp("Optional enabled flag."),
            "period":             stringProp("Optional period (e.g. \"00:01:00\")."),
            propLabels:           objectProp("Optional labels object."),
            schemaKeyDescription: stringProp("Optional description."),
            propCheckGroupUID:    stringProp("Optional check group UID."),
        }, []string{schemaKeyType, schemaKeyConfig}),
    }
}

func (h *Handler) toolValidateCheck(
    ctx context.Context, orgSlug string, args map[string]any,
) ToolCallResult {
    config := getMapArg(args, "config")
    if config == nil { return errorResult("config is required") }
    typeStr := getStringArg(args, "type")
    if typeStr == "" { return errorResult("type is required") }

    req := checks.CreateCheckRequest{
        Type:    typeStr,
        Config:  config,
        Name:    getStringArg(args, "name"),
        // ... mirror create_check exactly
    }
    result, err := h.checksSvc.ValidateCheck(ctx, orgSlug, req)
    if err != nil { return errorResult(err.Error()) }
    return marshalResult(result)
}
```

For `list_check_types` and `get_check_type_samples`, just call into `h.checkTypesSvc.ListServerCheckTypes()` / `h.checkTypesSvc.ListSampleConfigs(typeStr)` and `marshalResult(...)`. These are read-only and don't take `orgSlug` — fine.

### Registration

`server/internal/mcp/tools.go:9-22`:

```go
{listCheckTypesDef(), h.toolListCheckTypes},
{getCheckTypeSamplesDef(), h.toolGetCheckTypeSamples},
{validateCheckDef(), h.toolValidateCheck},
```

## Tests

`server/internal/mcp/tools_checktypes_test.go`:

1. **`list_check_types` returns the configured set.** Stub `checkTypesSvc` to return 3 types; assert all returned via `marshalResult`.
2. **`get_check_type_samples` requires `type`.** Missing → `errorResult`.
3. **`get_check_type_samples` with valid type** → samples returned.
4. **`validate_check` happy path.** Stub returns valid; result includes no errors.
5. **`validate_check` with missing required field** — service returns validation error; surface as `errorResult` (or as a structured response with `IsError: false` if the validate API returns errors as data — verify with the real service).
6. **Round-trip via `tools/call`** for at least one of these to confirm wiring.

## Verification

`make dev-test` on port 4000.

```bash
TOKEN=$(cat /tmp/token.txt)

# Discover types
curl -s -X POST -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"list_check_types","arguments":{}}}' \
  http://localhost:4000/api/v1/mcp | jq '.result.content[0].text' | jq '.checkTypes[].type'

# Get HTTP samples
curl -s -X POST -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"get_check_type_samples","arguments":{"type":"http"}}}' \
  http://localhost:4000/api/v1/mcp | jq '.result.content[0].text' | jq

# Validate a config
curl -s -X POST -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"validate_check","arguments":{"type":"http","config":{"url":"https://example.com"},"name":"Example"}}}' \
  http://localhost:4000/api/v1/mcp | jq

# Validate a broken config (missing url)
curl -s -X POST -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"validate_check","arguments":{"type":"http","config":{}}}}' \
  http://localhost:4000/api/v1/mcp | jq
# Expect: validation error response
```

Then via Claude Desktop: *"Set up an HTTP check for https://example.com that fails if it doesn't return 200 within 5 seconds."* Verify the LLM does:
1. `get_check_type_samples` with `type=http`
2. modifies a sample with the URL
3. `validate_check` to confirm
4. `create_check` to persist

## Files touched

- `server/internal/mcp/handler.go` — add `checkTypesSvc *checktypes.Service` field; extend `NewHandler` signature.
- `server/internal/app/server.go` — pass the existing `checkTypesSvc` into `mcp.NewHandler` (locate the call site).
- `server/internal/mcp/tools_checktypes.go` — new (~150 lines for 3 tools).
- `server/internal/mcp/tools.go` — register the 3 tools.
- `server/internal/mcp/tools_checktypes_test.go` — new.

No DB change. No new dependency.

## Implementation Plan

1. Locate the existing `checktypes.NewService(...)` call site in `server/internal/app/server.go`.
2. Add `checkTypesSvc` parameter to `mcp.NewHandler`; update the call site to pass the existing instance.
3. Write `tools_checktypes.go` with all three tools. Tool descriptions must explicitly chain the workflow.
4. Register in `tools.go`.
5. Tests for the 5 cases above plus one round-trip.
6. `make gotest` + `make lint-back` clean.
7. End-to-end smoke test via Claude Desktop natural-language flow.
