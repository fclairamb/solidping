---
model: sonnet
effort: high
---

# The members list says when someone joined but never when they were last here

## Problem

An org admin looking at **Organization → Members** (`/orgs/$org/organization/members`)
cannot tell whether a member still uses SolidPing. The table shows role, paging
coverage and a **Joined** date
(`web/dash0/src/routes/orgs/$org/organization.members.tsx:237-239` /
`:332-334`), and that is the only time signal on the page. "Joined 8 months ago"
says nothing about whether alice last opened the dashboard yesterday, whether
her CI job still calls the API with her token every night, or whether nothing
of hers has touched SolidPing since the day she was invited — which is exactly
what an admin wants to know before pruning seats, chasing an unverified paging
contact, deciding who to page about an on-call gap, or working out which
departed colleague's automation is still running under a personal token.

The data already exists, we just never surface it per member. Every credential
a member authenticates with is a `user_tokens` row
(`server/internal/db/models/auth.go:339-352`), and each type leaves its own
trace:

- **Dashboard sessions** (`type = refresh`): created at login with
  `last_active_at = now` (`server/internal/handlers/auth/service.go:785`) and
  bumped — at most once an hour — by the access-token refresh path
  (`slideSessionExpiry`, `service.go:1297-1315`, called from `:1371`). With the
  default one-hour access token (`server/internal/config/config.go:1629`) an
  active dashboard user's session row moves roughly hourly. This is the same
  value the member's own **Account → Sessions** page shows as `lastActiveAt`
  (`service.go:1725-1726`; that page lists `?type=refresh` only,
  `web/dash0/src/api/hooks.ts:2441`).
- **Personal access tokens** (`type = pat`): `last_active_at` is bumped on use,
  again throttled to once an hour (`service.go:1484-1489`). A PAT that was
  created but never used keeps `last_active_at = NULL`.
- **OAuth grants** for MCP / CLI clients (`type = oauth_refresh`): never bumped
  in place, but every rotation soft-deletes the old row and mints a fresh one
  (`server/internal/oauth/service.go:298` then `mintTokens`, `:433-450`), so the
  newest row's `created_at` — deleted rows included — is the last time that
  client refreshed its access token. Same hourly-ish granularity as a session.
- **`users.last_active_at`** records the last *login* only
  (`service.go:719`, `:2270`, `:4277`, `join_policy.go:373`) — nothing else
  writes it, despite its column comment promising "last API or UI activity"
  (`server/internal/db/postgres/migrations/001_v0_1_0.up.sql:68`).

Logout and revocation **soft-delete** the row (`DeleteUserToken` sets
`deleted_at`, `server/internal/db/postgres/postgres.go:1151-1157`) and nothing
purges `user_tokens`, so the timestamps survive.

None of this reaches the members endpoint: `MemberResponse`
(`server/internal/handlers/members/service.go:68-77`) exposes `joinedAt` /
`createdAt` only, and `ListMembersByOrg` (`postgres.go:968-980`,
`sqlite.go:894-906`) preloads the `User` relation but the service never reads
it — it re-fetches each user with `GetUser` (`service.go:145-148`).

## Proposal

Expose **two** timestamps per member — one for the person, one for their
credentials — and show them as **one** "Last seen" column whose value is the
most recent of the two, tagged with *how* they were last seen.

### Definitions

**`lastSessionActivityAt`** — when the member was last in the dashboard. The
most recent of:

1. `MAX(user_tokens.last_active_at)` over the member's `type = 'refresh'` rows,
   **including soft-deleted rows** — a session the member logged out of, or an
   admin revoked, is still evidence of when they were last here, and
   `last_active_at` is never rewritten by the delete.
2. `users.last_active_at` (the last login). A login already stamps the new
   refresh row, so this is belt-and-braces, but it costs nothing and keeps the
   answer right if the token semantics change.

**`lastTokenActivityAt`** — when one of the member's credentials was last used
without them being in the dashboard. The most recent of:

1. `MAX(user_tokens.last_active_at)` over `type = 'pat'` rows, soft-deleted
   included. `NULL` rows (created, never used) do not count — a minted token is
   not an access.
