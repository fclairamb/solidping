# MCP — proper protocol-version negotiation

## Context

`server/internal/mcp/handler.go:29` hardcodes the advertised MCP protocol version:

```go
const mcpProtocolVer = "2025-03-26"
```

This string is returned unconditionally in `handleInitialize` (`handler.go:179-186`) regardless of what the client advertised in `params.ProtocolVersion`. Two issues:

1. **The MCP spec evolves.** When clients start sending `protocolVersion: "2025-06-18"` (which adds `outputSchema`, `structuredContent` formal recognition, and tool annotations), we still respond with `2025-03-26`. The newer-spec client should fall back to `2025-03-26` semantics, but we should *also* be able to opt into newer features by advertising the newer version when the client does.
2. **No supported-set declaration.** A client speaking a version we genuinely don't support (e.g. some far-future `2027-01-01`) gets back `2025-03-26` and may proceed with feature assumptions that don't hold. Per the spec, we should respond with the highest version we support that's `<=` the client's request, or fail cleanly.

The audit flagged this as Tier 3 — minor today, but a silent timebomb when newer client versions become common.

## Honest opinion

Negotiation isn't complicated. Maintain a small ordered list of supported versions, pick the highest the client also supports (or the highest we support if the client is newer), and return that. Reject if there's no overlap.

Don't over-engineer with a registry of per-version capability differences. The MCP protocol versions to date are mostly additive. We negotiate the version string; capability *flags* (already in `ServerCaps`) handle the per-feature fine print. One tiny function, one constant slice.

I'd ship this *before* spec `2026-05-03-21-mcp-structured-content-output.md` if possible, since `structuredContent` is officially `2025-06-18`-only — having proper negotiation lets us advertise the newer version when supported.

## Scope

**In:**
- Replace the `mcpProtocolVer` constant with a `supportedProtocolVersions []string` slice (descending-preferred).
- Implement `negotiateProtocolVersion(clientVersion string) (string, bool)` returning the chosen version and whether negotiation succeeded.
- Use the negotiated version in the `initialize` response.
- If negotiation fails, return a clear error (per MCP spec: respond with our latest supported version and let the client decide to disconnect — *do not* error the response).
- Tests for: matched version, client-newer (we cap), client-older (we honor), unsupported.

**Out:**
- Per-version capability gating. Use the `ServerCaps` flags model that's already in place.
- A version-changelog or migration helper. YAGNI.
- Bumping `2025-06-18` to "default supported" yet — only add it once we've shipped at least one feature that requires it (e.g. spec `2026-05-03-21`).

## Implementation

### Replace the constant

`server/internal/mcp/handler.go:29`:

```go
// supportedProtocolVersions in descending preference order.
// Add new versions to the front as we adopt them.
var supportedProtocolVersions = []string{
    "2025-03-26",
    // "2025-06-18", // enable once structuredContent / outputSchema are wired
}
```

`var` not `const` because slice. Rename or re-export only if used externally — it isn't.

### Negotiation function

```go
// negotiateProtocolVersion returns the version we should advertise back to a
// client that requested clientVersion. Per the MCP spec:
//   - if we support clientVersion, return it
//   - if we don't, return our latest supported version (client decides whether
//     to proceed or disconnect)
//   - empty string client → return our latest
func negotiateProtocolVersion(clientVersion string) string {
    if clientVersion == "" {
        return supportedProtocolVersions[0]
    }
    for _, v := range supportedProtocolVersions {
        if v == clientVersion {
            return v
        }
    }
    return supportedProtocolVersions[0]
}
```

The behavior on no-overlap (return our latest) matches the spec's recommended pattern. Per [MCP lifecycle docs](https://modelcontextprotocol.io/specification/2025-06-18/basic/lifecycle), the *client* is responsible for disconnecting if it can't speak the version we returned.

### Use it in `handleInitialize`

`handler.go:179-186`:

```go
negotiated := negotiateProtocolVersion(params.ProtocolVersion)
resp := successResponse(req.ID, InitializeResult{
    ProtocolVersion: negotiated,
    Capabilities: ServerCaps{
        Tools:     &ToolsCap{},
        Resources: &ResourcesCap{},
    },
    ServerInfo: ServerInfo{Name: "solidping", Version: "0.1.0"},
})
```

Also store the negotiated version on the session (already does this at `handler.go:170` — keep that, but verify it stores the *negotiated* version, not raw `params.ProtocolVersion`. If today it stores the raw client value, switch to negotiated for consistency).

### Logging

When the client requests a version we don't support, log it once — useful signal for "time to add the next version":

```go
if params.ProtocolVersion != "" && negotiated != params.ProtocolVersion {
    slog.InfoContext(req.Context(), "MCP version negotiation fallback",
        "clientRequested", params.ProtocolVersion,
        "serverReturned", negotiated)
}
```

Don't log on the empty-version case — that's just a vague client.

## Tests

`server/internal/mcp/handler_test.go` (extend the existing `initialize` test):

1. **Client requests `"2025-03-26"` → response `"2025-03-26"`.**
2. **Client requests `""` (empty) → response `"2025-03-26"` (our latest).**
3. **Client requests `"2099-01-01"` → response `"2025-03-26"` (our latest).** Log line emitted (capture via slog handler in test).
4. **(Forward-looking)** With `supportedProtocolVersions = ["2025-06-18", "2025-03-26"]`, client requests `"2025-03-26"` → we return `"2025-03-26"` (honor older). Same set, client requests `"2025-06-18"` → return `"2025-06-18"`.
5. **Session stores the negotiated version**, not raw client value.

## Verification

```bash
TOKEN=$(cat /tmp/token.txt)

# Match
curl -s -X POST -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"curl","version":"1"}}}' \
  http://localhost:4000/api/v1/mcp | jq '.result.protocolVersion'
# "2025-03-26"

# Newer client
curl -s -X POST -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"jsonrpc":"2.0","id":2,"method":"initialize","params":{"protocolVersion":"2099-01-01","capabilities":{},"clientInfo":{"name":"curl","version":"1"}}}' \
  http://localhost:4000/api/v1/mcp | jq '.result.protocolVersion'
# "2025-03-26"

# Empty
curl -s -X POST -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"jsonrpc":"2.0","id":3,"method":"initialize","params":{"capabilities":{},"clientInfo":{"name":"curl","version":"1"}}}' \
  http://localhost:4000/api/v1/mcp | jq '.result.protocolVersion'
# "2025-03-26"
```

Server logs should show the fallback line for the `2099-01-01` request.

## Files touched

- `server/internal/mcp/handler.go` — replace constant, add `negotiateProtocolVersion`, use it in `handleInitialize`.
- `server/internal/mcp/handler_test.go` — add 5 negotiation cases.

No DB change. No new dependency. No spec ahead-of-time enabled (we keep `2025-03-26` only until features depending on `2025-06-18` actually ship).

## Implementation Plan

1. Replace the `mcpProtocolVer` const with the `supportedProtocolVersions` slice.
2. Add `negotiateProtocolVersion`. Add the structured slog line on fallback.
3. Wire the negotiated version into both the response and the session record.
4. Tests for the 5 cases.
5. `make gotest` + `make lint-back` clean.
6. Smoke-test with curl (the three commands above).
