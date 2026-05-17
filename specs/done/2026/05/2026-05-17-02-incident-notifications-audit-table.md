# Incident notifications audit table

## Context

Today it is impossible to answer "was user Y paged for incident X?" from the database.
Notifications fan out through two independent paths:

1. **`check_connections` fan-out** — when an incident opens/resolves, every channel
   attached to the check is queued as a `notification` job
   ([server/internal/handlers/incidents/service.go:1148](server/internal/handlers/incidents/service.go)
   `enqueueNotificationJob`). The job is soft-deleted after `sender.Send` returns and the
   `SendResult` is discarded.

2. **Escalation policy cycle** — delayed `escalation_step` jobs fan out to
   `user`, `schedule`, `all_admins`, or `connection` targets
   ([server/internal/jobs/jobtypes/job_escalation_step.go](server/internal/jobs/jobtypes/job_escalation_step.go)).
   Direct-email sends (user/schedule/all_admins paths) also discard `SendResult`. The
   only survivor is an `events` row with aggregate counts (`target_count`,
   `notification_jobs`, `direct_emails`, `skipped`) — no per-recipient detail.

The `jobs` table is a transient outbox. The `events` table is an aggregate audit log.
Neither can reconstruct the per-recipient delivery timeline for a given incident.

This spec adds a permanent `incident_notifications` table that captures one row per
dispatch target per event, with full lifecycle tracking (`pending → sent | failed |
cancelled | skipped`). It does **not** add any read API or UI — those are in
[2026-05-17-03-incident-notifications-read-api-and-ui.md](2026-05-17-03-incident-notifications-read-api-and-ui.md).

## Goal

- Every notification dispatch — whether via a queued notification job or a direct
  escalation email — produces a queryable row attributable to an incident, a user or
  channel, and a final delivery status.
- Operators can answer "who got paged for incident X, and did it succeed?" via SQL or,
  later, the read API.
- The writepath never blocks incident dispatch on audit failure — audit writes are
  best-effort.

## Schema

New table `incident_notifications`. Next migration number: **023** for both dialects.

### `server/internal/db/postgres/migrations/023_incident_notifications.up.sql`

```sql
CREATE TABLE incident_notifications (
    uid              uuid        PRIMARY KEY,
    organization_uid uuid        NOT NULL REFERENCES organizations(uid)              ON DELETE CASCADE,
    incident_uid     uuid        NOT NULL REFERENCES incidents(uid)                  ON DELETE CASCADE,
    event_type       text        NOT NULL,
    step_uid         uuid        NULL     REFERENCES escalation_policy_steps(uid)    ON DELETE SET NULL,
    repeat_index     int         NULL,
    source           text        NOT NULL,
    user_uid         uuid        NULL     REFERENCES users(uid)                       ON DELETE SET NULL,
    connection_uid   uuid        NULL     REFERENCES integration_connections(uid)     ON DELETE SET NULL,
    channel_type     text        NOT NULL,
    status           text        NOT NULL,
    skip_reason      text        NULL,
    error            text        NULL,
    job_uid          uuid        NULL,
    message_id       text        NULL,
    created_at       timestamptz NOT NULL DEFAULT now(),
    sent_at          timestamptz NULL,
    cancelled_at     timestamptz NULL,
    failed_at        timestamptz NULL
);

CREATE INDEX idx_in_incident ON incident_notifications (incident_uid, created_at DESC);
CREATE INDEX idx_in_user     ON incident_notifications (user_uid,     created_at DESC) WHERE user_uid IS NOT NULL;
CREATE INDEX idx_in_org_time ON incident_notifications (organization_uid, created_at DESC);
CREATE INDEX idx_in_job      ON incident_notifications (job_uid) WHERE job_uid IS NOT NULL;
```

### `server/internal/db/postgres/migrations/023_incident_notifications.down.sql`

```sql
DROP TABLE IF EXISTS incident_notifications;
```

### `server/internal/db/sqlite/migrations/023_incident_notifications.up.sql`

Mirror the Postgres schema with SQLite dialect rules (see
`007_escalation_policies.up.sql` for the canonical set):

