---
model: sonnet
effort: medium
---

# Three ingest paths build an incidents service that ignores `sp.check_timeout_ms`, so the confirmation-hold cap depends on which path saw the result

## Problem

`incidents.Service` carries a `defaultCheckTimeout` used as the last term of
the confirmation-hold cap in `ancestorHoldRemaining`
(`server/internal/handlers/incidents/rollup.go:447`):

```
holdCap = FirstFailureAt + confirmationPeriodSeconds + period + ancestor.TimeoutOrDefault(defaultCheckTimeout)
```

The constructor seeds it to `DefaultCheckTimeoutFallback` (15s,
`server/internal/handlers/incidents/service.go:281`) and expects every caller
to overwrite it with the operator-configured `scheduling.check_timeout_ms`
(`sp.check_timeout_ms` / `SP_SCHEDULING_CHECK_TIMEOUT_MS`) via
`SetDefaultCheckTimeout`. Three call sites never do:

- `server/internal/handlers/heartbeat/service.go:141` — heartbeat ingest
- `server/internal/handlers/emailcheck/handler.go:106` — inbound-mail ingest
- `server/internal/mcp/handler.go:126` — MCP surface (note: `internal/mcp/`,
  not `internal/handlers/mcp/`); this one is already handed the whole
  `*config.Config` and simply ignores it

The correctly-wired sites are `server/internal/app/server.go` (~1072, ~1169,
~1243, through `Server.defaultCheckTimeout()` at ~3702) and
`server/internal/checkworker/worker.go:200`.

At shipped defaults the two values coincide, so nothing is observable. The
moment an operator raises or lowers the ceiling, the same check gets a
different hold cap depending on which ingest path processed the result — a
heartbeat-driven child and a probe-driven child of the same parent stop
agreeing on how long the hold lasts. That is a silent, deployment-specific
inconsistency in a gate whose whole purpose is to make cascade behaviour
predictable.

The structural cause is that the ms→`time.Duration` conversion was
copy-pasted per call site rather than living in one place, so a site that
forgot to copy it inherited the fallback with no signal.

Found during an independent audit of
`specs/done/2026/08/2026-08-31-06-hold-child-confirmation-while-hard-parent-validating.md`.

## Proposal

1. **Remove the copy-paste.** Add `SchedulingConfig.CheckTimeout() time.Duration`
   in `server/internal/config/config.go` as the single ms→Duration conversion,
   and route `Server.defaultCheckTimeout()` and `checkworker.NewCheckWorker`
   through it. A non-positive `CheckTimeoutMs` converts to `0`; applying the
   documented built-in default stays each consumer's job (the
   `SetDefaultCheckTimeout` setter already ignores non-positive values, so the
   fallback stands).

2. **Wire the three sites.**
   - `heartbeat.NewService` and `emailcheck.NewHandler` take a **required**
     `defaultCheckTimeout time.Duration` parameter — the same reasoning the
     existing `publicationHook` parameter is documented with: a required
     parameter forces a new call site to make a deliberate choice instead of
     inheriting the bug. Production callers in `server.go` pass
     `s.defaultCheckTimeout()`; test callers pass `0` (fallback preserved,
     behaviour unchanged).
   - `mcp.NewHandler` needs no signature change — use the `cfg` it already
     receives, guarding for the nil-`cfg` test path.

3. **Regression tests** — one wiring test per ingest package, asserting the
   resolved timeout is the configured value and not the fallback. Two things
   make them worth having rather than tautological:
   - a **positive control** (`require.NotEqual(DefaultCheckTimeoutFallback,
     configured)`) so the assertion cannot pass vacuously if the constant ever
     drifts to match the fixture;
   - a **companion case** pinning the other half of the contract —
     non-positive input keeps `DefaultCheckTimeoutFallback` rather than
     collapsing to a zero-length hold that would resolve every child
     immediately.

   This needs one new accessor, `incidents.Service.DefaultCheckTimeout()`,
   since the field is unexported and the assertions live in other packages.
   Add a `SchedulingConfig.CheckTimeout()` table test alongside.

4. **Verify the tests are real** by reverting just the three
   `SetDefaultCheckTimeout` calls and confirming all three fail with
   `expected: <configured> / actual: 15s`.

## Status (2026-09-01)

**Already implemented in the working tree, uncommitted.** This spec was filed
after the fact as the tracking artifact. Whoever picks it up should *verify*
rather than re-implement:

- `server/internal/config/config.go` — `SchedulingConfig.CheckTimeout()`
- `server/internal/handlers/heartbeat/service.go`,
  `server/internal/handlers/emailcheck/handler.go`,
  `server/internal/mcp/handler.go` — the three fixes
- `server/internal/handlers/incidents/service.go` — `DefaultCheckTimeout()` accessor
- `check_timeout_wiring_test.go` in each of the three ingest packages, plus
  `TestSchedulingCheckTimeout` in `internal/config`

All six production `incidents.NewService` sites now plumb the value; a sweep
found no others. `go build ./...`, `go vet ./...` and `make lint-back` are
clean, and the 15 affected test packages pass. The revert check in step 4 was
run: all three failed with `expected: 42s / actual: 15s`.

Earlier there was a concurrent, unrelated edit to
`server/internal/handlers/incidents/rollup_forward_test.go` in this tree from
work on `2026-08-31-07-rollup-detach-erases-attribution`. That work has since
been committed, so the tree now contains only this change: 13 modified files
plus the 3 new `check_timeout_wiring_test.go` files.
