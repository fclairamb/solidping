# GET on /api/v1/mcp falls through to the dashboard SPA instead of behaving like an MCP endpoint

## Problem

The docs (`web/docs/docs/features/mcp.md`, `wiki/features/mcp.md`) tell users to
connect MCP clients to `{SP_BASE_URL}/api/v1/mcp` (e.g.
`claude mcp add --transport http solidping https://solidping.k8xp.com/api/v1/mcp`,
plus Cursor/VS Code deep links). But opening that URL — or any client probing it
with `GET` — lands on the dashboard web interface instead of anything
MCP-shaped.

Verified against the live dev deployment (2026-07-10):

```
GET  https://solidping.k8xp.com/api/v1/mcp  → 200 text/html   (the SPA)
POST https://solidping.k8xp.com/api/v1/mcp  → 401 application/json
     WWW-Authenticate: Bearer resource_metadata="https://solidping.k8xp.com/.well-known/oauth-protected-resource"
GET  /.well-known/oauth-protected-resource  → 200 application/json
```

Root cause: the MCP route only registers `POST`
(`server/internal/app/server.go:575-576`):

```go
mcpGroup := api.NewGroup("/mcp").Use(authMiddleware.RequireMCPAuth)
mcpGroup.POST("", s.mcpHandler.Handle)
```

Any other method on `/api/v1/mcp` is unmatched, so the request falls through to
the SPA catch-all `mainGroup.GET("/*path", s.serveAppRoot)`
(`server/internal/app/server.go:1218`), which serves the dashboard HTML. This
is wrong twice over:

1. **MCP Streamable HTTP spec violation.** The transport spec requires the
   server to handle `GET` on the MCP endpoint: either open an SSE stream for
   server-initiated messages, or return **405 Method Not Allowed**. A `200
   text/html` response is neither — clients that probe with GET (Claude's
   connector does, to open a listening stream) get an HTML page where they
   expect SSE or 405. Same for `DELETE` (session termination): the spec allows
   405 if unsupported, not an HTML page.
2. **API-namespace hygiene.** Nothing under `/api/v1/` should ever serve the
   SPA. Any unmatched method/path in the API prefix should produce a JSON
   error (`NOT_FOUND` / 405), not `text/html` — HTML responses inside the API
   namespace confuse every non-browser client, not just MCP ones.

Note the parts that already work: `POST` auth is correct (401 JSON with the
`WWW-Authenticate: Bearer resource_metadata=…` header MCP clients need for
OAuth discovery), and the OAuth protected-resource metadata document resolves.
So the breakage is confined to non-POST methods — but it is the first thing a
human (browser) or a probing client sees, making the whole integration look
broken.

Related placement problem: the dashboard page that documents this integration
lives at `/dash0/orgs/$org/organization/ai`
(`web/dash0/src/routes/orgs/$org/organization.ai.tsx`), inside the
**Organization** settings section. That's the wrong home — connecting an MCP
client is a **user**-level setup (the page's instructions revolve around
per-user PAT tokens / per-user OAuth grants), not org configuration. It should
live at `/dash0/orgs/$org/mcp`.

## Proposal

1. **Move the dashboard page `/orgs/$org/organization/ai` → `/orgs/$org/mcp`**:
   - Rename `web/dash0/src/routes/orgs/$org/organization.ai.tsx` to
     `mcp.tsx` (route `/orgs/$org/mcp`) and pull the nav entry out of the
     Organization settings sub-nav
     (`web/dash0/src/routes/orgs/$org/organization.tsx:31`) into the org-level
     navigation.
   - Keep a redirect from the old `/orgs/$org/organization/ai` path so
     existing bookmarks/links don't 404.
   - The page reads an optional `?from=get` search param (see next point) and,
     when present, shows a short contextual hint: "you opened the MCP API
     endpoint in a browser — here's how to actually connect a client".

