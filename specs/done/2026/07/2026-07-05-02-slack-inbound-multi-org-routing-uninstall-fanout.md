# Slack multi-org workspaces (2/2): inbound routing and uninstall fan-out

## Problem

Once **2026-07-05-01** lands, one Slack workspace (`team_id`) can have an
integration connection in several orgs. But everything *inbound* from Slack —
events, slash commands, @mentions, interactions, uninstall notifications —
carries only `team_id` (+ channel/user), and the server resolves it to a
**single** connection via `GetConnectionByTeamID`
(`server/internal/integrations/slack/service.go:147`), which wraps the
org-agnostic `GetChannelByProperty`
(`server/internal/db/postgres/postgres.go:2937`). With two rows per
`team_id`, that `Scan` returns an arbitrary row — commands would create
checks in a random org.

Affected call sites (all resolve org context from `team_id` alone):

- `service.go` — `GetClient:684`, `CreateCheckWithOptions:714`,
  `SetDefaultChannel:763`
- `mention_commands.go:118,196,240,320,559` (@solidping commands)
- `events.go:54,91,212` (app home, member-joined, etc.)
- `commands.go:76` (`/solidping` slash command)
- `interactions.go:103` (buttons/modals)

Two more single-connection assumptions:

- **`HandleAppUninstalled` cleans up only one org.**
  (`service.go:658`) deletes the first matching connection; when the app is
  removed from the workspace, every other org's connection keeps a dead token
  and keeps rendering as "connected".
- **`CountInstalledTeams` counts connections, not teams.**
  (`service.go:644`) — the Socket Mode status snapshot over-counts once a
  team is connected to several orgs.

Note: uninstall affects all orgs at once *by nature* — every org's connection
stores credentials for the same (app × workspace) bot, so revoking the app in
Slack kills them all. The cleanup must reflect that.

## Product decision

Inbound Slack commands operate on the workspace's **home org** — the org
recorded in `organization_providers` for `(slack, team_id)` (first install
wins). This is already the org that Marketplace installs and
Sign-in-with-Slack land in (`server/internal/handlers/auth/slack_service.go:377`),
so "the workspace's commands live in its home org" is coherent and
predictable. Notifications (outbound) remain fully multi-org.

Per-channel org overrides or an interactive org picker are explicitly **not**
built now — only if real users ask for command routing to a non-home org.

## Proposal

### 1. Deterministic team → connection resolution

Rework `GetConnectionByTeamID` (single choke point — callers unchanged):

1. Look up the home org: `GetOrganizationProviderByProviderID(slack, teamID)`.
   If found, fetch **that org's** connection with the org-scoped lookup from
   spec 01 (`GetChannelByPropertyForOrg`).
