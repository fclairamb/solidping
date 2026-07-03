# Check form: show a probe-count estimate under Confirmation / Recovery periods (keep seconds as the source of truth)

## Context

On the check edit form
(`http://localhost:4000/dash0/orgs/default/checks/<uid>/edit`), the **Incident
Tracking** section has two number inputs measured in **seconds** — *Confirmation
Period* and *Recovery Period* (default 120 each). A raw value like `120` does not
tell the operator how many probes that window actually spans: at a 1-minute
interval it's ~2 checks; at a 5-second interval it's 24. The operator has to do
the division in their head.

The fix is **not** to switch the stored unit to a count. Seconds is the correct
source of truth and stays:

- The backend incident state machine is genuinely time-based — it stores
  `FirstFailureAt` / `FirstSuccessSinceFailureAt` timestamps and asks "has
  `now - FirstFailureAt >= ConfirmationPeriod`?" (`incidents/service.go:466–485`).
  Nothing counts probes.
- This is a **distributed, multi-worker prober**: probe delivery is jittery
  (late / reordered / briefly missing). A wall-clock window tolerates that; a
  consecutive-probe count is brittle to it.
- **Passive checks** (heartbeat / email — "expected interval") fire irregularly
  and have no natural "number of failures".
- Detection latency (the thing on-call and SLAs care about) is wall-clock.
  Changing the interval should not silently change the detection guarantee.

So this spec is **UI-only**: keep the seconds inputs, and render a live,
read-only estimate beneath each one — the human duration plus, for active checks,
the approximate probe count at the current interval. This gives the operator the
intuitive "how many checks is that?" reading without making the count
authoritative.

> Sibling spec `2026-06-30-07-adaptive-recovery-flap-backoff-redesign.md` reworks
> the **Adaptive Resolution** section below this one. The two touch different
> sections of the same form and are independently landable; spec 07 reuses the
> estimate helper introduced here if it has landed, else inlines its own.

---

## Current state (verified against source)

| Concern | Location | Today |
|---|---|---|
| Confirmation/Recovery inputs | `web/dash0/src/components/shared/check-form.tsx:2323–2337` | "Incident Tracking" section: two `Input type=number min=0 max=86400 placeholder="120 (default)"`; helper `<p>` per field |
| Form state | `check-form.tsx:536–541` | `confirmationPeriodSeconds` / `recoveryPeriodSeconds` string state, seeded from `initialData?.…?.toString() ?? ""` |
| Submit payload | `check-form.tsx:1136–1141` | sends `confirmationPeriodSeconds` / `recoveryPeriodSeconds` as parsed ints when non-empty |
| Check interval field | `check-form.tsx:~2232–2264` | active checks: `<Select>` of interval options (5s … 24h); passive (heartbeat/email): number + unit picker. Source of the "current interval" the estimate needs |
| Help copy | `web/dash0/src/locales/en/checks.json:111–115` | `confirmationPeriod` / `confirmationPeriodHelp` / `recoveryPeriod` / `recoveryPeriodHelp` |
| Backend model | `server/internal/db/models/check.go:82–100,137,139` | `ConfirmationPeriodSeconds` / `RecoveryPeriodSeconds int` (seconds), `NewCheck` default 120 each — **unchanged by this spec** |

No backend, model, migration, or API change. The stored and wire unit stays
`seconds`.

---

## Recommended implementation (dash0 only)

Per the repo design-reference rule, build from existing primitives
(`web/dash0/src/routes/orgs/$org/design-reference.tsx`); this only adds a muted
helper line, no new primitive.

1. **Estimate helper.** Add a pure function (e.g. `lib/period-estimate.ts` or
   alongside the form) `describePeriod(windowSeconds, intervalSeconds, kind)` →
   string, where `kind ∈ {"confirmation","recovery"}`:
   - `windowSeconds === 0` → the "immediate" copy: confirmation → *"Opens an
     incident immediately on the first failure."*; recovery → *"Resolves the
     incident immediately on the first success."*
   - otherwise → human duration (`120 → "2 min"`, `90 → "1 min 30 s"`,
     `3600 → "1 h"`), and **for active checks with `intervalSeconds > 0`** append
     `· ≈ {count} check(s) at the {interval} interval`, where
     `count = max(1, Math.round(windowSeconds / intervalSeconds))`.
   - Passive checks (no real probe cadence) → show the duration only, no count.
   Mark it clearly as an estimate (the leading `≈`) so it never reads as the
   authoritative value.

