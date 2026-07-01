# Heavy-check demand control — enforce `MinPeriod` server-side, surface duty cycle, default-on cost-aware timeout

## Context

The 2026-07-01 dev-instance measurement (see `2026-07-01-03`) found the
scheduler drowning in a load **no scheduler can fix**: 20 `browser` checks on a
**10 s period with ~10 s execution cost — duty cycle ≈ 1.0 each**, i.e. each
check demands a permanently-occupied runner goroutine, ~20 of a 25-slot pool.
Specs `2026-07-01-02`/`-03` make the scheduler *fair* under overload; this spec
attacks the **demand side**: stop the platform from accepting check
configurations that are physically unserviceable, tell users when an existing
check is eating a full-time slot, and shrink how long a slow check can hold a
slot.

Today the guardrail half-exists: `CheckTypeMeta` has had `MinPeriod` /
`MaxPeriod` fields all along, they are exposed to the dashboard
(`minPeriodSeconds` via the check-types API) and the form respects them — but
the **API itself never validates the period** (any client, script, or older UI
can create a 1 s browser check), and `browser` — the single most expensive
check type — **declares no `MinPeriod` at all**.

## Current state (verified 2026-07-01; re-verify at build)

| Concern | Location | Today |
|---|---|---|
| Period metadata | `checkers/checkerdef/types.go:230-238` (`CheckTypeMeta.MinPeriod/MaxPeriod/DefaultPeriod`) | exists; set only for `ssl` (1 h), `domain` (6 h), `dnsbl` (15 min); `dns`/`ntp`/`freebox_line`/`sleep` set only `DefaultPeriod` |
| `browser` meta | `checkers/checkerdef/types.go:275` | `labelUnsafe, labelReqChrome` — **no MinPeriod, no DefaultPeriod** (global 1-min default applies; user can set 10 s) |
| API exposure | `handlers/checktypes/service.go:19,137` (`minPeriodSeconds`) | metadata reaches the dashboard |
| Frontend enforcement | `web/dash0/src/components/shared/check-form.tsx` + `src/api/hooks.ts` | form clamps to `minPeriodSeconds` — **frontend-only** |
| Server enforcement | `handlers/checks/service.go:898-905` (create; PATCH analogous) | period parsed and stored **without any validation** against meta |
| Per-check cost signal | `check_jobs.cost_ewma_ms` (one row per region) | internal to the scheduler; **not exposed** on the checks API, invisible to users |
| Cost-aware timeout | `scheduling/scheduling.go:138-157` (`ExecutionTimeout`), config `cost_timeout_factor`/`cost_timeout_floor_ms` (`config.go:492-497`) | **off by default** (factor 0 ⇒ flat 30 s); `cost==0` falls back to the floor, not 30 s, once enabled |
| Aggregate visibility | `GET /api/mgmt/scheduling/cost-distribution` | fleet-level percentiles only, super-admin |

## Design decisions

### D1 — The API validates the period; the form stays a convenience

On create **and** PATCH, resolve the effective bounds — `meta.MinPeriod` (else
a global floor, see D2) and `meta.MaxPeriod` (else none) — and reject
violations with `VALIDATION_ERROR` and an explicit message ("period for
browser checks must be at least 60s"). Frontend-only validation is not
enforcement; any API/PAT/MCP client bypasses it today.

**Grandfathering:** existing rows are untouched (no migration rewrites user
config). The limit bites on the next write to the check. Internal checks
(`check.Internal`, e.g. worker self-stats) and the synthetic `sleep` type are
exempt — `sleep` is the load-harness dial from `2026-07-01-01` and must stay
free to express pathological mixes.

### D2 — Heavy types declare a `MinPeriod`; a modest global floor backstops the rest

Recommended values (open decision — confirm before build):

| Type | MinPeriod | DefaultPeriod | Rationale |
|---|---|---|---|
| `browser` | **60 s** | **5 min** | headless-Chrome cost measured ≈ 10 s; duty ≤ ~0.17 at the floor |
| `js` | **30 s** | 1 min | scripting, measured cost ≈ 0.4 s but unbounded by construction |
| global floor (all types) | **10 s** | 1 min (existing) | matches today's de-facto smallest period; keeps sub-10 s checks (which the results/aggregation model isn't built for) out |

Everything else keeps its current metadata. Values live in the
`checkTypesRegistry` literal so `activation_test.go` and the checktypes API
pick them up with no new plumbing.

### D3 — Surface the duty cycle on the check itself, not just fleet-wide

Users cannot see that their check is a slot hog. Add a read-only
`scheduling` block to the check detail response (GET by uid/slug), derived
from the check's `check_jobs` rows (max across regions):

```json
"scheduling": { "costEwmaMs": 10036, "delayEwmaMs": 13432, "dutyCyclePct": 100 }
```

`dutyCyclePct = round(100 × cost_ewma_ms / period_ms)`, omitted until first
run (`cost_ewma_ms = 0`). Dashboard: on the check detail page, a warning alert
when `dutyCyclePct ≥ 50` — "This check takes ~10s to run every 10s and
occupies a runner continuously; increase its period." Use the alert primitive
from the design reference (`web/dash0/src/routes/orgs/$org/design-reference.tsx`),
translate in all four locales (`en`/`fr`/`de`/`es` `checks.json`), keep the
layout mobile-usable. List responses stay lean (no join fan-out): detail only.

### D4 — Cost-aware timeout goes default-on, with the cold-start hole fixed