2. Fallback (no provider row, e.g. workspace only ever dashboard-installed,
   or home org's connection deleted): oldest connection for the team —
   deterministic `ORDER BY created_at ASC LIMIT 1` — plus a warning log when
   more than one connection exists, so ambiguity is observable.

This keeps `/solidping`, @mentions, App Home, and interactions working
exactly as today for single-org workspaces, and pins them to the home org for
multi-org ones.

### 2. Uninstall fan-out

`HandleAppUninstalled` deletes **all** connections for the team:

- New DB method on the interface (`server/internal/db/service.go`):
  `ListChannelsByProperty(ctx, connType, propertyName, propertyValue)`
  returning all matching non-deleted connections **across orgs**, implemented
  for both postgres and sqlite.
- Loop and soft-delete each (`DeleteChannel`), logging one line per org.
  Also used as the deterministic-fallback source for §1 (oldest = first of
  the `created_at ASC` list) so both backends share one query shape.

### 3. Honest team counting

`CountInstalledTeams` counts **distinct `team_id`** across Slack connections
instead of rows. (Also fixes its current comment claiming it counts
organizations.) Keep it best-effort as today.

## Out of scope

- Changing `organization_providers` semantics or Sign-in-with-Slack routing
  (still lands in the home org).
- Per-channel org overrides, `/solidping use-org <slug>`, or interactive org
  pickers for command routing.
- Re-homing a workspace (moving the provider row to another org) — manual DB
  operation for now.
- Any dashboard UI for "commands are routed to org X" (worth a hint on the
  integration page eventually; not blocking).

## Acceptance criteria

- Workspace connected to orgs A (home) and B: `/solidping create <url>`,
  @mention commands, App Home, and interactions all operate on **A**, every
  time; nothing is created in B.
- Workspace with connections but no `organization_providers` row: commands
  resolve to the oldest connection on both backends, and a warning is logged
  when several connections exist.
- `app_uninstalled` removes the connections of **all** orgs for that team;
  a workspace uninstalled in Slack shows no stale "connected" integrations
  anywhere.
- Socket Mode status reports 1 installed team for a two-org workspace.
- Table-driven tests for resolution order, fallback determinism, and
  uninstall fan-out, green on PostgreSQL and SQLite.

## Implementation plan

- [ ] DB: add `ListChannelsByProperty` (postgres + sqlite + interface) with
      `created_at ASC` ordering and tests on both backends.
- [ ] Service: rework `GetConnectionByTeamID` (home-org first, deterministic
      fallback, ambiguity warning); unit tests with provider-row present /
      absent / two connections.
- [ ] Service: fan-out `HandleAppUninstalled`; test with two orgs connected.
- [ ] Service: `CountInstalledTeams` → distinct team IDs; fix comment.
- [ ] Verify mention/slash/interaction handlers need no changes (they all go
      through `GetConnectionByTeamID` / `GetClient`).
- [ ] Run `make test`, `make lint`.

## Dependencies

Builds on **2026-07-05-01** (org-scoped connections must exist for any of
this to matter; reuses `GetChannelByPropertyForOrg`).

## Implementation Plan

1. **DB: `ListChannelsByProperty` (postgres + sqlite + interface)**
   - Add to the `db.Service` interface (`server/internal/db/service.go`), next
     to `GetChannelByPropertyForOrg`: returns **all** non-deleted connections
     matching `(connType, propertyName, propertyValue)` across every org,
     ordered `created_at ASC` (oldest first — both the uninstall fan-out loop
     and the §1 deterministic fallback need this exact order).
   - Implement for postgres (`server/internal/db/postgres/postgres.go`,
     alongside `GetChannelByPropertyForOrg`) and sqlite (mirrors the
     `json_extract` pattern already used there).
   - Add a shared cross-engine test in `server/internal/db/service_test.go`
     (same pattern as `testChannelByPropertyForOrg`): two orgs share a
     `team_id`, `ListChannelsByProperty` returns both ordered oldest-first;
     excludes soft-deleted rows; empty slice (not error) when nothing matches.

2. **Service: rework `GetConnectionByTeamID`**
   (`server/internal/integrations/slack/service.go:147`) — single choke
   point, callers unchanged:
   - Look up `GetOrganizationProviderByProviderID(ctx, models.ProviderTypeSlack, teamID)`.
     If found, resolve that org's connection via `GetChannelByPropertyForOrg`.
     If that specific lookup 404s (home org's connection was deleted but
     other orgs' rows remain), fall through to the deterministic fallback
     below rather than returning not-found.
   - Fallback (no provider row, or home-org connection missing): call the new
     `ListChannelsByProperty`, take the first (oldest, `created_at ASC`) of
     the returned slice; `sql.ErrNoRows` → `ErrConnectionNotFound` if empty.
     Log a warning (`slog.WarnContext`) when the fallback list has more than
     one entry, since routing is ambiguous.
   - Add `server/internal/integrations/slack/service_routing_test.go` with
     table-driven cases: (a) provider row present, home org resolves; (b)
     provider row present but home org's own connection was deleted, falls
     back to oldest remaining; (c) no provider row, two connections, resolves
     to oldest + warning path exercised; (d) no provider row, one connection;
     (e) nothing at all → `ErrConnectionNotFound`. Uses the existing
     `setupSlackService` sqlite-in-memory harness — no separate postgres
     variant needed since `GetConnectionByTeamID` itself is DB-engine
     agnostic (the engine parity is already covered by the DB-layer test in
     step 1).

3. **Service: fan-out `HandleAppUninstalled`**
   (`server/internal/integrations/slack/service.go:713`):
   - Replace the single `GetChannelByProperty` + `DeleteChannel` with
     `ListChannelsByProperty` + loop `DeleteChannel` over every result,
     logging one `slog.InfoContext` line per deleted connection (org UID +
     connection UID). No matches → no-op (already-clean state), matching
     today's "connection already deleted, ignore" behavior.
   - Test: two orgs connected to the same team, `HandleAppUninstalled` soft-
     deletes both; a third, unrelated team's connection is untouched.

4. **Service: `CountInstalledTeams` → distinct team IDs**
   (`server/internal/integrations/slack/service.go:699`):
   - Current implementation calls `ListChannels` with only `Type` set and no
     `OrganizationUID` — since every `ListChannels` implementation
     unconditionally filters `Where("organization_uid = ?", filter.OrganizationUID)`,
     an empty-string org UID matches zero rows, so this has always silently
     returned 0. Fix at the root: make the `organization_uid` filter
     conditional on `filter.OrganizationUID != ""` in both postgres and
     sqlite `ListChannels` (every other caller already passes a real org
     UID, so this is a no-op for them) so an org-less filter genuinely lists
     across all orgs.
   - Update `CountInstalledTeams` to dedupe by `Settings["team_id"]` after
     listing (via `models.SlackSettingsFromJSONMap`, skip rows that fail to
     parse rather than erroring the whole count) and fix the doc comment
     (counts distinct teams, not organizations/rows).
   - Test: three connections, two sharing a team_id across two orgs, one
     distinct team in a third org → count is 2.

5. **Verify mention/slash/interaction handlers need no changes** — confirmed
   during investigation: every listed call site
   (`mention_commands.go:118,196,240,320,559`, `events.go:54,91,212`,
   `commands.go:76`, `interactions.go:103,202,233,313,370`) goes through
   `GetConnectionByTeamID` or `GetClient` (which itself calls
   `GetConnectionByTeamID`), so step 2 alone fixes routing for all of them.
   No handler edits needed; note this explicitly in the self-review instead
   of touching those files.

6. **Run `make build-backend lint-back test`.** Fix any lint/test fallout.
   No dashboard-visible change in this spec (routing is server-internal), so
   no E2E/dash0 work.
