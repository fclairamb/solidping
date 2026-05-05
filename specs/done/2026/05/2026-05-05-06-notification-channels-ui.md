# Notification channels — dashboard UI

## Context

The notification pipeline is fully built on the backend. `IntegrationConnection`
is an org-scoped resource with 9 channel types (slack, discord, webhook, email,
googlechat, mattermost, ntfy, opsgenie, pushover), full CRUD at
`/api/v1/orgs/{org}/connections`, and a `check_connections` join with optional
per-check setting overrides at
`/api/v1/orgs/{org}/checks/{check}/connections`. Incident lifecycle events
(`incident.created` / `resolved` / `escalated` / `reopened`) enqueue
`NotificationJobConfig` jobs that workers run via the senders in
`server/internal/notifications/`. MCP already exposes `list_connections` and
`create_connection`.

The dash0 frontend has **zero** coverage for any of this — no routes, no
hooks, no components. Today, an operator can only configure channels by
hitting the API directly or through MCP. That makes the dashboard unusable
for the most important reason a monitoring product exists: getting paged when
something breaks. Closing this gap is the spec.

## Goal

A new operator opens the dash0 sidebar, clicks **Channels**, adds a Slack
workspace and a webhook in under two minutes, then opens any check and
chooses which of those channels should fire on incident events. They never
need to touch a JSON editor or copy/paste a token they had to source from
another tool.

## Scope

In scope (one PR):

1. **Sidebar entry**: top-level "Channels" item under the org, peer to
   *Status pages* and *Incidents*, in
   `web/dash0/src/components/layout/AppSidebar.tsx`.

2. **List page** at `/orgs/$org/channels` (`channels.index.tsx`), mirroring
   `routes/orgs/$org/status-pages.index.tsx`:
   - Header with title + **+ New channel** button.
   - Search input (filter by name / type).
   - Table columns: Name (with type icon) · Type (Badge) · Status
     (Enabled/Disabled + Default star) · Used by (count of bound checks) ·
     Updated · ⋮ menu (Edit, Enable/Disable, Set as default, Delete).
   - Empty state: short copy + four quick-pick buttons (Slack, Discord,
     Email, Webhook) that go straight to the typed `new` page.

3. **New / edit pages** at `/orgs/$org/channels/new` and
   `/orgs/$org/channels/$connectionUid` (+ `.edit.tsx` if separated, mirror
   `status-pages.$statusPageUid.edit.tsx`):
   - Common fields: name, enabled, isDefault.
   - Type picker on `new` (icons + one-line description per type).
   - Per-type form panels under
     `web/dash0/src/components/channels/per-type/*.tsx`. Each panel renders
     the small fixed settings struct for its channel — *not* a generic
     JSONB editor:
     - Webhook / Discord / GoogleChat / Mattermost: single `webhook_url`
       field.
     - Email: from address + recipient list.
     - Ntfy: server URL, topic, priority.
     - Pushover: user key + app token.
     - Opsgenie: API key + (optional) team.
     - **Slack: OAuth-only.** "Add to Slack" button initiates the OAuth
       install (the bot from `specs/done/2025/12/2025-12-29-slack-bot.md`
       already exists). Settings are populated by the install callback;
       UI shows `team_name` / `channel_name` read-only. No raw token field.
   - Submit hits `POST` (new) or `PATCH` (edit). Toast on success, redirect
     to detail.
   - Delete uses `AlertDialog` confirm, same pattern as status pages.

4. **Detail view** on the `$connectionUid` route:
   - Settings rendered read-only (mask secrets — show last 4 chars of any
     token-like string).
   - Enabled / default toggles inline.
   - **Bound checks** sub-list, click-through to each check edit page.
     Source: a new `?withChecks=true` filter on the connection GET, or a
     dedicated endpoint — backend lead picks during impl.