2. `MAX(user_tokens.created_at)` over `type = 'oauth_refresh'` rows,
   soft-deleted included — the rotation chain described above. The very first
   grant is minted by a person completing an OAuth consent, so counting its
   creation is correct too.

Both are `null` when nothing exists — a member added by an admin through
`POST /orgs/:org/members` who has never signed in and owns no token.

Keeping the two apart is the point: a departed employee whose nightly cron job
still runs looks *active* if the two are merged, and looks exactly like what
they are — "no dashboard since March, token used last night" — if they are not.
The frontend derives the headline value (the max) and the channel; the API
never pre-merges them.

### Backend

- New `db.Service` method (interface in `server/internal/db/service.go`,
  implemented in both `postgres/` and `sqlite/`):

  ```go
  type UserTokenActivity struct {
      SessionAt *time.Time // max last_active_at over refresh rows
      TokenAt   *time.Time // max(last_active_at over pat rows, created_at over oauth_refresh rows)
  }
  TokenActivityByUsers(ctx context.Context, userUIDs []string) (map[string]UserTokenActivity, error)
  ```

  One query for the whole org: select `(user_uid, type, last_active_at,
  created_at)` for the given users and the three types, **no `deleted_at`
  filter**, and fold into the struct **in Go**. SQLite stores timestamps as
  `text` (`server/internal/db/sqlite/migrations/001_v0_1_0.up.sql:93`);
  folding in Go sidesteps any lexical-vs-temporal `MAX` question there and keeps
  the two backends byte-identical. Return `nil, nil` for an empty input without
  touching the DB.
- `ListMembers` (`server/internal/handlers/members/service.go:126-160`): call it
  once with every member's `UserUID`, then set
  `LastSessionActivityAt = maxOf(activity.SessionAt, user.LastActiveAt)` and
  `LastTokenActivityAt = activity.TokenAt`. While there, read the user from the
  already-preloaded `member.User` instead of `GetUser` per member (keep the
  "skip members with missing users" behaviour: a nil `User` is skipped). Do not
  add a per-member query inside that loop — it is being fixed here, not
  extended.
- `MemberResponse`: add
  `LastSessionActivityAt *time.Time \`json:"lastSessionActivityAt,omitempty"\``
  and `LastTokenActivityAt *time.Time \`json:"lastTokenActivityAt,omitempty"\``.
  Populate both in `GetMember` too, so the single-member endpoint agrees with
  the list.
- OpenAPI: add both fields (`date-time`, nullable) to the `Member` schema at
  `server/internal/app/openapi/openapi.yaml:11064-11093`, each with a one-line
  description carrying the definition above (what counts, soft-deleted rows
  included, hourly granularity, unused PATs excluded). Regenerate the client if
  `pkg/client` is generated from it.
- Wiki: document both fields under `GET /api/v1/orgs/:org/members` in
  `wiki/api-specification/orgs.md:237`.

### Frontend

- `MemberResponse` in `web/dash0/src/api/hooks.ts:3736-3745`: add
  `lastSessionActivityAt?: string` and `lastTokenActivityAt?: string`.
- Derive per row: `lastSeenAt = max(lastSessionActivityAt, lastTokenActivityAt)`
  and `lastSeenVia: "session" | "token"` (ties go to `session`). Put the helper
  in `web/dash0/src/lib/` with a unit test, not inline in the route.
- Members table (`organization.members.tsx`): one **Last seen** column right
  after **Joined**, same `hidden lg:table-cell text-muted-foreground`
  treatment. The cell renders:
  - the relative time via `TimeAgoOrDash` from `@/components/ui/time-ago`
    (`web/dash0/src/components/ui/time-ago.tsx:180-187`) — the design
    reference's "TimeAgo (hover/tap + click-to-copy)" pattern, so hover still
    gives the absolute time — with `emptyLabel={t("members.lastSeen.never")}`
    ("Never") rather than the default dash: "never signed in" is a real state an
    admin acts on, not missing data;
  - next to it, a small channel icon (lucide `Monitor` for `session`,
    `KeyRound` for `token`) carrying the design reference's `Tooltip`, whose
    body lists **both** channels with their own relative times, e.g.
    "Dashboard · 3 weeks ago" / "API token · 2 hours ago", or "Dashboard · never"
    when one side is empty. No icon when both are null.