2. **Make `GET /api/v1/mcp` redirect to that dashboard page** instead of
   serving the SPA shell (`server/internal/app/server.go:575`):
   - Register an explicit `GET` handler that responds `302` →
     `/dash0/mcp?from=get` (e.g. on dev:
     `https://solidping.k8xp.com/dash0/orgs/test/mcp?from=get` after org
     resolution). The GET is unauthenticated so the server doesn't know the
     org; add a tiny org-less dash0 route `/mcp` that — like the existing
     root redirect (`web/dash0/src/routes/index.tsx`) — resolves the org from
     the auth context and forwards to `/orgs/$org/mcp`, preserving the query
     string. Logged-out users go through the normal login-with-`returnTo`
     flow.
   - Spec-compliance caveat: the Streamable HTTP transport expects GET on the
     MCP endpoint to be either an SSE stream or `405 Method Not Allowed`.
     Content-negotiate: if the request `Accept`s `text/event-stream` (an MCP
     client opening a listening stream), return `405` with `Allow: POST`;
     otherwise (a human in a browser) return the `302`. Browsers get the
     helpful page, clients get the spec answer.
   - The GET route sits **outside** `RequireMCPAuth` (a browser hitting the
     URL has no token; the point is a helpful redirect).
   - `DELETE` → terminate the session named by `Mcp-Session-Id` (the handler
     keeps a `sessions sync.Map` with a 1h TTL, so explicit termination is
     cheap to support), keeping auth via `RequireMCPAuth`.

3. **Stop the SPA catch-all from swallowing `/api/v1/*`**: make unmatched
   requests under the API prefix return the standard JSON error shape
   (`{"title": …, "code": "NOT_FOUND"}`) instead of falling through to
   `serveAppRoot`. This fixes the whole class, not just MCP.

4. **Test the integration completely**, not just the status codes:
   - Go handler tests for GET/DELETE on `/api/v1/mcp` (302 + `Location` for
     browser-`Accept`, 405 + `Allow: POST` for `Accept: text/event-stream`,
     session termination on DELETE) and for JSON-404 on an unmatched
     `/api/v1/...` path.
   - Playwright e2e (`web/dash0/e2e/`): the moved `/orgs/$org/mcp` page
     renders, the old `/orgs/$org/organization/ai` path redirects to it, and
     following `GET /api/v1/mcp` in a browser lands on the MCP page with the
     `?from=get` hint visible.
   - An end-to-end exercise of the documented client flow against a running
     server: `claude mcp add --transport http` (or a scripted equivalent:
     OAuth discovery → dynamic client registration → authorize → token →
     `initialize` → `tools/list` → one `tools/call`), plus the PAT-token
     path (`Authorization: Bearer <PAT>` straight to POST). The existing
     protocol-level tests live in `server/internal/mcp/` — the gap is the
     HTTP-routing/method layer and a full client-shaped walk-through.
   - Re-verify on `solidping.k8xp.com` after deploy: browser GET must land on
     `/dash0/orgs/<org>/mcp?from=get`, not the raw SPA shell.
   - Update docs (`web/docs/docs/features/mcp.md`, `wiki/features/mcp.md`) if
     they reference the old dashboard page location.

## Open questions

- Exact placement of the "MCP" entry in the org-level navigation (main
  sidebar vs. elsewhere) — follow the design reference
  (`web/dash0/src/routes/orgs/$org/design-reference.tsx`) conventions when
  implementing.

## Implementation Plan

### 1. Backend — method-aware `/api/v1/mcp` (`server/internal/mcp/handler.go`)
- `HandleGet`: content-negotiate on `Accept`. If the header lists
  `text/event-stream` (an MCP client opening a listening stream) → `405
  Method Not Allowed` + `Allow: POST, DELETE` and the standard JSON error
  shape; otherwise (a human in a browser) → `302` with
  `Location: /dash0/mcp?from=get`.
- `HandleDelete`: session termination per Streamable HTTP. Requires claims
  (route sits behind `RequireMCPAuth`). Reads `Mcp-Session-Id`; missing →
  `400 VALIDATION_ERROR`; unknown session or org mismatch → `404 NOT_FOUND`;
  found → delete from the `sessions sync.Map`, respond `204`.
