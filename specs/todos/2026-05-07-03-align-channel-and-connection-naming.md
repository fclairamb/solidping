# Align "channel" and "connection" naming across the stack

## Context

The same domain object — Slack workspace, Discord webhook, email recipient
list, generic webhook URL, etc. — has two different names depending on
which layer of the stack you read:

| Layer | Term used | Examples |
|---|---|---|
| Database | **connection** | `integration_connections` table |
| Backend models / services | **connection** | `IntegrationConnection`, `ConnectionResponse`, `ListConnections` |
| REST API path | **connection** | `GET /api/v1/orgs/$org/connections`, `useConnections(org)` |
| Notifier subsystem | **connection** | `notifier.dispatch(connection)` |
| Dashboard route | **channel** | `/orgs/$org/channels`, `/channels/new`, `/channels/$connectionUid` |
| Dashboard sidebar / breadcrumb | **channel** | `nav:channels`, "Channels" / "Canaux" / "Kanäle" / "Canales" |
| Dashboard component dir | **channel** | `web/dash0/src/components/channels/`, `channel-icon.tsx` |
| Dashboard i18n namespace | **channel** | `locales/{en,fr,de,es}/channels.json` |
| Dashboard internal types | **mixed** | `Connection` (from `api/hooks.ts`) but used inside `ChannelsListPage` |
| User-visible labels | **channel** | "New channel", "No channels yet", "Channel created" |

The split started innocently: the database table was named
`integration_connections` because not everything is a notification target
(some integrations could conceivably read from external systems, like the
Slack OAuth install flow that *also* registers a workspace, not just a
recipient channel). Then the dashboard team picked the user-friendly word
"channel" for navigation and labels, and nobody reconciled the two.

The result is a daily papercut:

- Operators hunt for `/api/v1/orgs/$org/channels` in the API; it's at
  `/connections`. Same for `useConnections` / `useDeleteConnection` etc.
- Logs say `connection_uid=xyz` but the dashboard URL says `connectionUid`
  — same word, different surface, and you have to know to translate
  "channels" → "connections" before grepping.
- The new `/channels/$connectionUid` route name is itself the
  contradiction in miniature: noun mismatch with parameter name.
- The recently re-skinned listing page (spec
  `2026-05-06-02-align-listing-pages-to-checks-style.md`) called the
  resource "channels" in the i18n keys but kept `Connection` types from
  the API — every contributor has to mentally reconcile the two on every
  edit.

## The decision

**Standardize on "channel" everywhere, including the API path.** Reasons:

1. **User-facing wins over implementation-facing.** The thing operators
   click on, talk to support about, and read in marketing copy is a
   "Slack channel" or "email channel". Renaming the *internals* to match
   the *user-facing word* is the direction that reduces confusion forever
   — renaming the user-facing word the other way would create a churn we'd
   regret next year.
2. **The "integration" vs "channel" semantic split was always thin.** In
   v1 every `integration_connection` row IS a notification destination.
   The ambition that some connections might be inbound integrations
   (read-from rather than write-to) hasn't materialized. If it does, those
   should live in their own table — not share the notification-target
   table.
3. **Naming alignment with checks-row terminology.** The check edit page
   already speaks of "channels" — "Notify via these channels". Any
   internal docs that use "connection" feel like a different product.

The cost is bigger than option (b) "rename UI to connection" because we
also need to migrate the database table. Most of that migration is a
mechanical rename; the schema stays identical. We accept the cost.

## Approach

A coordinated, three-phase rename:

### Phase 1 — Add `/channels` API alias next to `/connections` (additive, no break)

Add an aliased route group: every `/orgs/$org/connections*` endpoint
also responds at `/orgs/$org/channels*` with identical handler bindings.
Same handler functions, same request/response shapes, same auth, same
error codes. The notifier and DB layers don't change yet. The
dashboard's `useConnections` etc. start hitting `/channels`.

After this phase, an external CLI / MCP client that hardcoded
`/connections` keeps working; new clients pointed at `/channels`
also work. The API spec doc adds the `/channels` paths and marks
`/connections` as deprecated.

