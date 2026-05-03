# MCP — scoped tokens for MCP clients

## Context

The MCP endpoint at `POST /api/v1/mcp` authenticates via the same JWT used for the dashboard and REST API (`server/internal/mcp/handler.go:103-108`):

```go
claims, ok := middleware.GetClaimsFromContext(req.Context())
if !ok {
    return writeJSON(writer, http.StatusUnauthorized,
        errorResponse(nil, CodeInvalidRequest, "Authentication required"))
}
orgSlug := claims.OrgSlug
```

That JWT carries the user's full role (admin, user, viewer) and grants every permission the user has in the organization. A user setting up Claude Desktop or another MCP client today has to:

1. Log into the dashboard.
2. Open browser dev tools, copy their full-access JWT.
3. Paste it into the client's config.

Three problems:

1. **Wrong tool for the job.** The user JWT is short-lived and rotates with login. MCP clients want a long-lived credential they can paste once.
2. **No least-privilege.** A user who only wants Claude to *read* their checks is forced to hand over a token that can also delete every check.
3. **No revocation per-client.** Rotating means relogging in everywhere.

The org-token endpoint already exists (`POST /api/v1/orgs/$org/tokens`), per the project CLAUDE.md, and the `tokens` API is plumbed through `auth/`. What's missing: an explicit *MCP scope* and a UI surface to mint a token for MCP use.

## Honest opinion

Do this in two layers:

1. **Backend: add an `mcp` scope** (and potentially `mcp:read`) to the existing token model. MCP middleware checks the scope before allowing access; for `mcp:read`, refuse non-read tools (anything starting with `create_`, `update_`, `delete_`, `set_`).
2. **Frontend: a "Generate MCP token" button** in the org settings → tokens page that pre-selects the right scope and shows the generated token *once* with a copy-to-clipboard control and clear "save it now, you won't see it again" UI.

I'd resist building a fancier "per-tool permission grid" for v1 — too much surface, low real demand. Two scopes (`mcp` full, `mcp:read` read-only) is the right granularity to start. Promote to per-tool only if real users ask.

The frontend half can ship later than the backend half — once the backend supports it, a power user can `curl` their way to a token. The button is for ergonomics.

## Scope

**In:**
- Add `mcp` and `mcp:read` to the token-scope model. Locate the existing scope set in the tokens code (likely `server/internal/handlers/auth/` or `server/internal/db/models/token.go` — verify).
- Update MCP middleware (or `Handler.Handle`) to:
  - Accept tokens with scope including `mcp` or `mcp:read`.
  - Reject tokens with neither.
  - For `mcp:read`-only tokens, refuse `tools/call` for any mutation tool.
- Update the existing tokens UI to allow selecting `mcp` or `mcp:read` when creating a token.
- Add a "Generate MCP token" shortcut button on the same page.
- Tests for: `mcp` token grants full access, `mcp:read` token grants list/get/diagnose only, no scope → 403.

**Out:**
- Per-tool ACL grid. Two scopes is enough for v1.
- OAuth-style flow for MCP clients to enroll themselves. Manual paste is fine.
- Showing the user which MCP client is using which token (audit log). Defer.
- Migrating existing dashboard JWTs to deny MCP. Backward compat: dashboard JWTs continue to work for MCP unless explicitly opted out (see "Backward compat" below).

## Backward compatibility

Existing JWT auth on `/api/v1/mcp` continues to work — admins can still paste their dashboard token if they want. The new scope is **additive**: a token with `mcp` (or `mcp:read`) scope can be used; a normal user JWT is still allowed because it has implicit "all scopes within my role" semantics.

If we want to lock this down later (require explicit `mcp` scope), do it as a follow-up with deprecation warnings first. Not in this spec.

## Backend

### Scope model

Locate the scope definition. In typical Go API setups it's something like:

```go
type TokenScope string
const (
    ScopeAdmin    TokenScope = "admin"
    ScopeUser     TokenScope = "user"
    ScopeViewer   TokenScope = "viewer"
    ...
)
```

