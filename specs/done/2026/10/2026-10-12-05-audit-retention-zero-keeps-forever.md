---
model: sonnet
effort: medium
---

# `SP_AUDIT_RETENTION_DAYS=0` must keep audit events forever, end to end

## Problem
The docs say `SP_AUDIT_RETENTION_DAYS=0` keeps everything forever (`server/internal/app/docsres/features/audit-log.md:105`, `server/internal/config/config.go:1225-1228`, `server/internal/systemconfig/systemconfig.go:126-130`). It does not. Only a negative value works today:

- `applyAuditEnv` skips a 0 value: `if n := envInt("SP_AUDIT_RETENTION_DAYS"); n != 0` (`server/internal/config/config.go:2812`). `envInt` returns 0 for both "unset" and "0" and for an unparseable value (`config.go:3686`), so the three cases cannot be told apart.
- The job resolves retention through `resolveRetentionTier` (`server/internal/jobs/jobtypes/job_aggregation.go:373`), shared with the aggregation tiers where 0 is invalid. `retentionFromEnv` accepts only `n >= 1` (`:409`) and the legacy tier only `legacy >= 1` (`:390`). So env `0`, a DB parameter `0` and a koanf `0` all fall through to the default 365. Only `legacy < 0` is special-cased before the call (`job_events_cleanup.go:97`), which is why negatives work.
- `Run` already skips the sweep when the resolved value is `<= 0` (`job_events_cleanup.go:113`), so the job body is fine, but the resolver never hands it 0.
- The comment at `job_events_cleanup.go:86-91` claims 0 passes through the legacy tier. It does not.

## Proposal
1. Config parsing, `server/internal/config/config.go`: in `applyAuditEnv`, read `SP_AUDIT_RETENTION_DAYS` with `os.LookupEnv` plus `strconv.Atoi`. Any parseable integer (0 and negatives included) is applied; unset or empty keeps the current value; unparseable logs a warning and keeps the current value (same style as `SP_AUDIT_CAPTURE_IP` just above). Do not change `envInt` (other callers rely on it).
2. Config file / koanf: `Audit.RetentionDays` already defaults to 365 in `Load` (`config.go:1998`), so a file value of 0 is deliberate. Confirm it survives koanf unmarshalling as 0 (not replaced by the default) and add a test for it.
3. Job resolver, `server/internal/jobs/jobtypes/job_events_cleanup.go:92`: stop calling the shared `resolveRetentionTier` for audit. Write an audit-specific resolver with the precedence env, then global DB parameter `audit.retention_days`, then koanf, then default, where **0 and negatives mean "keep forever" at every tier**, and only an absent or unparseable value falls through. Leave `resolveRetentionTier` untouched for aggregation (0 stays invalid there). Reuse `retentionFromDBParam` if it can be parameterised, otherwise parse the parameter locally. Return `0` for any value `<= 0` so `Run` skips the delete.
4. Fix the stale comment at `job_events_cleanup.go:86-91` and the one in `config.go:1225-1232` so they describe the real precedence.
5. envcheck / validation: make sure `SP_AUDIT_RETENTION_DAYS=0` is accepted without warning or error (see To verify). It is already listed as a known variable (`server/internal/config/envvars.go:195`).
6. `audit.retention_days` as a parameter (`server/internal/systemconfig/systemconfig.go:130`): same fix as step 3 on the read side. On the write side, make sure the system-parameter validation (admin API and dash0 form) accepts `0` for this key, and document it. It is a global-only parameter, so there is no per-org variant to fix; confirm that.
7. Docs, keep consistent: `server/internal/app/docsres/features/audit-log.md:96-106` (state that `0` or any negative value keeps everything, and that it also works as the `audit.retention_days` parameter), `server/internal/app/docsres/configuration/data-retention.md:79` (add "0 keeps forever" for the audit row, as the support row does), and the `audit.retention_days` parameter description if it is listed in the docs or OpenAPI. The `web/docs` content is embedded from `server/internal/app/docsres`, so edit it there; check `web/docs/docs/` has no separate copy.
8. Changelog entry per `wiki/conventions/changelog.md`, in the style of the existing `SP_SUPPORT_RETENTION_DAYS=0` entry.

## Tests
Table tests, `testify/require`, `t.Parallel()`, `t.Setenv` is not compatible with `t.Parallel()` so the env cases must either run sequentially in their own test or inject the lookup.
- `server/internal/config` (new or existing `config_test.go`): `applyAuditEnv` cases: unset keeps 365; `"0"` gives 0; `"-1"` gives -1; `"30"` gives 30; `"abc"` keeps 365 and does not panic; `""` keeps 365. Plus a koanf file case with `audit.retention_days: 0` yielding 0.
- `server/internal/jobs/jobtypes/job_events_cleanup_test.go` (helper `newEventsCleanupTestContext` at `:23`): `resolveRetentionDays` table covering env `0`, env `-5`, env `30`, DB parameter `0`, DB parameter `30`, koanf `0`, koanf `30`, nothing set (365), and precedence (env `0` beats DB `30`; env unset and DB `0` beats koanf `30`). Positive control: a run with retention 0 leaves an old event in the table (event older than 365 days still present, nothing deleted), and a run with retention 30 deletes it.
- Aggregation regression: `resolveRetentionTier` with env `0` still falls through (0 stays invalid for the performance tiers).
- `server/internal/envcheck/envcheck_test.go`: `checkEnviron([]string{"SP_AUDIT_RETENTION_DAYS=0"})` returns no findings, next to the existing `SP_SUPPORT_RETENTION_DAYS=0` case (`:98`).
- If the parameter write path validates values: a case accepting `audit.retention_days = 0` and rejecting a non-integer.

Before pushing: `cd server && ~/go/bin/golangci-lint run ./...` and `go test -short` on `./internal/config/... ./internal/envcheck/... ./internal/jobs/... ./internal/systemconfig/...` plus any handler package touched.

Delivery: branch `fix/audit-retention-zero`, PR title `fix(audit): SP_AUDIT_RETENTION_DAYS=0 keeps audit events forever`, per AGENTS.md. No real company names in tests, comments, commit or PR text (use `acme`).

## To verify
- The report said envcheck/config validation rejects 0. I found no such check: `server/internal/envcheck/envcheck.go` only tracks variable names, and I did not find a range validation for `SP_AUDIT_RETENTION_DAYS` in `server/internal/config`. Grep for the validation (`Validate`, `retention`, `must be`) and for any startup warning on 0; if it exists, fix it, otherwise note in the PR that only the parse and resolver sides were broken.
- Whether `retentionFromDBParam` (same file as `resolveRetentionTier`, `job_aggregation.go`) rejects `< 1` and what the systemconfig parameter validator/admin API accepts for `audit.retention_days`.
- Whether `web/docs/docs/` holds its own copy of the audit-log or data-retention pages.