### Phase 2 — Rename code, keep DB table

Rename:

- Backend: `IntegrationConnection` → `Channel`; `ConnectionResponse` →
  `ChannelResponse`; package `handlers/connections/` → `handlers/channels/`;
  service methods (`ListConnections` → `ListChannels`, etc.); error codes
  (`ErrorCodeConnectionNotFound` → `ErrorCodeChannelNotFound`).
- Frontend: `Connection` type alias gets renamed to `Channel`; the dashboard
  no longer juggles two names internally.
- DB layer: keep the table name `integration_connections` for now — the
  ORM model moves to `Channel` but maps to the legacy table via the bun
  `bun:"table:integration_connections"` tag.

This is a big rename PR but a mechanical one. The DB migration is *zero* in
this phase — the schema is unchanged.

### Phase 3 — Rename DB table (separate spec, follows Phase 2)

Drop the integration-vs-connection ambiguity at the storage layer:

- Migration: `ALTER TABLE integration_connections RENAME TO channels`.
- `check_connections` (the binding table) becomes `check_channels`.
- `connection_uid` columns become `channel_uid` everywhere they're foreign
  keys.
- Drop the `bun:"table:integration_connections"` tag now that the model
  name and table name agree.

Ship Phase 3 in its own change so the DB migration is auditable on its own.
A failure in Phase 3 doesn't block the user-visible Phase 1+2 win.

## Files affected (Phase 1 + 2)

This is a non-exhaustive map; the implementer should expect to touch every
file under each path.

### Backend (Phase 2 rename, Phase 1 only adds new routes)

- `server/internal/app/server.go`: route registration — add `/channels`
  group in Phase 1; rename handler-package import path in Phase 2.
- `server/internal/handlers/connections/` → `server/internal/handlers/channels/`
  (Phase 2). Package name, file names, type names, function names. Tests
  follow.
- `server/internal/handlers/base/`: error codes
  `ErrorCodeConnectionNotFound` → `ErrorCodeChannelNotFound`. Add the new
  code in Phase 1 alongside the old one (both work); drop old in a
  follow-up spec.
- `server/internal/notifier/`: every `Connection`/`ConnectionUID` parameter
  renamed to `Channel`/`ChannelUID`.
- `server/internal/db/models/integration.go`: rename `IntegrationConnection`
  struct to `Channel`. Keep `bun:"table:integration_connections"` tag.
- `server/internal/db/`: methods like `db.GetConnection` → `db.GetChannel`,
  `db.ListConnectionsForCheck` → `db.ListChannelsForCheck`, etc.
- `docs/api-specification.md`: document the new `/channels` paths.
- `docs/database-model.md`: footnote that the `integration_connections`
  table will be renamed to `channels` in a follow-up; flag the legacy name
  so readers don't think they've found a separate table.

### Frontend (Phase 2)

- `web/dash0/src/api/hooks.ts`: rename `Connection` type to `Channel`,
  `useConnections` → `useChannels`, `useConnection` → `useChannel`,
  `useDeleteConnection` → `useDeleteChannel`, `useUpdateConnection` →
  `useUpdateChannel`. Update fetch URLs to `/channels`. Keep
  type-import path stable.
- `web/dash0/src/routes/orgs/$org/channels.*.tsx`: replace `Connection` /
  `connectionUid` usage with `Channel` / `channelUid`. Route param names
  change: `/channels/$connectionUid` → `/channels/$channelUid`.
- `web/dash0/src/components/channels/`: any internal types using
  `Connection`.
- `web/dash0/src/routes/orgs/$org.tsx`: breadcrumb branch — the
  `connection` param name in `params.connectionUid` becomes
  `params.channelUid`.
- `web/dash0/e2e/channels.spec.ts` and the new `listing-pages-style.spec.ts`:
  update any `connectionUid` references.

### CLI / MCP