- One column, not two: the table already carries six columns plus actions and
  hides two of them below `lg`. The headline answers "still around?"; the icon
  answers "person or automation?"; the tooltip has the full breakdown. Both
  raw values are in the API, so a two-column layout later needs no backend
  change.
- Below `lg` the **Joined** and **Last seen** columns are both hidden, and below
  `md` so is the email. Keep the mobile view useful: render the same
  value + icon as a muted secondary line under the name inside the member cell
  (`organization.members.tsx:249-266`) with `lg:hidden`, so a phone still shows
  "Last seen 3 days ago" without a horizontal scroll.
- `data-testid={\`member-last-seen-${member.email}\`}` on the cell and
  `data-testid={\`member-last-seen-via-${member.email}\`}` with
  `data-via="session" | "token"` on the icon, matching the existing
  `member-role-…` / `member-paging-…` convention (`:278`, `:345`).
- Locale keys in `web/dash0/src/locales/{en,fr,de,es}/org.json` —
  `locale-parity.test.ts` fails the build otherwise: `members.column.lastSeen`,
  `members.lastSeen.never`, `members.lastSeen.viaSession` ("Dashboard"),
  `members.lastSeen.viaToken` ("API token"), plus whatever the tooltip lines
  need.
- Design reference (`web/dash0/src/routes/orgs/$org/design-reference.tsx`,
  TimeAgo section around `:2074`): add the "relative time + channel icon with
  breakdown tooltip" combination as a snippet, since it is a reusable pattern
  and not yet catalogued.

### Tests

