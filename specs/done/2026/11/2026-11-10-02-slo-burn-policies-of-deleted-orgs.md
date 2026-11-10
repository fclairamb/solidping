---
model: opus
effort: medium
---

# SLO burn-rate policies of a deleted organization stay in the instance-wide evaluation queue forever

## Problem

Found by the security scan of 2026-11-10 (finding AI4).

- **Severity:** Low.
- **Who can trigger it:** any org admin. On SaaS that is anyone, since any user can create an org and becomes its owner.

The burn-rate job evaluates every minute a batch of at most `evaluationBatchSize = 500` policies, the least recently
evaluated first:

- the batch size is set in `server/internal/handlers/sloalerts/service.go:50`;
- the job calls `EvaluateBurnRates`, `:128`;
- `server/internal/jobs/jobtypes/job_slo_burn_eval.go` drives it.

`ListEnabledSLOAlertPolicies` (`server/internal/db/postgres/postgres.go:7745`, `server/internal/db/sqlite/sqlite.go:7704`)
only joins `slos`: the policy and its SLO are enabled, and the SLO is not deleted. It never looks at the organization.

`DeleteOrganization` (`postgres.go:586`, `sqlite.go:585`) soft-deletes the org and, since #824, disables its report
schedules. The handler (`server/internal/handlers/auth/org_delete.go:63`) also stops the checks. Nothing touches `slos`
or `slo_alert_policies`.

After an org is deleted:

- **Its policies keep being evaluated.** Each one costs, once per pass:
  - `GetSLO`
  - the windowed availability aggregates
  - `FindActiveBurnIncident`
  - an `UPDATE` of the policy
- **Nobody can turn them off.** The org answers 404 everywhere, so neither its former admins nor the instance operator
  can disable them through the API.
- **They pile up.** The org's SLO quota (`entitlements/usage.go:120`) counts a live org only, so each create-and-delete
  cycle adds up to `MaxSlos` × the policies per SLO, permanently.
- **Every tenant pays for it.** With more than 500 enabled policies, each one is evaluated about every
  `N / 500` minutes. Orphans therefore slow down the burn-rate alerts of every live org on the instance. Example: with
  5,000 orphans, a fast-burn alert fires up to about 10 minutes late.
- **Nothing pages.** The check of a deleted org is soft-deleted, so `anchorCheck` (`sloalerts/service.go:280`) finds no
  live check.

This is the class #824 fixed for report schedules (`dbcontract/reportschedules.go`).

## Proposal

1. In `DeleteOrganization`, both dialects, next to the report-schedule update: set `enabled = false` on the org's
   `slo_alert_policies` (and `updated_at`).
2. In `ListEnabledSLOAlertPolicies`, both dialects: join `organizations AS o ON o.uid = slo_alert_policies.organization_uid`
   and add `o.deleted_at IS NULL`. That keeps orgs deleted before this change out of the queue, the same belt and braces
   as `ListEnabledReportSchedules` (`postgres.go:7911`).
3. Look for the same class in the other instance-wide sweeps: list every `ListEnabled*` / sweep query a job runs across
   all orgs and check that it skips deleted orgs. Fix the ones that would do work, send anything, or take a slot in a
   shared batch. Name the ones left as is, with the reason, in the PR.

## Tests

- `server/internal/db/dbcontract/slos.go` (runs on SQLite and Postgres): create two orgs, each with an enabled SLO and an
  enabled policy, then delete one org.
  - `ListEnabledSLOAlertPolicies` returns only the live org's policy. It returns both before the fix.
  - Positive control: before the delete, both are listed.
- Same contract, for an org deleted with the policy still enabled (simulating a deletion before this fix: set
  `organizations.deleted_at` directly): the policy is not listed.
- `server/internal/handlers/sloalerts` service test: after the org is deleted, `EvaluateBurnRates` evaluates 0 policies
  of that org and still evaluates the live one.