```sql
CREATE TABLE incident_notifications (
    uid              text        PRIMARY KEY,
    organization_uid text        NOT NULL REFERENCES organizations(uid)              ON DELETE CASCADE,
    incident_uid     text        NOT NULL REFERENCES incidents(uid)                  ON DELETE CASCADE,
    event_type       text        NOT NULL,
    step_uid         text        NULL     REFERENCES escalation_policy_steps(uid)    ON DELETE SET NULL,
    repeat_index     integer     NULL,
    source           text        NOT NULL,
    user_uid         text        NULL     REFERENCES users(uid)                       ON DELETE SET NULL,
    connection_uid   text        NULL     REFERENCES integration_connections(uid)     ON DELETE SET NULL,
    channel_type     text        NOT NULL,
    status           text        NOT NULL,
    skip_reason      text        NULL,
    error            text        NULL,
    job_uid          text        NULL,
    message_id       text        NULL,
    created_at       text        NOT NULL DEFAULT (datetime('now')),
    sent_at          text        NULL,
    cancelled_at     text        NULL,
    failed_at        text        NULL
);

CREATE INDEX idx_in_incident ON incident_notifications (incident_uid, created_at DESC);
CREATE INDEX idx_in_user     ON incident_notifications (user_uid,     created_at DESC) WHERE user_uid IS NOT NULL;
CREATE INDEX idx_in_org_time ON incident_notifications (organization_uid, created_at DESC);
CREATE INDEX idx_in_job      ON incident_notifications (job_uid) WHERE job_uid IS NOT NULL;
```

### `server/internal/db/sqlite/migrations/023_incident_notifications.down.sql`

```sql
DROP TABLE IF EXISTS incident_notifications;
```

### Column semantics

| Column | Notes |
|---|---|
| `event_type` | Mirrors the `events` table event types (`incident.created`, `incident.escalated`, `incident.resolved`, …). Allows the API to filter by trigger event. |
| `step_uid` / `repeat_index` | Set for escalation-path rows; NULL for `check_connection` fan-out rows. |
| `source` | `check_connection` \| `escalation_user` \| `escalation_schedule` \| `escalation_all_admins` \| `escalation_connection` |
| `user_uid` | Set for direct-email escalation targets; NULL for channel rows. ON DELETE SET NULL so audit history survives user removal (uid becomes NULL, rest of row stays). |
| `connection_uid` | Set for channel rows (`check_connection`, `escalation_connection`). NULL for direct-email rows. |
| `channel_type` | `email` for direct-email rows; mirrors `integration_connections.connection_type` for channel rows. `none` only for `skipped` rows with neither uid set. |
| `status` | `pending` \| `sent` \| `failed` \| `cancelled` \| `skipped` |
| `skip_reason` | Non-NULL only when `status='skipped'`: `empty_schedule`, `no_admins`, `schedule_resolve_failed`. |
| `job_uid` | Back-link to the `jobs` row. NULL for direct-email rows (no job created). Allows `NotificationJobRun.Run` to update the correct audit row when the job executes. |
| `message_id` | RFC 5322 `Message-ID` from `email.SendResult.MessageID`. NULL for non-email channels. |

## Bun model

New file `server/internal/db/models/incident_notification.go`. Follow the
`escalation_policy.go` template (struct tags, nullable pointer fields, typed constant
blocks, `New*` constructors).

```go
// Status constants
const (
    IncidentNotificationStatusPending   = "pending"
    IncidentNotificationStatusSent      = "sent"
    IncidentNotificationStatusFailed    = "failed"
    IncidentNotificationStatusCancelled = "cancelled"
    IncidentNotificationStatusSkipped   = "skipped"
)

// Source constants
const (
    IncidentNotificationSourceCheckConnection      = "check_connection"
    IncidentNotificationSourceEscalationUser       = "escalation_user"
    IncidentNotificationSourceEscalationSchedule   = "escalation_schedule"
    IncidentNotificationSourceEscalationAllAdmins  = "escalation_all_admins"
    IncidentNotificationSourceEscalationConnection = "escalation_connection"
)

type IncidentNotification struct {
    bun.BaseModel `bun:"table:incident_notifications"`

    UID             string     `bun:"uid,pk,type:varchar(36)"`
    OrganizationUID string     `bun:"organization_uid,notnull,type:varchar(36)"`
    IncidentUID     string     `bun:"incident_uid,notnull,type:varchar(36)"`
    EventType       string     `bun:"event_type,notnull"`
    StepUID         *string    `bun:"step_uid,type:varchar(36)"`
    RepeatIndex     *int       `bun:"repeat_index"`
    Source          string     `bun:"source,notnull"`
    UserUID         *string    `bun:"user_uid,type:varchar(36)"`
    ConnectionUID   *string    `bun:"connection_uid,type:varchar(36)"`
    ChannelType     string     `bun:"channel_type,notnull"`
    Status          string     `bun:"status,notnull"`
    SkipReason      *string    `bun:"skip_reason"`
    Error           *string    `bun:"error"`
    JobUID          *string    `bun:"job_uid,type:varchar(36)"`
    MessageID       *string    `bun:"message_id"`
    CreatedAt       time.Time  `bun:"created_at,notnull,default:current_timestamp"`
    SentAt          *time.Time `bun:"sent_at"`
    CancelledAt     *time.Time `bun:"cancelled_at"`
    FailedAt        *time.Time `bun:"failed_at"`
}
```

