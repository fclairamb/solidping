---
model: opus
effort: high
---

# An open, unacknowledged incident alerts once, maybe escalates once, then goes silent until recovery

## Problem
A team whose only channel is Slack (or email, Discord, Teams...) gets one `incident.created`, maybe one `incident.escalated`, then nothing until `incident.resolved`. An alert missed at 3 am stays missed for the whole outage.

- `incident.escalated` fires once per incident, when the failure count reaches `escalationThreshold` (`server/internal/handlers/incidents/service.go:1357-1371`, guarded by `incident.EscalatedAt == nil`).
- Only escalation policies repeat (`repeatMax`, `repeatAfterSeconds`; `server/internal/jobs/jobtypes/job_escalation_step.go:1583-1610`). A policy pages people, not the check's own channels, and many orgs have no policy at all.
- Nothing re-sends to the check's channels (`queueNotifications`, `service.go:2107-2124`) while an incident stays open.

The building blocks already exist:
- **Delayed jobs.** `jobsvc.CreateJob` takes `JobOptions.ScheduledAt` (`server/internal/jobs/jobsvc/service.go:46-48`). It dedups a pending job with the same org, type and config by moving its `scheduled_at` instead of inserting a new row (`service.go:283-327`).
- **Cancel sweep.** `CancelPendingForIncident` soft-deletes every pending job whose config carries the incident UID (`jobsvc/service.go:490-550`). `cancelPendingNotifications` (`service.go:3005`) calls it on:
  - ack (`service.go:2922`)
  - unack (`service.go:3574`)
  - manual resolve (`service.go:3879`)
  - check deleted (`handlers/incidents/check_deleted.go:40`)
  - snooze, which only cancels jobs due before `until` (`service.go:3712`)

  Auto-resolve (`resolveIncident`, `service.go:1525`) does not sweep. Escalation steps rely on a fire-time guard, `incidentNeedsPaging` (`job_escalation_step.go:167-187`), which covers ack, resolved, snoozed and paging-suppressed.
- **Unsnooze.** There are two paths: manual `unsnoozeIncidentByOrgUID` (`service.go:3757`) and the sweep job, which writes to the DB directly to avoid an import cycle (`jobs/jobtypes/job_snooze_sweep.go:47-99`).
- **Org default.** A per-check setting that inherits an org default already exists: `TracerouteOnFailure *bool` (nil means inherit, `server/internal/db/models/check.go:405-417`). It is resolved against an org parameter in `service.go:434-449` (`tracerouteEnabled`), exposed in `OrgSettingsResponse` / `UpdateOrgSettingsRequest` (`handlers/auth/service.go:3982`, `:4108`), and shown in the dash0 org settings page.

## Proposal
1. **Schema.** Add to the upcoming release's migration (`server/internal/db/{postgres,sqlite}/migrations/027_v0_39_0.up.sql`, both dialects, with the matching `.down.sql`):
   - `checks.reminder_interval_seconds INTEGER NULL`: nil means inherit the org default, 0 means off.
   - `incidents.reminder_count INTEGER NOT NULL DEFAULT 0`.
   - `incidents.last_reminder_at TIMESTAMP NULL`.

   Add the model fields in `db/models/check.go` (no `default:` tag, see the note at `check.go:300-304`) and `db/models/incident.go:84-92`. Add the org parameter key `incidents.reminder_interval_seconds` in `db/models/parameter.go` (next to `ParamKeyTracerouteEnabled`, `:64`). An absent parameter means 0.
2. **Effective interval.** Add `jobtypes.ReminderInterval(ctx, db, check) time.Duration` in jobtypes, so the incidents service and the jobs share it (incidents already imports jobtypes for `ScheduleEscalationCycle`). The check value wins when non-nil, otherwise the org parameter applies, otherwise 0. Copy the resolution style of `service.go:434-449` (`tracerouteEnabled`).
3. **API and validation.** Add `reminderIntervalSeconds` (`*int`) in `CheckResponse`, `CreateCheckRequest`, `UpdateCheckRequest` and `UpsertCheckRequest` (`handlers/checks/service.go:861`, `:1459`, `:1900`, `:1974`).
   - Allowed values: null (inherit), 0 (off), or 900 to 86400 seconds. Anything else returns a `VALIDATION_ERROR` naming the field, in the same style as `errIncidentPeriodOutOfRange` (`service.go:229-238`).
   - Wire the field through clone, export/import (`handlers/checks/export_v2.go`), diff (`handlers/checks/diff.go`) and the OpenAPI spec (`server/internal/app/openapi/openapi.yaml`).
   - Org settings: add `reminderIntervalSeconds` (int, always present, 0 when unset) to `OrgSettingsResponse` and to `UpdateOrgSettingsRequest` (`*int`), with the same range check.
