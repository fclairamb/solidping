# Execute Channels → Integrations rename (PR-B…PR-E)

## Context

This finishes the rename specced in
`specs/done/2026/05/2026-05-25-05-integrations-umbrella-and-capability-split.md`.
PR-A (the capability split) landed; PR-B (frontend rename), PR-C (backend
rename), PR-D (table migration), and PR-E (route canonicalization, drop
`/connections`) were deferred there to ride together with the tail of
`2026-05-07-03-align-channel-and-connection-naming.md` so route and table
names are touched exactly once more, then frozen.

The two-level taxonomy from `2026-05-25-05` stays locked:

- **Integration** — the umbrella: the model, the table, the nav section, the
  picker, the "New integration" flow. Slack, Discord, webhook, email, Freebox
  are all Integrations.
- **Channel** — the *role* an Integration plays when it can receive
  notifications (`CanNotify`). Not a separate entity; surfaces only in
  notify-role copy ("Notify via these channels"), the `channelType` field on
  notification audit records, and Slack's own `#channel` vocabulary.

## Goal

1. Frontend nav, routes, components, and i18n use **Integration** as the
   umbrella; `channels.*` copy survives only for the notify-role wording.
2. Backend model, package, error codes, MCP tool names, and API path use
   **Integration**.
3. DB table renamed `integration_connections` → `integrations`; binding table
   `check_connections` → `check_channels`; FK column `connection_uid` →
   `integration_uid`. The `bun:"table:integration_connections"` tag is removed.
4. `/orgs/:org/integrations` becomes canonical at both org-level and per-check
   (`/orgs/:org/checks/:check/integrations`). `/channels` survives one release
   cycle as alias. `/connections` is dropped.

## Non-goals

- Reopening the umbrella/role decision. "Channel" is kept where it means a
  notify target — do not rename `channelType` in notification audit payloads,
  Slack literal channels, the "Notify via these channels" copy, the notify
  picker label, or `e2e/channels.spec.ts` notify-role assertions.
- Re-doing PR-A (capability split is already shipped).
- Migrating historical audit-event `connection_uid` / `channel_uid` payload
  keys (append-only, already aliased by readers per `2026-05-07-03`).

## Approach

One bundled rename across four PRs, in this order, each `make build lint test`
green. The bundle ships together; the table migration cannot land alone, and
the route canonicalization needs the model/handler renames in place.

### PR-B — Frontend rename (UI surfaces)

- **Routes** (`web/dash0/src/routes/orgs/$org/`):
  - `channels.tsx` → `integrations.tsx`
  - `channels.index.tsx` → `integrations.index.tsx`
  - `channels.new.tsx` → `integrations.new.tsx`
  - `channels.$channelUid.tsx` → `integrations.$integrationUid.tsx`
  - URL becomes `/orgs/$org/integrations[/new|/$integrationUid]`.
  - Route param `$channelUid` → `$integrationUid` (and all reads of
    `params.channelUid`).
- **Components**: `src/components/channels/` → `src/components/integrations/`.
  `channel-icon.tsx`, `channel-form.tsx`, `freebox-form.tsx` rename to
  `integration-icon.tsx`, `integration-form.tsx`, `freebox-form.tsx`. Exported
  symbols `ChannelIcon`, `channelIconComponent`, `channelLabel`, `ChannelForm`,
  `ChannelFormState` rename to `Integration*` equivalents.
- **i18n**: `locales/{en,fr,de,es}/channels.json` → `integrations.json`. Register
  the new namespace in `src/i18n.ts` (lines 20/38/56/74 + 99/119/139/159 in the
  current file). Keep a `channels.*` subtree inside the new namespace for the
  notify-role copy ("Notify via these channels", "Pick a channel",
  channel-column headers) so those strings are not over-renamed. Update
  `nav.json:channels` → `nav.json:integrations` (all four locales).
