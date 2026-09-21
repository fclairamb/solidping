---
model: opus
effort: high
---

# The ICMP check form only offers `host`, so `count`, `interval`, `packet_size` and `ttl` are unreachable from the dashboard

*Raised by Florent on 2026-09-21. A user deleted his account believing one
sample per 10 seconds was the floor. It is not; the form just never showed him
the settings.*

## Problem

`web/dash0/src/components/checks/form/types/network.tsx`, the ICMP module:

```ts
ownedKeys: ["host"],
fromConfig: (config) => ({ host: getConfigField(config, "host") }),
toConfig: (state) => {
  if (state.host) cfg.host = state.host;
  return { config: cfg, errors: hostRequired(state.host) };
},
```

The checker accepts six fields (`server/internal/checkers/checkicmp/config.go`):
`host`, `timeout`, `count`, `interval`, `packet_size`, `ttl`. `timeout` reaches
the form through the shared timeout input and `ipVersion` through
`IPVersionSelect`, so of the ICMP-specific settings **the form exposes exactly
one: `host`.**

`count` and `interval` are the two that matter. Together they turn a single
ping into a burst — N packets, I apart, reported as `packet_loss_pct` plus
`rtt_ms_min` / `rtt_ms_max` / `rtt_ms_avg`. That is the difference between "is
it up" and "how good is the line", and it is the feature a user churned over
without ever learning it existed. He measured one sample per 10 seconds,
concluded the product was 100x too coarse for him, deleted both checks and
left.

## What already works, and must keep working

`assembleSubmittedConfig`
(`web/dash0/src/components/checks/form/types/common.ts`) carries unowned keys
through a round-trip untouched:

```
{ ...passthrough, ...moduleConfig, ...sharedConfig }
```

So a `count`/`interval` set over the API **survives** later edits in the
dashboard today. Adding these fields to `ownedKeys` moves them out of the
passthrough and into "omit means clear", which is correct but is the one thing
to get right: an existing check with `count: 10` set via the API must not lose
it the first time somebody renames it in the UI. Cover it with a test.

## What to build

Add the burst settings to the ICMP module. Suggested shape, collapsed by
default so the common case stays one field:

- `count` — packets per run. Default 1 (unchanged behaviour when untouched).
- `interval` — spacing between them. Only meaningful when `count > 1`, so
  disable or hide it at `count: 1`.
- `packet_size` and `ttl` — same section, lower priority. They are already
  validated server-side (0–65507, 1–255) and cost nothing to surface.

Keep the bounds in sync with the server rather than hardcoding a second copy:
`count` and `interval` limits are about to change in spec
`2026-09-21-01`, and a form that validates against stale constants is worse
than one that does not validate at all. Prefer the check-types metadata if it
can carry them.

**Say what the burst does**, in one line under the fields. "10 packets, 100ms
apart, every run" is not obvious from two numeric inputs, and the whole point
is that people do not currently know this exists.

## Ordering

`2026-09-21-01` fixes an execution-budget defect that truncates bursts and
over-reports packet loss. Shipping this form first would hand users a control
that produces wrong numbers the moment it meets real loss. **Land 01 first, or
land them together.**

## Related

The same user's two rageclicks in this form both landed on `NotifyViaSection`'s
first channel row, on a row that was already ticked. Unexplained, not
reproducible, and not part of this spec — noted only because it is the second
report in a week of someone fighting this form.

## Implementation Plan

- **Module** (`web/dash0/src/components/checks/form/types/network.tsx`): grow
  `IcmpState` with `count`, `interval`, `packetSize`, `ttl` (all strings,
  same string-state pattern as `port`); add the four keys to `ownedKeys`;
  `fromConfig` reads them, `toConfig` writes integers for `count` /
  `packet_size` / `ttl` and the raw duration string for `interval` (the
  checker stores a Go duration string, e.g. `"100ms"` — never a number).
- **Bounds-sync strategy**: the check-types metadata cannot carry per-field
  bounds (it has only `MinPeriodHint` / `secretFields` / period metadata —
  confirmed by reading `server/internal/handlers/checktypes/service.go` and
  `checkerdef/types.go`), so the client keeps NO second copy of the limits:
  `toConfig` only checks integer/format sanity, the Go validator
  (`checkicmp/checker.go`) stays the sole authority on 1–600 / 50ms–60s /
  0–65507 / 1–255, and the module carries a comment cross-referencing those
  constants.
- **UI**: reuse the existing `CollapsibleSection` primitive (same pattern as
  the TCP "Payload & reply" section) — collapsed by default, auto-open when a
  stored value exists, `summary` + one-liner under the fields computed live
  ("N packets, I apart, every run" / singular phrasing at count 1). `interval`
  input is disabled while `count` ≤ 1. Two-column grid on `sm`, single column
  at 375px.
- **Round-trip**: the four new `ownedKeys` are "omit means clear", covered by
  `network.test.ts` — an API-created check with `count: 10` must survive an
  untouched save and a rename-only edit; a cleared input must delete the key.
- **Locales**: new `network.*` keys in en/fr/de/es (locale-parity test
  enforces parity); plural handled in code (two keys), not i18next plural
  rules (no plural config exists).
- **Tests**: unit round-trips in `network.test.ts`; Playwright spec
  `e2e/check-icmp-burst.spec.ts` (create with count/interval via the form →
  persisted; API-created `count: 10` check survives an unrelated edit).
- **Design reference**: no new primitives — `CollapsibleSection` and
  `Input type="number"` are already catalogued there.