Add:

```go
ScopeMCP     TokenScope = "mcp"
ScopeMCPRead TokenScope = "mcp:read"
```

If scopes are validated against an allowlist on token creation, add the new values there too.

### MCP handler check

`server/internal/mcp/handler.go` — extend `Handle()` (~line 96) to read the scopes from the claims and reject if neither user-role auth nor `mcp`/`mcp:read` is present:

```go
claims, ok := middleware.GetClaimsFromContext(req.Context())
if !ok {
    return writeJSON(writer, http.StatusUnauthorized,
        errorResponse(nil, CodeInvalidRequest, "Authentication required"))
}

if !hasMCPAccess(claims) {
    return writeJSON(writer, http.StatusForbidden,
        errorResponse(nil, CodeForbidden, "Token lacks mcp or mcp:read scope"))
}
```

`hasMCPAccess`: returns true if the claim is a user JWT (any role), or an org token with `mcp` or `mcp:read` scope. (Adjust to whatever the claims struct looks like.)

For `mcp:read`-only enforcement, add to `handleToolsCall` (~line 196):

```go
if isMCPReadOnly(claims) && isMutationTool(params.Name) {
    resp := errorResponse(req.ID, CodeForbidden,
        "Tool requires mcp scope, current token has mcp:read only")
    return &resp, http.StatusOK
}
```

