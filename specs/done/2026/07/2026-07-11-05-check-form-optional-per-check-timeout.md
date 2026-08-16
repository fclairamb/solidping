# Check form: optional per-check timeout (`timeout` config key, 30s server-side cap, context = timeout + 1s)

## Problem

The edit-check form (e.g.
`/dash0/orgs/acmetech/checks/5b39634d-…/edit`, a TCP check) offers no way
to set a per-check timeout. The shared form's **General** section
(`web/dash0/src/components/shared/check-form.tsx:2490`) exposes only
Enabled, Check Interval, Name, and Slug; no check type surfaces a timeout
field, on the new or edit route
(`web/dash0/src/routes/orgs/$org/checks.$checkUid.edit.tsx`).

The backend already understands a `timeout` key in the check `config`
JSONB for most checkers — e.g. checktcp has
`Timeout time.Duration` with `json:"timeout,omitempty"`
(`server/internal/checkers/checktcp/config.go:15`), parsed as a duration
string (`config.go:59-66`), default 5s (`checker.go:20`), validated
`> 0 and <= 60s` (`checker.go:56-58`). Similar per-checker `Timeout`
fields with `maxTimeout` caps of 30s or 60s exist in checka2s, checkrdp,
checksftp, checkpostgres, checkftp, checkredis, checkwebsocket,
checkmongodb, checkkubernetes, ….

Two gaps beyond the missing UI:

1. **Inconsistent server-side cap.** Per-checker validation allows up to
   60s in several checkers; there is no uniform maximum.
2. **A per-check timeout can't actually extend the execution budget.**
   Since spec 2026-07-10-11, the worker's execution budget is the global
   check timeout (default 15s) and the execution context deadline is
   `checkTimeout + 1s` (`server/internal/checkworker/worker.go:764-772`).
   A check configured with `timeout: 30s` is hard-cancelled by the global
   ~16s context long before its own timeout fires — this was left open as
   OQ2 of that spec.

## Proposal

Let users set an **optional per-check timeout**, stored as the plain
`timeout` key in the check's `config` JSON, with a **30s maximum enforced
server-side**, and have the worker honor it: the execution context
deadline becomes **`timeout + 1s`** when the per-check timeout is set.

### Frontend (dash0)

- Add an optional **Timeout** field to the shared check form's General
  section (next to Check Interval,
  `web/dash0/src/components/shared/check-form.tsx:2490`), shown for all
  check types on both the new and edit routes.
- Empty = unset: the key is omitted from `config` and the checker/server
  defaults apply. Clearing the field on edit removes the key.
- The value written is the duration string the checkers already parse
  (e.g. `"10s"`); the input UX should be seconds-based (numeric input or
  a small select), rendered to the `"Ns"` string on submit.
- Client-side hint of the 1–30s range; the server stays authoritative.
- Follow the design reference
  (`web/dash0/src/routes/orgs/$org/design-reference.tsx`) for the input
  primitive; add the pattern there if missing.

### Server — validation (30s cap)

- Enforce `timeout <= 30s` (and `> 0`) at check create/update time so the
  cap is uniform across all check types — a `VALIDATION_ERROR` on the
  `timeout` parameter otherwise.
- Tighten the per-checker `maxTimeout` validations that currently allow
  60s (e.g. `checktcp/checker.go:57`, and the 60s caps in checksftp,
  checkrdp, checkpostgres, …) down to 30s so checker-level validation and
  the global cap agree. Existing stored configs above 30s keep working
  (validated on write, not on read) but are clamped at execution time by
  the worker rule below.

### Worker — context cancellation = timeout + 1s

- In `executeJob` (`server/internal/checkworker/worker.go:770-775`), when
  the check config carries a `timeout` value, that value becomes the
  effective execution budget: the execution context is
  `context.WithTimeout(background, perCheckTimeout + 1s)` and the budget
  passed to `runCheckerGuarded` is `perCheckTimeout` (no +1s), mirroring
  the existing global rule from spec 2026-07-10-11. The +1s margin keeps
  letting a checker that honors its own timeout report a clean
  `StatusTimeout` before the hard context cancellation.
- When set, the per-check timeout takes precedence over the cost-aware
  clamp (`scheduling.Params.ExecutionTimeout`) — an explicit user choice
  should not be shrunk by the EWMA-based ceiling nor cut off by the
  global default (15s) when the user asked for up to 30s.
- Unset → behavior unchanged (global check timeout, context +1s).
- Clamp defensively at 30s in the worker too, so legacy configs with 60s
  values don't get a 61s context.

### Tests

- Server validation: create/PATCH with `timeout: "31s"` →
  `VALIDATION_ERROR`; `"30s"` accepted; `"0s"`/negative rejected; absent
  key accepted.
- Worker: fake checker records its context deadline — with
  `config.timeout = 20s` the context deadline is 21s and the budget passed
  down is 20s; unset falls back to the global rule.
- E2E (dash0): set a timeout on the edit form → persisted in `config`;
  clear it → key removed.

## Open questions

1. **Checkers without a `timeout` config field** (e.g. some protocol
   checkers never parse the key): the worker-level context rule still
   bounds them; do any also need the field threaded into their internal
   deadlines, or is the context bound enough for v1?
