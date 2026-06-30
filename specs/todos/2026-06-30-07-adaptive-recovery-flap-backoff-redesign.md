# Redesign adaptive recovery: flapping backoff with a cap (rename "Adaptive Resolution", implement the dead field)

## Context

The check form's **Adaptive Resolution** section
(`check-form.tsx:2339–2353`) exposes two fields, and the section is both
mislabelled and half-dead:

- **Reopen cooldown multiplier** (`reopen_cooldown_multiplier`, default 5) — this
  one *works*. It drives `calculateCooldown()`
  (`incidents/service.go:568–595`): `cooldown = multiplier × period`, clamped to
  **2–30 min**. If the check fails again inside that window after resolving, the
  just-resolved incident is **reopened** (`tryReopenIncident`, `:608–638`)
  instead of opening a brand-new one. This is **blip de-duplication** — "don't
  page twice for one short outage". It is **kept as-is** by this spec.
- **Max adaptive increase** (`max_adaptive_increase`, placeholder "5 (default)")
  — **dead**. It is stored and shown but consumed by nothing:
  `effectiveRecoveryPeriodSeconds()` is a pass-through and its own comment
  (`incidents/service.go:597–606`) says *"The adaptive-per-relapse multiplier is
  gone (spec 2026-05-08-02 dropped count-based adaptive recovery)"*. The field's
  helper text even reads "Reserved for the reopen-cooldown calculation." Showing
  users a reserved no-op knob is actively misleading.

