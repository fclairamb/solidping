---
model: opus
effort: high
---

# A single failing region can never open an incident, because every result is treated as describing the whole check

## Problem

`ProcessCheckResult` is one state machine that ignores which region a result
came from ([incidents/service.go:605-702](server/internal/handlers/incidents/service.go#L605)).
With 2+ regions and the default 120 s confirmation, **one failing region never
opens an incident**: any success from any other region clears the
confirmation clock and sets `up`, so the status flips between up and
validating. An incident opens only when *every* region fails for the whole
window. That is an accidental "all regions must agree" rule, not a stated
one. A real regional failure (target unreachable from Asia only) is never
surfaced.

`wiki/competitors/positioning.md` sold "distributed multi-region
confirmation" before this existed; a sibling spec
(`specs/done/2026/09/2026-09-25-06-auto-region-placement-and-quorum.md`) has
already added a dated correction there noting the claim doesn't hold yet.
This spec is what makes it true.

## Split from a sibling spec

This was originally Part B of
`specs/done/2026/09/2026-09-25-06-auto-region-placement-and-quorum.md`
("A1-A4: placement", "B1-B2: quorum"). That spec's own text explicitly
allowed splitting B out: "Separable. Land A first, then B in the same spec
or split it out." Part A (placement: `pinned`/`auto`, re-placement on region
health, migration of existing checks, every consumer surface) shipped in
full, verified by an independent audit with two fix rounds, and is already
in `specs/done/`. Part B was deliberately not started — not half-implemented,
not half-broken, genuinely zero code — so it is reproduced here verbatim
from that spec's Proposal and split into its own todo.

Placement (part A) is not a hard dependency for this spec's mechanics —
per-region state tracking and `failQuorum` work the same whether a check's
regions are pinned or auto-placed — but it is useful context: an **auto**
check's regions can change under it (re-placement swaps a dark region for a
healthy one), so per-region state for a region the check no longer runs in
should be treated as stale/irrelevant to the current quorum evaluation, not
carried forward as a phantom "failing region" after a swap. This spec should
account for that interaction explicitly, since it did not exist when Part B
was originally scoped.

## Proposal

### B1. Per-region state

Keep the last status per (check, region), in a small table or derived from
the latest result per region. `ProcessCheckResult` updates the region's
state first, then derives the check's status from all region states. It
stops treating every result as if it described the whole check.

Account for the placement interaction noted above: if an auto check's
regions have changed since a stored per-region state was last written (the
region is no longer in `checks.regions`), that stale per-region state must
not count toward quorum. Decide and implement a concrete rule (e.g. per-region
state keyed to the check's current region set only, or explicitly pruned on
`check.placement_changed`).

### B2. `failQuorum`

A check setting:

- default `all` for N ≤ 2 (today's behavior, made explicit);
- default `majority` for N ≥ 3;
- also accepts an integer.

Derived check status:

- **down:** at least `failQuorum` regions have been failing for the
  confirmation period. The incident opens as today.
- **warning ("regional issue"):** some regions failing, fewer than the
  quorum. Shown with the failing regions. No incident, but visible, unlike
  today's silent up/validating flip.
- **up:** all regions passing.
- Recovery mirrors the rule with the recovery period.

This replaces the accidental "all regions agree" behavior with a stated one.
Once shipped, the positioning claim in `wiki/competitors/positioning.md`
becomes true again — update that page's dated correction to say so.

### Surfaces (not in the original Part B text, but required for this to be usable)

Apply the same end-to-end discipline the placement spec (Part A) used for
its own new fields — a setting nobody can see or set is not shipped:

- **API/OpenAPI:** add `failQuorum` to `Check`, `CreateCheckRequest`,
  `UpdateCheckRequest`. Add per-region status to the check detail response
  (or reuse whatever per-region surface Part A already added via
  `regionFreshness`/`region_health`, if it fits).
- **MCP:** expose `failQuorum` on `create_check`/`update_check`.
- **Config-as-code:** export v2 and the plan diff carry the field.
- **dash0:** check detail shows the derived per-region state and the
  "regional issue" warning distinctly from `down`/`up`/`stale`; the check
  form exposes `failQuorum` (with sensible defaults pre-filled, not asking
  every user to think about it). Follow the design reference. Locale keys in
  all 4 locales.
- **status0:** confirm whether the public status surface needs a "regional
  issue" state or can keep collapsing it into existing states — this needs
  an explicit decision, not a silent default.

## Tests

- Quorum: N=3, one region failing gives a regional warning and no incident;
  two failing past confirmation gives down and an incident. N=2 behaves as
  today (`all`).
- An explicit integer `failQuorum` is respected (e.g. N=4, `failQuorum: 2`).
- Recovery mirrors the down rule with the recovery period.
- A region that stops being one of the check's regions (auto re-placement
  swapped it out) does not count toward quorum using its last known state.
- The existing "all regions agree" behavior is preserved exactly for the
  N ≤ 2 default case — this must not regress any existing single- or
  dual-region check's incident timing.

