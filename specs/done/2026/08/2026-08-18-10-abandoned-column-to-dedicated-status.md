---
model: opus
effort: high
---

# Replace the `results.abandoned` boolean column with a dedicated `ResultStatusAbandoned` status value

## Problem

Spec 2026-08-18-03 introduced the abandoned-result reaper: a raw row stuck in
`status=created` well past its check's plausible execution window is finalized
to `status=error` **plus** a new `abandoned boolean not null default false`
column (migration `015_v0_17_0`), and every availability computation excludes
`abandoned=true` rows via the shared predicate
`Result.ExcludedFromAvailability()`
([result.go:143](../../server/internal/db/models/result.go)).

That two-field encoding ("it's an error, but with an invisible asterisk") was
chosen partly for backward compatibility of the status enum — a concern that
does not apply pre-1.0. On review it is the weaker design:

- **The two axes collapse in practice.** The only writer of `abandoned=true`
  is the reaper (`claimAbandonedResult`, both dialects), the state is
  terminal, and in the current codebase the only rows that ever sit in
  `status=created` are `CreateCheck`'s one-time "Check created" marker rows
  ([sqlite.go:1373](../../server/internal/db/sqlite/sqlite.go),
  `abandonedResultLifecycleStatuses` comment at
  [sqlite.go:2556](../../server/internal/db/sqlite/sqlite.go)). There is no
  independent "abandoned but some other status" combination to preserve.
- **It is less honest than a distinct status.** An abandoned row is evidence
  that *our* side never completed the attempt — rendering it as `error`
  (which everywhere else means "the attempt ran and failed") and then quietly
  un-counting it from availability is exactly the ambiguity a dedicated
  status removes. The timeline could render it as its own state instead of a
  fake error.
- **Every consumer already special-cases it anyway** — as a second field
  check instead of one more case in an existing status switch.
- It costs a permanent extra column on `results`, the largest table in the
  system.

## Proposal

Introduce `ResultStatusAbandoned ResultStatus = 9` (8 is already
`ResultStatusWarning`, 7 is `ResultStatusDegraded` — see
[result.go:12-32](../../server/internal/db/models/result.go)), make the reaper
write it, and drop the `abandoned` column.

### Backend

1. **Enum & model** (`server/internal/db/models/result.go`):
   - Add `ResultStatusAbandoned = 9` with a doc comment stating it is
     server-minted only (by the reaper), never reported by workers/agents.
   - `StatusToString`: map 9 → `"ABANDONED"`.
   - `ExcludedFromAvailability()`: replace the `r.Abandoned` field check with
     `status == ResultStatusAbandoned` (keep the lifecycle-marker branch).
     `CountsAsUp()` is unchanged (9 is not up).
   - Delete the `Abandoned bool` field from `Result`.
2. **Reaper** (`ReapAbandonedResults` / `claimAbandonedResult` in
   [postgres.go:2729](../../server/internal/db/postgres/postgres.go) and the
   sqlite mirror): set `status = ResultStatusAbandoned` instead of
   `error` + `abandoned=true`. Keep the atomic
   re-assert-current-status-in-WHERE claim semantics and the
   `abandonedResultOutput()` payload.
3. **Migration** — new consolidated file for the upcoming release, both
   dialects (`016_v0_17_0.up.sql` / `.down.sql` if v0.17.0 is still uncut;
   follow the version-named-migration rule in `server/CLAUDE.md`; do NOT edit
   the already-applied `015_v0_17_0` in place — dev DBs that ran it would
   silently skip the change):
   - `UPDATE results SET status = 9 WHERE period_type = 'raw' AND status = 6 AND abandoned;`
   - `ALTER TABLE results DROP COLUMN abandoned;` (drop the Postgres column
     comment with it; SQLite supports `DROP COLUMN`).
   - The partial index `idx_results_lifecycle_pending` (`status = 1`) is
     untouched.
4. **Status consumers** — add the new case deliberately, not mechanically:
   - `handlers/results/service.go`: `statusIntToString` (new string
     `"abandoned"`) and `mapStatusStringsToInts` (filterable on its own;
     decide whether `down` should keep excluding it — it should, an abandoned
     row is precisely *not* downtime).
   - `handlers/checks/service.go:2657` status switch (lastResult shaping).
   - `handlers/badges/service.go:725,751`: treat 9 like the lifecycle-marker
     skip for availability; for a "current status" badge an abandoned last
     row reads as unknown, not down.
   - `handlers/incidents/failure_details.go:97` switch.
   - Aggregation ([job_aggregation.go](../../server/internal/jobs/jobtypes/job_aggregation.go)):
     `processRawResult` already routes through `ExcludedFromAvailability`;
     verify the non-measurable status list at line ~326 and the rollup-status
     derivation don't need 9 added explicitly. The existing
     `job_aggregation_abandoned_test.go` invariant (an abandoned row moves
     nothing) must keep passing with the new encoding.
   - `uptimebar/bucketing.go` and `handlers/availability` route through the
     shared predicates — verify, don't fork.
   - `agentws/handler.go:997` validates inbound agent status to
     `[Created..Error]`; it must keep rejecting 9 (abandonment is
     server-minted). Leave the range as is.
   - LastResult queries ([postgres.go:2557](../../server/internal/db/postgres/postgres.go)
     and sqlite mirror) deliberately *include* reaped rows as a legitimate
     last-checked entry — that stays true with status 9 (only `created` is
     excluded).
5. **Do not conflate** with `checkworker.ErrCheckerAbandoned`
   ([worker.go:56](../../server/internal/checkworker/worker.go)) — the
   watchdog's "checker didn't honor its context" writes a genuine `error`
   result that legitimately counts as a failure. That concept keeps its name
   and its semantics; only the DB column disappears.

