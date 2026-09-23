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