Three `New*` constructors to reduce callsite boilerplate:

- `NewIncidentNotificationForJob(orgUID, incidentUID, eventType string, source string, connectionUID, jobUID, channelType string, stepUID *string, repeatIndex *int) *IncidentNotification`
- `NewIncidentNotificationForUser(orgUID, incidentUID, eventType, source, userUID, channelType string, stepUID *string, repeatIndex *int) *IncidentNotification`
- `NewSkippedIncidentNotification(orgUID, incidentUID, eventType, source, skipReason string, stepUID *string, repeatIndex *int) *IncidentNotification`

Each calls `uuid.New().String()` for `UID`, sets `Status = IncidentNotificationStatusPending`
(or `Skipped` for the third), `CreatedAt = time.Now()`.

## Repo layer

### Interface (`server/internal/db/service.go`)

Add a new section after the events CRUD block:

```go
// --- IncidentNotifications ---
CreateIncidentNotification(ctx context.Context, n *models.IncidentNotification) error
MarkIncidentNotificationSentByUID(ctx context.Context, uid string, sentAt time.Time, messageID string) error
MarkIncidentNotificationFailedByUID(ctx context.Context, uid string, failedAt time.Time, errMsg string) error
MarkIncidentNotificationSentByJob(ctx context.Context, jobUID string, sentAt time.Time, messageID string) error
MarkIncidentNotificationFailedByJob(ctx context.Context, jobUID string, failedAt time.Time, errMsg string, retryable bool) error
CancelIncidentNotificationsForIncident(ctx context.Context, incidentUID string, cancelledAt time.Time) (int64, error)
```

The `ByUID` variants are used by the direct-email paths (no `job_uid`). The `ByJob`
variants are used by `NotificationJobRun.Run` when matching via `job_uid`. The `retryable`
param on `MarkFailed` keeps the row at `pending` (so a retry can update it) instead of
flipping to `failed`.

### Implementations

New sibling files following the `escalation.go` pattern:

- `server/internal/db/postgres/incident_notification.go`
- `server/internal/db/sqlite/incident_notification.go`

Byte-for-byte identical except the `package` line; Bun's dialect-agnostic query builder
handles everything. Key queries:

```go
// CreateIncidentNotification
s.db.NewInsert().Model(n).Exec(ctx)

// MarkSentByJob
s.db.NewUpdate().
    TableExpr("incident_notifications").
    Set("status = ?", models.IncidentNotificationStatusSent).
    Set("sent_at = ?", sentAt).
    Set("message_id = ?", messageID).
    Where("job_uid = ? AND status = ?", jobUID, models.IncidentNotificationStatusPending).
    Exec(ctx)

// CancelForIncident
s.db.NewUpdate().
    TableExpr("incident_notifications").
    Set("status = ?", models.IncidentNotificationStatusCancelled).
    Set("cancelled_at = ?", cancelledAt).
    Where("incident_uid = ? AND status = ?", incidentUID, models.IncidentNotificationStatusPending).
    Exec(ctx)
```

`CancelForIncident` filters on `status = 'pending'` specifically to avoid clobbering a
row that completed between the cancellation trigger and the sweep.

## Write-path instrumentation

Six call sites gain audit writes. All audit errors are warn-logged and swallowed — they
must never propagate up to the incident state machine.

### Site 1 — `enqueueNotificationJob` (check_connection fan-out)

**File:** `server/internal/handlers/incidents/service.go` around line 1148.

After the successful `s.jobsSvc.CreateJob(...)` call, insert a `pending` audit row:

