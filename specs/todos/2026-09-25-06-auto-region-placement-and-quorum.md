---
model: opus
effort: max
---

# Every check is pinned to the regions it was created with, so one dead region stops it, and nobody chose that

## Problem

**Regions are resolved once and frozen, with no record of intent.**

- `ResolveRegionsForCheck` picks the check's own list, then the org
  `default_regions`, then the system `default_regions`, then every region
  ([regions.go:248-286](server/internal/regions/regions.go#L248)).
- The result is written into `checks.regions` on create and update
  ([service.go:1480, 1878-1885](server/internal/handlers/checks/service.go#L1480)).
  The check never follows later default changes.
- The dash0 form seeds its picker from `defaultRegions` and always sends an
  explicit list (`check-form.tsx:732, 1108-1119`). Every new check is therefore
  **pinned**, whether the user chose its region or not.

What this looked like in prod on 2026-09-24:

- 135 of 182 enabled checks ran from exactly one region.
- The system default is `["gravelines"]`, so 67 checks outside the
  showcase org are pinned to gravelines only. Nobody picked that.
- When `lauterbourg` went dark for 8 hours, the 12 checks pinned to it stopped
  completely. Paris, Gravelines and Nuremberg were healthy the whole time.

**Nothing can move a check.**

- `region_spread` is only a timing offset between regions
  ([phase.go:95-105](server/internal/checkworker/scheduling/phase.go#L95)).
- The only mover is the super-admin `MigrateRegion`, which renames a slug
  server-wide.
- At boot, `ReconcileStaleJobSchedules` "heals" any job whose region is not in
  `checks.regions`
  ([region_migration.go:54-88](server/internal/db/postgres/region_migration.go#L54)).
  So any placement that is not written into `checks.regions` gets undone.

**The region list has no grouping.** Entries are `{slug, emoji, name,
capabilities}` ([regions.go:16-31](server/internal/regions/regions.go#L16)).
Prefix matching exists, but prod slugs (gravelines, paris, …) don't use it.

**Multi-region confirmation is implicit and invisible.**

- `ProcessCheckResult` is one state machine that ignores the region
  ([incidents/service.go:605-702](server/internal/handlers/incidents/service.go#L605)).
- With 2+ regions and the default 120 s confirmation, **one failing region
  never opens an incident**. Any success clears the confirmation clock (`:773-778`)
  and sets `up` (`:907-928`), so the status flips between up and validating.
  An incident opens only when *every* region fails for the whole window.
- That is an accidental "all regions agree" rule. A real regional failure
  (target unreachable from Asia only) is never surfaced.
- `wiki/competitors/positioning.md:300-303` already sells "distributed
  multi-region confirmation", which does not exist.

**Cost.** `maxChecksPerMinute` demand is `Σ max(1, len(regions)) × 60/period`
([check_rate.go:102-134](server/internal/entitlements/check_rate.go#L102)), so
each region counts as a run.

## Proposal

### Part A: placement

#### A1. Record intent: `pinned` or `auto`

New check fields:

| Field | Meaning |
|---|---|
| `placement` | `pinned` or `auto` |
| `regionCount` | auto only: N, how many regions run the check (default 2) |
| `regionPool` | auto only: candidate slugs, or empty for "any cloud region" |

- **Pinned:** `regions` is the user's explicit list. It is never moved. If its
  region goes dark, the check goes stale (spec `2026-09-25-02`) and the org gets
  the region notice (spec `2026-09-25-03`).
- **Auto:** `regions` holds the **current placement**, written by the
  scheduler. Keeping the placement in `checks.regions` keeps the boot repair,
  `reconcileCheckJobs`, the phase computation and the rate accounting correct
  with no change.
- **Private regions are pinned-only.** Credential sealing keys off the check's
  `@` regions (`service.go:5115-5140`), and a private target may only be
  reachable from inside. Auto never places into, or out of, an `@` region.
- **Capabilities are respected.** A browser check is never placed in a region
  without the `browser` capability (tokyo today), and an IPv6-only target never
  in a region without IPv6.

Initial placement order:
1. the org `default_regions`, then the system `default_regions`, then the rest
   of the region list;
2. filtered by the pool, the required capabilities and region health;
3. the first N are taken.

It is deterministic, so the same inputs give the same placement.

#### A2. Re-place when a region goes dark

The per-minute region health sweep (spec `2026-09-25-03`) drives it. When a
cloud region turns dark, for every **auto** check placed there:

- Pick the next healthy region from the check's candidate order that it does not
  already use. Rewrite `checks.regions` and reconcile its jobs (same mechanics
  as `MigrateRegion`, `region_migration.go:73-90, 295-324`).
- The new job is due **now**: the check has already been blind for about
  5 minutes.
- In-flight runs from the old region may still land. Their results keep their
  real region, and their lease release fails harmlessly
  (`checkjobsvc/service.go:905-926`).
- Write a check event `check.placement_changed` `{from, to, reason}` for the
  timeline and audit.
- If there is no healthy candidate left, keep the placement. The check goes
  stale, same as a pinned one.
- **No automatic move back** in v1. A recovered region moves nothing. Placements
  stay stable, phases stay stable, and there is no flapping.

#### A3. Defaults and migration

- New checks default to **auto, N = 2** when 2+ eligible cloud regions exist,
  otherwise N = the number available.
  - If the org's `maxChecksPerMinute` cannot fit N runs per period, create with
    the largest N that fits and say so in the response and the form.
- The dash0 form shows "Regions: Automatic (2 regions) · change". "Choose
  regions" switches to pinned. The form stops seeding an explicit list
  (`check-form.tsx:732`); it sends `placement: auto`.
- Existing checks migrate as **pinned**, so behavior doesn't change. One
  exception, **recommended**: a check whose `regions` equals the system
  `default_regions` list becomes `auto` with `regionCount = len(regions)` and an
  empty pool.
  - Its cost and its normal region stay the same, and it gains failover.
  - This is the 67-checks-on-gravelines case.
  - Announce it in the changelog. Decide before implementing (see open
    questions).
- Add a bulk action "Switch to automatic placement" on the checks list, and the
  same fields in config-as-code.

#### A4. Surfaces

- **Check detail:**
  - "Placement: automatic, 2 regions: paris, gravelines";
  - placement history ("lauterbourg → paris at 13:47, region offline");
  - per-region last result.
- **API and OpenAPI:**
  - Add `placement`, `regionCount`, `regionPool` to `Check`,
    `CreateCheckRequest` and `UpdateCheckRequest`.
  - Also add the missing `regions`: today the schemas only have
    `regionSpread` (`openapi.yaml:9887, 10312, 10456`).
- **MCP:** expose the new fields on `create_check` and `update_check`. Fix the
  `regions` descriptions (spec `2026-09-25-01-mcp-…`).
- **Config-as-code:**
  - export v2 and the plan diff carry the fields;
  - the dry run and the real upsert agree;
  - an empty `regions` with `placement: auto` is meaningful (`plan.go:394-403`,
    `service.go:2182`).
- **status0:** unchanged. It already merges per-region series.

### Part B: explicit multi-region quorum

Separable. Land A first, then B in the same spec or split it out.

#### B1. Per-region state

Keep the last status per (check, region), in a small table or derived from the
latest result per region. `ProcessCheckResult` updates the region's state first,
then derives the check's status from all region states. It stops treating every
result as if it described the whole check.

#### B2. `failQuorum`

A check setting:

- default `all` for N ≤ 2 (today's behavior, made explicit);
- default `majority` for N ≥ 3;
- also accepts an integer.

Derived check status:

- **down:** at least `failQuorum` regions have been failing for the
  confirmation period. The incident opens as today.
- **warning ("regional issue"):** some regions failing, fewer than the quorum.
  Shown with the failing regions. No incident, but visible, unlike today's
  silent up/validating flip.
- **up:** all regions passing.
- Recovery mirrors the rule with the recovery period.

This replaces the accidental "all regions agree" behavior with a stated one.
Once shipped, the positioning claim becomes true; until then, fix the wiki.

### Tests

- Auto N=2 with pool = any: placed on the first two healthy eligible regions in
  default order. A browser check skips non-browser regions.
- One placed region goes dark: the check is re-placed within one sweep, the new
  job is due immediately, one `check.placement_changed` event is written, and
  the results carry the new region.
- No healthy candidate left: placement is kept and the check goes stale.
- The region recovers: nothing moves.
- Pinned checks and `@` regions are never moved.
- Restart after a re-placement: the boot repair keeps the new placement.
- Migration: only checks equal to the system default list become auto (if
  adopted). All others stay pinned with identical jobs.
- Rate cap: N is reduced to what `maxChecksPerMinute` allows, with a message.
- Quorum: N=3, one region failing gives a regional warning and no incident; two
  failing past confirmation gives down and an incident. N=2 behaves as today.

## Open questions

1. **Migration of defaulted checks.** Adopt the recommendation (defaulted →
   auto) or keep everything pinned and rely on the bulk action?
2. **Default N.** Is 2 right for every plan? Free-plan cost doubles per check
   compared to today's single default region.
3. **Pools by area.** Add an `area` field (eu/na/asia) to the region list and
   `SP_REGIONS`, so "any EU region" is expressible, or are explicit slug lists
   enough for v1?
4. **Move back.** Should a recovered region win its checks back after it has
   been healthy for a while (to rebalance load), or does stability win?

## Resolved open questions

1. **Migration of defaulted checks.** Decision: adopt the recommendation — a check whose
   `regions` equals the system `default_regions` list migrates to `auto` with
   `regionCount = len(regions)` and an empty pool. Placement and cost stay identical (same
   regions, same run count); it only adds failover capability. This is the
   67-checks-on-gravelines case from A3, and it must be announced in the CHANGELOG since those
   checks gain new (though initially inert) re-placement behavior without an explicit opt-in.
2. **Default N.** Decision: keep N=2 as the general default for new checks. Let the existing
   "largest N that fits `maxChecksPerMinute`" rule (A3) cap it down for constrained plans —
   free-plan checks that can't fit 2 runs per period fall back to N=1 automatically, same as
   today's single-region behavior, rather than hard-coding a separate default for that tier.
3. **Pools by area.** Decision: ship v1 with explicit slug pools only — no `area` field on the
   region list or `SP_REGIONS`. Add area-based grouping (eu/na/asia) later as a follow-up if
   "any EU region" demand shows up; don't build the metadata/migration for it speculatively now.
4. **Move back.** Decision: no automatic move back in v1, exactly as A2 already states —
   stability wins. A recovered region moves nothing; there is no rebalancing and no flapping.
   (This reconciles the Open Questions list with the Proposal, which already committed to this
   answer in §A2; the two sections were just out of sync in wording, not in substance.)

## Dependencies

- `2026-09-25-01`: region health must be correct before it drives placement.
- `2026-09-25-03`: the per-minute region health sweep triggers re-placement.
- `2026-09-25-02`: pinned checks in a dark region go stale instead of lying.

## Implementation Plan

### Scope decision (made before coding): Part A in full, Part B deferred

Part A alone touches the schema (both engines), the create/update/upsert/clone
write paths, a new pure placement algorithm, the per-minute region sweep, the
config-as-code export/import/diff/apply pipeline, the OpenAPI document and the
generated client, the MCP tools, and three dash0 surfaces (form, detail, list).
Part B rewrites the incident state machine (`ProcessCheckResult`), which every
check on every engine goes through on every result. Landing both in one pass
would put the riskiest change in the batch behind the largest one, with no
room to verify either properly.

**Decision: implement A1–A4 completely, verify them, and do not start B.** B is
left for its own spec (the spec explicitly allows the split). Nothing of B is
half-implemented. The one B-adjacent deliverable the spec asks for "until then"
is done here: the wiki's positioning claim of "distributed multi-region
confirmation" is corrected to say what the product does today.

### Resolved behaviors (from the spec's resolved questions, made concrete)

- `placement` is `pinned` or `auto`. Existing rows default to `pinned`
  (column default), except the migration case below.
- Private (`@`) regions are pinned-only: auto never places into one, a pool may
  not name one, and a check whose regions include one cannot be auto.
- Passive checks (heartbeat, email, private-location) have no regions: always
  `pinned`, `regionCount`/`regionPool` null.
- Default on create when the request names no placement and no regions:
  `auto`, `regionCount = min(2, eligible)`, except when the org's own
  `default_regions` names a private region (then the old pinned resolution).
  A request with an explicit `regions` list and no placement stays `pinned`
  (backward compatible). `regionCount`/`regionPool` without `placement` imply
  `auto`.
- `regionCount` is stored as the effective N: capped by the number of eligible
  candidates, then by `maxChecksPerMinute` (largest N that fits, floor 1) with
  an advisory warning `PLACEMENT_REGION_COUNT_REDUCED` in the response.
- No automatic move back, ever (resolved question 4).
- No `area` field (resolved question 3): pools are explicit slug lists.

### A1. Record intent + initial placement

- **Migration** (appended as a new `SECTION: auto-region-placement` to the
  unreleased `024_v0_33_0` on both engines, up + down):
  `checks.placement text not null default 'pinned'` (CHECK in pinned/auto),
  `checks.region_count int null`, `checks.region_pool text[]` (Postgres) /
  JSON text (SQLite).
- **Model** `server/internal/db/models/check.go`: `Placement`, `RegionCount`,
  `RegionPool` on `Check` and `CheckUpdate` (+ `ClearRegionCount`,
  `ClearRegionPool`); constants `PlacementPinned`/`PlacementAuto`;
  `IsAutoPlaced()`. DB `UpdateCheck` on both engines writes the new columns.
- **Pure algorithm** `server/internal/regions/placement.go`:
  - `CandidateOrder(orgDefaults, systemDefaults, all []string) []string` —
    org defaults, then system defaults, then the rest; de-duplicated, cloud
    only.
  - `PlacementInput{Candidates, Pool, Required capabilities, Capabilities
    index, Healthy set, Current, Count}` and `Place(input) []string`:
    eligible = candidates ∩ pool, minus any region whose required capability is
    an explicit "no" ("unknown" stays eligible). Keep current regions that are
    still eligible and healthy (stability), fill from candidate order with
    healthy regions first, then (only if still short) unhealthy eligible ones.
    When no eligible region is healthy (fresh install, tests), health is
    ignored entirely so the result is the plain candidate order.
    Deterministic: same inputs, same output (unit-tested).
  - `Replace(current, candidates, healthy, dark) ([]string, []Swap)` — the A2
    swap: every dark region of `current` is replaced in place by the first
    healthy candidate not already used; a dark region with no candidate stays.
- **Service glue** `server/internal/handlers/checks/placement.go`:
  `placementContext` loads org/system defaults, the global region list, the
  capability index, live workers per cloud region and the region-outage
  markers (`regionoutage.List`); `requiredCapabilities(type, config)` (browser
  → `browser`, `ipVersion: ipv6|ipv4` → that family); `resolvePlacement` used by
  create, update, upsert, the import dry run and the diff, so all of them agree.
  Rate cap through `entitlements.ProjectChecksPerMinute`.
- **Request validation** (`validate.go`, shared by create/validate/upsert
  through `requestFieldFindings`): `placement` enum, `regionCount >= 1`,
  `regionPool` cloud slugs only, `placement: auto` with a non-empty `regions`
  is a contradiction, `placement: pinned` with `regionCount`/`regionPool` too.
  Sentinel errors mapped to 400 `VALIDATION_ERROR` in `handler.go`.

### A2. Re-place when a region goes dark

- `checks.Service.ReplaceAutoChecks(ctx, ReplacementRequest{Region,
  CheckUIDs, Healthy, Reason})` in `placement_replace.go`: for each listed check
  that is still `auto`, enabled, not deleted and still placed in the region,
  compute `Replace`, write `checks.regions`, run the existing
  `reconcileCheckJobs` (same mechanics `MigrateRegion` uses), then set the new
  region's job `scheduled_at = effective_scheduled_at = now` (due now), and
  write one `check.placement_changed` event per swap `{from, to, reason}`
  (system actor, `check_uid` set). In-flight runs of the old job are left alone
  (their lease release fails harmlessly, results keep their real region).
- **Hook** `server/internal/regionsweep`: `Deps.Placer` (optional interface,
  the jobtypes wiring passes the same `*checks.Service`). Called from `goDark`
  AND from `stayUnhealthy` while the region is observed dark, for the auto
  checks among the sweep's own `RegionJob` snapshot in that region, before the
  org notices are computed. Healthy set = cloud regions with live workers and
  no outage phase after this sweep. Re-placed checks are excluded from the
  org's blind/reduced counts (they are not blind any more). No second poller.
- New event type `check.placement_changed` in `models/event.go`, added to the
  two exhaustive switches (`system/service.go`, `incidents/service.go`), the
  events catalogue, dash0 `event-display.tsx` + its test, and all 4 locales.
- No healthy candidate: placement kept, the check goes stale through the
  existing freshness sweep (verified by a test, not assumed).
- A recovered region moves nothing (tested).

### A3. Defaults, migration, bulk action

- Create default as described above; the dash0 form stops seeding an explicit
  region list: "Regions: Automatic (N regions) · Choose regions", sends
  `placement: auto` (or `pinned` + `regions` after "Choose regions").
- **Data migration (the 67-checks-on-gravelines case)** in the same SQL
  section: every non-deleted, non-passive check whose `regions` holds exactly
  the same set of slugs as the system `default_regions` parameter (compared as
  sets, order-insensitive, no `@` region) becomes `placement = 'auto'`,
  `region_count = cardinality(regions)`, `region_pool = NULL` (empty pool).
  Regions and jobs are untouched, so cost and normal region are identical.
  Every other check stays `pinned`.
- **Bulk action** `POST /api/v1/orgs/:org/checks/auto-placement`
  (`{checkUids?, dryRun?}` → `{data: [...switched], skipped: [...]}`),
  same conversion as the migration (keep regions, N = len(regions), empty
  pool); dash0 button on the checks list with a confirmation dialog showing the
  dry-run count.
- **Config-as-code** carries the fields (A4).
- Announcement: `CHANGELOG.md` is generated by release-please from commit
  messages, so the migration ships in a `feat(checks):` commit whose message is
  the changelog prose (flagged in the final report).

### A4. Surfaces

- **API** `CheckResponse` + `CreateCheckRequest`/`UpdateCheckRequest`/
  `UpsertCheckRequest`/`ValidateCheckRequest` gain `placement`,
  `regionCount`, `regionPool`.
- **OpenAPI** `openapi.yaml`: the three fields on `Check`,
  `CreateCheckRequest`, `UpdateCheckRequest`, `UpsertCheckRequest`, plus the
  missing `regions`; new `check.placement_changed` where event types are
  listed; the bulk endpoint; regenerate `pkg/client`.
- **MCP** `tools_checks.go`: `placement`, `regionCount`, `regionPool` on
  `create_check`/`update_check`; the `regions` descriptions extended (the
  earlier fix kept).
- **Config-as-code**: `ExportCheck` + v2 wire (`placement`, `regionCount`,
  `regionPool`; an auto check exports no `regions` and is left out of the modal
  regions default, and the v2 resolver does not apply the default regions to an
  auto entry); `buildImportUpsertRequest`; `diffCheck` compares placement /
  regionCount / regionPool (and not the scheduler-owned regions of an auto
  check); `planUpdateCheck`/`PlanUpsert` run the same placement resolution, so
  the dry run and the real upsert agree. `placement: auto` with empty
  `regions` is meaningful on update (switches the check).
- **dash0**: `Check` type fields; form (above); check detail "Placement"
  card: "Automatic, 2 regions: paris, gravelines" or "Pinned: …", per-region
  last result (from `regionFreshness`), placement history from
  `check.placement_changed` events; checks-list bulk action; design reference
  entry for the new placement summary if it is a new pattern.
- **status0**: unchanged.
- **Docs**: `web/docs/docs/features/region-placement.md`.

### Tests (mapping to the spec's list)

1. Auto N=2 pool any → first two healthy eligible in default order; browser
   skips non-browser: `regions/placement_test.go` + service test.
2. Region goes dark → re-placed in one sweep, new job due now, one
   `check.placement_changed`, (results carry the new region — the job row now
   carries the new region, which is what the worker stamps): `regionsweep`
   test.
3. No healthy candidate → placement kept, the freshness sweep marks it stale.
4. Region recovers → nothing moves.
5. Pinned checks and `@` regions never moved.
6. Restart after re-placement → `ReconcileStaleJobSchedules` keeps it.
7. Migration → only default-equal checks become auto, others pinned with
   identical jobs (SQLite migration test + Postgres twin).
8. Rate cap → N reduced with a warning.
9. Quorum → **deferred with Part B**.
