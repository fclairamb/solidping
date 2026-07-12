# Configurable global check timeout (default 15s), execution context = timeout + 1s

## Problem

The per-check execution ceiling is a hardcoded constant, not a config
parameter:

- `scheduling.DefaultExecutionTimeout = 30 * time.Second`
  (`server/internal/checkworker/scheduling/scheduling.go:28`) is the flat
  ceiling every check gets.
- `Params.ExecutionTimeout()` (`scheduling.go:220`) clamps the cost-aware
  timeout to `[floor, DefaultExecutionTimeout]` and falls back to the flat
  constant when the cost signal is absent or the feature is off.
- The worker applies it directly as the execution context:
  `context.WithTimeout(context.Background(), execTimeout)`
  (`server/internal/checkworker/worker.go:762-763`).

Two issues:

1. **Not configurable.** An operator cannot lower (or raise) the global
   ceiling without a rebuild. 30s is generous for most fleets; 15s is a
   better default.
2. **No margin between the check timeout and the context kill.** The same
   duration is both "the timeout the check is given" and "the moment the
   context hard-cancels it". Checkers that enforce their own internal
   timeout (tcp, redis, postgres, … — each has a `Timeout` config field)
   race the outer context; when the context wins, the result is a generic
   `context deadline exceeded` instead of the checker's cleaner
   `StatusTimeout` output. The context ceiling should be the check timeout
   **plus one second** so the checker-level timeout always fires first.

No schema change is needed — this is purely a server config parameter, not
a new column on `checks` or `check_jobs`.

## Proposal

Add a single config knob for the global check timeout:

- **Config**: `scheduling.check_timeout_ms` (float, milliseconds), default
  `15000`, in `SchedulingConfig`
  (`server/internal/config/config.go:547`). Add the matching manual env
  read `SP_SCHEDULING_CHECK_TIMEOUT_MS` in `applySchedulingEnv`
  (`config.go:1094`) — koanf's env provider misses multi-word keys, same
  quirk as the existing `SP_SCHEDULING_COST_TIMEOUT_*` knobs.
- **Thread it into the worker**: add a `CheckTimeout time.Duration` field to
  `scheduling.Params` (`scheduling.go`), populated from config in
  `checkworker/worker.go` (next to `CostTimeoutFactor`, `worker.go:252`).
  `0`/unset falls back to 15s. Keep `DefaultExecutionTimeout` as the
  fallback constant but change its value to 15s (it is also the EWMA pin
  value at `worker.go:979` and the basis of `MaxDeprioritizeOffset`,
  `scheduling.go:48` — both should derive from the effective configured
  timeout, or at minimum stay consistent with the new default).
- **Semantics**:
  - `ExecutionTimeout()` returns the *check timeout*: cost-aware
    `clamp(factor × cost_ewma, floor, check_timeout)` when enabled,
    else the flat configured timeout. The upper clamp bound becomes the
    configured value instead of the hardcoded 30s.
  - The execution context at `worker.go:763` becomes
    `context.WithTimeout(background, checkTimeout + 1*time.Second)` — the
    +1s margin lets a checker that honors its own timeout report a clean
    `StatusTimeout` result before the hard context cancellation. The
    timeout value passed down to `runCheckerGuarded` (and thus to checkers
    as their effective timeout budget) stays `checkTimeout`, without the
    +1s.
- **No DB change**: per-check `config.Timeout` fields (per-checker
  validation constants `maxTimeout` of 30s/60s in
  `server/internal/checkers/*/config.go`) are untouched.

### Tests

- Config: default 15s, YAML override, `SP_SCHEDULING_CHECK_TIMEOUT_MS`
  env override (extend the existing scheduling-env config tests).
- `ExecutionTimeout()`: clamp upper bound follows the configured value;
  0/unset falls back to the default.
- Worker: execution context deadline is check timeout + 1s (observable via
  a slow fake checker that inspects its context deadline).

## Open questions

1. **"15s by default, 30s"** — interpreted as: new default 15s, replacing
   today's hardcoded 30s. If the intent was instead "default 15s, capped
   at 30s max", add a validation upper bound of 30s on the parameter.
2. **Per-check timeouts above the global.** Several checkers accept an
   explicit per-check `Timeout` up to 60s (sftp, rdp, postgres, …). With a
   flat global context of 16s, such a check would be killed early.
   Recommendation: the effective check timeout is
   `max(globalTimeout, per-check configured Timeout)` and the context is
   that + 1s, so an explicit per-check timeout keeps working. If the flat
   interpretation is preferred, the per-checker `maxTimeout` validation
   caps should be revisited to not exceed the global.

## Implementation Plan

Decisions taken (per the Proposal section, which is authoritative over the
Open questions):

- **OQ1** — flat interpretation: the new global default is **15s**, replacing
  the hardcoded 30s. No 30s validation cap is added.
- **OQ2** — flat interpretation: the global check timeout is applied flatly;
  per-checker `maxTimeout` caps and per-check `config.Timeout` fields are left
  untouched (no DB change, matching the Proposal's "No DB change"). The
  `max(global, per-check)` variant is left as a documented open question.

### Step 1 — scheduling math (`scheduling.go`)
- Change `DefaultExecutionTimeout` `30s → 15s`. `MaxDeprioritizeOffset`
  auto-derives to `2 × 15s = 30s` (still structurally bounded, well under the
  claim window); update its comment.
- Add `CheckTimeout time.Duration` to `scheduling.Params` (0 = unset → falls
  back to `DefaultExecutionTimeout`).
- Add `func (p Params) EffectiveCheckTimeout() time.Duration` returning
  `CheckTimeout` when > 0 else `DefaultExecutionTimeout`.
- `ExecutionTimeout()`: the flat fallback and the upper clamp bound both become
  `EffectiveCheckTimeout()` instead of the hardcoded `DefaultExecutionTimeout`.

### Step 2 — config knob (`config.go`)
- Add `CheckTimeoutMs float64 \`koanf:"check_timeout_ms"\`` to
  `SchedulingConfig`.
- Default `CheckTimeoutMs: 15000` in `Load()`.
- Read `SP_SCHEDULING_CHECK_TIMEOUT_MS` in `applySchedulingEnv` (koanf
  multi-word env quirk).

### Step 3 — thread into the worker (`worker.go`)
- `schedulingParamsFromConfig`: populate `CheckTimeout: millis(cfg.CheckTimeoutMs)`.
- `executeJob`: `checkTimeout := ExecutionTimeout(cost)`; the execution context
  becomes `context.WithTimeout(background, checkTimeout + 1*time.Second)`; the
  value passed to `runCheckerGuarded` (the checker's effective budget) stays
  `checkTimeout`, no +1s.
- `costSampleMs`: pin the timeout cost sample to
  `r.schedParams.EffectiveCheckTimeout()` (derives from the effective configured
  timeout) instead of the raw constant.

### Step 4 — tests
- `config_test.go`: default 15000, YAML/struct override, and
  `SP_SCHEDULING_CHECK_TIMEOUT_MS` env override.
- `scheduling_test.go`: `ExecutionTimeout` upper clamp follows the configured
  `CheckTimeout`; 0/unset falls back to the default; update the existing
  assertions that hardcoded 30s to the new 15s default.
- `worker_test.go`: a fake checker records its context deadline; assert the
  execution context deadline is `checkTimeout + 1s` while the timeout budget
  passed to the checker path stays `checkTimeout`.