- `server/internal/cli`: any `connection` subcommand becomes `channel`.
- `server/internal/mcp`: any tool name like `list_connections` becomes
  `list_channels`. Tool descriptions and prompt text update accordingly.

## Sequencing for least disruption

The branch order is important. PRs land like this:

1. **PR-1: API path alias** — add `/channels/*` next to `/connections/*`,
   no removals. Backward-compatible. Dashboard and CLI still call
   `/connections`. Docs add the new paths. Easy revert.
2. **PR-2: Frontend switch** — point `useConnections` / dashboard hooks
   at `/channels`. Still backward-compatible because both paths work.
   Type names stay `Connection` for now to keep the diff small.
3. **PR-3: Backend rename** — files, packages, types, error codes. The
   wire format doesn't change because both `/channels` and `/connections`
   still work; only internal names move. Frontend already aligned.
4. **PR-4: Frontend type rename** — `Connection` → `Channel` in
   `api/hooks.ts` and downstream. Pure rename inside the dashboard.
5. **PR-5: Drop the deprecated `/connections` paths** — once internal
   callers and at least one external release cycle have switched. Note in
   the API spec that `/connections` is gone.
6. **PR-6: DB table rename** — `integration_connections` → `channels`,
   `check_connections` → `check_channels`, FK column renames, ORM tag
   removal. Separate spec.

## Out of scope

- The DB table rename and column renames (Phase 3 / PR-6) — separate spec.
- Renaming "integration" the noun in the user-visible copy when it's
  *correct*. The Slack OAuth install creates a workspace integration and
  multiple channels — those two concepts genuinely differ. We aren't
  collapsing them; we're renaming "connection" (which always meant
  "notification target") to "channel".
- Migrating existing audit-event payloads' `connection_uid` keys to
  `channel_uid`. The events table is append-only; old rows keep their
  legacy keys, the dashboard already handles both via the breadcrumb
  alias work in spec 2026-05-05-14.

## Verification

1. After each PR, `make build`, `make test`, `make lint` pass.
2. After PR-1: a curl GET against `/api/v1/orgs/test/channels` returns
   the same payload as `/api/v1/orgs/test/connections` (modulo the
   pagination param spec landing first).
3. After PR-2: the dashboard's network tab shows `/channels` calls;
   nothing breaks visually.
4. After PR-3: backend tests pass; the rename is mechanical and
   primarily caught by `go build`.
5. After PR-4: dashboard typecheck (`bun run build`) passes.
6. After PR-5: external API spec lists only `/channels`; old path
   returns 404.
7. After PR-6: the DB has a `channels` table; existing queries via the
   ORM continue to work because the model's `bun:"table:..."` tag is
   removed only after the rename has applied.

## Implementation Plan (this spec covers PR-1 through PR-4; PR-5 and PR-6
are explicitly deferred to follow-up specs)

1. **PR-1** ✅ landed 2026-05-07: `/orgs/:org/channels{,/:uid}` route group
   added next to `/connections`, identical handler bindings. Verified by
   `TestChannelsAliasMatchesConnections` in
   `server/test/integration/channels_alias_test.go`. The check-binding
   path `/checks/:check/connections` is *not* yet aliased (the spec
   covers org-level connections only); per-check aliasing happens with
   the wider rename in PR-3.
2. **PR-2** ✅ landed 2026-05-07: top-level dashboard hooks
   (`useConnections`, `useConnection`, `useCreateConnection`,
   `useUpdateConnection`, `useDeleteConnection`) now call
   `/orgs/$org/channels` on the wire. The `Connection` TS type and the
   `connections` query-cache key are unchanged — those move in PR-4.
3. **PR-3** (pending): backend rename — files, packages, types, methods,
   error codes. Big diff but mechanical. Blocked on the in-flight WIP
   for spec 04 which touches `channels.index.tsx` and related files;
   ship after that lands.
4. **PR-4** (pending): frontend type rename — `Connection` → `Channel`
   in `api/hooks.ts` and route param names (`connectionUid` →
   `channelUid` in `channels.$channelUid.tsx`).
