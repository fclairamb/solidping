# Integrations umbrella + sink/source capability split

## Context

Two prior decisions now contradict each other:

- **`2026-05-07-03-align-channel-and-connection-naming.md`** standardized the
  whole stack on **"channel"**, on the explicit premise that *every*
  `integration_connections` row **is a notification target**. Its decision text
  says, verbatim: *"If [inbound read-from integrations] do [materialize], those
  should live in their own table — not share the notification-target table."*
  That spec is mostly landed (PR-1…PR-4); **PR-5** (drop the deprecated
  `/connections` paths) and **PR-6** (rename the table
  `integration_connections` → `channels`) are still pending.
- **`2026-05-24-06-freebox-os-integration.md`** then added a `freebox`
  connection type — a **data source, not a notification sink** — directly into
  the channels table, contradicting the "give sources their own table" plan.

The contradiction is visible in the code as a carve-out:
[`server/internal/notifications/registry.go:28`](../../server/internal/notifications/registry.go)
— `freebox` is the only `ConnectionType` whose sender lookup returns
`(nil, false)`, with an apologetic comment: *"Freebox is a monitoring source,
not a notification sink."* The model docstring
([`server/internal/db/models/integration.go:28`](../../server/internal/db/models/integration.go))
still defines a Channel as *"a notification target,"* which `freebox` violates.

Today nothing stops an operator from binding a Freebox channel to a check as a
notification target. It is accepted and then silently no-ops at send time. That
is the user-facing bug underneath the naming question.

## Honest opinion

The thing that feels wrong is **not** that Freebox reused the
`integration_connections` storage. That was the right call — that table already
provides per-org scoping, the AES-256-GCM `settings_private` envelope, CRUD, a
type discriminator, and soft-delete. Building a parallel `integration_sources`
table (as `2026-05-07-03` imagined) would duplicate all of it for no benefit.

What's wrong is the **taxonomy**: the concept was relabeled "Channel"
(notification-target) while the storage stayed the neutral
`integration_connections`. Freebox falls outside the narrowed meaning. The
`2026-05-07-03` premise — *"sources won't materialize; if they do, separate
table"* — was falsified by a source shipping into the same table six weeks
later, **and the table reuse was correct.** So the fix is to widen the concept,
not to fork the table.

Two honest caveats for whoever picks this up:

1. **The rename is the cheap half. The capability split is the substance.**
   Renaming "Channels → Integrations" without `canNotify`/`canSource` would just
   move the `(nil,false)` carve-out to a page called Integrations and leave
   Freebox bindable as a notify target. Do the capability split even if the
   rename is deferred.