4. **New job type `incident_reminder`.** Add the constant in `jobs/jobdef/types.go` and register it in `jobs/jobtypes/registry.go:22`. Its config is `{"incidentUid", "seq"}`, where `seq` is the `reminder_count` it expects to bump. Because the config carries `incidentUid`, the existing cancel sweep removes the pending reminder on ack, unack, manual resolve and check deletion with no extra wiring. On run:
   - Load the incident and its check. If the check is gone, stop.
   - If `incidentNeedsPaging` is false (acked, resolved, snoozed, paging-suppressed), stop without rescheduling.
   - If the effective interval is now 0, stop.
   - If the check is paused (`!check.Enabled`) or in an active maintenance window (`maintenance.NewResolver(jctx.DBService, clock).IsActive`, `server/internal/maintenance/resolver.go:57-67`), skip the send but reschedule with the same `seq`. Reminders then resume on their own after the pause or window ends.
   - Claim the reminder with a compare-and-set. Add a new `db.Service.ClaimIncidentReminder(ctx, incidentUID, expectedCount, now) (bool, error)`, implemented for Postgres and SQLite. It runs `UPDATE incidents SET reminder_count = reminder_count + 1, last_reminder_at = ? WHERE uid = ? AND reminder_count = ? AND resolved_at IS NULL AND acknowledged_at IS NULL AND (snoozed_until IS NULL OR snoozed_until <= ?)`. Zero rows affected means another job won, so return without sending.
   - The winner enqueues one notification job per enabled check channel, with event type `incident.reminder`. Reuse the per-channel enqueue and audit-row logic of `enqueueNotificationFor` (`job_escalation_step.go:433-470`). Do not route through `incidents.Service`, which would create an import cycle.
   - The winner then schedules `seq+1` at `now + interval`.
5. **Scheduling helper.** Add `jobtypes.ScheduleIncidentReminder(ctx, jobs, db, incident, check, from)`. It does nothing when the interval is 0 or the incident kind is not `check` (`db/models/incident.go:51`). Otherwise it calls `CreateJob` at `from + interval` with `seq = incident.ReminderCount`. Call it:
   - on `incident.created` and `incident.reopened` in `queueLifecycleNotifications`, next to `scheduleEscalationPolicy` (`service.go:1886-1888`), after the `PagingSuppressed` early return (`:1840-1848`);
   - on unack, after the cancel sweep, next to `resumeEscalationAfterUnack` (`service.go:3585`), with `from = now`;
   - on manual unsnooze (`service.go:3757`) and in the snooze sweep (`unsnooze`, `job_snooze_sweep.go:77-99`), with `from = now`, so the interval restarts from the unsnooze;
   - on snooze, sweep pending `incident_reminder` jobs for the incident regardless of `until`. The existing sweep keeps jobs due after `until`, and the fire-time guard plus the `seq` dedup would handle them anyway, but a clean cancel keeps the jobs list honest.
6. **Event type.** Add `EventTypeIncidentReminder = "incident.reminder"` in `db/models/event.go` (near `:46`), listed in the never-paged group of the exhaustive switch in `queueLifecycleNotifications` (`service.go:1889`). Reminders write no `events` row: the `incident_notifications` audit rows already list each one. Add `ReminderSeq int` with `json:"reminderSeq,omitempty"` to `NotificationJobConfig` (`job_notification.go:39-56`). Each reminder's notification job then has a distinct config, so `CreateJob` cannot fold it into a still-pending earlier one, and senders can print "Reminder 3". Carry it on `Payload` (`notifications/sender.go:16`).
7. **Senders.** Add an `eventTypeIncidentReminder` constant next to the others (`notifications/email.go:97-110`). The text is "Still down after 1 h 15 min, not acknowledged (reminder 3)", with the ack button wherever the created message has one.
   - `slack.go`: new case in `buildMessage` (`:207-224`), posted as a thread reply.
   - `discord.go`: `:456`, `:501`.
   - `msteams.go`: `:188-200`.
   - `msteamsbot.go`: `isCardReplyEvent` / `isFollowUpEvent`, `:128`, `:205`.
   - `email.go`: `canAckEvent`, `isModeledIncidentEvent`, `incidentTemplateForEvent` (`:34-60`, `:117-139`), plus a new `email/templates/incident-reminder.html`.
   - `googlechat.go`, `mattermost.go`, `matrix.go`, `zulip.go`, `gotify.go`, `ntfy.go`, `pushover.go`, `webpush.go`, `webhook.go`: the webhook adds `data.incident.reminderCount`, which is additive.
   - Add `NotifiesReminders` to `SenderCapabilities` (`notifications/registry.go:31-43`) and to `AcceptsEventType` (`:77-94`). Set it to false for Twilio (a paid SMS every interval) and PagerDuty (it owns re-notification, and a trigger reusing the `dedup_key` adds nothing, `pagerduty.go:32-38`).
