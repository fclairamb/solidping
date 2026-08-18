---
model: sonnet
effort: high
---

# Add PagerDuty (Events API v2) and remove the sunsetting Opsgenie integration

## Problem

SolidPing ships an Opsgenie integration but no PagerDuty one — backwards
relative to the market:

- Atlassian is sunsetting Opsgenie (no new sales since June 2025, support ends
  April 2027). Teams are actively migrating off it, largely to PagerDuty.
- PagerDuty is the dominant incident-management platform and a procurement
  checkbox: BetterStack, UptimeRobot, Pingdom, StatusCake, Checkly,
  Healthchecks.io, Uptime Kuma and Gatus all support it (see
  `specs/ideas/2026-03-22-notification-channels.md`, which already sketched
  this channel).
- Keeping Opsgenie means maintaining a channel for a product that will stop
  existing, while its replacement audience has no native option.

So: add PagerDuty, remove Opsgenie in the same release.

## Proposal

### 1. Add the `pagerduty` notification channel

Model the sender on the Opsgenie one — same lifecycle, different API.

**Backend**

- New connection type `ConnectionTypePagerduty ConnectionType = "pagerduty"`
  in [integration.go:23](server/internal/db/models/integration.go) (replacing
  the Opsgenie constant; update the CanNotify doc comment at line 65).
- New sender `server/internal/notifications/pagerduty.go` using **Events API
  v2** only (`POST https://events.pagerduty.com/v2/enqueue`) — no OAuth, no
  REST API v2, no schedule import:
  - Settings: `routingKey` (required — the Events API v2 integration key).
    Register it as a secret field: `models.ConnectionTypePagerduty:
    {"routing_key"}` in
    [conn_secrets.go:38](server/internal/crypto/credentials/conn_secrets.go),
    following the existing opsgenie `api_key` pattern.
  - `dedup_key` = incident UID, so trigger/resolve correlate to one PagerDuty
    incident across the whole lifecycle.
  - Event mapping: `incidentCreated` / `incidentReopened` → `trigger`;
    `incidentResolved` → `resolve`; `incidentEscalated` / `incidentComment` →
    **send nothing**. Events API v2 has no note/annotation concept, and a
    `trigger` with an existing dedup_key re-opens a resolved incident — the
    "a comment must never page anyone" rule in
    [opsgenie.go](server/internal/notifications/opsgenie.go) (Send switch)
    carries over as a deliberate drop.
  - Severity: map SolidPing severities onto PagerDuty's enum
    (`critical` / `error` / `warning` / `info`) and add `"pagerduty": true`
    to the channels-with-severity map in
    [service.go:51](server/internal/handlers/severities/service.go).
  - Set `payload.source` (check name/URL) and `links` back to the dash0
    incident page, mirroring what the Opsgenie sender sends today.
  - Honor Events API v2 responses: 202 = accepted; 429/5xx are retryable —
    follow whatever retry semantics the sender error contract already gives
    the job runner (see other senders + the shared `httpclient.go`).
- Register the sender in
  [registry.go:95](server/internal/notifications/registry.go) and flip the
  CanNotify entry in
  [service.go:471](server/internal/handlers/integrations/service.go)
  (both currently list Opsgenie).
- Tests mirroring the Opsgenie ones: table-driven sender tests (trigger,
  resolve, dedup_key propagation, severity mapping, comment/escalation
  producing **no** HTTP call, missing routing key error, non-2xx handling)
  plus the registry test entry.

**Frontend (dash0)**

- Form case in
  [integration-form.tsx:502](web/dash0/src/components/integrations/integration-form.tsx)
  — a single secret "Integration key" field (with helper text pointing at
  PagerDuty → Service → Integrations → Events API v2).
- Channel lists/labels/icon:
  [channel-labels.ts:33](web/dash0/src/lib/channel-labels.ts),
  [integrations.new.tsx:53](web/dash0/src/routes/orgs/$org/integrations.new.tsx),
  [integration-icon.tsx](web/dash0/src/components/integrations/integration-icon.tsx).
- Locales: replace the opsgenie keys in `common.json` + `integrations.json`
  for all four locales (`en`, `fr`, `de`, `es`).

**Docs**

- Replace the Opsgenie sections in
  [notifications.md](web/docs/docs/configuration/notifications.md),
  [intro.md](web/docs/docs/intro.md) and
  [incidents.md](web/docs/docs/features/incidents.md) with PagerDuty setup
  instructions (create an Events API v2 integration on a PD service, paste
  the routing key).

### 2. Remove Opsgenie

- Delete `server/internal/notifications/opsgenie.go`, its test coverage, and
  every wiring point found above: the `ConnectionTypeOpsgenie` constant,
  registry entry, conn_secrets entry, severities map entry, CanNotify entry,
  the frontend form case / labels / icon / locale keys, and the docs
  sections. The stale comment in
  [job_notification.go:99](server/internal/jobs/jobtypes/job_notification.go)
  and the fixtures in
  [service_test.go](server/internal/crypto/credentials/service_test.go)
  also reference it.