2. **This reverses part of `2026-05-07-03` and is the third name this concept
   has worn** (connection → channel → integration). That is a real flip-flop
   cost. We accept it once, lock "Integration" as the umbrella, and **keep
   "channel" as the notify-capable role** so the `2026-05-07-03` wins ("Notify
   via these channels" copy, Slack's literal channels) survive intact.

## The decision

A two-level taxonomy, replacing the flat "everything is a Channel" model:

- **Integration** — the stored, per-org, credentialed connection to a
  third-party system. This is the umbrella: the table, the model, the nav
  section, the "New integration" flow. Slack, Discord, webhook, email, Freebox
  are all Integrations.
- **Channel** — the *role* an integration plays when it can **receive
  notifications**. Not a separate entity; a capability-filtered view. The check
  edit page keeps saying *"Notify via these channels"* and only lists
  notify-capable integrations.

Each `ConnectionType` declares two capabilities:

| Type | `canNotify` (sink / "channel") | `canSource` (checks read from it) |
|---|---|---|
| slack, discord, webhook, email, googlechat, mattermost, ntfy, opsgenie, pushover | ✅ | ❌ |
| freebox | ❌ | ✅ |

The flags are independent — a future type may be both.

**This supersedes the still-pending tail of `2026-05-07-03`:**
- **PR-6 is redirected**: the table is renamed `integration_connections` →
  **`integrations`**, *not* `channels`. (Lower-risk alternative: keep
  `integration_connections` as-is — the name is already neutral and correct —
  and skip the table migration entirely. See Sequencing.)
- **PR-5** (drop deprecated `/connections` paths) still happens, but the
  canonical path becomes `/integrations`, with `/channels` kept as the alias for
  one release cycle (it is now itself the legacy name).

## Goal

1. A data-driven capability registry (`canNotify` / `canSource`) replacing the
   `(nil,false)` special-case in the notifications sender lookup.
2. Server-side rejection: binding a non-`canNotify` integration to a check as a
   notification target returns a validation error (closes the silent-no-op bug).
3. User-facing concept renamed to **Integrations**, with the "New integration"
   picker grouped into **Notification channels** (`canNotify`) and **Data
   sources** (`canSource`).
4. The change sequenced to ride along with the deferred PR-5/PR-6 of
   `2026-05-07-03` so the table/route naming lands once, not twice.

## Non-goals

- A second storage table for sources. Explicitly rejected — reuse
  `integration_connections`.
- Changing the Freebox pairing flow, the line-quality check, or LAN discovery.
- Migrating historical audit-event `connection_uid` / `channel_uid` payload
  keys (append-only; readers already alias both, per `2026-05-07-03`).
- Adding new source types (SNMP creds, UniFi, etc.) — the capability model just
  makes them cheap to add later.

## Capability model (backend)

New, in `server/internal/db/models/integration.go` (or a sibling
`capabilities.go`):

```go
// Capabilities describes what roles an integration type can play.
type Capabilities struct {
    CanNotify bool // can receive outbound notifications (acts as a "channel")
    CanSource bool // provides data that checks read from
}

func CapabilitiesFor(t ConnectionType) Capabilities {
    switch t {
    case ConnectionTypeFreebox:
        return Capabilities{CanSource: true}
    default: // all current notification sinks
        return Capabilities{CanNotify: true}
    }
}
```

- `notifications.GetSender` consults `CapabilitiesFor(t).CanNotify` before its
  switch; the Freebox carve-out comment is deleted (the behavior becomes a
  consequence of the registry, not a special case).
- The per-check notify-binding service (the handler behind
  `POST /orgs/:org/checks/:check/channels`, see
  [`server/internal/app/server.go:546`](../../server/internal/app/server.go))
  rejects binding an integration whose `CanNotify` is false:
  `VALIDATION_ERROR`, message *"This integration cannot receive
  notifications."*
- The `freebox_line` checker's connection resolver and the ICMP "Discover from
  Freebox" picker already filter by `c.type === "freebox"`; restate that as
  `CanSource` filtering so future source types are picked up automatically.

## Frontend

- **Nav / route / i18n**: `Channels` → `Integrations`. Canonical route
  `/orgs/$org/integrations`; `channels.*.tsx` → `integrations.*.tsx`; i18n
  namespace `channels.json` → `integrations.json` (keep a `channels.*` subtree
  for the notify-role copy: "Notify via these channels").
- **New-integration picker** (`channels.new.tsx` →
  `integrations.new.tsx`): group the type list into **Notification channels**
  (`canNotify`) and **Data sources** (`canSource`) using a frontend mirror of
  the capability map.
- **Check notify picker** (`check-form.tsx`, `checks.new.tsx`): filter the
  bindable list to `canNotify` integrations — Freebox disappears from the
  "Notify via these channels" list. This is the visible bug fix.
- **`ConnectionType`** union and the `Channel` TS type
  ([`web/dash0/src/api/hooks.ts:2597`](../../web/dash0/src/api/hooks.ts)):
  rename type `Channel` → `Integration`; add a `CAPABILITIES` const map.
- `components/channels/` → `components/integrations/`; `channel-icon.tsx`
  unchanged in behavior.

## Files affected

- Backend: `db/models/integration.go` (model `Channel` → `Integration`, table
  tag, `CapabilitiesFor`), `notifications/registry.go`,
  per-check binding service + handler, `handlers/channels/` →
  `handlers/integrations/`, error codes (`ErrorCodeChannelNotFound` →
  `ErrorCodeIntegrationNotFound`, keep old as alias for one cycle),
  `app/server.go` routes, `mcp/tools_connections.go` (`list_channels` →
  `list_integrations`), `docs/api-specification.md`, `docs/database-model.md`.
- Frontend: `api/hooks.ts`, `routes/orgs/$org/channels.*.tsx`,
  `components/channels/*`, `components/shared/check-form.tsx`,
  `routes/orgs/$org/checks.new.tsx`, `locales/{en,fr,de,es}/channels.json`,
  `e2e/channels.spec.ts`.

## Sequencing

Order matters; each PR builds and keeps `make lint test` green.

1. **PR-A — capability split (no rename, highest value).** Add `CapabilitiesFor`
   + `CAPABILITIES`; route sender lookup and notify-binding validation through
   it; filter the check notify picker by `canNotify`; group the new-integration
   picker. **This alone closes the silent-no-op bug** and is shippable even if
   the rename never happens.
2. **PR-B — frontend rename** Channels → Integrations (nav, routes, components,
   i18n namespace), keeping `channels.*` copy for the notify role. Backend
   `/channels` API unchanged at this point.
3. **PR-C — backend rename** `Channel` → `Integration` (model, handler package,
   error codes, MCP tools). Add `/orgs/:org/integrations` route group alongside
   `/channels` + `/connections`.
4. **PR-D — redirected PR-6**: migrate table `integration_connections` →
   `integrations` (+ `check_connections` → `check_channels` stays as the binding
   table; FK column `connection_uid` → `integration_uid`), drop the `bun:"table:
   integration_connections"` tag. **Decision point:** if the table migration is
   judged not worth the risk, *cancel this PR* — `integration_connections` is
   already a neutral, correct name and the model/table mismatch is benign.
5. **PR-E — redirected PR-5**: drop the deprecated `/connections` paths; make
   `/integrations` canonical with `/channels` as the one-cycle alias.

## Verification

1. `make build && make lint && make test` after every PR.
2. **Bug-fix test (PR-A):** binding a `freebox` integration to a check as a
   notify target returns `400 VALIDATION_ERROR`; the check notify picker in the
   UI does not list Freebox.
3. **Data-driven sender test:** `notifications.GetSender(freebox)` returns
   `(nil,false)` purely via `CanNotify=false` — no type-specific branch remains.
4. **Source still works:** `freebox_line` check creation and the ICMP "Discover
   from Freebox" picker still find the paired Freebox (now via `CanSource`).
5. e2e (`make test-dash`): the renamed `/orgs/$org/integrations` route loads,
   the grouped picker shows Notification channels vs Data sources, and an
   existing Slack integration is still bindable to a check.

## Implementation Plan

PR-A is independently valuable and low-churn — land it first regardless of
whether the rename PRs (B–E) get scheduled. B–E are mechanical and should ride
together with the already-deferred PR-5/PR-6 of `2026-05-07-03` so the table and
route names are touched exactly once more, then frozen.
