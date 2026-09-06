---
model: opus
effort: high
---

# The `viewer` role is documented as read-only but nothing enforces it

## Problem

`models.MemberRole` ranks four roles, `owner > admin > user > viewer`
([auth.go:176](../../server/internal/db/models/auth.go:176)), with `viewer`
described as the read-only tier and offered as such in the members UI
([organization.members.tsx:162](../../web/dash0/src/routes/orgs/$org/organization.members.tsx:162))
and the membership-request approval flow
([membership_requests.go:244](../../server/internal/handlers/auth/membership_requests.go:244)).
An admin who assigns it reasonably believes the member can look but not
touch.

That belief is false. Nothing in the request path distinguishes `viewer` from
`user`:

- `RequireOrgAccess`
  ([middleware/auth.go:217](../../server/internal/middleware/auth.go:217))
  answers one question only — is this user a member of this org — via the
  shared `auth.AuthorizeOrgAccess`. Its body contains no role logic.
- The only role gates that exist are `RequireOrgAdmin` and `RequireOrgOwner`
  ([auth.go:523](../../server/internal/middleware/auth.go:523),
  [:531](../../server/internal/middleware/auth.go:531)), both built on
  `requireOrgRole` ([:539](../../server/internal/middleware/auth.go:539)).
  There is no `user`-level gate.
- No handler under `server/internal/handlers/checks/` calls
  `claims.HasOrgRole(...)`, and no code path anywhere gates on
  `models.MemberRoleUser`. Outside `models/auth.go`, the only references to
  `MemberRoleViewer` are the two places that *assign* it.

So every non-GET route registered through the plain `orgGroup` helper
([server.go:767](../../server/internal/app/server.go:767)) — `RequireAuth` +
`RequireOrgAccess`, nothing more — is open to a viewer exactly as it is to a
user. The exposed surface, from the route table:

| Group | Writes a viewer can perform today |
|---|---|
| `/orgs/:org/checks` ([:1003](../../server/internal/app/server.go:1003), [:1023-1028](../../server/internal/app/server.go:1023)) | create, upsert, patch, delete, clone checks |
| `/orgs/:org/incidents` ([:1264-1269](../../server/internal/app/server.go:1264)) | ack, unack, snooze, unsnooze, **resolve**, comment |
| labels, regions, check-groups, severities, dependencies, on-call schedules, escalation policies, maintenance windows, SLOs | full CRUD |
| integrations (`group.POST("", CreateIntegration)`), channels (Slack/MS Teams/Discord per-channel routes), email suppressions | create and edit notification plumbing |
| status pages, incident publications, `status-updates` ([:1655](../../server/internal/app/server.go:1655)) | publish to the public status page |
| report schedules ([:1800-1804](../../server/internal/app/server.go:1800)) | create, edit, delete, **send a test report** |
| files ([:1407](../../server/internal/app/server.go:1407)) | delete attachments |

What is *not* exposed: the groups that already spell out an admin or owner
chain by hand — `orgOwnerGroup` ([:785](../../server/internal/app/server.go:785)),
`orgChecksAdmin` ([:1010](../../server/internal/app/server.go:1010)),
discovery, agents admin, members admin, jobs admin, the integration-identity
admin group and the Slack / MS Teams / Discord org-integration groups. Those
refuse a viewer because they refuse a `user` too.

**The MCP server has the same hole from a different angle.** Tool calls are
gated by *token scope* only: `mcp:read` tokens are refused mutation tools
([mcp/handler.go:403](../../server/internal/mcp/handler.go:403),
[scope.go:49](../../server/internal/mcp/scope.go:49)), but an `mcp`-scoped
PAT belonging to a viewer calls `toolFn(ctx, orgSlug, …)` directly into the
services, bypassing REST middleware entirely. A viewer with a full-scope PAT
can `create_check` through MCP even after the REST gate below lands, unless
MCP checks the role too.

Why this was never noticed: dash0 does not hide write affordances from viewers
(the `MemberRole` type at [hooks.ts:3703](../../web/dash0/src/api/hooks.ts:3703)
is consumed only by the members and requests pages), so a viewer sees every
"New" and "Delete" button — and they all work. There was no 403 to trip over.