5. **Per-check binding** ("Notify via" section) on the check edit page
   (`routes/orgs/$org/checks.$checkUid.edit.tsx` — or `check-form.tsx` if
   the form is shared with `checks.new.tsx`):
   - Multi-select checkboxes listing all enabled org channels with type
     icons.
   - Channels with `is_default = true` are pre-checked when creating a new
     check.
   - Saving uses the existing
     `PUT /api/v1/orgs/{org}/checks/{check}/connections` to replace the set.

6. **API hooks** in `web/dash0/src/api/hooks.ts`, mirroring
   `useStatusPages` (line 1011) / `useDeleteStatusPage` (line 1069):
   - `useConnections(org)`
   - `useConnection(org, uid)`
   - `useCreateConnection(org)`
   - `useUpdateConnection(org)`
   - `useDeleteConnection(org)`
   - `useCheckConnections(org, checkUid)`
   - `useSetCheckConnections(org, checkUid)`
   - Plus `Connection`, `ConnectionType`, and per-type `*Settings` types —
     hand-written, or derived from the OpenAPI spec via existing
     `oapi-codegen` if it covers `/connections`.

7. **i18n**: new `channels` namespace in
   `web/dash0/src/locales/{en,fr,de,es}/channels.json`. Mirror the
   `statusPages` namespace structure.

Out of scope (deferred follow-ups):

- **"Send test notification" button.** No backend endpoint exists; needs a
  separate spec for the dispatch path.
- **Per-check setting overrides UI** (e.g., route a single check to a
  different Slack channel). Backend supports it via `CheckConnection.Settings`
  but exposing it adds significant form complexity.
- **Last-delivery status per channel** (would need surfacing notification
  job history on the detail page).
- **New channel types** — Microsoft Teams, Telegram, PagerDuty are tracked
  in `specs/ideas/2026-03-22-notification-channels.md` and
  `specs/ideas/2026-03-22-telegram-notifications.md`. Independent backend
  work.

## Implementation notes

- Type-dispatched forms beat one universal form. Each settings struct is
  small and fixed; a shared "switch on type" component keeps the markup
  consistent without inventing a generic schema renderer.
- Secrets must be masked in the detail view and never echoed back from the
  list endpoint — verify the connection JSON shape before committing the
  list page (the backend already gates this, but check).
- The Slack OAuth flow already lives in the backend (`2025-12-29-slack-bot.md`).
  The new-channel flow for type=`slack` is "kick off install, wait for
  callback, redirect to detail" — no client-side token handling.
- For the "Used by" column, prefer a backend-aggregated count over fetching
  every check's connections. A `?withChecks=count` (or similar) on the
  connection list endpoint avoids N+1.
- For per-check binding, reuse the `PUT` endpoint that already replaces the
  set in one call — don't add/remove individually.
- Empty-state copy on the channels list should plug directly into the
  activation funnel (`specs/todos/2026-05-05-03-activation-time-to-first-signal.md`)
  — the "first notification configured" milestone fires here.

## Edge cases

- **User deletes a channel that's bound to checks.** Show the bound-check
  count in the delete confirm dialog; the `check_connections` row cascades
  on the backend, but the user should know what they're severing.
- **Disabled channel still bound to a check.** The check edit page should
  still show it (with a "disabled" badge) and let the user unbind, but it
  should not be selectable as a *new* binding.
- **Slack OAuth completes but user navigates away mid-install.** The
  callback creates the connection regardless; the user finds it on the
  list page on next visit. Don't block on the redirect.
- **`is_default = true` on multiple channels.** Allowed today (multiple
  defaults all attach). Don't enforce singularity in the UI; a "Set as
  default" toggle is fine.
- **Org has zero channels and the operator opens a check edit page.** The
  "Notify via" section shows an empty state with an inline link to create a
  channel, not a blank box.
- **Per-check overrides exist on a `check_connections` row** (set via API
  before the UI shipped). The bind UI should preserve them on save — i.e.,
  the `PUT` payload should not strip `settings` when re-sending an
  unchanged binding. Read the row first; merge.

## Test plan