```go
if err := s.db.CreateIncidentNotification(ctx, models.NewIncidentNotificationForJob(
    orgUID, incidentUID, string(eventType),
    models.IncidentNotificationSourceCheckConnection,
    connectionUID, job.UID, connection.Type,
    nil, nil, // no step, no repeat index
)); err != nil {
    log.WarnContext(ctx, "failed to create notification audit row", "error", err)
}
```

The `enqueueNotificationJob` function needs access to `connection.Type` — it currently
receives `connectionUID` only. Either pass `channelType string` as an additional param, or
load the connection inline (prefer the param to avoid an extra DB round-trip; callers
already have the connection).

### Site 2 — `enqueueNotificationFor` (escalation_connection fan-out)

**File:** `server/internal/jobs/jobtypes/job_escalation_step.go` around line 283.

Same pattern as Site 1, but with `source = IncidentNotificationSourceEscalationConnection`,
`stepUID = &r.config.StepUID`, `repeatIndex = &r.config.RepeatIndex`. The connection type
is available from the loaded connection object.

### Site 3 — `sendEscalationEmail` (direct-email path)

**File:** `server/internal/jobs/jobtypes/job_escalation_step.go` around line 396.

`sendEscalationEmail` is a shared helper called by `pageUser`, `pageSchedule`, and
`pageAllAdmins`. Add two parameters: `userUID string` and `source string`. Callers that
currently pass only `recipient` string now also pass the user's UID (which they already
have) and the source constant.

Instrumented body:

```go
n := models.NewIncidentNotificationForUser(
    r.config.OrgUID, r.config.IncidentUID, string(EventTypeIncidentEscalated),
    source, userUID, "email", &r.config.StepUID, &r.config.RepeatIndex,
)
if err := jctx.Services.DB.CreateIncidentNotification(ctx, n); err != nil {
    log.WarnContext(ctx, "failed to create notification audit row", "error", err)
}

result, err := jctx.Services.EmailSender.Send(ctx, msg)
if err != nil {
    log.WarnContext(ctx, "failed to send escalation email", "recipient", recipient, "error", err)
    _ = jctx.Services.DB.MarkIncidentNotificationFailedByUID(ctx, n.UID, time.Now(), err.Error())
    return 0
}
_ = jctx.Services.DB.MarkIncidentNotificationSentByUID(ctx, n.UID, time.Now(), result.MessageID)
return 1
```

Note: `EmailSender.Send` currently returns `(*SendResult, error)` but the result is
discarded with `_` at all call sites. This is the only site that starts capturing it;
the function signature is unchanged.

### Site 4 — `pageSchedule` pre-flight failure (skipped path)

**File:** `server/internal/jobs/jobtypes/job_escalation_step.go` around line 339–362.

When `resolveOnCallUser` returns an error or nil user (empty schedule), an `emitEscalationFailed`
event is already written. Also insert a `skipped` audit row:

```go
n := models.NewSkippedIncidentNotification(
    r.config.OrgUID, r.config.IncidentUID, string(EventTypeIncidentEscalated),
    models.IncidentNotificationSourceEscalationSchedule, "schedule_resolve_failed",
    &r.config.StepUID, &r.config.RepeatIndex,
)
n.Status = models.IncidentNotificationStatusSkipped
if err := jctx.Services.DB.CreateIncidentNotification(ctx, n); err != nil {
    log.WarnContext(ctx, "failed to create skipped notification audit row", "error", err)
}
```

Apply the same pattern if `pageAllAdmins` finds zero admin members (i.e., `count == 0`
at the end of the loop), with `source = SourceEscalationAllAdmins` and
`skip_reason = "no_admins"`.

### Site 5 — `NotificationJobRun.Run` (channel delivery)

**File:** `server/internal/jobs/jobtypes/job_notification.go` around line 154.

After `sender.Send` returns, update the audit row by `job_uid`. The row was created at
Sites 1 or 2, so it must exist — but handle the missing-row case gracefully (pre-deploy
incidents or audit-write failure):

```go
if err := sender.Send(ctx, jctx, payload); err != nil {
    if notifications.IsNetworkError(err) {
        // Retryable: leave the audit row at 'pending' so the retry updates it.
        return jobdef.NewRetryableError(err)
    }
    _ = jctx.Services.DB.MarkIncidentNotificationFailedByJob(
        ctx, r.config.JobUID, time.Now(), err.Error(), false,
    )
    return err
}
_ = jctx.Services.DB.MarkIncidentNotificationSentByJob(
    ctx, r.config.JobUID, time.Now(), "", // non-email senders have no message_id
)
```