Backend (`server/internal/db/service_test.go` runs both backends; put the new
method's test there, next to the `ListMembersByOrg` coverage at `:772`):

- Two users, one with two `refresh` rows: `SessionAt` is the later
  `last_active_at`.
- A `refresh` row with `deleted_at` set and the latest `last_active_at`: **it
  wins** (logout does not erase presence).
- A `pat` row newer than every session: it lands in `TokenAt` and **not** in
  `SessionAt` — assert both fields. A `pat` row with `last_active_at = NULL`
  contributes nothing.
- Two `oauth_refresh` rows, the older soft-deleted: `TokenAt` is the newer
  `created_at`. An `oauth_refresh` row's `last_active_at` (always `NULL` today)
  is not consulted.
- A user with only `oauth_refresh` rows: `SessionAt` nil, `TokenAt` set.
- A user with no rows: absent from the map. Empty input: `nil, nil` and no
  query.

Members service (`server/internal/handlers/members/` — the existing
`TestListMembersResponseFields` at `service_test.go:137` only checks struct
plumbing; add real cases through `ListMembers` using the package's in-memory
SQLite fixture `setupMembersTest`, `owner_test.go:16-27`):

- Session newer than login → `lastSessionActivityAt` is the session time.
- Login but no session activity → `lastSessionActivityAt` is the login time.
- PAT used after the session → `lastTokenActivityAt` is the PAT time **and**
  `lastSessionActivityAt` is still the session time.
- Never signed in, no tokens → both fields omitted from the JSON.

Frontend:

- Unit (`bun run test:unit`): the `lastSeenAt` / `lastSeenVia` helper — session
  only, token only, both with each side winning, tie → `session`, both null;
  and the locale-parity test for the new keys in all four locales.
- Playwright (`web/dash0/e2e/`, alongside `member-paging-coverage.spec.ts`):
  1. after the test user logs in, their row shows a `member-last-seen-…` cell
     with relative-time text (not "Never") and `data-via="session"`;
  2. create a PAT for that user through the API, call any authenticated
     endpoint with it, reload: the breakdown tooltip lists both "Dashboard" and
     "API token" with non-"never" values. Assert the tooltip contents, not
     which icon won — the two events are seconds apart and the test must not
     depend on sub-second ordering;
  3. a member added via the API who never logged in shows "Never" and no icon.

### Out of scope

- Sorting or filtering the table by last seen; the column is display-only for
  now. The existing sort (`organization.members.tsx:122-131`) stays role → name.
- Splitting PATs from OAuth grants in the UI. Both are "a credential, not the
  person"; the API keeps them together in `lastTokenActivityAt` because the
  tooltip has no room for three lines and the admin's question is binary. If a
  per-token breakdown is ever needed, that is the member's own tokens list, not
  the org members table.
- Bumping `oauth_refresh.last_active_at` on rotation, or making
  `users.last_active_at` true to its column comment by writing it from the
  refresh / PAT paths. Either would let the list read denormalised columns with
  no aggregate and is a reasonable follow-up, but both change writes on the
  auth hot path and need a backfill; the read-side aggregate above answers the
  request without touching authentication.

## Implementation Plan

1. **Backend — `db` layer**: add `UserTokenActivity` struct and
   `TokenActivityByUsers(ctx, userUIDs)` to the `db.Service` interface
   (`server/internal/db/service.go`). Implement identically in
   `postgres/postgres.go` and `sqlite/sqlite.go`: one `SELECT user_uid, type,
   last_active_at, created_at FROM user_tokens WHERE user_uid IN (...) AND type
   IN (refresh, pat, oauth_refresh)` (no `deleted_at` filter), then fold into
   the map with a single shared Go function (`db.FoldTokenActivity`, new file
   `server/internal/db/tokenactivity.go`) so both backends run byte-identical
   aggregation logic instead of risking SQLite's text-typed `MAX` doing a
   lexical comparison. Empty input returns `nil, nil` without querying.
2. **Backend — members service**: add `LastSessionActivityAt` /
   `LastTokenActivityAt` to `MemberResponse`. In `ListMembers`, call
   `TokenActivityByUsers` once for every member's `UserUID`, read the user from
   the already-preloaded `member.User` (removing the per-member `GetUser` N+1),
   and set `LastSessionActivityAt = max(activity.SessionAt, user.LastActiveAt)`
   via a small `maxTimePtr` helper. Populate the same two fields in `GetMember`.
   Keep the "skip members with nil User" behaviour.
3. **Backend — OpenAPI + wiki**: add both fields to the `Member` schema in
   `server/internal/app/openapi/openapi.yaml`, regenerate the client
   (`go generate ./pkg/client/...`), and document them in
   `wiki/api-specification/orgs.md`.
4. **Backend tests**: `server/internal/db/service_test.go` — new
   `TokenActivityByUsers` subtest inside `testUsersWithOrg` covering all six
   cases from the spec (two refresh rows, soft-deleted-wins, PAT vs session
   isolation + NULL PAT contributes nothing, oauth_refresh rotation with
   soft-delete, oauth-only user, no-rows/empty-input). `members/service_test.go`
   — real `ListMembers`/`GetMember` cases through the in-memory SQLite fixture
   covering session-newer-than-login, login-only, PAT-after-session, and
   never-signed-in-no-tokens.
5. **Frontend — helper**: `web/dash0/src/lib/last-seen.ts` (name TBD at
   implementation time) exporting `lastSeenAt`/`lastSeenVia` derivation from
   the two raw fields (tie → session), with a colocated unit test.
6. **Frontend — API types**: add the two optional ISO-string fields to the
   `MemberResponse` type in `web/dash0/src/api/hooks.ts`.
7. **Frontend — members table**: add a "Last seen" column after "Joined" in
   `organization.members.tsx` (desktop, `hidden lg:table-cell`) using
   `TimeAgoOrDash` with `emptyLabel="Never"`, a channel icon (`Monitor` /
   `KeyRound`) with a `Tooltip` breakdown of both channels, and the matching
   `data-testid`s. Add the same value as an `lg:hidden` secondary line under
   the member's name for mobile.
8. **Frontend — locales**: add the new keys to
   `web/dash0/src/locales/{en,fr,de,es}/org.json`.
9. **Frontend — design reference**: add the "relative time + channel icon with
   breakdown tooltip" snippet to `design-reference.tsx`'s TimeAgo section.
10. **Frontend tests**: unit tests for the helper + locale parity; three
    Playwright cases in `web/dash0/e2e/` per the spec (session-only row,
    PAT-plus-session tooltip breakdown, never-logged-in negative control).
11. **QA gate**: `make build-backend lint-back test`, `make build-dash0`,
    `bun run lint && bun run test:unit && bun run typecheck:e2e && bun run
    lint:e2e`, then the three Playwright cases plus
    `member-paging-coverage.spec.ts` against a locally started test server.
