---
model: sonnet
effort: medium
pr: batch
automerge: false
---

# An email job with an unparseable recipient fails at send time instead of being refused upstream

GitHub Issue: https://github.com/fclairamb/solidping-business/issues/55

## Source

The daily pod-log review (item 2 of the issue) found `Job failed` ERROR lines in production:
`sending email: setting to addresses: failed to parse mail address: missing '@' or angle-addr`.
A recipient stored without a valid address reaches the mail sender, and the failure recurs.
Items 1 (uptimebar lag warning) and 3 (agent connection loss) of the same issue are handled
elsewhere: item 1 was fixed directly (`fix: stop the uptimebar aggregation-lag warning…`),
item 3 is `2026-11-08-04-agent-connection-loss-log-levels.md`. Item 4 is a collector config
change in the private infrastructure repo, item 5 was already fixed.

## Problem

- `server/internal/email/sender.go:206-210` (`setRecipients`) is the first place an address is
  parsed; a bad one makes `EmailJobRun.Run` (`server/internal/jobs/jobtypes/job_email.go:143-155`)
  return a plain error, logged as `Job failed` at ERROR. It is not retried (only network errors
  are `NewRetryableError`), so "keeps failing" means the **producer** keeps enqueueing jobs for
  the same bad address.
- Email jobs are enqueued from several places, and only some validate the address when it is
  stored: `handlers/reportschedules/service.go:287,373`, `handlers/statussubscribers`,
  `handlers/auth/email_change.go:72` do; the other producers
  (`regionsweep/orgs.go:511`, `opsnotifywire/wire.go:150`, `jobs/jobtypes/phone_cap_notice.go:159`,
  `jobs/jobtypes/uptime_report_mail.go:61`, `customdomain/alert.go:193`,
  `handlers/auth/service.go:716`, and the email notification connection) take the address from
  user/contact/connection rows as-is.
- Which producer sent the failing jobs is not known yet: the job row's `config.to` and the job's
  type/producer tell it (`stn`/DB lookup on the failed `email` jobs of the last days).

## Why a spec

Several subsystems (each email producer, the stored data), possibly a data backfill for rows
already holding an invalid address, and write-time validation may change API responses (400 on
create/update).

## Proposal

1. Find the producer of the failing jobs (query failed `email` jobs, group by template/producer).
2. `email` job: classify an address parse failure as a permanent failure, logged at WARN with
   the template and a redacted recipient (no address in clear), not ERROR; never retried.
3. Producers: filter recipients through `mail.ParseAddress` before `CreateJob` (one shared
   helper in `internal/email`, e.g. `ValidRecipients([]string) (valid, invalid []string)`), skip
   invalid ones with a WARN, and don't enqueue a job with no valid `To`.
4. Write-time validation wherever the source field is still unvalidated (email notification
   connection settings, contact/user emails if any path skips it), 400 with the usual
   validation error code.
5. Tests: a job with `to: ["not-an-address"]` fails permanently without ERROR; each touched
   producer skips an invalid recipient; the create/update endpoints refuse one.

## Closing the issue

This spec closes fclairamb/solidping-business#55 together with
`2026-11-08-04-agent-connection-loss-log-levels.md`. The issue was closed when the specs were
written; when the spec is archived to `specs/done/`, check it is closed and close it by hand
otherwise:

    gh issue close 55 --repo fclairamb/solidping-business --comment "Implemented by <PR or commit>; spec: <archived spec path>"
