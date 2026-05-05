# Incident acknowledgment, snooze, and manual resolve

## Context

The incident model already carries `AcknowledgedAt` and `AcknowledgedBy` columns
(`server/internal/db/models/incident.go`), and a working `AcknowledgeIncident`
service method exists at `server/internal/handlers/incidents/service.go:1291`.
What's missing is everything *around* it:

- No HTTP route exposes acknowledgment to the dashboard. The incidents handler
  (`server/internal/handlers/incidents/handler.go`) only registers `ListIncidents`
  and `GetIncident` — confirmed at `server/internal/app/server.go:443-448`.
- There is no concept of *snooze* (silence-until-time). Today it's binary: open
  or resolved.
- There is no manual-resolve endpoint. Resolution is purely automatic, driven by
  the adaptive resolution logic. Operators cannot close an incident they know
  is fixed.
- Acknowledgment from outside the dashboard works only through the Slack
  interactive button (`AcknowledgeIncidentFromSlack`). Email recipients can't
  ack; mobile users without dashboard access can't ack.
- Pending notification jobs (queued per
  `service.go:920 queueNotifications`) keep firing on ongoing incidents even
  after a human has ack'd. Once escalation policies land
  (`2026-05-02-19-escalation-policies.md`), this becomes critical — but even today
  it produces redundant pings.

This spec is the foundation for the on-call/escalation work. Ship it first
because it's small, leverages what's already there, and unblocks the rest.

## Scope

In scope:
- HTTP endpoints for ack / unack / snooze / unsnooze / resolve.
- New `snoozed_until` column on incidents + auto-unsnooze handling.
- Magic-link ack via signed URL (so email recipients can ack without auth).
- Cancel pending notification jobs and (future) escalation steps on ack/resolve.
- Wire ack/snooze/resolve into the existing event timeline so it appears in
  incident history.
- Frontend buttons + state badges on the incident detail and incident list
  pages.

Out of scope (own specs):
- On-call schedules (`2026-05-02-17-on-call-schedules.md`).
- Escalation policies (`2026-05-02-19-escalation-policies.md`). This spec adds
  *hooks* the escalation worker will use — but no escalation logic itself.
- Mobile push notifications and "ack from your phone" — when those land, they
  call the same endpoints described here.

## Data model

### Incident (additions)

Add to `server/internal/db/models/incident.go`:

```go
SnoozedUntil   *time.Time `bun:"snoozed_until"`
SnoozedBy      *string    `bun:"snoozed_by"`         // user UID
SnoozeReason   *string    `bun:"snooze_reason"`
ResolvedBy     *string    `bun:"resolved_by"`        // user UID, nil for auto-resolve
ResolutionType *string    `bun:"resolution_type"`    // "auto" | "manual" | "expired"
```

`AcknowledgedBy` already exists but is currently a free-form string (the Slack
path stores a Slack user id). Keep it as `*string`; for the web path it stores
the User UID. Disambiguate via the existing event payload (`via: "web" | "slack"
| "email"`).

`SnoozedUntil` is the *single source of truth* for snooze state. A scheduled
sweeper job (existing job framework) wakes incidents whose `snoozed_until <
NOW()` and emits an `incident.unsnoozed` event. Don't introduce a separate
`is_snoozed` boolean — derive it.

### Migration

`server/internal/migrations/` — add a migration adding the five columns above.
Match the style of recent migrations (search the dir for the highest existing
migration number first, then add the next one). All nullable, no defaults.

## API

All routes scoped to `/api/v1/orgs/$org/incidents/$uid`.

### POST `/ack`

Body:

```json
{ "note": "investigating" }    // optional
```

Behavior:
- Calls `Service.AcknowledgeIncident` with `Via: "web"` and the
  authenticated user's UID as `AcknowledgedBy`.
- Idempotent: if already acked, returns 200 with the existing incident
  (matches the current service behavior at `service.go:1304`).
- Returns the updated incident.
- Emits the existing `EventTypeIncidentAcknowledged` event with
  `payload.note` and `payload.via = "web"`.
- **Cancels pending notification jobs** for this incident (see "Cancellation"
  below).

### POST `/unack`

Clears `AcknowledgedAt` / `AcknowledgedBy`. Use case: ack'd by mistake, want
escalation to resume. Emits new event type `incident.unacknowledged`.

### POST `/snooze`

Body:

```json
{ "until": "2026-05-02T18:00:00Z", "reason": "deploying fix" }
```

