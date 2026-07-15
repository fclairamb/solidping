---
model: opus
effort: xhigh
---

# Cross-org navigation breaks the live WS — because the WS is the only surface enforcing org scope (REST has none)

## Problem

Reported repro: load `/dash0/orgs/test/organization/members`, then
`/dash0/orgs/default/organization/members` — the second page has no working
WebSocket, while the page itself (REST) works fine.

**Diagnosed 2026-07-15. The reported symptom is real, but its cause is the
inverse of the obvious reading: the WebSocket is not under-authenticated —
the REST API is under-authorized.** Two distinct bugs, one of them critical.

### Reproduction (local dev, SQLite, `make dev`)

A non-superadmin user (`dual@example.com`) who is a genuine member of **both**
`default` and `test`, holding a token minted for `test`:

| token scope | target org | REST | WS (cookie) | WS (Authorization header) |
|---|---|---|---|---|
| `test`, role=admin | `test` | 200 | `hello` ✅ | `hello` ✅ |
| `test`, role=admin | `default` | **200** | **CLOSE 4403** | **CLOSE 4403** |
| `default`, role=admin | `test` | **200** | **CLOSE 4403** | **CLOSE 4403** |

Superadmins never reproduce this — `claims.IsSuperAdmin()` short-circuits every
check. Both seeded accounts (local `admin@solidping.com`, k8xp
`test@test.com`) are superadmins, which is why the API-level matrix and a
scripted browser run both come back green on k8xp. **You must test with a
non-superadmin member of two orgs.**

### Bug 1 — CRITICAL: REST org routes have no org authorization at all

`middleware.RequireOrgAccess` exists and is correct, but it is wired to only
three route groups (`server/internal/app/server.go:605`, `:620`, `:658` —
jobs + two admin groups). Everything else is `RequireAuth` only
(`server.go:461,465,470,476,481,648`), and `RequireAuth` never reads
`:org`. Handlers take the slug straight from the path and query with it
(`server/internal/handlers/checks/handler.go:108` — `orgSlug := req.Param("org")`,
no authorization anywhere in the function).

Verified with a user who is a member of **only** `test` and not `default`:

```
GET /api/v1/orgs/default/checks     -> 200
GET /api/v1/orgs/default/members    -> 200   ← returns default's member list + emails
GET /api/v1/orgs/default/incidents  -> 200
```

**Any authenticated user of any org can read any other org's checks,
incidents, and member list (including member emails).** This is a
cross-tenant data leak, not merely a scoping inconsistency. It is also what
masks bug 2: REST silently allows the cross-org page to render, so the WS
looks like the only thing "broken" when it is in fact the only surface doing
its job.

### Bug 2 — the WS uses claims-org equality, not membership, and 4403 is terminal

`realtimews.authorizeOrg` (`server/internal/handlers/realtimews/handshake.go:101`):

```go
if !claims.IsSuperAdmin() && claims.OrgSlug != orgSlug {
    return nil, CloseForbidden, "access to this organization is denied"
}
```

Its doc comment says it "mirrors middleware.RequireOrgAccess exactly" — and it
does mirror that function faithfully. The problem is that **the function it
mirrors is effectively dead code**, so the WS enforces a policy the rest of the
product does not. For a legitimate member of both orgs whose token happens to
be scoped to the other one, this is a false rejection: membership, not
`claims.OrgSlug` equality, is the product's real access rule (that is what the
member-list endpoint effectively grants today, and what the org switcher
implies).

Client-side, 4403 is **terminal**: `live-socket.ts:313` maps it to
`"disabled"` → `run()` calls `callbacks.onDisabled()` and returns
(`live-socket.ts:244-247`), so the reconnect loop exits permanently and
`setStatus("disabled")` is pinned as terminal
(`LiveEventsContext.tsx:455-466`). One 4403 kills live updates for the whole
page lifetime with no retry — which is exactly "the second link won't have a
working websocket connection".

### Both stated hypotheses were tested and are refuted

