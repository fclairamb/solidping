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