`r.config` needs a `JobUID` field. Add it to `NotificationJobConfig`
(`server/internal/jobs/jobtypes/job_notification.go:37`) and populate it in Sites 1+2
when inserting the job. The job config already flows to `Run`, so no extra plumbing
beyond the new field.

### Site 6 — `CancelPendingForIncident` (cancellation sweep)

**File:** `server/internal/jobs/jobsvc/service.go` around line 285.

After the existing `UPDATE jobs SET deleted_at = now() WHERE ...` bulk cancel, add:

```go
cancelled, err := s.db.CancelIncidentNotificationsForIncident(ctx, incidentUID, time.Now())
if err != nil {
    log.WarnContext(ctx, "failed to cancel notification audit rows", "incident_uid", incidentUID, "error", err)
} else {
    log.DebugContext(ctx, "cancelled notification audit rows", "incident_uid", incidentUID, "count", cancelled)
}
```

The `CancelPendingForIncident` function needs access to `db.Service`. Check whether
`jobsvc.Service` already holds a `db.Service` reference; if not, inject it via
`NewJobsvc(db db.Service, ...)`.

## Service wiring

`server/internal/app/server.go`: any place `jobsvc.NewService(...)` is constructed, pass
the `db.Service` if not already there. Same for `jobtypes.SetXxx` registrations — the
services already injected into `JobContext` (`jctx.Services`) include `DB db.Service`, so
Sites 2–5 in `jobtypes` have access without extra wiring.

## Consistency and error handling

The two-write sequence (CreateJob → CreateNotification) is **not** transactional. If the
audit INSERT fails:

- The job still runs normally — the notification is sent.
- Site 5 (`MarkByJob`) will find no row and warn-log, but will not fail the job.
- This is acceptable: degraded audit telemetry does not block paging.

The reverse failure (CreateNotification succeeds, CreateJob fails) leaves an orphaned
`pending` row with a `job_uid` that never fires. A future retention sweeper can cull
`pending` rows older than the maximum job retry window.

The cancellation sweep (`CancelForIncident`) filters `status = 'pending'` so a completed
`sent`/`failed` row is never overwritten — even if delivery and cancellation race.

## Integration tests

New file `server/test/integration/incident_notifications_audit_test.go`. Mirror
`escalation_policies_test.go` structure.

Test cases:

1. **Check-connection send**: open an incident for a check with one `connection`
   attachment; assert one `incident_notifications` row appears with `source='check_connection'`,
   `connection_uid` set, `user_uid` NULL, `status='sent'`.

2. **Escalation user send**: configure an escalation policy with a `user` target; open an
   incident and advance the job runner; assert `status='sent'`, `user_uid` set,
   `channel_type='email'`, `message_id` non-empty.

3. **Cancellation**: open an incident, ack it before the escalation step job fires; assert
   all `pending` rows flip to `status='cancelled'`.

4. **Empty schedule skipped**: configure an escalation policy with a `schedule` target
   that has no users; assert `status='skipped'`, `skip_reason='empty_schedule'`.

5. **Delivery failure**: mock `EmailSender.Send` to return an error (non-retryable); assert
   `status='failed'`, `error` column non-empty.

## Out of scope

- REST list endpoints and UI — covered by
  [2026-05-17-03-incident-notifications-read-api-and-ui.md](2026-05-17-03-incident-notifications-read-api-and-ui.md).
- Backfill for pre-deploy incidents — `events` aggregate counts are insufficient to
  reconstruct per-recipient rows; the table simply starts empty.
- Retention sweeper — deferred until volume warrants.
- Per-recipient fidelity inside channel senders (knowing which Slack user received a
  channel message) — V1 limitation; one row per connection.

## Verification

1. `make build` — confirms compilation with new model + interface methods.
2. `make lint` — no new linting errors.
3. `make test` — new integration test file passes, existing tests unaffected.
4. `make migrate` in dev, then open an incident manually (`make dev-test`);
   check rows:
   ```sql
   SELECT status, source, channel_type, user_uid, connection_uid
   FROM incident_notifications
   ORDER BY created_at DESC;
   ```
5. Ack the incident; confirm `pending` rows become `cancelled`.
6. Verify no regression in `make test` for existing incident lifecycle tests
   (`server/test/integration/incidents_test.go`).

## Implementation Plan

Already in the spec — see above section. No additional notes needed.