- **API client** (`src/api/hooks.ts`): rename TS type `Channel` → `Integration`,
  `CreateChannelRequest` → `CreateIntegrationRequest`, `UpdateChannelRequest` →
  `UpdateIntegrationRequest`. Rename hooks: `useChannels` → `useIntegrations`,
  `useChannel` → `useIntegration`, `useCreateChannel` → `useCreateIntegration`,
  `useUpdateChannel` → `useUpdateIntegration`, `useDeleteChannel` →
  `useDeleteIntegration`, `useRotateWebhookSecret`, `useTestWebhookChannel` →
  `useTestWebhookIntegration`. Update fetch URLs and query-cache keys to
  `integrations`. **Keep** `CheckConnection`, `useCheckConnections`,
  `IncidentNotification.channelType` as-is (notify-role surfaces; not renamed
  here — see Non-goals).
- **Cross-feature `<Link to=...>` / navigate targets** updated wherever they
  point at the renamed routes: `check-form.tsx` (2 sites), internal links in
  `channels.index.tsx`, `escalation/step-target-row.tsx` (hard-coded URL),
  `AppSidebar.tsx:88-90`, `CommandMenu.tsx:54`, `auth.slack.complete.tsx`
  (redirects to `/orgs/$org/integrations/$integrationUid`).
- **Playwright**: `web/dash0/e2e/channels.spec.ts` → `integrations.spec.ts`.
  Update route assertions but keep any notify-role-copy assertions.
- `routeTree.gen.ts` regenerates from the new filenames; do not hand-edit.

### PR-C — Backend rename (code, no DB change)

- **Model** (`server/internal/db/models/integration.go`): rename `Channel` →
  `Integration`, `NewChannel` → `NewIntegration`, `ChannelUpdate` →
  `IntegrationUpdate`, `ListChannelsFilter` → `ListIntegrationsFilter`. Update
  the docstring at the top. **Leave the `bun:"table:integration_connections"`
  tag in place** — it moves in PR-D.
- **Handler package** (`server/internal/handlers/channels/` →
  `server/internal/handlers/integrations/`): rename package, file, type, and
  method names. `ChannelResponse` → `IntegrationResponse`,
  `CreateChannelRequest` → `CreateIntegrationRequest`, etc. Methods
  `ListChannels`, `CreateChannel`, `GetChannel`, `UpdateChannel`,
  `DeleteChannel`, `RotateWebhookSecret`, `TestWebhookChannel` → `*Integration`.
- **Check-binding handler** (`server/internal/handlers/checkconnections/`):
  rename the package `checkconnections` → `checkchannels` to match its
  notify-role purpose. Update method names (`ListChannels` stays;
  `SetConnections`/`AddConnection` → `SetChannels`/`AddChannel`).
  `ErrNotNotifyCapable` keeps its meaning.
- **Notifications** (`server/internal/notifications/`): rename `sender.go` field
  `Connection *models.Channel` → `Integration *models.Integration`. Package name
  stays `notifications`.
- **Error codes** (`server/internal/handlers/base/`):
  `ErrorCodeChannelNotFound` → `ErrorCodeIntegrationNotFound`. Keep
  `ErrorCodeChannelNotFound` as a deprecated alias for one cycle.
- **MCP tools** (`server/internal/mcp/tools_connections.go` →
  `tools_integrations.go`): tool names `list_connections`/`create_connection` →
  `list_integrations`/`create_integration`. Update tool descriptions and prompt
  text.
- **Routes** (`server/internal/app/server.go`): add a third path prefix
  `/integrations` to both the org-level dual route (near line 773) and the
  per-check dual route (near line 576). All three bind the same handlers.
  PR-E removes `/connections`.
- **Docs**: update `wiki/api-specification.md` and `wiki/database-model.md` —
  document `/integrations` as canonical, mark `/channels` deprecated for one
  cycle, mark `/connections` removed at PR-E.

### PR-D — DB table migration

New migration `035_rename_integration_connections_to_integrations.{up,down}.sql`
in **both** `server/internal/db/postgres/migrations/` and
`server/internal/db/sqlite/migrations/`.

Up migration:
1. `ALTER TABLE integration_connections RENAME TO integrations`.
2. `ALTER TABLE check_connections RENAME TO check_channels` (binding table keeps
   "channel" to match the role taxonomy).