- Error bodies reuse `base.ErrorResponse` (`{title, code, detail}`).

### 2. Backend — routing (`server/internal/app/server.go`)
- Register `GET /api/v1/mcp` on the unauthenticated group (browser has no
  token; the point is the helpful redirect); keep `POST` and add `DELETE`
  behind `RequireMCPAuth`.
- Stop the SPA catch-all from swallowing the API namespace: `serveAppRoot`
  returns the standard JSON `NOT_FOUND` error for any unmatched path under
  `/api/` instead of serving the SPA shell.

### 3. Backend — tests
- `server/internal/mcp/handler_test.go`: table/unit tests for `HandleGet`
  (302 + `Location` for browser Accept; 405 + `Allow` for
  `Accept: text/event-stream`, including multi-value Accept) and
  `HandleDelete` (204 happy path, 400 missing header, 404 unknown session,
  404 cross-org session).
- `server/internal/app/mcp_endpoint_test.go`: full-stack integration test —
  real `NewServer` (sqlite-memory) + `SetupRoutes` + `httptest.Server`:
  - unmatched `GET /api/v1/...` → JSON 404, not HTML;
  - `GET /api/v1/mcp` browser → 302 → `/dash0/mcp?from=get`; SSE Accept →
    405 + `Allow`;
  - full client-shaped walk-through: OAuth discovery
    (protected-resource + authorization-server metadata) → dynamic client
    registration → authorize (session bearer, consent approve POST) → PKCE
    token exchange → `initialize` → `tools/list` → one `tools/call` →
    `DELETE` session termination;
  - PAT path: mint a PAT via `POST /api/v1/orgs/:org/tokens`, then straight
    `POST /api/v1/mcp` with `Authorization: Bearer <PAT>`.

### 4. Frontend — move the page to `/orgs/$org/mcp` (`web/dash0`)
- `src/routes/orgs/$org/mcp.tsx`: the moved AI-assistants page (from
  `organization.ai.tsx`), with `validateSearch` for `from` and a contextual
  hint Alert when `from === "get"` ("you opened the MCP API endpoint in a
  browser — here's how to connect a client"). Standalone page heading (it no
  longer lives under the Organization tabs layout).
- `src/routes/orgs/$org/organization.ai.tsx`: `beforeLoad` redirect to
  `/orgs/$org/mcp` so old bookmarks don't 404.
- `src/routes/mcp.tsx`: org-less route that resolves the org from the auth
  context (like `index.tsx`) and forwards to `/orgs/$org/mcp` preserving the
  query string; logged-out users go through the normal login `returnTo` flow
  via the `/orgs/$org` guard.
- `src/routes/orgs/$org/organization.tsx`: drop the "AI assistants" tab.
- `src/components/layout/AppSidebar.tsx`: add an org-level nav entry (Bot
  icon, `nav:ai`) pointing at `/orgs/$org/mcp`.
- Locales: add the `from=get` hint strings to `org.json` (en/fr/es/de).

### 5. Frontend — e2e (`web/dash0/e2e`)
- Rename `organization-ai.spec.ts` → `mcp-page.spec.ts`; update paths to
  `orgs/test/mcp`; add: old `/organization/ai` path redirects to the new
  page; browser `GET /api/v1/mcp` lands on the MCP page with the `?from=get`
  hint visible.

### 6. Docs
- Update `web/docs/docs/features/mcp.md`, `web/docs/docs/intro.md`, and
  `wiki/features/mcp.md` where they reference "Organization → AI
  assistants" to the new sidebar location.

### 7. QA
- `make build-backend lint-back test`; `make build-dash0` +
  `cd web/dash0 && bun run lint`; run the new/renamed Playwright spec
  against a test-mode server.
- Live re-verify on `solidping.k8xp.com` requires a deploy — out of scope
  for this change; noted for post-deploy verification.
