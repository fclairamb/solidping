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

## Implementation Plan

### 1. Close the API surface (service layer, not the HTTP handler)

The three request structs (`CreateCheckRequest`, `UpdateCheckRequest`,
`UpsertCheckRequest`) are shared by every *client* surface — REST, MCP,
import/apply, the chat integrations — while the two legitimate
internal-check creators (`checkworker.createInternalCheck`,
`jobworker.createInternalCheck`) bypass the service entirely and write
through `dbService.CreateCheck`. So rejecting inside the service closes
every client door at once and cannot touch the server-side ones.

- New sentinel `ErrInternalFieldNotWritable`.
- `CreateCheck` / `UpdateCheck` / `UpsertCheck`: a **non-nil** `internal`
  (true *or* false) is rejected. Keep the struct field so the attempt is
  detectable — dropping the field would silently ignore it, which the
  Proposal rules out.
- `CreateCheck`'s `isInternal` collapses to a constant `false`: the
  MaxChecks quota and the per-type period bounds now always apply on the
  public path. `UpdateCheck` reads `internal` from the stored row only.
- `CloneCheck` is the same loophole through another door — it copies
  `source.Internal` and exempts internal clones from MaxChecks. A clone is
  a client-triggered creation, so the clone is always non-internal and
  always metered.
- Import/apply (`ImportChecks` → `UpsertCheck`) currently forwards
  `&exportedCheck.Internal` unconditionally. It stops forwarding the flag,
  and an explicit `internal: true` in a document is a per-check
  `ImportError` naming the field. `internal: false`/absent keeps working
  (export never emits it: `ExportChecks` lists with the default filter,
  which is `internal = FALSE`, so an internal check is never in a document
  this server produced).

### 2. Handler error mapping

`handleCreateError` / `handleUpdateError` / `handleUpsertError` map the
sentinel to `WriteValidationError` with the field named:
`{"title": "Validation error", "code": "VALIDATION_ERROR",
"fields": [{"name": "internal", ...}]}`.

### 3. Make the accounting consistent — exempt internal from both rate gates

`check_jobs` needs no new column: both claim paths already attach the
check row (`checkjobsvc.attachChecks`, inside the claim transaction), so
`job.Check.Internal` is available at both gates. **No migration.**

- `models.CheckJob.IsInternal()` — one helper so the two gates can never
  disagree, mirroring how `isPassiveCheckType` delegates to `checkerdef`.
- `checkworker.applyRateLimitGate`: return `false, nil` for an internal
  job before reserving — no token, no Prometheus tick, no
  `RecordRateLimitedSkip`.
- `agentws.handleClaim`: same guard before `ReserveCheckExecution`.

After this the three systems agree: an internal check counts nowhere
(MaxChecks, demand, MaxChecksPerMinute).

### 4. OpenAPI + generated client

Drop `internal` from the `CreateCheckRequest` / `UpdateCheckRequest` /
`UpsertCheckRequest` schemas; keep it on the `Check` response schema as
`readOnly: true`. Regenerate `server/pkg/client` (`go generate
./pkg/client/...`).

### 5. Tests (each with its positive control)

- `checks`: create/update/upsert with `internal: true` → rejected; import
  document with `internal: true` → `ImportError`; clone of an internal
  check produces a non-internal, metered check; **positive control** —
  `dbService.CreateCheck` with `Internal = true` (the worker/jobworker
  path) still writes an internal check, and it is still excluded from the
  MaxChecks count and from `ListOrgCheckRates`.
- `checkworker`: internal job → not deferred, `check_rate_limited`
  counter untouched even with the cap pinned at 0; **positive control** —
  the same fixture with a non-internal check still defers and still counts.
- `agentws`: same pair on the dispatch gate.

### 6. Data sweep (item 4) — a query, not a migration

No migration ships. Nothing in the code can distinguish a client-created
internal check from a server-created one after the fact, but the
server-side creators use two reserved slug prefixes, so the residue is
exactly:

```sql
SELECT o.slug AS org, c.slug, c.type, c.enabled, c.created_at
FROM checks c JOIN organizations o ON o.uid = c.organization_uid
WHERE c.internal = TRUE
  AND c.deleted_at IS NULL
  AND c.slug NOT LIKE 'int-checks-%'
  AND c.slug NOT LIKE 'int-jobs-%';
```

Expected: zero rows. If an install has rows, the fix is a one-line
`UPDATE ... SET internal = FALSE` for those UIDs — after which those
checks start counting against MaxChecks, which is the point. Flagged in
the release notes rather than automated: silently re-metering a customer's
checks during a migration is a billing-visible change that an operator
should make deliberately.
