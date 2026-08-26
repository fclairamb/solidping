---
model: opus
effort: high
---

# Client-settable `internal` checks dodge the checks quota but still burn rate budget

## Problem

The `internal` flag on checks is meant for server-created plumbing checks,
but it leaks into the public API surface, and the three accounting systems
disagree about what it means:

- **Clients can set it.** `internal` is accepted from
  `CreateCheckRequest` on the public create/update paths
  (`server/internal/handlers/checks/service.go:728` and `:1211`).
- **It exempts the check from the `MaxChecks` quota**
  (`service.go:1255–1258`) — so a customer who sends `internal: true`
  creates checks that never count against their plan's check allowance.
- **It is invisible to the demand figure**: the entitlements
  checks-per-minute demand computation
  (`server/internal/entitlements/check_rate.go`, via `ListOrgCheckRates`,
  which filters `internal = false` in both
  `server/internal/db/postgres/postgres.go` and
  `server/internal/db/sqlite/sqlite.go`) excludes internal checks.
- **But it still consumes real rate budget**: neither rate-limit gate
  special-cases `Internal` — not the worker gate
  (`server/internal/checkworker/worker.go`, `applyRateLimitGate`) nor the
  agentws dispatch gate (`server/internal/handlers/agentws/handler.go`) —
  so an internal check draws per-org `MaxChecksPerMinute` tokens and can
  increment the `skippedToday` counter.

Net: a customer-created `internal: true` check is unmetered by `MaxChecks`,
invisible to the org's displayed demand, yet competes for the org's
per-minute execution budget — the "predictive" demand figure and the
"factual" skip counter can disagree for the same org, and the checks quota
has a client-triggerable bypass.

Legitimate internal checks are only ever created server-side (e.g.
`server/internal/checkworker/worker.go:1700–1704`,
`server/internal/jobs/jobworker/worker.go:691–695`) and with
`Enabled = false`, so today the demand exclusion happens to be inert for
them — the inconsistency only bites when a client sets the flag.

Found during the 2026-08-26 entitlements banner audit; this is pre-existing
behavior, not a regression from that work.

## Proposal

1. **Close the API surface**: the public create/update paths **reject** a
   client-supplied `internal` with a `VALIDATION_ERROR` naming the field —
   rejecting beats silently dropping, so callers learn. Only server-side
   creation paths (and, if ever needed, a superadmin path) may set it.
   Audit the OpenAPI spec so the field is not advertised as writable.
2. **Make the accounting consistent** for whatever internal checks remain
   (server-created): a check excluded from `MaxChecks` and from the demand
   figure must not consume `MaxChecksPerMinute` tokens either — exempt
   `Internal` checks in both rate gates (worker `applyRateLimitGate` and
   the agentws dispatch gate), mirroring the passive-type exemption that
   already returns before the gate.
3. **Tests proving the negative**:
   - a customer create/update with `internal: true` is rejected (or
     provably ignored), with a positive control that the server-side
     creation path still produces internal checks;
   - an internal check consumes no rate token and never increments the
     `check_rate_limited` usage counter, with a positive control that a
     normal check still does;
   - existing internal-check creators keep working (no regression in the
     worker/jobworker plumbing paths).
4. Sweep existing data: a release note / migration consideration for any
   org that already has client-created `internal` checks (count them
   first; likely zero — if so, note it and skip the migration).