- **Data migration** (both engines — `server/internal/db/postgres/migrations/`
  and `server/internal/db/sqlite/migrations/`, next numbered pair after
  `014_v0_17_0`): delete integrations rows with
  `connection_type = 'opsgenie'` along with their dependent rows
  (check↔integration links, per-integration severity overrides — follow the
  actual FK graph). Hard delete, not a tombstone: the product is pre-1.0 and
  keeping dead rows would make every type switch in the code carry a zombie
  case forever.
- The removal is a **breaking change**: the commit/PR must carry a
  `BREAKING CHANGE` note so release-please surfaces it in the changelog —
  "Opsgenie integrations are removed on upgrade; recreate them as PagerDuty
  (or webhook) integrations. Atlassian is retiring Opsgenie in April 2027."

### Acceptance

- A PagerDuty integration can be created in dash0, receives a `trigger` on
  incident open and a `resolve` on incident close, with matching `dedup_key`,
  verified by sender unit tests.
- Comments and escalations send nothing to PagerDuty (test proves the
  negative with a positive control on incidentCreated).
- `grep -ri opsgenie` over `server/`, `web/dash0/src/`, `web/docs/docs/`
  returns nothing (built artifacts like `docsres`/`dash0res` regenerate).
- Migrations run on both Postgres and SQLite; an org with a pre-existing
  opsgenie integration upgrades cleanly and the row is gone.

## Implementation Plan

1. **Backend model**: replace `ConnectionTypeOpsgenie` with
   `ConnectionTypePagerduty` in `server/internal/db/models/integration.go`
   (constant + doc comment), update `integration_test.go`.
2. **PagerDuty sender**: new `server/internal/notifications/pagerduty.go`
   modeled on `opsgenie.go`:
   - Settings: `routingKey` (required, secret field `routing_key`).
   - `dedup_key` = incident UID.
   - `incidentCreated`/`incidentReopened` -> `trigger`; `incidentResolved` ->
     `resolve`; `incidentEscalated`/`incidentComment` -> Send returns nil with
     **no HTTP call at all** (stronger than Opsgenie's "note" behavior —
     Events API v2 has no note concept).
   - Severity mapped to PD's `critical`/`error`/`warning`/`info`.
   - `payload.source`, `links` back to the incident page (reuse
     `checkDashURL`/`incidentDashURL` helpers from `slack.go`).
   - POST to `https://events.pagerduty.com/v2/enqueue`; 202 success,
     429/5xx surfaced as retryable via the existing `IsNetworkError`/error
     contract convention other senders use.
3. **Wiring**: register in `notifications/registry.go` senderFactories,
   `crypto/credentials/conn_secrets.go` (`routing_key`), `handlers/severities/service.go`
   allowedChannels (`pagerduty` in, `opsgenie` out), `handlers/integrations/service.go`
   validConnectionTypes (`pagerduty` in, `opsgenie` out). Update
   `registry_test.go`, `service_test.go` (credentials) fixtures. Fix the stale
   opsgenie comment in `jobs/jobtypes/job_notification.go:99`.
4. **Sender tests**: table-driven tests in `pagerduty_test.go` mirroring
   `opsgenie` coverage: trigger, resolve, dedup_key propagation, severity
   mapping, comment/escalation -> **zero HTTP requests** (with a positive
   control on incidentCreated in the same test), missing routing key error,
   non-2xx handling.
5. **Delete Opsgenie**: remove `opsgenie.go` (no separate test file exists
   for it — coverage was via registry/severities tests, updated in step 3).
6. **Frontend (dash0)**: swap the opsgenie form case in
   `integration-form.tsx` for a single secret "Integration key" field
   (`routing_key`) with a helper hint; update `channel-labels.ts`,
   `integrations.new.tsx`, `integration-icon.tsx`, `api/hooks.ts`
   (`ConnectionType` union + `CAPABILITIES` map — hand-written, not
   generated). Replace opsgenie locale keys with pagerduty ones in
   `common.json` + `integrations.json` for en/fr/de/es.
7. **Docs**: replace the Opsgenie sections/table rows in
   `notifications.md`, `intro.md`, `incidents.md` (comment-fanout exclusion
   list: PagerDuty joins Twilio as "sends nothing for a comment", replacing
   the old "Opsgenie adds a note" bullet).
8. **Data migration**: new pair `015_remove_opsgenie_integrations` (both
   engines) — this is a scratch migration for the still-unreleased v0.17.0
   cycle (014 is its consolidated file so far; per
   `wiki/conventions/database.md` "development workflow", additional schema
   work before release is a new numbered scratch file, folded into the
   final `NNN_vX_Y_Z` at actual release/consolidation time). Deletes, in FK
   order: `escalation_policy_targets` rows with `target_type = 'connection'`
   pointing at an opsgenie integration (not DB-enforced — polymorphic
   `target_uid`), then `integrations` rows with `type = 'opsgenie'` (cascades
   `check_channels` and `user_integration_identities`, nulls
   `incident_notifications.connection_uid` — both enforced by existing FKs).
   Down migrations are parity-only best-effort (data already gone, cannot be
   un-deleted — same spirit as other hard-delete migrations in this repo).
9. **BREAKING CHANGE** note on the removal commit for release-please.
10. QA gate: `make build-backend lint-back test`, `make build-dash0` +
    `bun run lint`, `make build-docs`. Final acceptance grep for `opsgenie`
    across `server/`, `web/dash0/src/`, `web/docs/docs/`.
