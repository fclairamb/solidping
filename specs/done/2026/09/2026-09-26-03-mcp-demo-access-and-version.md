---
effort: medium
---

# The public demo account cannot use MCP at all, and MCP reports version 0.1.0

## Problem

### 1. Every MCP request from a demo session is refused

A demo session (`claims.Demo`, e.g. `demo@solidping.io` in org `demo` with
`SP_DEMO_ENABLED=true`) gets `403 {"code":"DEMO_READ_ONLY"}` on **every** POST
to `/api/v1/mcp`: `initialize`, `tools/list`, and read tools like `list_checks`.
So anyone pointing an agent at solidping.io with the published demo credentials
hits a wall before the handshake completes. Found 2026-09-26 while listing
solidping on Glama.

Cause: `RequireMCPAuth` applies the route-level demo write guard
(`server/internal/middleware/auth.go:234-242`). MCP is JSON-RPC over POST, and no
`/mcp` route is on the allowlist (`demoAllowedRoutes`,
`server/internal/handlers/auth/demo_guard.go:78-85`), so the guard is an
unconditional deny for a demo principal. The comment above it says so on
purpose. The guard cannot tell a read from a write here, because on MCP that
distinction lives in the JSON-RPC method and tool name, not in the HTTP method.

The REST API gives a demo session more than that: all reads, plus creating,
editing, deleting and cloning checks it created itself. Ownership is enforced in
`checks.Service` via `assertDemoMayWriteCheck`
(`server/internal/handlers/checks/demo.go:116`, called at
`server/internal/handlers/checks/service.go:2036` and `:2695`), which reads
`claims.Demo` / `claims.UserUID` from the context
(`demo.go:84-91`). New checks get `created_by` from `createdByForCaller`
(`service.go:1562`).

Reproduce:

```sh
SP_RUN_MODE=demo SP_DEMO_ENABLED=true solidping serve
# POST /api/v1/auth/login {"email":"demo@solidping.io","password":"demo","org":"demo"}
# POST /api/v1/mcp initialize with Authorization: Bearer <accessToken>  -> 403 DEMO_READ_ONLY
```

### 2. `serverInfo.version` is hardcoded

`handleInitialize` answers `ServerInfo{Name: "solidping", Version: "0.1.0"}`
(`server/internal/mcp/handler.go:419`) whatever the build. v0.32.1 reports
0.1.0. The build version is already injected at link time into
`server/internal/version.Version` (`server/internal/version/version.go:14`,
`Dockerfile:129-132`).

## Proposal

### 1. Move the demo decision from the route to the tool call

- In `RequireMCPAuth` (`middleware/auth.go:234-242`), stop applying the route
  guard to the MCP endpoint. Keep everything else in that middleware (token,
  audience, user checks) as is. `RequireAuth` (`auth.go:104`) is unchanged: the
  REST guard stays exactly where it is.
- In `handleToolsCall` (`server/internal/mcp/handler.go:430`), next to the
  existing scope and role gates (`handler.go:445-460`), add a demo gate that runs
  when `claims.Demo` is true and `isMutationTool(name)` (`scope.go:69`):
  - allow `create_check`, `update_check`, `delete_check`. Ownership is then
    enforced by `checks.Service` exactly as on REST, as long as the claims are
    on the context the tool receives. Verify that, and add a test that proves
    `update_check` on a seeded check (null `created_by`) is refused;
  - refuse every other mutation tool (status pages, integrations, maintenance
    windows, incident publications, `set_*`) with `CodeForbidden` and
    `auth.DemoWriteMessage`, so the MCP refusal reads the same as the REST one.
- Non-tool methods (`initialize`, `notifications/*`, `tools/list`,
  `resources/*`, `prompts/*`) are reads and pass. `DELETE /mcp` (session close)
  passes too, for the same reason `POST /auth/logout` is on the REST allowlist.
- Check the read-shaped tools that are not prefixed `create_`/`update_`/
  `delete_`/`set_` and could still write or spend resources for a demo
  visitor, starting with `diagnose_check` (`tools_diagnose.go`) and any
  validate or dry-run tool. Anything that persists state is treated as a
  mutation for demo purposes.
- Update the comment at `middleware/auth.go:234` and the "exactly four things"
  comment on `models.User.Demo` (`server/internal/db/models/auth.go:84-92`) so
  they describe where the MCP decision now lives.

Tests: follow `server/internal/mcp/write_role_test.go` for a demo principal
covering `initialize`, `tools/list`, `list_checks` (allowed), `create_check`
(allowed, owned), `update_check` on a seeded check (refused), and
`create_status_page` (refused with `DEMO_READ_ONLY`). Add a middleware test in
`server/internal/middleware/mcpauth_test.go` that a demo token now reaches the
handler.

### 2. Report the real version

Use `version.Version` in `handleInitialize` (`handler.go:419`). Local builds
report `dev`, which is correct. Update any test asserting `0.1.0`.