### API / frontend

- `openapi.yaml` currently has no `abandoned` field documented and dash0
  never reads one — the blast radius is the status enum only. Add
  `abandoned` wherever result status strings are enumerated (OpenAPI enum,
  generated client, dash0/status0 status label+color maps, uptime bar/timeline
  rendering). Render it as a neutral "not counted" state (muted/grey), never
  as red.

### Tests

Update/extend the spec-2026-08-18-03 suites to the new encoding, keeping
their positive controls: `reap_abandoned_results_test.go` (both dialects),
`last_result_test.go` (both), `models/result_test.go`,
`job_aggregation_abandoned_test.go`, `uptimebar/bucketing_test.go`,
`availability/service_test.go`. Add a migration test proving an existing
`status=6, abandoned=true` row comes out as `status=9` and the column is
gone.

## Open questions

- Exact migration number/version depends on whether v0.17.0 has been cut by
  implementation time — follow the migration-naming rule, never renumber
  applied files.

## Implementation Plan

### Resolved: migration number

v0.17.0 is **not yet cut** (`015_v0_17_0` is the newest, unreleased). Per the
migration-naming rule the new file keeps the same upcoming-release name and takes
the **next free number**: `016_v0_17_0.up.sql` / `.down.sql` on both engines.
`014` stays a gap (a withdrawn migration whose number still lives in some
`bun_migrations`), and `015` is **not** edited — it has already been applied on dev
databases and `internal/db/migrationguard` checksums it at boot.

### Steps

1. **Enum & model** (`internal/db/models/result.go`)
   - `ResultStatusAbandoned ResultStatus = 9`, documented as server-minted only
     (the reaper), never reported by a worker or agent.
   - `StatusToString`: 9 → `"ABANDONED"`.
   - New `ResultStatus.ExcludedFromAvailability()` holding the rule
     (`abandoned || lifecycle marker`); `(*Result).ExcludedFromAvailability()`
     delegates to it so its three call sites (hour rollup, uptimebar union,
     `RawAvailability`) keep the single shared predicate.
   - Delete the `Abandoned bool` field.

2. **Reaper** (`ReapAbandonedResults` / `claimAbandonedResult`, both dialects)
   - `Set("status", ResultStatusAbandoned)`, drop `Set("abandoned", true)`.
   - Candidate scan stays `status IN (created)` — 9 is terminal, so re-runs stay
     no-ops and a `running` heartbeat row is still never touched.
   - `abandonedResultOutput()` payload unchanged.

3. **Migration `016_v0_17_0`** (both engines), in this order:
   - `update results set status = 9 where period_type = 'raw' and status = 6 and abandoned;`
     — converts existing reaped rows and, by requiring `abandoned`, cannot sweep a
     genuine `error`.
   - `alter table results drop column abandoned;` (Postgres: `if exists`; the
     column comment goes with the column).
   - `.down.sql` re-adds the column and reverses the conversion (parity only).
   - `idx_results_lifecycle_pending` (`status = 1`) untouched.

4. **Status consumers** — deliberately, one at a time:
   - `handlers/results/service.go`: `statusIntToString` → `"abandoned"`;
     `mapStatusStringsToInts` gains its own `abandoned` filter key, and `down`
     keeps excluding it (an abandoned row is precisely *not* downtime).
   - `handlers/checks/service.go` `resultStatusString` → `"abandoned"`.
   - `handlers/badges/service.go` `calculateStatusDuration`: skipped like a
     lifecycle marker in both loops, so a "current status" badge reads unknown
     rather than down.
   - `handlers/incidents/failure_details.go`: added to the typed switch (the
     `exhaustive` linter requires it) alongside the other non-specific statuses.
   - `GetLastResultForChecks` (both dialects): **still only `created` is
     excluded** — a reaped attempt is terminal and stays a legitimate, if
     uninformative, "last checked" entry, exactly as before the refactor. The
     `Created`-only narrowing (heartbeat's long-lived `running`) survives.
   - Aggregation: `processRawResult` already routes through the shared predicate;
     `lifecycleMarkerStatuses()` / `measurableSourceUIDs()` deliberately keep
     **not** listing 9, so an abandoned row is still a deletable rollup source
     (same as its status-6 predecessor) while contributing nothing to the stats.
   - `uptimebar/bucketing.go`, `handlers/availability`: verified as transitive,
     not forked.
   - `handlers/agentws/handler.go` range check `[Created..Error]` left as is, so
     an agent can never report 9.
   - `checkworker.ErrCheckerAbandoned` untouched — a genuinely failed execution.

5. **API / frontend**
   - `openapi.yaml`: `abandoned` added to the `LastResult`, `LastResultListItem`
     and `OrgResult` status enums; `pkg/client` regenerated.
   - dash0 `lib/status-style.ts`: explicit neutral/grey case (never red,
     `isDown: false`) + `status.abandoned` in en/fr/de/es; `api/hooks.ts`
     `lastResult.status` union.
   - status0 `lib/status-style.ts` + `status.abandoned` label in en/fr/de/es
     (its response-time chart already colours anything unrecognised neutral).

6. **Tests** — ported to the new encoding, positive controls kept:
   - `models/result_test.go`, `job_aggregation_abandoned_test.go`,
     `uptimebar/bucketing_test.go`, `handlers/availability/service_test.go`,
     `reap_abandoned_results_test.go` (both), `last_result_test.go` (both).
   - New migration test on both engines: a pre-existing
     `status = 6, abandoned = true` row comes out as `status = 9`, a genuine
     `status = 6, abandoned = false` row is left alone, and the column is gone.