1. **"Reply 401 instead of 101."** Already shipped. The handshake authenticates
   *before* `websocket.Accept` and returns a real HTTP 401
   (`realtimews/handler.go:105-108`). Verified:
   ```
   no token      -> HTTP 401 {"title":"Authorization token is required","code":"NO_TOKEN"}
   garbage token -> HTTP 401
   ```
   The 101-then-close the report describes is **4403 = authorization**, not
   authentication. Org denial is deliberately post-upgrade because a browser
   cannot read the HTTP status of a failed WS handshake — only a close code.
   That trade-off is sound and should stay.

2. **"The WS lacks an `Authorization` header; adding it would fix it."** Two
   independent reasons this is not the bug:
   - Browsers **cannot** set headers on `new WebSocket(url)` — there is no such
     API. That is precisely why the cookie handshake exists (spec
     2026-07-14-04). It is unimplementable in the dashboard as stated.
   - Empirically it changes nothing: the table above dials the WS **with** an
     explicit `Authorization: Bearer` header and still gets **4403**. The token
     is identical either way; the rejection is org-scope, not the auth channel.
     `realtimews.extractToken` (`handshake.go:30`) already prefers the header
     and falls back to the cookie.

## Proposal

Order matters — bug 1 is a live cross-tenant leak and should not wait behind
the WS fix.

1. **Close the cross-tenant hole (security, ship first).** Put org
   authorization on every `/orgs/:org/*` route rather than three groups. Prefer
   making it structural: a single parent group carrying
   `RequireAuth + RequireOrgAccess` that all org routes hang off, so a new
   route cannot silently opt out. Audit every group at `server.go:461-660` and
   the public/service-token exceptions (`ServiceTokenBypass`, status-page
   subscriber routes at `server.go:1107`) so they stay reachable. Expect this to
   surface real breakage: any multi-org UI that relies on today's leniency will
   start 403ing, which is the point.