2. **Minimum value** — is a floor needed (e.g. ≥ 1s) beyond `> 0`? The
   spec assumes `> 0` with sub-second values allowed since checkers parse
   duration strings.

## Implementation Plan

### Resolved open questions

1. **Checkers without a `timeout` config field**: the worker-level context
   bound (`timeout + 1s` hard cancel) is enough for v1. No per-checker
   threading — checkers that ignore the key are cancelled by the context,
   surfacing as `StatusTimeout` via the existing `executeJob` branch.
2. **Minimum value**: no floor beyond `> 0` at the API level — checkers
   parse duration strings, so sub-second values (e.g. `"500ms"`) stay
   valid. The dashboard input is whole seconds with a 1–30 client-side
   hint; the server stays authoritative with `> 0 && <= 30s`.
3. **Passive check types** (`heartbeat`, `email`): the Timeout field is
   hidden — they make no outbound probe (the worker only inspects inbound
   signals) and the edit form doesn't even send `config` for them.
4. **`checkbrowser` 120s cap**: tightened to 30s along with the 60s caps.
   The global create/update cap (30s) makes >30s unwritable for every
   type anyway, and the worker clamp would cut a longer run regardless;
   leaving 120s would keep checker-level validation and the global cap in
   disagreement, which the spec explicitly wants aligned. The browser
   default (30s) remains valid.

### Steps

1. **Server — uniform 30s validation at write time**
   (`server/internal/handlers/checks/`):
   - New `timeout.go` with `maxConfigTimeout = 30 * time.Second` and
     `validateConfigTimeout(config map[string]any) error` returning
     `checkerdef.ConfigError` on: non-string value, unparseable duration,
     `<= 0`, or `> 30s`. Absent/nil key → nil.
   - Call it in `CreateCheck` (on `req.Config`), `UpdateCheck` (on the
     merged config from `applyConfigPatch`, so upsert/import/apply are
     covered too), and `ValidateCheck` (live-validation endpoint) so the
     dashboard's field-level validation agrees.
   - `handleUpdateError` + `handleUpsertError` gain the same
     `checkerdef.IsConfigError` branch `handleCreateError` already has, so
     PATCH/PUT surface a 400 `VALIDATION_ERROR` on the `timeout` field.
2. **Server — tighten per-checker caps to 30s**: `maxTimeout` consts (60s
   → 30s) in checkrdp, checksftp, checkpostgres, checkkubernetes,
   checkftp, checkredis, checkwebsocket, checkmongodb, checkssl,
   checkgrpc, checkimap, checksnmp, checkmqtt, checksip, checkmssql,
   checkoracle, checkdnsbl, checkntp, checkrabbitmq, checkdocker; the
   inline 60s in `checktcp/checker.go`; checkbrowser 120s → 30s. Fix any
   checker tests asserting the old caps.
3. **Worker — per-check timeout wins, context = timeout + 1s**
   (`server/internal/checkworker/worker.go`): `perCheckTimeout(config)`
   helper (duration-string parse, `> 0`, defensive clamp at 30s). In
   `executeJob`, when present it replaces the cost-aware
   `schedParams.ExecutionTimeout(...)` as the execution budget; the
   context stays budget + 1s. Unset → unchanged.
4. **Backend tests**: table-driven handler tests (mirroring
   `TestCheckPeriodBoundsOnCreate/Patch`) — create with `"31s"` → 400
   `VALIDATION_ERROR`, `"30s"` → 201, `"0s"`/`"-5s"`/non-string → 400,
   absent → 201; PATCH with `"31s"` → 400, `"10s"` → 200, config without
   the key removes it. Unit tests for `validateConfigTimeout` and
   `perCheckTimeout`. Worker test mirroring
   `TestExecuteJob_ExecutionContextIsCheckTimeoutPlusOneSecond`: config
   `timeout: "2s"` → observed context deadline ≈ 3s (budget 2s + 1s
   margin) even when the global/cost-aware ceiling differs; legacy
   `"60s"` config → deadline ≈ 31s (30s worker clamp + 1s).
5. **Frontend (dash0)** — `web/dash0/src/components/shared/check-form.tsx`:
   optional "Timeout" numeric input (whole seconds, 1–30) in the General
   section next to Check Interval, hidden for passive types, using the
   canonical Label + `Input type="number"` + muted hint pattern from the
   design reference's Forms section (no new primitive needed). State seeds
   from `config.timeout` via `durationStringToSeconds`; sample-apply and
   live-validation (`currentConfig`) include it; submit writes
   `config.timeout = "<n>s"` when set and omits the key when empty
   (non-secret keys are replace-wholesale on PATCH, so clearing removes
   it). Field-level error from live validation rendered under the input.
6. **E2E (dash0)** — extend `web/dash0/e2e/checks.spec.ts`: create a check
   with a timeout via the form, verify the edit form shows it after
   reload (persisted in `config`), clear it, save, verify it's empty after
   reload (key removed).
7. **QA**: `make build-backend lint-back test`, `make build-dash0`,
   `cd web/dash0 && bun run lint` (no new errors in touched files).