2. **Current interval.** Derive `intervalSeconds` from the form's existing Check
   Interval state (the value feeding the select/number+unit at ~2232–2264) and
   the check type (active vs passive). Recompute the estimate as either the
   period input or the interval changes (plain derived value in render — no new
   query).

3. **Render.** Under each input (`check-form.tsx:2329` and `:2334`), keep the
   conceptual helper from i18n and add the dynamic estimate as a second muted
   line (`text-xs text-muted-foreground`). Example with interval = 1 min,
   confirmation = 120: helper line unchanged, then *"= 2 min · ≈ 2 checks at the
   1-minute interval."*

4. **Copy tweak (optional, same change).** Tighten the two help strings so the
   static helper explains the *behaviour* and the dynamic line carries the
   *numbers* — e.g. keep `confirmationPeriodHelp` as the "wait then open; 0 =
   immediately" sentence; the estimate line supplies "how long / how many".

5. **i18n.** Add interpolated keys in `locales/{en,fr,de,es}/checks.json` for the
   estimate, e.g. `form.periodEstimate` (duration), `form.periodEstimateChecks`
   (`"≈ {{count}} check at the {{interval}} interval"`, with pluralization),
   `form.periodImmediateOpen`, `form.periodImmediateResolve`. Translate all four
   locales — no hardcoded English in the component.

---

## Out of scope

- **Changing the stored/wire unit.** Confirmation/Recovery stay `int` seconds
  end to end. This spec only renders a derived estimate.
- The **Adaptive Resolution** section — owned by spec 07.
- Any backend, model, migration, OpenAPI, or notification change.
- A dual-unit *input* (type "3 checks" → seconds). Possible later; this spec is
  display-only to stay low-risk.

---

## Verification

```bash
make dev-test   # backend + dash0 + status0 on :4000
```

- Edit a check, interval **1 minute**, confirmation **120** → estimate reads
  `= 2 min · ≈ 2 checks at the 1-minute interval`; recovery likewise.
- Set interval **5 seconds** → confirmation 120 shows `≈ 24 checks`; set interval
  **1 hour** → `≈ 1 check` (floored to ≥1 while window > 0).
- Set confirmation **0** → "Opens an incident immediately on the first failure.";
  recovery 0 → the resolve-immediately copy.
- Heartbeat/email (passive) check → duration shown, **no** probe count.
- Save → reload: values persist unchanged (no payload/unit change).
- Mobile width: the helper lines wrap, inputs stay usable (all pages must work on
  mobile per repo conventions).
- `make lint` / `make test-dash` clean — never relax config
  [[feedback_lint_strict]]. Optionally extend a dash0 e2e to assert the estimate
  text for a known interval; prefer Playwright [[feedback_browser_testing]] and
  treat any flake as a bug [[feedback_flaky_tests_are_bugs]].

---

## Key files

| File | Change |
|---|---|
| `web/dash0/src/components/shared/check-form.tsx` | **~** render the estimate line under both period inputs (2329, 2334); read current interval + check type |
| `web/dash0/src/lib/period-estimate.ts` (new) | **+** `describePeriod()` pure helper (+ small unit test) |
| `web/dash0/src/locales/{en,fr,de,es}/checks.json` | **~/+** estimate + immediate keys (≈ lines 111–115) |
| `web/dash0/e2e/` (optional) | **~** assert estimate text for a known interval |

---

## Risk log

| Risk | Mitigation |
|---|---|
| Estimate read as authoritative / users think they're setting a count | Leading `≈`, "estimate" framing, the editable input stays seconds; copy says "about N checks". |
| Passive checks have no real interval | Guard: count only when active with `intervalSeconds > 0`; otherwise duration only. |
| i18n pluralization (1 check vs N checks) across en/fr/de/es | Use the i18n plural form; cover count = 1 and N in the unit test. |
| Interval stored as a Go-duration string, not seconds | Parse the form's interval value to seconds in the helper (reuse existing parse used by the interval field). |

**Status**: Todo | **Created**: 2026-06-30

---

## Implementation Plan

1. `describePeriod()` helper + unit test (duration formatting, `≈ N checks`,
   immediate cases, passive = duration-only).
2. Wire the current interval + check type out of existing form state.
3. Render the estimate line under both period inputs.
4. i18n keys across en/fr/de/es; remove any hardcoded copy.
5. QA: the interval/period combinations above, mobile, `make lint` /
   `make test-dash`; optional e2e assertion.