Or duration form (frontend convenience):

```json
{ "duration": "1h", "reason": "deploying fix" }
```

Validation: `until` must be in the future and within 7 days. Longer
than 7 days → 400 `VALIDATION_ERROR`. (Rationale: a week-long snooze is almost
certainly a mistake; if the incident is *that* expected, resolve it manually
or open a maintenance window.) Duration is parsed via `time.ParseDuration`
and converted to `until = now + duration`.

Sets `SnoozedUntil`, `SnoozedBy`, `SnoozeReason`. Emits
`incident.snoozed` event with payload `{until, reason, via}`. Cancels pending
notification jobs whose scheduled time is before `until`. Pending jobs after
`until` are *kept* — they'll fire if the incident is still open then.

Snoozing implies acknowledgment. If `AcknowledgedAt` is nil, set it too in the
same update. (You can ack without snoozing, but you can't snooze without
acking — silencing an unack'd incident is the worst-of-both-worlds state.)

### POST `/unsnooze`

Clears `SnoozedUntil`, `SnoozedBy`, `SnoozeReason`. Emits `incident.unsnoozed`
event with `payload.via = "manual" | "auto"` (the auto sweeper uses
`"auto"`).

### POST `/resolve`

Body:

```json
{ "note": "fixed by deploy abc123" }   // optional
```

Sets `ResolvedAt = now`, `ResolvedBy = user UID`, `ResolutionType = "manual"`,
`State = StateResolved`. Cancels all pending notification jobs for this
incident. Emits the existing `EventTypeIncidentResolved` event with `payload =
{via: "web", note, resolution_type: "manual"}`.

Idempotent: if already resolved, return 200 with current incident.

If a result rolls in *after* a manual resolve and is still failing, the
existing incident-creation logic will create a new incident (the previous one
stays resolved). That's the correct behavior — do not "reopen" the manual
resolution.

### GET `/ack` (magic link)

Token-based ack from email. The email notification body includes a URL of the
form:

```
${SP_BASE_URL}/api/v1/orgs/${org}/incidents/${uid}/ack?token=${signed-token}
```

The token is HMAC-signed with `SP_AUTH_JWT_SECRET` (already used for JWTs)
and contains: `{incident_uid, recipient_email, exp: incident.created_at + 7d}`.
Verifying the token both authenticates the request *and* identifies the
acknowledger.

Response: a tiny HTML page (`text/html`) saying "Incident acknowledged. You can
close this tab." Reason for HTML over JSON: the link is opened from a mail
client, not a fetch call — show something human-readable.

Token reuse is fine (idempotent). Token expiry returns a 410 Gone HTML page
with a link to the dashboard.

The `AcknowledgedBy` for magic-link acks is the recipient's User UID *if* the
email matches a known user, else nil with `payload.acknowledged_by_email =
"<email>"` so the audit trail is complete.

## Cancellation

When an incident transitions to ack'd / snoozed / resolved, pending background
notifications and (future) escalation steps must stop firing.

There's no current API for "cancel pending jobs by incident UID" because the
job system is generic. Cleanest approach without rewriting the job model:

1. Add a column to the existing job table: `cancel_check_incident_uid TEXT
   NULL` (or repurpose an existing tag/metadata column if one exists — check
   `server/internal/db/models/job.go` first).
2. When a notification job is queued from
   `incidents/service.go:queueNotifications`, set this column to
   `incident.UID`.
3. On ack/snooze/resolve, run an UPDATE setting `state = 'cancelled'` (or
   whatever "skip" state exists) and `cancelled_at = now()` for all rows
   matching `cancel_check_incident_uid = ? AND state IN ('pending',
   'scheduled')`. For snooze, add `AND scheduled_at < snoozed_until`.
4. The job worker honors the cancelled state — it should already, but verify.

If `models/job.go` already has a way to express "this job belongs to incident
X" (e.g., a JSONB `metadata` column), use that with a JSONB index. *Do not*
fabricate a new table for this — keep it inside the existing job system.

This is also where the escalation-policies spec will plug in: same column,
different `incident.uid` query, but the cancellation pathway is identical.

## Frontend (dash0)

### Incident detail page

`web/dash0/src/routes/orgs/$org/incidents.$incidentUid.index.tsx` (or wherever
it lives; if it doesn't exist yet, this spec adds it). Header card shows:

- State badge: `Open` / `Acknowledged by <name> at <time>` /
  `Snoozed until <time>` / `Resolved (manually|automatically)` — pick the
  most-derived state.
- Action row, conditional on state:
  - `Open` → buttons: `Acknowledge`, `Snooze…`, `Resolve…`.
  - `Acknowledged` → buttons: `Unacknowledge`, `Snooze…`, `Resolve…`.
  - `Snoozed` → buttons: `Wake up`, `Resolve…`.
  - `Resolved` → no actions, just a re-open hint if a future failure arrives.

### Incident list page

Add a state column with the same badges. Add a `?state=acked,snoozed` filter
chip. Wire to the existing `state` query param if it exists, or extend
`parseListIncidentsOptions` (`handler.go:74`) to accept the new logical states
(`acked` and `snoozed` are derived from columns, not a new state machine value).

### Snooze dialog

Quick-select buttons: 15m / 1h / 4h / Until tomorrow 9am. Plus a free-form
duration input. Optional reason field. On submit, call POST `/snooze` with
duration form. Re-fetch the incident.

### i18n

All new strings under the `incidents` namespace. Keys:
`incidents.actions.acknowledge`, `incidents.actions.snooze`,
`incidents.actions.resolve`, `incidents.state.acknowledged`,
`incidents.state.snoozedUntil`, etc. Add to both `en` and the other supported
locales — match the convention already used in the dashboard (search `i18n`
for the pattern).

## Notifications: ack callbacks

For each notification channel that already supports interactive ack
(currently Slack), no changes. For email, add an "Acknowledge" button (HTML
button styled link) pointing to the magic-link URL. For Discord/Mattermost/
Google Chat/Pushover/Ntfy/Opsgenie/webhooks, add the magic-link URL to the
message body as plain text — no interactive buttons in this spec. (Discord
interactive buttons can come later; they require an OAuth-installed bot, more
than this spec wants to cover.)

## Verification

Manual:
1. `make build && ./solidping migrate && make dev-test`.
2. Trigger a check failure to open an incident.
3. UI: click `Acknowledge`. State badge updates. The pending email notification
   that would have fired in the next minute is suppressed (check
   `select * from jobs where state = 'cancelled'`).
4. UI: click `Snooze`, pick `1h`. Verify `snoozed_until` set to ~1h from now.
5. Wait through the auto-unsnooze sweeper (or manually `UPDATE incidents SET
   snoozed_until = now()`). Confirm `incident.unsnoozed` event appears in the
   timeline.
6. Click `Resolve`. Incident closes; subsequent failing results should open a
   *new* incident, not reopen this one.
7. Trigger a failure with email notifications enabled. Confirm the email
   contains a magic-link. Click it from a fresh browser (no session). The
   incident is acknowledged; the audit trail records the email recipient.
8. Click the same magic-link a second time. No error — idempotent.
9. Tamper with the token (flip a character). Get 410 Gone with a
   dashboard link.

Automated tests (`server/internal/handlers/incidents/handler_test.go`,
`service_test.go`):
- Ack idempotency, unack-then-reack flow.
- Snooze with `until` in past → 400. Snooze >7d → 400.
- Snooze sets ack if not already set.
- Resolve idempotency.
- Magic-link token verification: valid, expired, tampered, wrong incident.
- Job cancellation: queue 3 future notification jobs, ack the incident, verify
  all 3 are cancelled.

## Risks / unknowns to flag before coding

- **Job table schema**: Verify `models/job.go` has a usable hook for
  per-incident cancellation. If the only available mechanism is a generic
  `metadata jsonb`, that's fine but jsonb queries need indexing — add a
  GIN index on the relevant key.
- **Magic-link security**: The token must be revocable. If a user reports their
  email account was compromised and someone ack'd incidents, the operator
  needs a way to invalidate outstanding tokens. Acceptable v1: rotating
  `SP_AUTH_JWT_SECRET` invalidates everything. Note in the spec but don't
  build a per-token revocation list yet.
- **Adaptive resolution interaction**: `service.go` line 36 references
  `keyEscalationThreshold` — this is the *adaptive incident resolution*
  escalation, distinct from the on-call escalation policies in the sibling
  spec. They share the word "escalate" and that *will* confuse readers later.
  When the on-call work lands, rename one of them (probably the on-call
  side: "policy steps" rather than "escalation steps"). Out of scope for
  this spec, but worth flagging now.
- **Slack ack already works**: Don't refactor `AcknowledgeIncidentFromSlack`
  to share code with the new web path beyond what's already shared. The Slack
  call site is stable; touching it adds risk.