`isMutationTool`: deny-list approach — refuse anything matching `create_*`, `update_*`, `delete_*`, `set_*`, `validate_*` (validate writes nothing but it's a mutation operation conceptually; revisit). Use a small map literal in `tools.go` or annotate the `ToolDefinition` with a `Mutation bool` field — the latter is cleaner long-term but requires updating every `*Def()`. **Recommendation: deny-list for v1.**

### Tests

`server/internal/mcp/handler_test.go` — add:

1. **Token with `mcp` scope can call any tool.**
2. **Token with `mcp:read` scope can call `list_*` / `get_*` / `diagnose_*` tools.**
3. **Token with `mcp:read` scope refused on `create_check`, `update_check`, `delete_check`, etc. with 403 + descriptive error.**
4. **Token with no relevant scope refused on the entire endpoint with 403.**
5. **Existing user JWT still works (backward compat).**

## Frontend

### Tokens page (existing)

Find the org tokens management page in `web/dash0/src/` (likely `web/dash0/src/pages/settings/tokens/` or similar — locate by searching for the existing token-list UI).

Add to the "Create token" form a scope multi-select (or radio if scopes are mutually exclusive in your model). Include `mcp` and `mcp:read` as options.

### "Generate MCP token" shortcut

A button at the top of the tokens page labelled "Generate MCP token" that:

1. Opens a small dialog asking: name (default "Claude Desktop"), scope (`mcp` full / `mcp:read` read-only — default `mcp`).
2. On submit, calls `POST /api/v1/orgs/$org/tokens` with the selected scope.
3. Shows the generated token in a one-time-display modal with copy-to-clipboard, MCP-client config snippet (e.g. `Authorization: Bearer <token>` or the Claude Desktop JSON config block), and a clear warning: *"This is the only time you'll see this token. Copy it now."*

i18n: keys for the new strings under `web/dash0/src/locales/{en,fr,de,es}/tokens.json` (or wherever existing token strings live).

### Tests

If there's existing Playwright coverage for the tokens page, extend it:

1. Click "Generate MCP token" → modal opens, defaults populated.
2. Submit → token shown once, copy button works.
3. Refresh page → token visible in list (by name) but value not exposed.

## Verification

```bash
TOKEN=$(cat /tmp/token.txt)

# Create an MCP-scope token via REST
NEW=$(curl -s -X POST -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"name":"claude-desktop","scopes":["mcp"]}' \
  http://localhost:4000/api/v1/orgs/default/tokens | jq -r '.token')

# Use it on /api/v1/mcp
curl -s -X POST -H "Authorization: Bearer $NEW" -H 'Content-Type: application/json' \
  -d '{"jsonrpc":"2.0","id":1,"method":"tools/list"}' \
  http://localhost:4000/api/v1/mcp | jq '.result.tools | length'

# Read-only token rejects create_check
NEW_READ=$(curl -s -X POST -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"name":"claude-desktop-ro","scopes":["mcp:read"]}' \
  http://localhost:4000/api/v1/orgs/default/tokens | jq -r '.token')

curl -s -X POST -H "Authorization: Bearer $NEW_READ" -H 'Content-Type: application/json' \
  -d '{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"create_check","arguments":{"slug":"x","type":"http","config":{"url":"https://example.com"}}}}' \
  http://localhost:4000/api/v1/mcp | jq
# Expect: error response with "mcp:read only" message

curl -s -X POST -H "Authorization: Bearer $NEW_READ" -H 'Content-Type: application/json' \
  -d '{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"list_checks","arguments":{}}}' \
  http://localhost:4000/api/v1/mcp | jq '.result.content[0].text' | jq '.data | length'
# Expect: numeric count, no error
```

UI: visit `/dash0/orgs/default/settings/tokens`, click "Generate MCP token", confirm the modal flow.

## Files touched

- `server/internal/db/models/token.go` (or wherever scopes live) — add `mcp` and `mcp:read` constants.
- `server/internal/handlers/auth/` — accept the new scopes on token creation.
- `server/internal/middleware/` — claims may need to expose scopes if not already; check.
- `server/internal/mcp/handler.go` — `hasMCPAccess`, `isMCPReadOnly`, `isMutationTool` checks.
- `server/internal/mcp/handler_test.go` — new test cases.
- `web/dash0/src/pages/settings/tokens/` (locate exact path) — scope selector + "Generate MCP token" button + one-time-display dialog.
- `web/dash0/src/locales/{en,fr,de,es}/tokens.json` — new i18n keys.
- `web/dash0/tests/...` — Playwright coverage if present.

No DB migration if the scopes column is `text[]` or similar — verify. If scopes are stored as a constrained enum, add a migration.

## Implementation Plan

There is no existing token-scope system in solidping today: PATs have no scope storage, and `Claims` (`server/internal/handlers/auth/service.go:97`) carries only `UserUID/OrgSlug/Role`. This plan adds the missing plumbing along with the MCP gate.

1. Add `Scopes []string` to `Claims` and a `[]string` field on `CreateTokenRequest`. Store the scopes inside the token's existing `Properties JSONMap` (`token.Properties["scopes"]`) so no DB migration is needed.
2. In `Service.CreateToken` (`auth/service.go:1153`), persist the scope list when supplied. In `Service.ValidatePATToken`, read the scopes back from `Properties` and populate `Claims.Scopes`. Update the PAT cache to remember the new field.
3. In `mcp/handler.go`:
   - `hasMCPAccess(claims)` — true when scopes are empty (back-compat: dashboard JWT keeps working) OR scopes contain `mcp` / `mcp:read`. Wire into `Handle()`.
   - `isMCPReadOnly(claims)` — true when scopes contain `mcp:read` and not `mcp`.
   - `isMutationTool(name)` — deny-list of name prefixes (`create_`, `update_`, `delete_`, `set_`).
   - In `handleToolsCall`, refuse mutation tools for read-only tokens with `CodeForbidden`.
4. Add `internal/mcp/scope_test.go` for the predicate functions and extend `handler_test.go` with the five scenarios (mcp full / mcp:read full / mcp:read mutation refused / no scope rejected / back-compat user JWT allowed).
5. Drop the spec's frontend section out of this PR — explicitly call it out in the spec as deferred. The REST API alone lets a power user `curl` a token, which is what the spec says is acceptable for shipping the backend half.
6. `make fmt && make lint-back && make test`.