Related: [2026-09-06-02-public-live-demo-account.md](2026-09-06-02-public-live-demo-account.md)
gives its demo user the `user` role and builds its own guard *because* this
gap exists. The two guards stay independent: the demo guard is an allowlist
keyed on a `users.demo` flag; this one is a role floor. Neither should be
refactored into the other.

## Proposal

### 1. A `RequireOrgWrite` middleware, applied structurally

Add `AuthMiddleware.RequireOrgWrite` next to `RequireOrgAdmin` in
[middleware/auth.go](../../server/internal/middleware/auth.go:523):

- Pass through `GET`, `HEAD` and `OPTIONS` untouched.
- Pass through service-authorized requests (`isServiceAuthorized`, as
  `RequireOrgAccess` does at [:222](../../server/internal/middleware/auth.go:222)).
- For any other method, require the caller's membership to satisfy
  `Role.AtLeast(models.MemberRoleUser)`; super admins pass. Reuse
  `requireOrgRole(models.MemberRoleUser, "Write access requires the user role")`
  wrapped in the method check, so the role is read from the **membership
  row**, not from `claims.Role`. A demotion to viewer must take effect on the
  next request, not at the next token refresh — and it also means a PAT minted
  while the user was a `user` stops writing the moment they are demoted.
- Deny with the standard shape: `403` / `FORBIDDEN`
  ([base.go:25](../../server/internal/handlers/base/base.go:25)). No new
  error code — dash0 already renders `FORBIDDEN` as Permission Denied and
  never redirects (`wiki/conventions/frontend-errors.md`).

Wire it into the `orgGroup` helper
([server.go:767](../../server/internal/app/server.go:767)) so the chain
becomes `orgSlugRedirect → RequireAuth → RequireOrgAccess → RequireOrgWrite`.
The helper's own comment explains why this is the right place: org
authorization is structural there, and "a new org route can't silently ship
without" it. The hand-built admin/owner chains need nothing — they already
sit above the floor — but the route-table test in §4 checks them anyway
rather than trusting the observation.

### 2. The allowlist: writes a viewer legitimately owns

Expressed at registration, not as a path matcher inside the middleware: a
second helper `orgGroupSelf(path)` builds the same chain **without**
`RequireOrgWrite`, and only these groups use it:

| Group | Why a viewer may write here |
|---|---|
| `/orgs/:org/users/me/*` ([:1366-1374](../../server/internal/app/server.go:1366)) | The member's *own* notification contacts, routes, verification and Telegram link. Nothing here touches anyone else. |
| `POST /orgs/:org/tokens` ([:797](../../server/internal/app/server.go:797)) | The member's *own* PAT. The token inherits the membership role, so it is bound by this very gate; a viewer can automate reading, nothing more. |

Confirm during implementation whether web-push subscription routes are
org-scoped (only `GET /webpush/vapid-public-key` appears under `orgWebPush` at
[:1582](../../server/internal/app/server.go:1582)); if they are, they are
self-scoped too and join this table.

**Denied on purpose, and why** — the incident actions at
[:1264-1269](../../server/internal/app/server.go:1264) are the ones most
likely to be argued for, so the decision is written down: acknowledging,
snoozing and resolving an incident **changes what the whole team is paged
about**, and a comment lands in the timeline everyone reads. Those are
operational writes. A team that wants someone to ack incidents gives them
`user`; `viewer` means *read-only*, full stop, so that the word keeps meaning
one thing. Should product later want a "responder" tier, that is a new role
between `viewer` and `user`, not an exception carved into this one.

### 3. The same floor inside MCP

In `handleToolsCall`
([mcp/handler.go:389](../../server/internal/mcp/handler.go:389)), after the
scope check at [:403](../../server/internal/mcp/handler.go:403): when
`isMutationTool(params.Name)`, resolve the caller's membership for
`claims.UserUID` in the org behind `orgSlug` (`GetMemberByUserAndOrg`, as
`requireOrgRole` does) and refuse with `CodeForbidden` unless
`Role.AtLeast(models.MemberRoleUser)` or super admin. Message:
*"Tool X requires the user role in this organization"*. `isMutationTool`'s
prefix list ([scope.go:17](../../server/internal/mcp/scope.go:17)) is
already the authoritative "what mutates" table for MCP; reuse it, do not
duplicate it.

### 4. Tests — the route-table proof is the deliverable

