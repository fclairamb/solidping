---
model: opus
effort: medium
---

# Inbound mail is still recorded on the email checks of a deleted organization

## Problem

Found by the security scan of 2026-11-15 (finding AN3). Severity: Low. It can be triggered by anyone who can send mail and knows the inbound address of a deleted org's email check: former members, and every system that was configured to mail it.

- `DeleteOrganization` soft-deletes the org row and disables its report schedules and SLO burn policies, but leaves its checks live (`server/internal/db/postgres/postgres.go:586-615`, `server/internal/db/sqlite/sqlite.go:582`).
- `GetCheckByEmailToken` filters on `checks.deleted_at` only, with no join on `organizations` (`postgres.go:1915-1935`, `sqlite.go:1898`).
- `HandleEmail` then records a result and runs `ProcessCheckResult` (`server/internal/handlers/emailcheck/handler.go:176-198`, `:479-492`), with no org-liveness check.

Every other inbound path stops for a deleted org. Heartbeats resolve the org by slug first (`server/internal/handlers/heartbeat/service.go:264-282`). The codebase already treats waking a deleted org's incident as a bug (`ListExpiredSnoozedIncidents`, `postgres.go:4262-4268`). Here, mail keeps writing results into the deleted org and can open or resolve incidents there.

## Proposal

1. Add `JOIN organizations AS o ON o.uid = check.organization_uid` and `o.deleted_at IS NULL` to `GetCheckByEmailToken` in both dialects. A token of a deleted org then reads as "no check", and the mail is rejected as for an unknown token.
2. Leave `DeleteOrganization` alone in this spec. Soft-deleting the org's checks is broader and touches every consumer.

## Tests

- `db/sqlite/email_token_unique_test.go` and its Postgres twin `email_token_unique_postgres_test.go`: create an email check, look it up by token (found: positive control), delete its organization, look it up again. Expect `sql.ErrNoRows`. This fails before the fix.
- `handlers/emailcheck/handler_cover_test.go`: mail to a deleted org's check returns `OutcomeRejected`, and no result row is written.

## To verify

- Whether `ProcessCheckResult` → notification dispatch already skips deleted orgs. If not, the current bug also sends notifications through the deleted org's integrations: say so in the PR.