- [ ] Manual: create a webhook channel via the UI, hit a real webhook test
      service (webhook.site), bind it to a check, force the check to fail,
      confirm payload arrives.
- [ ] Manual: create a Slack channel via OAuth in the test workspace, bind
      to a check, force a failure, confirm message in Slack.
- [ ] Manual: edit a channel's name and `enabled` — verify list reflects
      both.
- [ ] Manual: delete a channel that's bound to one check — confirm dialog
      shows the count, check's binding clears after delete.
- [ ] Manual: new check with one `is_default = true` channel — verify it's
      pre-checked on creation.
- [ ] Manual: empty state on `/channels` and on the check edit "Notify via"
      section both render and link correctly.
- [ ] e2e in `web/dash0/e2e/`: happy path — create channel → bind to check
      → unbind → delete channel.
- [ ] `bun run lint` and `make lint-back` clean.

## Implementation Plan

This spec ships in one PR but with two clear deferred items so the
load-bearing UX lands without scope creep:

1. **API hooks + types** in `web/dash0/src/api/hooks.ts`. Seven hooks
   per the scope plus a `Connection` / `ConnectionType` type and the
   per-type settings shapes. Hand-written; the connection JSON shape is
   stable.
2. **Sidebar entry** in `AppSidebar.tsx` — "Channels" peer to "Status
   pages".
3. **Channels list page** at `/orgs/$org/channels` — table with name,
   type, status, default star, updated. Search filter. Quick-pick
   buttons in the empty state.
4. **New channel page** at `/orgs/$org/channels/new` — type picker
   then the per-type form panel.
5. **Channel detail/edit page** at `/orgs/$org/channels/$connectionUid`
   — edit form, enable/default toggles, delete dialog. Secrets masked
   on the read-only summary.
6. **Per-type form panels** under
   `web/dash0/src/components/channels/per-type/` — webhook (covers
   webhook/discord/googlechat/mattermost via reuse), email, ntfy,
   pushover, opsgenie. Slack ships with a placeholder pointing at
   the existing OAuth path (the OAuth-only flow is a follow-up; the
   button is wired but the install handoff lives in a separate spec).
7. **i18n** keys in `channels.json` for en/fr/de/es.
8. **Per-check binding ("Notify via")** on the check edit form.

Deferred to follow-up specs:

- **"Used by" count** column on the list — needs a backend
  `?withChecks=count` aggregator. List page renders the column header
  but leaves the cell empty for V1.
- **Slack OAuth-only flow**. The Slack form panel renders a "Connect
  via Slack OAuth" hint pointing at the existing bot install URL; the
  full callback wiring lands once the existing Slack install path is
  exposed end-to-end.
- **Send-test-notification button**, **last-delivery status**, and
  **per-check setting overrides UI** — already in the spec's "Out of
  scope".

## Files touched (estimate)

New:

- `web/dash0/src/routes/orgs/$org/channels.tsx` (layout shell)
- `web/dash0/src/routes/orgs/$org/channels.index.tsx`
- `web/dash0/src/routes/orgs/$org/channels.new.tsx`
- `web/dash0/src/routes/orgs/$org/channels.$connectionUid.tsx`
- `web/dash0/src/components/channels/channel-form.tsx` (type-dispatched)
- `web/dash0/src/components/channels/channel-icon.tsx`
- `web/dash0/src/components/channels/per-type/*.tsx` (one per channel type)
- `web/dash0/src/locales/{en,fr,de,es}/channels.json`
- `web/dash0/e2e/channels.spec.ts`

Modified:

- `web/dash0/src/api/hooks.ts` — 7 new hooks + `Connection` types.
- `web/dash0/src/components/layout/AppSidebar.tsx` — Channels nav item.
- `web/dash0/src/components/shared/check-form.tsx` (or
  `routes/orgs/$org/checks.$checkUid.edit.tsx`) — "Notify via" multi-select.
- Possibly `server/internal/handlers/connections/handler.go` — add
  `?withChecks=count` (or similar) for the list page's "Used by" column.