3. `ALTER TABLE check_channels RENAME COLUMN connection_uid TO integration_uid`.
4. Rename indexes: `idx_integration_connections_*` → `idx_integrations_*`;
   `check_connections_*` → `check_channels_*`.
5. Recreate the FK constraint `check_channels.integration_uid → integrations(uid)`
   by name (Postgres carries it through RENAME but the constraint name should
   align; verify).

Down migration reverses all of the above.

Code changes in the same PR:
- Drop `bun:"table:integration_connections,alias:integration_connection"` from
  `models.Integration`; use `bun:"table:integrations,alias:integration"`.
- Update the binding model to map `check_channels` with `integration_uid`.
- Grep `integration_connections|check_connections|connection_uid` across
  `server/` and update any hard-coded SQL strings.
- Update `bench-checks` and any seed/fixture SQL.

Risk: irreversible-ish. Round-trip `make migrate` down + up on the dev DB on
both Postgres and SQLite before merging.

### PR-E — Route canonicalization

- Drop `/connections` from both route loops in `server/internal/app/server.go`.
  Org-level becomes `[]string{"/orgs/:org/integrations", "/orgs/:org/channels"}`;
  per-check becomes `[]string{"/integrations", "/channels"}`.
- `/channels` stays one release cycle as alias (follow-up spec drops it).
- Remove `ErrorCodeChannelNotFound` alias if a cycle has elapsed; otherwise
  carry to the follow-up.
- Update `wiki/api-specification.md` to mark `/connections` removed.
- Rename `TestChannelsAliasMatchesConnections` →
  `TestIntegrationsAliasMatchesChannels` in
  `server/test/integration/channels_alias_test.go`; update the assertion to
  cover the `/integrations` → `/channels` alias pair.

## Files affected (representative)

Greps to drive the rename:
- `rtk grep -rin "channel" server/internal/handlers/channels server/internal/db/models/integration.go server/internal/notifications server/internal/mcp server/internal/app/server.go`
  (**do not** sweep `server/internal/integrations/slack/` — Slack literal
  channels live there).
- `rtk grep -rin "Channel\|channelUid\|channelLabel\|ChannelIcon\|ChannelForm" web/dash0/src`
  excluding `channelType` / "Notify via these channels" / notify-role copy.
- `rtk grep -rn "integration_connections\|check_connections\|connection_uid" server`
  to drive PR-D code edits.

## Sequencing

B → C → D → E on `feat/bulk`. Frontend (B) and backend (C) can be authored in
parallel but merge C first (PR-B's API renames reference C's new routes).
PR-D requires C's model rename. PR-E requires D.

## Verification

1. **Per PR**: `make build && make lint && make test && make test-dash` pass.
2. **PR-B (UI)**: `/orgs/default/integrations` loads; create a Slack integration
   end-to-end; bind to a check — the notify picker reads "Notify via these
   channels" (notify-role copy intact). Freebox absent from the notify picker
   (PR-A). Nav, command palette, breadcrumbs say "Integrations".
3. **PR-C (backend)**: `curl /api/v1/orgs/default/integrations` returns the same
   payload as `/channels` and `/connections`. MCP `list_integrations` returns the
   same rows as the old `list_connections`.
4. **PR-D (DB)**: `make migrate` up + down round-trips cleanly on both Postgres
   and SQLite. `\d integrations` and `\d check_channels` show expected schema;
   row counts match. `bench-checks` still runs.
5. **PR-E (routes)**: `curl /api/v1/orgs/default/connections` → 404;
   `/channels` and `/integrations` → 200 with identical payloads. The alias
   integration test asserts the new canonical/alias pair.
6. **E2E smoke**: `integrations.spec.ts` covers list/create/edit/delete on the
   canonical route; grouped picker (Notification channels vs Data sources) works.

## Implementation Plan

Branched off `feat/batch-todos-2026-05-29` as `feat/channels-to-integrations-rename`.
PR-A (capability split) already landed in the prior spec. This branch executes
PR-B…PR-E in order, each granular-committed and kept build/lint/test green.