Defaults change from off to `cost_timeout_factor = 3`,
`cost_timeout_floor_ms = 5000` ⇒ `timeout = clamp(3 × cost_ewma, 5 s, 30 s)`:
a 200 ms check's worst-case slot occupancy drops 30 s → 5 s, while measured
slow checks keep the full ceiling (3 × 10 s clamps to 30 s).

Prerequisite fix in `ExecutionTimeout`: **`cost == 0` (never ran) must return
`DefaultExecutionTimeout`, not the floor** — otherwise a brand-new browser
check's first run gets 5 s, times out, and pins its cost to the ceiling before
it ever measures honestly. (The current comment claims a no-signal job "is
never starved of time"; with a configured floor that is false today.) Config
stays overridable; `0` still disables.

## Implementation

1. `checkerdef/types.go`: registry values per D2; global floor constant.
2. `handlers/checks/service.go`: shared `validatePeriodForType(type, period)`
   used by create + PATCH (PATCH must also re-validate when only `type`
   changes, if type changes are allowed there); exemptions per D1; unit tests
   per type/bound/exemption.
3. `scheduling/scheduling.go`: `cost == 0` → default ceiling in
   `ExecutionTimeout` (+ test); `config.go`: new defaults + doc comments.
4. Checks API: `scheduling` block on detail GET (join the check's `check_jobs`
   in the existing detail query path — no N+1 on lists); `openapi.yaml` +
   generated client (`make generate`).
5. dash0: duty-cycle warning alert on check detail; period `min` already
   honors `minPeriodSeconds` from the API — verify `browser`/`js` now carry it;
   locale strings ×4. Frontend lint is red on base — scope to no **new**
   errors [[project_dash0_eslint_debt]].
6. Docs (`web/docs/`): note the per-type minimum periods on the checks page.

## Out of scope

- Scheduler-side fairness/lanes — `2026-07-01-02`, `2026-07-01-03`.
- Per-org execution budgets/rate caps beyond the existing
  `MaxChecksPerMinute` entitlement — `specs/backlog/2026-03-30-org-check-rate-limit.md`.
- Rewriting existing over-aggressive checks (grandfathered, D1).
- Billing/entitlement-tiered minimum periods (could later lower the floor for
  paid tiers; keep the mechanism plan-agnostic now).

## Verification

```bash
make build && make lint && make test        # [[feedback_lint_strict]]
make generate                               # openapi client after API change
make test-dash                              # Playwright E2E [[feedback_browser_testing]]
```

- API: creating a `browser` check with `period=10s` → 400 `VALIDATION_ERROR`
  (message names the 60 s floor); `period=60s` → 201; PATCH shrinking below
  the floor → 400; `sleep` with `period=10s` → 201 (exempt); internal checks
  exempt; global 10 s floor on a plain `http` check.
- `ExecutionTimeout`: `(factor=3, floor=5s)` ⇒ cost 0 → 30 s, cost 200 → 5 s,
  cost 4 000 → 12 s, cost 20 000 → 30 s.
- Detail API returns `scheduling` with the max-across-regions EWMAs; absent
  before first run; not present on list responses.
- E2E: check detail shows the duty-cycle warning for a seeded high-duty check
  and hides it for a fast one; all four locales render; mobile viewport OK.
- Manual on the dev instance: the 20 grandfathered browser@10s checks now show
  100% duty-cycle warnings, and re-saving one forces the new floor.

## Key files

| File | Change |
|---|---|
| `server/internal/checkers/checkerdef/types.go` | **~** `MinPeriod`/`DefaultPeriod` for `browser`, `js`; global floor |
| `server/internal/handlers/checks/service.go` (+`_test`) | **~** period validation on create/PATCH; `scheduling` block on detail |
| `server/internal/checkworker/scheduling/scheduling.go` (+`_test`) | **~** `cost==0` → default ceiling |
| `server/internal/config/config.go` | **~** cost-timeout defaults on |
| `server/internal/app/openapi/openapi.yaml` | **~** validation errors + `scheduling` block |
| `web/dash0/src/components/shared/check-form.tsx`, `src/api/hooks.ts` | **~** verify min-period plumbing for newly-floored types |
| `web/dash0/src/routes/orgs/$org/checks/…` (detail) | **~** duty-cycle warning alert |
| `web/dash0/src/locales/{en,fr,de,es}/checks.json` | **~** warning + error strings |
| `web/docs/` checks page | **~** document per-type minimums |

## Risk log

| Risk | Mitigation |
|---|---|
| Breaking change for API clients creating fast heavy checks | Intended and explicit (`VALIDATION_ERROR` with the floor in the message); grandfathering avoids breaking existing monitors; documented in docs site. |
| Default-on cost timeout cuts off a legitimately slow first run | D4's `cost==0` fix gives first runs the full 30 s; after one honest measurement the clamp is `≥ 3×` measured cost. |
| A check alternating fast/slow gets a too-tight timeout | Floor 5 s + factor 3 + EWMA α=0.3 tolerate 3× regressions; timeout pins cost to ceiling next run, restoring 30 s. |
| Detail-endpoint join regresses hot paths | Detail-only (no lists); single indexed lookup by `check_uid`. |
| dash0 lint debt blocks CI | No-new-errors scope [[project_dash0_eslint_debt]]. |

**Status**: Todo | **Created**: 2026-07-01 | **Complements**: `2026-07-01-02`, `2026-07-01-03` (demand side of the same measurement) | **Independent**: can ship in any order relative to them