## Out of scope

- Anything in Part A (placement) — already shipped.
- A public "regional issue" surface on status0, unless the surfaces section
  above resolves that it's needed for v1.

## Resolved open questions

- **status0: distinct "regional issue" state, or collapse it?** Keep it collapsed into the
  existing public states for v1. status0 gets no new state, no new styling and no new locale
  keys. A check in "regional issue" (warning) shows on the public page exactly as a `warning`
  check does today. The regional-issue wording lives in dash0 only.
- **Per-region state after auto re-placement (B1).** Key quorum to the check's **current**
  region set: when deriving the check status, only per-region rows whose region is in
  `checks.regions` count. Do not prune on `check.placement_changed`. This is stateless and
  cannot drift out of sync if an event is missed or reordered. Stored rows for a region the
  check left may stay in storage; they are simply ignored (a cleanup of old rows is optional
  and must never be what correctness depends on).

## Dependencies

- `2026-09-25-06` (now in `specs/done/`): this spec reuses that spec's
  per-region result data where it already exists (`regionFreshness`) rather
  than building a second one, and must account for auto re-placement
  changing a check's region set out from under stored per-region state.

## Implementation Plan

### Core decision: `all` keeps today's per-result state machine, byte for byte

The spec defines `all` as "today's behavior, made explicit", and requires the
N ≤ 2 default to keep today's incident timing exactly. Today's rule is not a
set rule ("all regions failing"), it is a per-result one: any passing result
from any region clears the confirmation clock. A set-based evaluation with
Q = N would move the clock start (it would start when the last region fails,
not at the first failure after the last success), so it cannot be
equivalent.

So the effective quorum Q is resolved against the check's current region
count N, and:

- **Legacy mode** (N ≤ 1, a passive/regionless check, or Q ≥ N — this is every
  N ≤ 2 default, `majority` of 2, and an explicit `all`): the result's own
  status drives `ProcessCheckResult` exactly as today. The code path is the
  unchanged one; the new inputs it gains (`regional`, `openable`) take values
  that reduce every expression to its previous form.
- **Quorum mode** (N ≥ 2 and Q < N): the check's signal is derived from the
  per-region states, not from the result.

### B1. Per-region state

- New table `check_region_states` (both engines, new `-- SECTION:
  multi-region-quorum` in the unreleased `024_v0_33_0` up/down):
  `(check_uid, region)` primary key, `organization_uid`, `status` (the result
  status of the region's newest real result), `status_since` (when the region
  entered its current passing/failing side), `last_result_at`, `updated_at`.
  FKs cascade on check/org delete.
- Written by `ProcessCheckResult` for every real result that carries a region,
  on a non-passive check with 2+ regions (single-region checks do not pay the
  write; they have nothing to agree with). One upsert, guarded so an older
  result processed late never overwrites a newer one. Written also inside a
  maintenance window (it is an observation, not an incident decision), after
  the freshness prelude (`freshnessAndMaintenance`) so the prelude's touch and
  live-row read stay first. Best-effort: a failed write is logged.
- The freshness prelude's live-row read (`CheckLiveState`) also returns
  `regions` and `fail_quorum`, so quorum always evaluates the check's CURRENT
  regions and setting, never a claim-time snapshot taken before an auto
  re-placement.
- **Placement interaction (resolved question):** quorum only counts rows whose
  region is in the live `checks.regions`. Rows of a region the check left stay
  in storage and are ignored. A current region with no row yet (just placed)
  counts as unknown, never failing. Also ignored: a row older than the check's
  staleness threshold (`max(3 × period, 5 min)`, `models.StaleThreshold`) —
  a silent region is the freshness feature's business, not evidence of
  failure, and this is what keeps a row written before the check spent a week
  in legacy mode from resurfacing.
- The result being processed always overrides its own region's stored row in
  the evaluation, so a lost or late upsert cannot make the current result
  invisible to its own evaluation.

### B2. `failQuorum`

- Column `checks.fail_quorum text NULL` (CHECK: NULL, `all`, `majority` or a
  positive integer). NULL = default.
- Pure package `internal/regionquorum`: parse/validate the setting, resolve it
  (`default` → N for N ≤ 2, majority for N ≥ 3; `majority` → ⌊N/2⌋+1; integer
  k → min(k, N)), decide the mode, and `Evaluate(regions, states, quorum,
  now, staleAfter)` → failing / passing / unknown regions. The incident engine,
  the API's `regionalIssue` block and MCP all use this one function.
- Wire value (`failQuorum`): `"default" | "all" | "majority" | <integer>`.
  Decodes a JSON string or number (`2` and `"2"` both work); encodes keywords as
  strings and a count as a number. Always emitted on a non-passive check.
  `effectiveFailQuorum` (read-only) is the resolved count for the current
  regions. Passive checks: the field is accepted and dropped, like regions.
