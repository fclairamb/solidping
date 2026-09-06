# Organizations

Organizations, their settings, org-scoped tokens, invitations, members, and the
self-service membership-request flow.

## Member roles

Four roles, ordered **owner > admin > user > viewer**. Gates are hierarchical:
an owner passes every admin gate, an admin passes every user gate, and so on.
Never compare a role for equality when you mean "at least" — use
`models.MemberRole.AtLeast` on the backend, or `claims.HasOrgRole` when the role
comes from a JWT claim.

| Role | What it adds |
|---|---|
| `owner` | Everything an admin can do, **plus** deleting the organization and granting/revoking ownership |
| `admin` | Full read/write on the org's resources, member management (except owners) |
| `user` | Read/write on monitoring resources |
| `viewer` | Read everything, change nothing — except their own notification settings and their own API tokens |

**The read-only floor is enforced, not merely documented.** `RequireOrgWrite`
(`internal/middleware/auth.go`) is applied structurally by the `orgGroup` helper
in `internal/app/server.go`: every non-GET route registered through it requires
at least `user`, and a viewer is refused with `403` / `FORBIDDEN`. The role is
read from the **membership row**, not from the JWT claim, so a demotion takes
effect on the next request and a PAT minted while its owner was a `user` stops
writing the moment they are demoted. The same floor is repeated inside the MCP
server (`internal/mcp`), whose tool calls never pass through this middleware.

Two groups deliberately keep the un-floored chain (`orgGroupSelf`), because what
they write is only ever the caller's own row: `/orgs/:org/users/me/*` (their own
notification contacts, routes, verification and Telegram link) and
`POST /orgs/:org/tokens` (their own PAT — which inherits their role, so it is
bound by this very gate). Everything else is a write, incident acknowledgement
and resolution included: those change what the whole team is paged about. A team
that wants someone to ack incidents gives them `user`.

`TestEveryOrgScopedWriteRouteRefusesViewers` (`internal/app`) walks the real
route table and fails on any org-scoped write route that does not refuse a
viewer, so a route added later is covered on the day it is registered.

Ownership rules:

- The creator of an organization is its owner — via `POST /api/v1/orgs`, and via
  every connector (Slack, Discord, Google, GitHub, GitLab, Microsoft, OIDC,
  SAML, LDAP), whose org-creation paths funnel through the shared login
  admission policy's zero-member bootstrap.
- Only an owner may grant `owner`, or change/remove a member who is an owner.
  An admin attempting either gets `403 FORBIDDEN`.
- An organization can never be left ownerless: demoting or removing its last
  owner is a `409 CONFLICT`. Multiple owners are allowed, which is also the
  ownership-transfer path — promote a second owner, then demote yourself.
- `owner` is **not** an invitable role, and not a role a membership request can
  be approved with: those grants are consumed later, when the granter's role is
  no longer verifiable.

## Organizations

### POST /api/v1/orgs
Create a new organization. Auth: required (any authenticated user).
The caller becomes the org's **owner**, and the response carries a session
scoped to the new org. The org is always created for the caller — the owner
comes from the access token, never from the body. A slug freed by a deleted
organization can be claimed again.

### PATCH /api/v1/orgs/:org
Update the organization's profile — `{name?, slug?, logoUrl?}`. Auth: required
(**owner**) — an admin gets `403 FORBIDDEN`. Standard PATCH semantics: an
omitted field is untouched, and `logoUrl` accepts an explicit `null` (or `""`)
to clear the logo. `logoUrl` must be an absolute `http(s)` URL; an uploaded
logo is set through `POST /api/v1/orgs/:org/logo` instead.