Two problems to fix: (1) "Adaptive Resolution" is the wrong name for what the
live field does (it's flap de-dup, not adaptive resolution); (2) the dead field
should become a **real** adaptive-recovery feature.

**Goal.** Implement genuine **adaptive recovery**: when a check *flaps* (repeated
outages over a short horizon), require progressively **longer stability** before
auto-resolving each successive incident — bounded by a cap, and reset after a
calm period. Replace the "Adaptive Resolution" section with an honestly-named
**Flapping** section.

> **Note on history.** Spec 2026-05-08-02 removed a *count-based* adaptive
> recovery (recovery measured in consecutive probes). This spec adds a
> *time-based* one (recovery measured in seconds, **multiplied** by a flap
> backoff). It is deliberately different from the thing that was removed, and is
> kept bounded and off-by-default-equivalent (factor 1 ⇒ today's constant
> behaviour) so it can't regress existing checks.

---

## Current state (verified against source)

| Concern | Location | Today |
|---|---|---|
| Form section | `check-form.tsx:2339–2353` | "Adaptive Resolution": `reopenCooldownMultiplier` + `maxAdaptiveIncrease` number inputs |
| Form state / payload | `check-form.tsx:534–535, 1134–1135`; type `217–218` | both sent as `*int | null` |
| Model | `check.go:102–104` | `ReopenCooldownMultiplier *int`, `MaxAdaptiveIncrease *int` (nullable, defaults-in-code) |
| Recovery gate | `incidents/service.go:478–485` (`recoveryElapsed`) | `now - FirstSuccessSinceFailureAt >= RecoveryPeriodSeconds` — **constant**, no flap awareness |
| Success handler | `incidents/service.go:447–461` (`handleSuccess`) | resolves iff `recoveryElapsed` |
| Reopen window (KEEP) | `incidents/service.go:568–595` (`calculateCooldown`), `608–638` (`tryReopenIncident`), `641–697` (`reopenIncident`) | `reopen_cooldown_multiplier × period`, clamp 2–30 min; reopen same incident on fast relapse, `RelapseCount++` |
| Dead pass-through | `incidents/service.go:597–606` (`effectiveRecoveryPeriodSeconds`) | returns `RecoveryPeriodSeconds` unchanged; feeds reopen event `keyEffectiveRecoveryThreshold` |
| Incident open | `incidents/service.go:487–512` (`createIncident`) | no flap state recorded |
| Defaults | `check.go:137–139` | confirmation/recovery 120s; reopen/maxAdaptive nil ⇒ code defaults (`defaultCooldownMultiplier = 5`) |
| Migrations | `server/internal/db/{postgres,sqlite}/migrations/` | last is `004_status_page_period`; next is **`005_…`**, added to **both** dialects |

---

## Design decision — the flapping math

Two candidate models (this was discussed with the requester):

> **Option A (recommended) — single backoff factor + cap.** For the *k*-th
> outage inside the flapping window (k = 0 for the first), the required recovery
> period is
>
> ```
> required_recovery = min( R · F^k , R · CAP , HARD_CEILING )
> ```
>
> with **R** = base recovery period (seconds), **F** = flap backoff factor
> (default **2** = "doubling"), **CAP** = max recovery multiplier (default
> **8**), and a wall-clock `HARD_CEILING` (e.g. 30 min, mirroring the existing
> cooldown clamp). `k` self-resets after a full calm window. One-sentence model:
> *"each flap doubles the patience, up to a max; a calm stretch resets it."*
> **Pros:** one escalation knob to reason about, hard ceiling, integer factor
> (no float column), predictable. **Cons:** less tuning flexibility than two
> knobs.
>
> **Option B — two knobs (initial multiplier M + escalation A).** The
> requester's original sketch: `required = R` for the first incident, then
> `R · M · A^(k-1)` for later ones (worked example: R=2m, M=2, A=1.5 →
> 2m, 6m, 9m, 13.5m…). **Pros:** separates the first-flap *step* from the
> per-flap *slope*. **Cons:** two interacting knobs are hard to predict, the
> first flap applies *both* (a ×M·A discontinuity vs ×A after), it needs
> **fractional** factors (1.5 ⇒ a float/scaled-int column), and it grows
> unbounded unless a cap is added anyway.

**Recommendation: Option A.** Fewer knobs, a real ceiling, and "max adaptive
increase" becomes honest — it *is* the cap. The rest of this spec is written
against Option A; switch to B by making the factor fractional and splitting it
into `M` + `A` (the field/state plumbing is identical). If B is preferred,
preserve A's hard ceiling regardless.

**Field mapping (Option A).** Keep `reopen_cooldown_multiplier` exactly as today
(blip de-dup). Reframe the dead `max_adaptive_increase` as the **cap**, and add
the window + factor:

| Concept | Column (checks) | Type / default | Meaning |
|---|---|---|---|
| Flapping window | `flapping_window_seconds` | `int notnull default 21600` (6h) | Outages within this rolling window accumulate the backoff. **0 ⇒ adaptive recovery off** (constant `R`). |
| Backoff factor F | `flap_backoff_factor` | `int notnull default 2` | Multiplies required recovery per flap. **1 ⇒ off** (constant `R`). |
| Cap | `max_recovery_multiplier` (renames `max_adaptive_increase`) | `int notnull default 8` | Required recovery never exceeds `R × this` (and `HARD_CEILING`). |
| Reopen window (unchanged) | `reopen_cooldown_multiplier` | `*int`, code default 5 | Fast-relapse → reopen same incident. **Separate layer.** |

The old `max_adaptive_increase` values are meaningless (the field was dead), so
the migration drops it and adds `max_recovery_multiplier` with the new default
rather than backfilling.

### Where flap state lives (hot-path safe)

The check-result path is perf-sensitive (`2026-06-05-02-check-result-hot-path-db-roundtrips`).
Do **not** add a COUNT query per result. Track a tiny rolling counter on the
check, updated only on the rare incident-open/reopen:

- Add to `checks`: `flap_count int notnull default 0` and
  `last_outage_at timestamp null`.
- On **every outage onset** (`createIncident` *and* `reopenIncident`): if
  `last_outage_at` is null or `now - last_outage_at > flapping_window` → reset
  `flap_count = 0`; else `flap_count++`. Then set `last_outage_at = now`. (Reopens
  count as flaps too — a fast relapse is the strongest flap signal.)
- `recoveryElapsed` reads `check.flap_count` (already loaded on the hot path; no
  new query) and computes
  `effective = min(R · F^flap_count, R · CAP, HARD_CEILING)`.
- Optionally mirror the level onto the incident (`incidents.flap_level int`) for
  history and the notification payload — nice-to-have, not required for the gate.

Worked example — R = 2 min, F = 2, window = 6h, CAP = 8:

| Outage onset (within 6h) | `flap_count` | Required recovery |
|---|---|---|
| 1st | 0 | **2 min** (`R·2⁰`) |
| 2nd | 1 | **4 min** |
| 3rd | 2 | **8 min** |
| 4th | 3 | **16 min** (`R·2³`) |
| 5th | 4 | `R·2⁴` = 32 → **16 min** (cap 8×) |
| after 6h calm | reset 0 | **2 min** |

---

## Recommended implementation

### Backend

1. **Model** (`check.go`):
   - Add `FlappingWindowSeconds int` (`flapping_window_seconds,notnull,default:21600`),
     `FlapBackoffFactor int` (`flap_backoff_factor,notnull,default:2`),
     `MaxRecoveryMultiplier int` (`max_recovery_multiplier,notnull,default:8`).
   - Add flap state: `FlapCount int` (`flap_count,notnull,default:0`),
     `LastOutageAt *time.Time` (`last_outage_at`).
   - Remove `MaxAdaptiveIncrease`; keep `ReopenCooldownMultiplier`.
   - `NewCheck` (`:129–145`): set the three config defaults explicitly.
   - Optional: `Incident.FlapLevel int` for history.

2. **Migration `005_adaptive_recovery` (both `postgres/` and `sqlite/`)**:
   - `checks`: `ADD COLUMN flapping_window_seconds … DEFAULT 21600`,
     `flap_backoff_factor … DEFAULT 2`, `max_recovery_multiplier … DEFAULT 8`,
     `flap_count … DEFAULT 0`, `last_outage_at` (nullable);
     `DROP COLUMN max_adaptive_increase`.
   - (optional) `incidents`: `ADD COLUMN flap_level … DEFAULT 0`.
   - `.down.sql` reverses (re-add `max_adaptive_increase`, drop the new columns).
   - Beware stale pre-consolidation dev DBs silently skipping migrations
     [[project_migration_consolidation_stale_db]]; reset/patch the dev DB.

3. **State machine** (`incidents/service.go`):
   - `recoveryElapsed(check, now)` → compute `effective` via a new
     `effectiveRecoveryPeriod(check) time.Duration` =
     `min(R · F^FlapCount, R · MaxRecoveryMultiplier, HARD_CEILING)`, with
     `F==1 || FlappingWindowSeconds==0 || FlapCount==0` short-circuiting to `R`
     (today's behaviour). Compare against `FirstSuccessSinceFailureAt + effective`.
   - Add a helper `bumpFlap(check, now)` implementing the reset/increment +
     `last_outage_at` rule above; call it from `createIncident` and
     `reopenIncident` and persist `flap_count` / `last_outage_at` on the check
     (fold into the existing update there to avoid an extra round-trip).
   - Replace the dead `effectiveRecoveryPeriodSeconds(check)` pass-through with
     the real `effectiveRecoveryPeriod` (in seconds) so the reopen event
     `keyEffectiveRecoveryThreshold` (`:691`) now carries the true adaptive
     value — this is what that payload field was always meant to report.
   - **Leave `calculateCooldown` / `tryReopenIncident` / `reopenIncident`'s
     reopen-window logic intact.** The short reopen window and the long flapping
     window are independent layers.

4. **API** (`handlers/checks/`): replace `maxAdaptiveIncrease` with
   `flappingWindowSeconds`, `flapBackoffFactor`, `maxRecoveryMultiplier` on the
   check request/response DTOs; keep `reopenCooldownMultiplier`. Validate ranges
   (`flap_backoff_factor >= 1`, `max_recovery_multiplier >= 1`,
   `flapping_window_seconds >= 0`) → `VALIDATION_ERROR`. Update
   `internal/app/openapi/openapi.yaml`.

### Frontend (dash0)

Per the design-reference rule, build from existing primitives
(`design-reference.tsx`).

- **Rename the section** "Adaptive Resolution" → **"Flapping"** (or "Flap
  suppression"). Replace `maxAdaptiveIncrease` with three inputs:
  `flappingWindowSeconds` (seconds; reuse the duration/estimate helper from spec
  06 to show "= 6 h"), `flapBackoffFactor`, `maxRecoveryMultiplier`. Keep
  `reopenCooldownMultiplier`, and **rewrite its helper** to the truth: *"If the
  check fails again shortly after recovering, reattach it to the same incident
  instead of paging again. Window = N × check interval (min 2 min, max 30 min).
  0 = always open a new incident."*
- New helper copy, e.g.:
  - Flapping window: *"If a check flaps repeatedly within this window, require
    longer stability before each resolve. 0 = off."*
  - Backoff factor: *"Each flap multiplies the required recovery time by this.
    1 = off (constant recovery)."*
  - Max recovery multiplier: *"Cap — required recovery never exceeds this × the
    base recovery period."*
- Update `check-form.tsx` state (534–535), payload (1134–1135), the type
  (217–218), and `api/hooks.ts` Check / Update types.
- i18n: section + 4 field labels/helpers across `locales/{en,fr,de,es}/checks.json`.

### Notifications

The reopen/resolve payload's effective-recovery value now varies. Verify the
notification renderers (slack/discord/webhook/email) that consume
`effectiveRecoveryThreshold` still format correctly with the real number; adjust
copy if it implied a constant.

---

## Out of scope

- The Confirmation/Recovery **seconds + probe-count estimate** rendering — spec
  06.
- Changing the **short reopen-cooldown** mechanism (`reopen_cooldown_multiplier`)
  — kept as the independent blip-dedup layer.
- **Grouped-check** flapping (`handleGroupSuccess` / `handleGroupFailure`).
  Adaptive recovery here targets **per-check** incidents (`handleSuccess`); group
  flapping is a follow-up.
- Escalation threshold / severity (a separate streak-based primitive).
- Fractional backoff factors (Option B) — possible later via a scaled-int/float
  column; A uses integer F.

---

## Verification

```bash
make dev-test   # backend + dash0 on :4000
make migrate    # apply 005 on a fresh AND a populated DB, both SQLite + Postgres
```

- **Migration:** existing checks get `flap_backoff_factor=2`,
  `max_recovery_multiplier=8`, `flapping_window_seconds=21600`,
  `flap_count=0`; `max_adaptive_increase` is gone. Verify on **both** dialects
  [[project_migration_consolidation_stale_db]].
- **State machine unit tests** (`incidents/service_test.go`, table-driven, with
  the injected `s.clock`): reproduce the worked example — incident #1 resolves
  after 2 min of success; a flap within the window needs 4 min, the next 8 min,
  the next 16 min; the cap holds at 16 min; after `> flapping_window` of calm the
  requirement falls back to 2 min. Cover `F=1` and `flapping_window=0` ⇒ constant
  `R` (no behaviour change). Use `testify/require`, `t.Parallel()`.
- **Reopen window unchanged:** a fast relapse within `reopen_cooldown_multiplier
  × period` still reopens the same incident (`RelapseCount++`), independent of
  the flap backoff.
- **Hot path:** confirm no new per-result query — `flap_count`/`last_outage_at`
  are written only on incident open/reopen, read off the already-loaded check.
- **Form:** edit a check → section reads **Flapping** with window / factor / cap
  + the (kept, re-explained) reopen multiplier; values round-trip; mobile layout
  holds.
- `make test`, `make lint` (no new findings — never relax config
  [[feedback_lint_strict]]), `make test-dash`. E2E: extend a dash0 check-form
  spec to round-trip the new fields; prefer Playwright [[feedback_browser_testing]],
  treat flakes as bugs [[feedback_flaky_tests_are_bugs]].

---

## Key files

| File | Change |
|---|---|
| `server/internal/db/models/check.go` | **~** add `FlappingWindowSeconds`, `FlapBackoffFactor`, `MaxRecoveryMultiplier`, `FlapCount`, `LastOutageAt`; drop `MaxAdaptiveIncrease`; `NewCheck` defaults |
| `server/internal/db/{postgres,sqlite}/migrations/005_adaptive_recovery.{up,down}.sql` | **+** add columns + drop `max_adaptive_increase` (both dialects) |
| `server/internal/handlers/incidents/service.go` | **~** `effectiveRecoveryPeriod` + flap-aware `recoveryElapsed`; `bumpFlap` in `createIncident`/`reopenIncident`; keep `calculateCooldown` |
| `server/internal/handlers/incidents/service_test.go` | **~** worked-example + cap + reset + off-switch tests |
| `server/internal/handlers/checks/{handler,service}.go`, `internal/app/openapi/openapi.yaml` | **~** new DTO fields + validation; drop `maxAdaptiveIncrease` |
| `web/dash0/src/components/shared/check-form.tsx` | **~** rename section to "Flapping"; window/factor/cap inputs; rewrite reopen helper |
| `web/dash0/src/api/hooks.ts` | **~** Check/Update types |
| `web/dash0/src/locales/{en,fr,de,es}/checks.json` | **~/+** section + field copy |
| `web/dash0/e2e/` | **~** round-trip the new fields |
| notification renderers (slack/discord/webhook/email) | **verify** `effectiveRecoveryThreshold` rendering |

---

## Risk log

| Risk | Mitigation |
|---|---|
| Migration on both Postgres **and** SQLite; stale dev DBs skip it | Add to both dialects; reset/patch dev DB; verify [[project_migration_consolidation_stale_db]]. |
| Unbounded recovery growth traps a genuinely-recovered flapper | Hard `CAP` (× multiplier) **and** wall-clock `HARD_CEILING`; reset after a calm window. |
| Hot-path regression from counting flaps per result | O(1) rolling counter on the check, written only on the rare open/reopen; read off the loaded row — no per-result query. |
| Confusing two windows (short reopen vs long flapping) | Keep names/sections distinct; reopen helper rewritten; spec documents the two layers. |
| Reintroducing complexity that 2026-05-08-02 removed | This is time-based (not the removed count-based model), bounded, and `F=1`/`window=0` reproduces today's constant behaviour. |
| Dropping `max_adaptive_increase` | It was dead (consumed by nothing); no behaviour depends on it. `.down.sql` re-adds it. |
| Requester may prefer the two-knob (M+A) model | Option B documented; plumbing is identical — only the factor type + math differ. Confirm before implementing. |

**Status**: Todo | **Created**: 2026-06-30 | **Related**: `2026-06-30-06-check-confirmation-recovery-period-interval-estimate.md`

---

## Implementation Plan

1. **Confirm the model** (A single-factor+cap vs B two-knob). Spec assumes A.
2. **Model + migration** — new columns + drop `max_adaptive_increase` on both
   dialects; `NewCheck` defaults; `make migrate` fresh + populated.
3. **State machine** — `effectiveRecoveryPeriod`, flap-aware `recoveryElapsed`,
   `bumpFlap` on open/reopen; keep the reopen window; replace the dead
   pass-through. Unit-test the worked example + cap + reset + off-switch.
4. **API + OpenAPI** — swap DTO fields, validate ranges.
5. **dash0** — rename section to "Flapping"; window/factor/cap inputs (reuse spec
   06 duration helper); rewrite the reopen helper; thread types/payload; i18n ×4.
6. **Notifications** — verify the effective-recovery value renders.
7. **QA** — `make test` / `make lint` / `make test-dash`; e2e round-trip; manual
   flap scenario against the worked example.