- Invalid values are a 400 `VALIDATION_ERROR`, code `INVALID_FAIL_QUORUM`,
  through the shared `requestFieldFindings` (create, validate, upsert, update,
  the import dry run and plan all agree). An integer larger than the region
  count is accepted and clamped to N (the region count can change later under
  auto placement; the clamp is what `effectiveFailQuorum` shows).

### Quorum mode in `ProcessCheckResult`

After the freshness prelude and the maintenance gate, when quorum mode applies
and the result has a current region:

| failing current regions F | effective signal | visible status |
|---|---|---|
| \|F\| ≥ Q | failure (arms/keeps `first_failure_at`, clears the recovery clock) | `validating` → `down` after confirmation |
| 0 < \|F\| < Q | success for the clocks and the incident (clears the confirmation clock, arms the recovery clock, may resolve) | `warning` — the regional issue |
| F = ∅ | the result's own status (up / checker warning) | `up` / `warning` |

- An incident opens, and `failure_count` grows, only on a result that itself
  failed. A passing region's result while the quorum is failing keeps the
  status `validating` (never `down` without an incident behind it) and does
  not open anything; a failing region's result opens it within one period.
- Recovery mirrors the rule: the incident resolves once fewer than Q regions
  have been failing for the (flap-aware) recovery period — the recovery clock
  is armed by the first result with |F| < Q and cleared by any result with
  |F| ≥ Q.
- A result from a region that is no longer one of the check's regions (an
  in-flight run after a re-placement) still updates its own row, but the
  evaluation ignores that row. A result with no region falls back to legacy.
- The confirmation hold (`holdForValidatingAncestor`) and the rollup code see
  the same `down`/`validating` semantics as before; `warning` is outside
  both, as it is today.

### Surfaces

- **Model/DB:** `Check.FailQuorum`, `CheckUpdate.FailQuorum/ClearFailQuorum`,
  `CheckRegionState`, `db.Service.UpsertCheckRegionState /
  ListCheckRegionStates` (both engines + the notifications test mock).
- **API:** `failQuorum` on Create/Update/Upsert/Validate requests and on the
  check response, `effectiveFailQuorum` on the response; clone copies it.
  `with=region_freshness` (the detail's existing per-region surface) gains,
  per region, `status` and `statusSince` from `check_region_states`, and a
  check-level `regionalIssue {failingRegions, failQuorum, regionCount}` when
  the check is in quorum mode with some but fewer than Q regions failing.
- **OpenAPI:** `failQuorum` (oneOf string enum / integer) on `Check`,
  `CreateCheckRequest`, `UpdateCheckRequest`, `UpsertCheckRequest`,
  `ValidateCheckRequest`; `effectiveFailQuorum`, `regionalIssue`, the new
  `RegionFreshness` fields. `pkg/client` is not regenerated (batch
  convention).
- **MCP:** `failQuorum` on `create_check` / `update_check`; `diagnose_check`
  reports a regional issue.
- **Config-as-code:** export v2 carries `failQuorum` (omitted when default),
  import/upsert passes it, the plan diff compares it, dry run and apply agree.
- **dash0:** check detail shows the per-region state (up / failing since …)
  and a distinct "Regional issue" callout (failing regions, "1 of 3 regions
  failing, quorum 2") that never reads as `down`, `up` or `stale`; the check
  form gets a "Failure quorum" select (Default / All regions / Majority /
  Custom count) defaulting to Default, with the resolved value for the
  current region count shown next to it; design-reference entry; 4 locales.
- **status0:** no change (resolved question): a regional issue is a plain
  `warning` there.
- **Docs/wiki:** `web/docs` region-placement page gains a quorum section;
  `wiki/competitors/positioning.md` dated correction updated to say
  multi-region quorum now ships.

### Tests

- `regionquorum` unit tests: parsing, wire encoding, resolution per N, mode,
  evaluation (non-current and stale rows ignored, unknown regions).
- Incident engine (SQLite, plus a Postgres twin of the core scenarios):
  1. N=3 default: one region failing → `warning`, no incident, for longer
     than the confirmation period; a second region failing past confirmation
     → `down` + incident.
  2. N=4, `failQuorum: 2`: respected.
  3. Recovery: fewer than Q failing for the recovery period resolves; a
     relapse to Q inside the window resets the clock.
  4. Auto re-placement: a failing region swapped out of `checks.regions`
     does not count; the swapped-in region counts as unknown until it reports.
  5. N ≤ 2 equivalence: a scripted N=2 timeline (alternating failure/success
     never opens; continuous failure opens at exactly the confirmation
     period; one success mid-window resets) produces the same statuses,
     clocks and incident as the same timeline on a regionless check, and the
     whole pre-existing incidents suite passes unchanged.
- API/validation: create/update/validate/upsert accept `"all"`, `"majority"`,
  `2`, `"2"`, `"default"`; refuse `0`, `"half"`, `true`; response round trip;
  config-as-code export → plan → apply round trip with no diff.
- dash0: unit test for the regional-issue helper; Playwright e2e for the form
  field and the detail's regional-issue rendering.
