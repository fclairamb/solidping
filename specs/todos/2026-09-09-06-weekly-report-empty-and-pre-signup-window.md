---
model: sonnet
effort: medium
---

# A brand-new organization is emailed an empty weekly report for a week it did not exist

## Problem

The first email solidping sends a new SaaS account is an uptime report with no
uptime in it, covering a period that ended before the account was created.

Observed in production on 2026-09-09. An account was created at 11:34:14Z; at
11:34:30Z `seedOrgDefaults` wrote its default weekly schedule
(`server/internal/handlers/auth/service.go:3234`); at **12:31:35Z — 57 minutes
after signup** — the hourly sweep sent it. The job row that went out:

```json
{"template":"uptime-report.html",
 "templateData":{"HasData":false,"CheckCount":0,"ScopeLabel":"All checks (0)",
                 "PeriodLabel":"31 Aug 2026 – 6 Sep 2026","AvailabilityPct":"",
                 "IncidentCount":0,"HasPreviousData":false}}
```

`status: success` — it was delivered. Rendered, the recipient got a subject of
`<org> uptime report — 31 Aug 2026 – 6 Sep 2026`
(`server/internal/email/templates/uptime-report.html:1`), a preheader reading
`No availability data · 0 incident(s)`, and a headline metric of **"No data"**
(`uptime-report.html:16`) for a week that closed two days before the
organization existed. The account had created no checks, so there was nothing
to report and nothing that period could have said.

Two independent defects stack to produce it.

### 1. A schedule whose scope is empty is mailed anyway

`Builder.Build` handles the empty scope correctly — it fills the labels and
returns early, leaving `HasData` false
(`server/internal/uptimereport/report.go:270-272`):

```go
if len(checks) == 0 {
    return data, nil
}
```

`runSchedule` then ignores that and fans the result out to every recipient
regardless (`server/internal/jobs/jobtypes/job_uptime_report.go:150-174`).
Nothing between `Build` and `EnqueueUptimeReportEmail` looks at
`data.CheckCount`. The template's `{{else}}` branches exist precisely because
`HasData` can be false, so the empty report renders cleanly — it is simply not
worth sending: it carries no fact the recipient does not already have.

This is not limited to new accounts. Any org that deletes its last check, or
any scoped schedule whose check group empties, keeps mailing "No data" every
period until somebody disables the schedule.

### 2. The first window can close before the organization exists

`report_schedules.last_period_start` is NULL on a new row
(`models.NewReportSchedule`, `server/internal/db/models/report_schedule.go:62-80`
sets no period), and `MarkReportScheduleRun` claims any period when the column
is NULL (`server/internal/db/postgres/postgres.go:7122`):

```sql
WHERE last_period_start IS NULL OR last_period_start < ?
```

The period offered is whatever `Window` says just closed —
`slo.PreviousWindow` (`server/internal/slo/window.go:98-110`), which is purely
calendar arithmetic on `now` and knows nothing about the schedule or the org.
For a weekly schedule that is the ISO week before the current one. An org
created on a Tuesday is therefore first reported on for **the previous
Monday–Sunday**, a window entirely in the past relative to its own
`organizations.created_at`.

Even with checks, that first report is wrong: it describes a period during
which the org demonstrably had no monitoring, and it always will.

The two defects are separable. Fixing only 1 leaves an org that creates a check
on Tuesday still receiving, at the next close, a report about the week before
it existed — with `HasData` true and an availability figure computed over a
window it did not span. Fixing only 2 leaves the "no checks left" case mailing
"No data" forever.

## Proposal

Both fixes belong in the **sweep**, `runSchedule`, and not in `Builder.Build`.
`Build` is shared with the manual test-send path
(`server/internal/handlers/reportschedules/service.go:307`, `TestSend`), where
a user has explicitly asked to see what the report looks like: suppressing
there would turn a deliberate action into a silent no-op. The builder's job is
to describe a window truthfully, including when the answer is "nothing";
deciding whether that is worth an email is the scheduler's.

Both suppressions happen **after** `MarkReportScheduleRun`, keeping today's
claim-then-send order. That matters: the claim is what advances
`last_period_start`, so a suppressed period is consumed rather than retried
by the next hourly sweep, and it can never be re-sent later. Skipping before
the claim would re-evaluate the same period every hour for the lifetime of the
schedule.

### A. Do not send a report with an empty scope

1. In `runSchedule` (`job_uptime_report.go`), after `builder.Build` succeeds
   and before the recipient loop:

   ```go
   if data.CheckCount == 0 {
       jctx.Logger.DebugContext(ctx, "Skipping uptime report with empty scope",
           "schedule_uid", schedule.UID, "period_start", window.Start)

       return 0, nil
   }
   ```

   `data.CheckCount` is `len(checks)` from the scope resolution
   (`report.go:261`), so it covers both an org-wide schedule on an org with no
   checks and a scoped schedule whose groups or check UIDs resolve to nothing.

2. `DebugContext`, not `WarnContext`: a fresh org skipping its first period is
   the normal, expected path, and an hourly WARN per empty schedule would be
   noise that trains the operator to ignore the log.