Renaming the slug moves every URL of the organization, including the public
ones. **Old links keep working** — see [Previous slugs](#previous-slugs-after-a-rename).
Because access tokens are scoped to an org slug, the response of a rename also
carries a fresh session (`accessToken`, `refreshToken`, `expiresIn`,
`tokenType`) plus `previousSlug`; the caller must adopt the tokens or its next
request 403s. Other live sessions self-heal on their next refresh, which
derives the claim from the org row by UID.

Errors: `422 VALIDATION_ERROR` (bad slug, blank/oversized name, non-http logo
URL), `409 CONFLICT` (slug already held by a live org).

### POST /api/v1/orgs/:org/logo
Upload the organization's logo (`multipart/form-data`, field `logo`). Auth:
required (**owner**). Allowed types: `image/png`, `image/jpeg`, `image/webp`,
`image/gif`, `image/svg+xml`; max 1 MB. Wrong type → `422 VALIDATION_ERROR`,
too big → `413 LOGO_TOO_LARGE`.

The type is the one the client **declares** in the multipart part header — the
bytes are never sniffed or validated against it. The declared value is clamped
to the allowlist and is what gets stored and echoed back as `Content-Type`, so
an SVG uploaded as `image/png` is stored and served as `image/png`. That is
safe rather than sloppy: the serving headers below never trust the type to be
harmless (always `nosniff`, and only raster types are served `inline`), so no
declared value can turn an upload into an executable document. Returns the updated profile with `logoUrl` set to
`/pub/assets/<fileUid>`.

### DELETE /api/v1/orgs/:org/logo
Clear the organization's logo. Auth: required (**owner**). Retires the uploaded
file, so its public URL stops resolving. Returns the updated profile.

### GET /pub/assets/:fileUid
Serve an uploaded logo. **Public, unsigned** — unlike `/pub/files/:uid`, which
needs an expiring signed URL, a logo URL must be stable enough to paste into a
status page or an email.

Authorization is by the stored file's **attachment topic**, not by signature and
not by a per-feature state query (spec 2026-08-22-03): the file is served only
while its topic is on the closed public allowlist (`organizations/<uid>/logo`,
`status-pages/<uid>/logo`, `status-pages/<uid>/favicon`) **and** its row is
live. Replacing the logo or clearing it soft-deletes the file and un-publishes
it immediately; deleting the organization reaps the whole
`organizations/<uid>/` prefix, which is what keeps a deleted org's logo from
staying readable.

The bespoke `/pub/org-logos/:fileUid` route this replaced is **retired**, not
aliased.

Uploaded bytes are served with `X-Content-Type-Options: nosniff`, and only
raster images get `Content-Disposition: inline` — an SVG is always
`attachment`, because an uploaded SVG is XML that may carry `<script>` and
serving it inline would be stored XSS on the app's own origin. It still renders
normally in an `<img>`, where scripts inside an SVG never execute.

## Previous slugs (after a rename)

Renaming an organization does not break the URLs its customers have already
pasted elsewhere. The previous slug is recorded and every org-scoped surface
answers a permanent redirect to the current slug: **301** for `GET`/`HEAD`,
**308** for other methods (a 301 would license a client to replay a `POST` as a
`GET` and drop its body).

Covered surfaces: **every** `/api/v1/orgs/:org/...` route — including the ones
whose groups carry their own middleware chain (jobs, jobs admin, config-as-code
export/import/apply, discovery, private locations and agent enrollment tokens,
entitlements, and the Slack/MS Teams integration routes) — plus the public
status-page endpoints (`/api/v1/status-pages/:org/...` — view, summary, badge,
feed, which is also what the `/embed/v1` widget polls), per-check SVG badges,
heartbeat ingest, the magic-link incident ack, status-page subscribe, and the
`/dash0/orgs/:org/...` and `/status0/:org/...` app URLs.

The single exception is the realtime WebSocket (`/api/v1/orgs/:org/events/ws`):
an HTTP redirect has no meaning in a WS handshake, so a client on a previous
slug must reconnect against the current one. Coverage is enforced by a test that
walks the real route table (`TestEveryOrgScopedAPIRouteRedirectsOnAPreviousSlug`),
so a new org-scoped group cannot quietly opt out.

A note for the entitlements endpoints specifically: the billing service signs
the request **path**, so a redirect from a previous slug means it must re-sign
for the canonical path rather than replay the original signature. The redirect
tells it where to sign for; it is not a transparent proxy.

Two rules bound the guarantee:

- **A live organization always wins.** Resolution tries the live slug first, so
  a previous slug can never shadow a real organization.
- **A previous slug is released the moment another organization claims it.**
  `POST /api/v1/orgs` may claim a slug held only as an alias, and doing so drops
  the alias — it never resolves across tenants afterwards.

Deleted organizations are explicitly out of scope: a deleted org 404s on
**both** its current and its previous slugs, with no alias, tombstone or
redirect.

### DELETE /api/v1/orgs/:org
Delete an organization. Auth: required (**owner**) — an admin gets
`403 FORBIDDEN`. Body: `{"slug":"<org-slug>"}`, retyped as confirmation;
a mismatch is a `422 VALIDATION_ERROR`. Returns `200` with a login-shaped
session payload (see below).

Deletion stops every check immediately (the org's scheduler rows are removed),
soft-deletes its checks and memberships, and revokes every org-scoped token.
From that instant the slug **404s everywhere**: dashboard API, public status
pages, badges and the embed widget. The slug is released for reuse and no alias,
tombstone or redirect is left behind. There is no restore endpoint; recovery is
a manual database intervention.

**The deleter stays signed in.** Every *other* member's session dies with the
org (deliberate — spec `2026-08-08-11`), but the caller's own token named the
org that just vanished, so the response hands back a replacement session rather
than a bare `204`:

| Caller's remaining memberships | Response |
|---|---|
| ≥ 1 | `accessToken` + `refreshToken` scoped to the first surviving org, `organization` set, `loginAction: "orgRedirect"` |
| 0 | org-less `accessToken`, no `refreshToken` (refresh grants are org-scoped), `organization` absent, `loginAction: "noOrg"` |

`organizations` lists what is left, so a client can repopulate its org switcher
without a second round-trip — it is omitted entirely when nothing remains, so
read it as "the empty list" when absent. The access-token cookie is refreshed with
the new token, mirroring the slug-rename path on `PATCH /api/v1/orgs/:org`.

Relatedly, `GET /api/v1/auth/me` no longer `401`s when the token's org slug does
not resolve: it degrades to the org-less response (`organization: null`). A
stale tab reloading after the org was deleted therefore lands on the empty-state
page instead of being logged out.

### GET /api/v1/orgs/:org/settings
Get organization settings. Auth: required

### PATCH /api/v1/orgs/:org/settings
Update organization settings. Auth: required (admin)

## Organization Tokens

### GET /api/v1/orgs/:org/tokens
List the current user's personal access tokens for this organization. Auth: required

### POST /api/v1/orgs/:org/tokens
Create a personal access token scoped to this organization. Auth: required

## Organization Invitations

### GET /api/v1/orgs/:org/invitations
List pending invitations. Auth: required (admin)

### POST /api/v1/orgs/:org/invitations
Create a new invitation (sends email). Auth: required (admin).
Role must be `admin`, `user` or `viewer` — `owner` is not invitable.

### DELETE /api/v1/orgs/:org/invitations/:uid
Revoke a pending invitation. Auth: required (admin)

## Members

### GET /api/v1/orgs/:org/members
List organization members. Auth: required

### POST /api/v1/orgs/:org/members
Add a member to the organization. Auth: required (admin; **owner** to add
another owner)

### GET /api/v1/orgs/:org/members/:uid
Get a member's details. Auth: required

### PATCH /api/v1/orgs/:org/members/:uid
Update a member's role. Auth: required (admin; **owner** to grant `owner` or to
change an owner's role). Demoting the last owner is a `409 CONFLICT`.

### DELETE /api/v1/orgs/:org/members/:uid
Remove a member from the organization. Auth: required (admin; **owner** to
remove an owner). Removing the last owner is a `409 CONFLICT`.

### GET /api/v1/orgs/:org/members/coverage
Per-member paging coverage. Auth: required (**admin**).

Exposes channel **types** plus `verified` / `enabled` flags — never a contact
value. Per-user contacts are otherwise `users/me`-scoped and belong to the
member; an admin needs "Bob has an unverified phone", not Bob's number.

```json
{ "data": [
  { "userUid": "…", "email": "bob@acme.test", "name": "Bob", "role": "user",
    "channels": [ { "type": "phone", "verified": false, "enabled": true } ],
    "emailFallbackOnly": true }
] }
```

`emailFallbackOnly` is true when no **enabled and verified** channel other than
email exists. Escalation still reaches such a member — through the silent email
fallback — but an email is not a page, which is what the on-call and escalation
editors warn about.

### POST /api/v1/orgs/:org/members/:uid/contacts
Pre-provision a paging contact for another member, in **unverified** state.
Auth: required (**admin**). Body `{ "type": "phone" | "whatsapp", "value":
"+15551234567", "label": "" }`.

**Invariant: an admin can never create or flip a contact to verified.** Only
types that require a verification code round-trip may be provisioned; an
already-present contact is refused with `409` rather than updated; and the
written row is never verified. The member becomes pageable on it once they
complete the normal verification flow themselves.

### POST /api/v1/orgs/:org/members/:uid/paging-nudge
Email the member a "set up your alert notifications" nudge linking to their own
notification settings. Carries no contact data and no verification code. Auth:
required (**admin**). `204` on success.

## Membership Requests

A confirmed user with no membership in an org can ask to join by slug.
Org admins can approve or reject. Each (org, user) pair has at most one
row; subsequent requests update it in place per the state machine
(pending → approved | rejected | cancelled, with re-request after
cooldown for rejected and immediate re-request for cancelled).

### POST /api/v1/auth/membership-requests
Open or re-open a request. Auth: required.
Body: `{"orgSlug":"<slug>","message":"<optional>"}`.
Errors: `ORGANIZATION_NOT_FOUND`, `ALREADY_A_MEMBER`,
`REQUEST_PENDING`, `REQUEST_COOLDOWN_ACTIVE`.

### GET /api/v1/auth/membership-requests
List the caller's own request history. Auth: required.

### DELETE /api/v1/auth/membership-requests/:uid
Cancel the caller's own request. Auth: required (owner). Admins must
use the reject endpoint instead.

### GET /api/v1/orgs/:org/membership-requests
List incoming requests for the org. Auth: required (admin).
Query parameters: `status` (optional, e.g. `pending`).

### POST /api/v1/orgs/:org/membership-requests/:uid/approve
Approve a request and create the membership in one transaction.
Auth: required (admin). Body: `{"role":"user|admin|viewer"}` (default
`user`; `owner` cannot be granted this way). Sends a decision email to the requester.

### POST /api/v1/orgs/:org/membership-requests/:uid/reject
Reject a request. Auth: required (admin). Body:
`{"reason":"<optional>"}`. Sends a decision email to the requester. The
requester can re-submit only after the cooldown
(`membership_requests.cooldown_days`, default 7).
