---
model: sonnet
effort: medium
---

# Log noise: stale-incident acks at ERROR, orphan report schedules, SMTP burst errors

GitHub Issue: https://github.com/fclairamb/solidping-business/issues/57

## Source

A daily review of production pod logs found three recurring, avoidable log lines in
solidping itself, plus one infrastructure item and one non-issue:

1. `Failed to acknowledge incident` at ERROR when a Slack/Discord "Acknowledge"
   button is clicked on an incident that no longer exists.
2. `Uptime report failed for schedule` WARN on each sweep for schedules whose
   organization is gone (`get organization: sql: no rows in result set`).
3. `failed to send email` ERROR in a burst when the weekly reports all go out at
   once: the SMTP server drops connections (`dial failed: EOF`). Every job is retried
   and delivered, but each attempt logs ERROR + WARN + `Job failed`.
4. A browser sidecar OOMKilled in one region (memory limit): deployment config, lives
   in the operator's infra repo, **out of scope here**.
5. A full-fleet restart with a short database outage: not a bug, nothing to do.

## Problem

1. `server/internal/integrations/slack/interactions.go:277` and
   `server/internal/integrations/discord/interactions.go:284` log every
   `AcknowledgeIncidentFrom*` error at ERROR, including
   `incidents.ErrIncidentNotFound` (`server/internal/handlers/incidents/service.go:34`),
   which is a user clicking a stale button, not a server fault.
2. `ListEnabledReportSchedules` (`server/internal/db/postgres/postgres.go:7847`, and
   the sqlite twin at `sqlite.go:7805`) filters on the schedule's own `deleted_at`
   only, not on its organization's. `runSchedule`
   (`server/internal/jobs/jobtypes/job_uptime_report.go`) then **claims** the period
   (`MarkReportScheduleRun`) before looking up the org, so the org lookup fails after
   the claim and `Run` (`:95`) logs a WARN per orphan schedule per sweep.
3. `server/internal/email/sender.go:307` opens one SMTP connection per message
   (`DialAndSendWithContext`) and logs ERROR at `:308` on any failure, retryable
   network errors included; the job worker then logs its own WARN and retry line.
   N reports enqueued at the same instant mean N concurrent dials.

## Why a spec

- Several subsystems in one issue (integrations, report jobs + both DB backends,
  email sender), and item 3 is a design decision (throttle vs pooled connection).

## Proposal

1. **Ack not-found**: `errors.Is(err, incidents.ErrIncidentNotFound)` → `WarnContext`
   (keep the user-facing ephemeral reply), ERROR otherwise; same in Discord. Test:
   a fake service returning the sentinel logs at WARN (capture handler), any other
   error at ERROR.
2. **Orphan schedules**:
   - `ListEnabledReportSchedules` joins `organizations` and requires
     `organizations.deleted_at IS NULL` (both backends). No migration.
   - In `runSchedule`, look the org up **before** claiming the period; a missing org
     (`sql.ErrNoRows` / not found) returns `0, nil` with a DEBUG line.
   - When an org is soft-deleted, disable its report schedules in the same code path
     (check the org deletion service; if it already cascades, skip).
   - Tests: an enabled schedule of a soft-deleted org is not listed; `runSchedule` on
     a missing org does not claim the period and returns no error.
3. **SMTP burst**: classify retryable network errors (EOF, connection reset/refused,
   timeouts, 4xx SMTP replies) in `describeSendError`; log them at WARN with
   `retryable=true` and let the job retry carry them; ERROR stays for permanent
   failures (auth, 5xx, bad address). Add a process-wide send concurrency limit
   (semaphore, default 2, config `email.max_concurrent_sends`) so a weekly batch no
   longer opens dozens of simultaneous connections. Tests: the semaphore bounds
   concurrent `Send` calls (fake dialer); an EOF error is logged at WARN and returned
   as retryable.
4. Item 4 (sidecar memory) and item 5: no change in this repo.

## Open questions

- **Throttle or pooled connection for item 3?** Recommended: a concurrency limit
  (simple, works with every SMTP provider, keeps one message per connection so a
  failure affects one message). Trade-off: a pooled/reused connection would be
  faster for large batches but needs reconnect handling and is more provider-sensitive.

## Closing the issue

This spec closes fclairamb/solidping-business#57. The implementing PR body **must**
carry `Closes fclairamb/solidping-business#57`. When the spec is archived to
`specs/done/`, verify the issue is closed and, if the merge did not close it, close
it by hand:

    gh issue close 57 --repo fclairamb/solidping-business --comment "Implemented by <PR or commit>; spec: <archived spec path>"