3. `TestSend` is untouched and keeps sending the empty report on request.

### B. Do not report on a period that closed before the organization existed

4. In the same function, after the organization is loaded (it already is, for
   `builder.Build`), skip when the window ended at or before the org was
   created:

   ```go
   if !window.End.After(org.CreatedAt) {
       jctx.Logger.DebugContext(ctx, "Skipping uptime report for a period predating the org",
           "schedule_uid", schedule.UID, "period_start", window.Start)

       return 0, nil
   }
   ```

   Placed **before** `builder.Build`, so a period that cannot say anything
   costs no availability query.

5. Deliberately `window.End`, not `window.Start`. An org created mid-week is
   still reported on at the next close, over a window it only partly spans —
   that report is about real monitoring and is worth sending. Only a window
   that closed entirely before the org existed is suppressed.

6. Deliberately `organizations.created_at`, not `report_schedules.created_at`.
   A schedule added by hand to an org that has been monitoring for months
   should report on the period that just closed; there is real data and the
   user asked for the digest. Only the org's own age bounds what can
   meaningfully be reported.

### Tests

`server/internal/jobs/jobtypes/job_uptime_report_test.go` already exercises the
sweep; extend it rather than starting a new file.

7. **Empty scope, org-wide**: org with zero checks, enabled weekly schedule,
   one recipient, a closed period → zero email jobs enqueued, and
   `last_period_start` **is** advanced (assert the row moved: the claim must
   still happen, or the sweep will retry hourly forever).
8. **Empty scope, scoped schedule**: schedule pinned to a check group with no
   members, org has checks → still suppressed. Guards against implementing A
   as "org has no checks" rather than "the scope resolves to none".
9. **Non-empty scope** (positive control): one check with results → exactly one
   email job per recipient, as today.
10. **Window closed before the org existed**: org `created_at` after
    `window.End`, org **has** checks (so A cannot be what suppresses it) →
    zero emails, period claimed. Assert `Build` was not reached — e.g. by
    asserting no availability query ran, or by pointing the builder at a
    database stub that fails if queried.
11. **Window straddling the org's creation** (positive control): `created_at`
    inside the window → the report is sent.
12. **Second sweep in the same period**: after any suppressed run, running the
    sweep again enqueues nothing — the claim, not the guard, is what makes
    this true, and it is the property that stops an hourly resend.
13. **`TestSend` unaffected**: `reportschedules` service test — an org with
    zero checks still produces one email job when a test send is requested.

### Resolved open questions

Every question this spec raised is decided below. Nothing here is left to the
implementer's judgement — implement exactly these decisions.

**Q. Should the empty-scope skip also disable the schedule after N consecutive
empty periods?**
**Decision: no.** Do not add any auto-disable. A schedule is a standing intent:
an org that adds its first check in March must get its March report without
re-enabling anything. Suppressing the individual send is the whole fix.

**Q. Should a suppressed period be visible in the UI (e.g. `last_run_at`
showing a run that mailed nothing)?**
**Decision: leave it as is.** Both `last_period_start` and `last_run_at` keep
moving on a suppressed period. That is accurate — the sweep did run — and no
surface currently claims an email was sent. Do not add a column, a flag, or a
UI change in this spec. If a "last sent" distinct from "last run" is ever
wanted, it needs its own column rather than a reinterpretation of these two.

**Q. Guard B makes the seeded schedule's first period a no-op for every new
org — is that acceptable?**
**Decision: yes, that is the intended behaviour.** A new account's first report
arrives at the end of its first full period. Do not add a compensating
"welcome report" and do not treat this as a regression.

## Implementation Plan

### 1. `server/internal/jobs/jobtypes/job_uptime_report.go`

- Insert the B guard at line 142, between the `GetOrganization` call
  (`job_uptime_report.go:138`) and `builder.Build` (`:143`), comparing
  `window.End` with `org.CreatedAt`.
- Insert the A guard immediately after `builder.Build` returns and before the
  recipient loop (`:150`), keyed on `data.CheckCount == 0`.
- Both return `(0, nil)` after a `DebugContext` line carrying `schedule_uid`
  and `period_start`. **Never log recipients** — `ReportSchedule.Recipients`
  is PII and `seedOrgDefaults` already carries that rule
  (`service.go:3217`).
- Comment each guard with *why it sits after the claim*, so a later reader
  does not "optimise" it above `MarkReportScheduleRun` and reintroduce the
  hourly retry.

### 2. Tests

- Extend `job_uptime_report_test.go` with the cases in 7–12 above, following
  the fixture style already in that file.
- Add case 13 to the `reportschedules` service tests.
- `go test ./server/...` is the gate; no frontend, API, or database change is
  involved, so no OpenAPI regeneration and no migration.

### 3. Not in scope

- No backfill. The one delivered empty report is out; nothing re-sends it, and
  the recipient of the production instance of this bug is being written to
  separately.
- No change to `Builder.Build`, the template, or `TestSend`.
- The seeded schedule itself stays exactly as `seedOrgDefaults` writes it — the
  fix is that the sweep now declines to mail a period that cannot say anything,
  which is the correct place for the decision.