- **`TestEveryOrgScopedWriteRouteRefusesViewers`** in `server/internal/app/`,
  modelled on `TestEveryOrgScopedAPIRouteRedirectsOnAPreviousSlug`
  ([org_rename_redirect_test.go:364](../../server/internal/app/org_rename_redirect_test.go:364)):
  `router.Walk` every pattern under `/api/v1/orgs/{org}` whose method is not
  `GET`, build a concrete URL with `concreteURLForPattern`, call it as a
  **viewer** session with an empty JSON body, and assert `403` unless the
  pattern is in the allowlist set from §2. Assert the walk found a
  non-trivial number of routes so the test cannot pass vacuously.
- **Positive control in the same test**: repeat the walk as a **`user`**
  session and assert that no route answers the role denial (the response may
  be `400`/`404`/`422` for a bogus body — assert on the `FORBIDDEN` body
  message, not merely on the status, so an unrelated 403 cannot mask a
  regression either way). A gate that also locks out users is worse than the
  hole it closes.
- **Pinned-by-name companion**, like `TestOrgScopedRoutesOutsideOrgGroupRedirect`
  ([:403](../../server/internal/app/org_rename_redirect_test.go:403)): the
  allowlisted groups listed explicitly, so a future reader sees *why* those
  chains differ instead of an anonymous set in a test helper.
- **`TestRequireOrgWrite_AuthMatrix`** in `server/internal/middleware/`,
  modelled on `TestRequireOrgAdmin_AuthMatrix`
  ([orgadmin_test.go:29](../../server/internal/middleware/orgadmin_test.go:29)):
  viewer GET passes; viewer POST/PATCH/PUT/DELETE denied; user, admin, owner,
  super admin pass; service-authorized passes; missing org context denied;
  method check is case-exact and `HEAD`/`OPTIONS` pass.
- **PAT path**: a PAT created by a viewer, used against `POST /orgs/:org/checks`,
  is denied; the same PAT against `GET` succeeds.
- **Demotion takes effect immediately**: a `user` with a live session is
  demoted to `viewer`; their next write is denied without re-login.
- **MCP**: viewer PAT with the `mcp` scope — `list_checks` succeeds,
  `create_check` is refused with `CodeForbidden`; the same call as a `user`
  succeeds.
- Existing suites for the realtime WS handshake, the public status-page
  subscribe and the magic-link ack (`GET …/incidents/:uid/ack`, a signed link
  — untouched by this spec) must stay green; they are GET or public and
  never enter `RequireOrgWrite`.

### 5. Documentation

- The members page of the docs site and any wiki page that lists roles state
  the rule in one sentence each: *viewer — read everything, change nothing,
  except your own notification settings and your own API tokens.*
- `CHANGELOG.md` entry under the security heading, following
  `wiki/conventions/changelog.md`; this is a behaviour change for any viewer
  who was, knowingly or not, relying on the hole.
- The OpenAPI description of the role enum
  (`server/internal/app/openapi/openapi.yaml`) gains the same sentence.

### Follow-up, deliberately out of scope here

**dash0 does not hide write affordances from viewers.** `/auth/me` already
returns `organizations[].role`
([service.go:391](../../server/internal/handlers/auth/service.go:391)), so
the data is there, but every New / Edit / Delete control renders for a viewer
today and will start answering Permission Denied once this lands. That is the
correct interim behaviour under the 403 convention — no loop, an honest
message — but it is not good UX. A separate frontend spec should hide or
disable those controls for viewers (and the check-type picker, the incident
action buttons, the status-page publish controls) using the role from
`/auth/me`. Not folded in here: this spec is a server-side security fix and
should land on its own.

## Decisions

- **Structural middleware in `orgGroup`, not per-handler checks.** The
  admin gates show the pattern; the redirect middleware shows why "add it to
  every group by hand" fails.
- **Registration-time exemption (`orgGroupSelf`), not a path table inside
  the middleware.** The exception is visible in review next to the routes it
  covers, and the walk test enforces the boundary.
- **Membership row, not claims.** Immediate effect on demotion; PATs and
  sessions behave identically.
- **Incident actions are writes.** `viewer` stays a single, honest word.
- **Standard `FORBIDDEN`, no new code.** The frontend convention already
  handles it.
- **MCP gets the same floor in the same change.** Closing REST while leaving
  MCP open would be a half-fix that reads as a full one.
- **Independent from the demo guard.** Different key (role vs. `users.demo`),
  different shape (floor vs. allowlist), different failure modes.