2. **Decide the real access rule, then make both surfaces share it.** The two
   candidates are token-scope equality (`claims.OrgSlug == orgSlug`, today's WS)
   versus membership (`GetMemberByUserAndOrg`, what the leak accidentally
   approximates). Membership is the better product answer for multi-org users;
   token-scope is stricter and forces a switch-org round trip on every org
   change. **This is a judgment call with UX and security consequences and
   should be made explicitly, not inherited.** Whichever wins, extract it into
   one function that REST middleware and `realtimews.authorizeOrg` both call, so
   they cannot drift again — the current "mirrors X exactly" comment is only
   true against dead code. If membership wins, the dashboard should also stop
   depending on a token minted for a different org (auto switch-org on org
   change), so `claims.OrgSlug` tracks the URL.

3. **Make 4403 non-terminal client-side.** Even with the policy unified, a
   permanent give-up on one 4403 is too brittle (it also fires for
   "organization not found", `handshake.go:91`). On 4403, refresh once
   (re-minting the cookie for the current session) and redial; only report
   `onDisabled` if a second attempt is also refused. Distinguishing org-denied
   from org-not-found with separate close codes would help the client decide.

4. **Regression coverage.**
   - Backend table test: non-superadmin member of two orgs — REST and WS must
     agree for every (token org × target org) pair, including the negative case
     (non-member ⇒ both refuse). This is the test that would have caught both bugs.
   - A test asserting a non-member gets 403 from `/orgs/:org/checks|members|incidents`.
   - `live-socket.test.ts`: 4403 retries once after a refresh before disabling.

### Notes for whoever picks this up

- Test users must be **non-superadmin**; the seeded admins hide the bug entirely.
- Local dev is SQLite at `server/solidping.db` (not the docker postgres —
  `solidping-postgres` isn't running; the `postgres` container on :54322
  belongs to another project). `sqlite3 -cmd ".timeout 8000"` to get past the
  live server's lock.
- Do not run the dash0 Playwright config against the dev server: its
  `globalSetup` sets `SP_DB_RESET=true` and will wipe the dev database. Drive
  Chromium directly, or use a side-car server on another port with `E2E_BASE_URL`.

## Implementation Plan

### Decision — the access rule is token-scope equality + membership (NOT membership-only)

The judgment call the spec flags (token-scope equality vs. membership) is
**decided in favor of token-scope equality plus membership** — i.e. exactly
what `middleware.RequireOrgAccess` and `realtimews.authorizeOrg` already
encode, now made the *only* rule and applied to every surface.

Rationale (this is the security-critical part, so it is spelled out):

- **The entire downstream authorization model trusts `claims.Role` /
  `claims.OrgSlug` as the role *for the org the token is scoped to*.** Grep
  shows many admin gates of the form `claims.Role == "admin"`
  (`handlers/discovery/handler.go:34`, `handlers/integrations/handler.go:33`,
  `handlers/entitlements/handler.go:90`, `handlers/auth/handler.go:671…827`,
  `handlers/auth/membership_requests_handler.go`). These read the role out of
  the JWT, not out of the target org's membership row.
- If we allowed **membership-only** cross-org access (drop the
  `claims.OrgSlug == orgSlug` guard), a user who is `admin` in org A (token
  scoped to A, `claims.Role == "admin"`) could navigate to org B where they are
  only a `viewer`, pass a membership check, and then have every
  `claims.Role == "admin"` gate grant them **admin** operations in B. That is a
  privilege-escalation hole strictly worse than the read leak we are closing.
- Token-scope equality guarantees `claims.Role` always describes the org being
  accessed, so it composes safely with every existing role gate. It is also the
  stricter, already-partially-deployed rule, which minimizes behavioral drift.

The UX cost of token-scope (a switch-org round trip on org change) is paid by
**auto switch-org on navigation** in the dashboard: the token is re-minted for
the URL's org before any org-scoped request fires, so `claims.OrgSlug` tracks
the URL. (Membership-only would have needed the same re-mint anyway to keep
`claims.Role` correct — so it buys no UX advantage while adding the escalation
footgun.)

### Steps

1. **Shared rule (single source of truth).** Add
   `auth.AuthorizeOrgAccess(ctx, db, claims, user, orgSlug) (*Organization, OrgAccessDenial)`
   in `handlers/auth/orgaccess.go`. It encodes: claims-super-admin ⇒ allow;
   non-super-admin token-scope mismatch ⇒ deny (checked *before* loading the
   org so a non-scoped user can't probe org existence); org load failure ⇒
   not-found; DB-super-admin ⇒ allow; otherwise membership required. Both REST
   middleware and the WS handshake call this exact function, so they cannot
   drift again (the old "mirrors X exactly" doc comments described two
   hand-maintained copies).

2. **Close the REST leak structurally (ship first).** Introduce an `orgGroup`
   helper in `server.go` that stamps `RequireAuth + RequireOrgAccess` on every
   `/orgs/:org/*` group, and route every previously `RequireAuth`-only org
   group through it. The intentional public `/orgs/:org/*` routes
   (`…/badges`, magic-link `…/incidents/:uid/ack`, the WS, public
   `…/status-pages/:uid/subscribers`) are registered directly on `api` (not via
   a `RequireAuth` group) and are therefore untouched; the service-token
   entitlements group keeps its `ServiceTokenBypass → RequireAuth →
   RequireOrgAccess` chain. `RequireOrgAccess` is rewritten to call
   `AuthorizeOrgAccess`.

3. **Unify the WS.** `realtimews.authorizeOrg` calls `AuthorizeOrgAccess`.
   Split the WS close codes: org-denied stays `4403` (`CloseForbidden`),
   org-not-found becomes a new terminal `4410` (`CloseNotFound`) so the client
   can tell "refresh and retry" apart from "this org is gone, give up".

4. **Client: 4403 non-terminal.** In `live-socket.ts`, a `4403` close no longer
   resolves terminally. The run loop refreshes the session once (re-minting the
   cookie) and redials; only a *second* consecutive `4403` reports
   `onDisabled`. `4404`/`4410` stay terminal.

5. **Dashboard auto switch-org.** `OrgLayout` re-mints the session for the URL's
   org when a non-super-admin member lands on an org that differs from
   `claims.OrgSlug` (uses the existing `switchOrg`), gating child rendering
   until the token matches — so no org-scoped request (or WS dial) fires with a
   foreign-scoped token.

6. **Regression coverage** (all four): `auth` table test over
   (token-org × target-org) for a two-org non-super-admin member — the single
   proof that REST and WS agree, since both call `AuthorizeOrgAccess`; a REST
   integration test asserting a non-member gets 403 from
   `/orgs/:org/checks|members|incidents`; a WS test for the two-org member; and
   a `live-socket.test.ts` case asserting 4403 retries once after a refresh
   before disabling.