8. **Resolve after ack.** No change: `incident.resolved` keeps going out after an ack (`service.go:1838-1870` does not look at ack state). The tests below pin it.
9. **Dash0.**
   - Check form: in the incident-tracking section (`web/dash0/src/components/shared/check-form.tsx:2247-2253`), add a Select with `data-testid="reminder-interval-select"` and the options Org default / Off / 15 min / 30 min / 1 h / 2 h / 4 h / 24 h. The org default option shows the resolved org value. Count the field in `incidentCustomized`.
   - Org settings page (`web/dash0/src/routes/orgs/$org/organization.settings.tsx`, next to `default-escalation-select` at `:490`): add `reminder-interval-default-select` and `reminder-interval-default-save`.
   - Add the API types in `web/dash0/src/api/hooks.ts` and `web/dash0/src/lib/check-request.ts`, and the strings in all four locales.
10. **Docs.**
    - `web/docs/docs/features/incidents.md:252-258`: add an `incident.reminder` row and a short "Reminders" section covering what stops them and how they restart.
    - `web/docs/docs/configuration/notifications.md:948-958`: event types table, plus the `reminderCount` bullet.
    - The check fields page that lists `confirmationPeriodSeconds`.
    - `CHANGELOG.md`.

## Tests
- `server/internal/jobs/jobtypes/job_incident_reminder_test.go` (new):
  - Interval 900: the job is scheduled at open + 900 s and not before. On run it enqueues one `incident.reminder` notification per enabled channel and none for a disabled channel. It schedules `seq+1` at `now + 900`.
  - Ack (no cancel sweep, guard only): no send, no reschedule.
  - Snoozed incident: no send.
  - Auto-resolved incident (no sweep ran): no send, no reschedule.
  - Interval set to 0 since scheduling: stops.
  - Paused check, and check in an active maintenance window: no send, but the job reschedules with the same `seq`.
  - Paging-suppressed child: stops.
  - Race: two pending jobs with the same `seq` run back to back. Exactly one set of notification jobs exists and `reminder_count` went up by one. Positive control: a single job does send.
  - Twilio and PagerDuty channels get no reminder.
- `server/internal/db` (SQLite plus a `_postgres_test.go`): `ClaimIncidentReminder` returns true once, then false for the same expected count. It returns false on an acked, resolved or still-snoozed incident.
- `server/internal/handlers/incidents/` (new `reminder_test.go`):
  - Open schedules a reminder when the org default is 3600 and the check value is nil.
  - A check value of 0 beats an org default of 3600: nothing is scheduled.
  - Degraded and SLO burn incidents never schedule one.
  - Ack cancels the pending reminder. A later auto-resolve still queues `incident.resolved` to the channels.
  - Snooze cancels it. Manual unsnooze schedules the next at unsnooze + interval, and the snooze sweep path does the same.
  - Unack restarts it.
  - Reopen schedules one.
- `server/internal/handlers/checks/`: validation accepts null, 0, 900 and 86400, and rejects -1, 899 and 86401. A PATCH without the field leaves it unchanged. Clone and export/import round-trip it.
- `server/internal/handlers/auth/`: org settings GET returns 0 by default, and PATCH stores the value and range-checks it.
- `server/internal/notifications/`: a reminder case in `slack_test.go`, `discord_webhook_test.go`, `msteams_test.go`, `msteamsbot_test.go`, `email_test.go` (template renders, ack link present), `webhook_test.go` (`type` is `incident.reminder`, `reminderCount` set), `registry_test.go` (`AcceptsEventType` is false for Twilio and PagerDuty and true for Slack).
- `web/dash0/e2e/incident-reminders.spec.ts` (new): set the check select to 30 min, save, reload, and the value persists. Set the org default in settings, and a new check shows "Org default (1 h)". Extend `organization-settings.spec.ts` if that is simpler.
- `make test`, `make test-postgres` for the CAS, `~/go/bin/golangci-lint run ./...`, and `bun run lint:e2e && bun run typecheck:e2e`.

## To verify
- Confirm that `027_v0_39_0` is still unreleased (latest tag `v0.38.3` at filing). If v0.39.0 has shipped, use the next file.
- The Slack client has no `reply_broadcast` today. A thread-only reply is easy to miss, so check whether adding `reply_broadcast: true` for reminders is a small change. If it is not, post a new top-level message that links to the thread.
- Whether a reopened incident keeps `acknowledged_at`. If it does, the guard correctly stops reminders, but say so in the docs.

## Resolved open questions

**Decision (user, 2026-10-12): adopt the recommended answer of every question below. Implement exactly those recommendations.**
- Default for existing and new orgs? Recommended: off for both (absent org parameter = 0). Nobody gets new noise after an upgrade, and turning it on is one setting.
- Minimum and maximum interval? Recommended: 15 min to 24 h.
- Cap on the number of reminders? Recommended: none. Ack or snooze is the stop button.
- Should reminders also go to escalation-policy targets? Recommended: no. Policies have their own `repeatMax` / `repeatAfterSeconds`.
- Should unack restart reminders like unsnooze does? Recommended: yes, with the interval counted from the unack.
- Should SMS (Twilio) channels get reminders? Recommended: no by default, since every reminder is a paid text. Revisit if users ask.