### PR-B — Frontend rename (UI surfaces)

1. Rename route files `routes/orgs/$org/channels*.tsx` → `integrations*.tsx`,
   route param `$channelUid` → `$integrationUid`, internal URLs to
   `/orgs/$org/integrations[/new|/$integrationUid]`.
2. Rename `src/components/channels/` → `src/components/integrations/`;
   `channel-icon.tsx` → `integration-icon.tsx`, `channel-form.tsx` →
   `integration-form.tsx`, `freebox-form.tsx` kept. Rename exported symbols
   `ChannelIcon`/`channelIconComponent`/`channelLabel`/`ChannelForm`/
   `ChannelFormState` → `Integration*`.
3. i18n: `locales/{en,fr,de,es}/channels.json` → `integrations.json`; register
   new namespace in `src/i18n.ts`; keep `channels.*` subtree for notify-role
   copy; `nav.json:channels` → `nav.json:integrations`.
4. API client (`src/api/hooks.ts`): rename TS types `Channel`→`Integration`,
   `Create/UpdateChannelRequest`→`*IntegrationRequest`; hooks `useChannel(s)`,
   `useCreate/Update/DeleteChannel`, `useTestWebhookChannel`→`*Integration`;
   fetch URLs + query keys → `integrations`. Keep `CheckConnection`,
   `useCheckConnections`, `IncidentNotification.channelType` as-is.
5. Cross-feature `<Link>`/navigate targets updated: `check-form.tsx`,
   `escalation/step-target-row.tsx`, `AppSidebar.tsx`, `CommandMenu.tsx`,
   `auth.slack.complete.tsx`.
6. Playwright `e2e/channels.spec.ts` → `integrations.spec.ts`; update route
   assertions, keep notify-role copy assertions. Regenerate `routeTree.gen.ts`.

### PR-C — Backend rename (code, no DB change)

1. Model `db/models/integration.go`: `Channel`→`Integration`,
   `NewChannel`→`NewIntegration`, `ChannelUpdate`→`IntegrationUpdate`,
   `ListChannelsFilter`→`ListIntegrationsFilter`; keep table tag for PR-D.
2. Handler package `handlers/channels/`→`handlers/integrations/`: rename
   package, types, methods (`*Channel`→`*Integration`).
3. Binding handler `handlers/checkconnections/`→`handlers/checkchannels/`:
   rename package; `SetConnections`/`AddConnection`→`SetChannels`/`AddChannel`;
   `ListChannels` stays; `ErrNotNotifyCapable` kept.
4. `notifications/sender.go` field `Connection *models.Channel`→
   `Integration *models.Integration`.
5. Error codes: `ErrorCodeChannelNotFound`→`ErrorCodeIntegrationNotFound`, keep
   old as deprecated alias.
6. MCP `tools_connections.go`→`tools_integrations.go`: tool names
   `list_connections`/`create_connection`→`list_integrations`/`create_integration`.
7. `app/server.go`: add `/integrations` prefix to org-level + per-check dual
   route loops (all three bind same handlers).
8. Docs: `wiki/api-specification.md`, `wiki/database-model.md`.

### PR-D — DB table migration

1. Migration `035_rename_integration_connections_to_integrations.{up,down}.sql`
   in both postgres + sqlite migrations dirs: rename tables
   `integration_connections`→`integrations`, `check_connections`→`check_channels`,
   column `connection_uid`→`integration_uid`, indexes, FK constraint. Down
   reverses.
2. Model tag → `bun:"table:integrations,alias:integration"`; binding model maps
   `check_channels`/`integration_uid`.
3. Grep + fix hard-coded SQL strings, bench-checks, seed/fixtures.

### PR-E — Route canonicalization

1. Drop `/connections` from both route loops; org-level
   `["/orgs/:org/integrations","/orgs/:org/channels"]`, per-check
   `["/integrations","/channels"]`.
2. Update docs to mark `/connections` removed.
3. Rename `TestChannelsAliasMatchesConnections`→`TestIntegrationsAliasMatchesChannels`
   in alias test; assert `/integrations`→`/channels` alias pair.
