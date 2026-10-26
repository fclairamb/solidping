---
model: sonnet
effort: medium
---

# Run the report schedule "send a test" as a background job

GitHub Issue: https://github.com/fclairamb/solidping-business/issues/16

## Source

In-app report by the maintainer (v0.41.0, `/d/orgs/<org>/organization/report-schedules/<uid>`):
sending a test availability report blocks the UI for a few seconds. Expected: the click starts a
job and returns at once. The reporter also asks to remove the blocking API.

The report is right: the request stays open for the whole report build.

## Problem

`POST /api/v1/orgs/{org}/report-schedules/{uid}/test`
(`server/internal/app/server.go:2225`, handler
`server/internal/handlers/reportschedules/handler.go:108`) calls `Service.TestSend`
(`server/internal/handlers/reportschedules/service.go:277`) synchronously:

- `service.go:313`: `s.builder.Build(ctx, org, row, window, now)` computes the whole report (per
  check availability, SLOs, incidents over the closed period) inside the HTTP request. This is the
  slow part, and it grows with the number of checks in scope.
- `service.go:323`: `s.mailer.SendReport` only enqueues an `email` job
  (`server/internal/app/uptime_report_mailer.go:20`, `jobtypes.EnqueueUptimeReportEmail`), so the
  SMTP send is already asynchronous.
- The handler answers `202 Accepted` (`handler.go:138`) although the work is done by then.

dash0 waits on it: `useTestReportSchedule` (`web/dash0/src/api/hooks.ts`, around line 9011) is a
plain mutation, and `organization.report-schedules.$uid.tsx` shows `testPending` until the POST
returns.

## Why a spec

- Needs a new job type (`report_test_send` or a test mode of `uptime_report`) and a changed
  endpoint contract (it returns a job reference instead of doing the work): rows "new job type"
  and "changed API endpoint / OpenAPI change" of the classification table.
- The issue also asks to remove the blocking API, which is a product decision (see Open
  questions).

## Proposal

1. **Job type.** Add `jobdef.JobTypeReportTestSend` (`report_test_send`) with config
   `{orgUid, scheduleUid, address, hint{lang,timezone}, now}`. Its run does what `TestSend` does
   after validation: resolve the schedule, `uptimereport.Window`, `builder.Build`,
   `UnsubscribeURL`, `EnqueueUptimeReportEmail`. One attempt plus the job system's default retry
   policy; a missing schedule (deleted meanwhile) ends the job as failed, not retried.
2. **Endpoint.** Keep the same route. Validation stays synchronous (schedule exists, address
   parses, delivery configured, address not suppressed) so the caller still gets
   `400/404/409`-style errors immediately. Then enqueue the job and answer `202` with
   `{ "jobUid": "<uid>" }`. Update `openapi.yaml` (`/api/v1/orgs/{org}/report-schedules/{uid}/test`,
   line 7209), regenerate the Go client (`go generate ./server/pkg/client/...`), and the docs page
   for report schedules if it describes the test send.
3. **dash0.** `useTestReportSchedule` returns the job uid; the button stops spinning as soon as
   the POST returns and shows a toast "Test report queued, it will arrive in your inbox shortly"
   (all four locales). No polling needed in v1.
4. **Tests.** Handler test: the POST returns `202` with a `jobUid` and does not call the builder
   (inject a builder that blocks or panics). Job test: the run builds and enqueues one email for
   the address, honours suppression at run time too, and fails cleanly on a deleted schedule.
   Keep `testsend_test.go`'s suppressed / non-suppressed / zero-check cases, moved to the job
   where they test the build path. Playwright: clicking "Send test" shows the queued toast.

## Open questions

1. **Remove the synchronous path entirely, or keep it?** Recommended: replace it (same route,
   now async), no `?sync=true` escape hatch. Trade-off: API clients that relied on "202 means
   the mail is queued" now only know the build is queued, which is acceptable since the mail
   itself was already asynchronous.
2. **Surface the job in the UI?** Recommended: toast only in v1. Linking the job page
   (`/orgs/$org/jobs/$jobUid`) is cheap but exposes an internal page to non-admin roles; leave
   it out unless the job page is already readable by the same role.

## Closing the issue

This spec closes fclairamb/solidping-business#16. The implementing PR body **must** carry
`Closes fclairamb/solidping-business#16` so the squash-merge closes it. When the spec is archived
to `specs/done/`, verify the issue is closed and, if the merge did not close it, close it by
hand:

    gh issue close 16 --repo fclairamb/solidping-business --comment "Implemented by <PR or commit>; spec: <archived spec path>"
