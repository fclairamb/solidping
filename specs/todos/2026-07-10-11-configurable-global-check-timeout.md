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
