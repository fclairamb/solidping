---
model: sonnet
effort: high
---

# `POST /checks/validate` silently ignores request fields the create path refuses, so a payload can validate clean and then 422

## Problem

`/checks/validate` is the documented pre-flight for `/checks`: the dash0 create
form, the MCP `validate_check` tool
(`server/internal/mcp/tools_checktypes.go:64`) and any checks-as-code tooling
call it to find out whether a body will be accepted. Today it can answer
`{"valid": true}` for a body that `POST /checks` then rejects with 422.

Observed against prod on 2026-08-28, copying a check between orgs. The same
payload, including `"internal": false`:

```
POST /api/v1/orgs/public/checks/validate  →  200 {"valid": true}
POST /api/v1/orgs/public/checks           →  422
{"code":"VALIDATION_ERROR","title":"Validation error",
 "fields":[{"name":"internal",
            "message":"The internal flag is read-only: it marks server-created checks and cannot be set by a client"}]}
```

### Root cause: two different request structs, not one skipped rule

The two endpoints decode into **different, unrelated structs**:

- `CreateCheckRequest` (`server/internal/handlers/checks/service.go:1147`)
  carries `Internal *bool` that is, per its own comment,
  "DECODED ONLY SO IT CAN BE REFUSED (spec 2026-08-27-01)" — the guard is
  `CreateCheck` at `server/internal/handlers/checks/service.go:1205`, returning
  `ErrInternalFieldNotWritable`, rendered by
  `writeInternalFieldError` (`server/internal/handlers/checks/handler.go:682`).
- `ValidateCheckRequest` (`server/internal/handlers/checks/service.go:45`) has
  **no `Internal` field at all**, and `ValidateCheck`
  (`server/internal/handlers/checks/handler.go:92`) decodes with a plain
  `json.NewDecoder(...).Decode(...)` — no `DisallowUnknownFields`. So `internal`
  is silently dropped before any validation runs. Validate is not skipping a
  check; it never sees the field.

Note what is *already* shared: config-level validation goes through
`s.firstConfigValidationError(...)`, which `CreateCheck`
(`server/internal/handlers/checks/service.go:1280`) explicitly calls out as
running "from the same list the dry-run validate endpoint reads". The gap is
specifically **request/field-level** validation, which has no shared routine.

### Why `internal` in particular matters

It is not a cosmetic field. `internal` is what exempts a check from the
`MaxChecks` entitlement quota — the create-path comment at
`server/internal/handlers/checks/service.go:1204` says accepting it "would hand
every caller a quota bypass". So the one field validate is blind to is the one
guarding a quota bypass. A client that trusts `valid: true` learns nothing about
the refusal until it writes.

### The same blindness covers more than `internal`

`ValidateCheckRequest` carries only `Type`, `Slug`, `Config`, `Regions`,
`DependsOn`, `Period`, `Enabled`, `ExcludeCheckUID`. Every other
`CreateCheckRequest` field is dropped on the floor: `Name`, `Description`,
`CheckGroupUID`, `Labels`, `RegionSpread`, `ConfirmationPeriodSeconds`,
`RecoveryPeriodSeconds`, `TracerouteOnFailure`, `ReopenCooldownMultiplier`,
`FlappingWindowSeconds`, `FlapBackoffFactor`, `MaxRecoveryMultiplier`,
`EscalationPolicyUID`. Some of these have create-time rules (e.g. the
`0 <= regionSpread < period` bound, the `tracerouteOnFailure` enum, group and
escalation-policy existence) that a caller cannot pre-flight at all today.

## Proposal

Make request-level validation a single routine both paths run, so validate and
create agree by construction rather than by two lists kept in sync by hand.

1. **Factor the request-level guards out of `CreateCheck`** into one function in
   `server/internal/handlers/checks/` that takes the proposed field set and
   returns findings, alongside the existing shared
   `firstConfigValidationError`. `CreateCheck` keeps mapping the first blocking
   finding to its typed error (`ErrInternalFieldNotWritable` and friends) so the
   422 shape and its `fields[].name` / `fields[].message` do not change.

2. **Teach `ValidateCheckRequest` the fields it currently drops**, starting with
   `Internal *bool` decoded-only-to-refuse, mirroring the create struct's
   comment so the intent survives.

3. **Mind the response-shape mismatch — this is the part to get right.** Create
   signals refusal as an *error* (422 `VALIDATION_ERROR`); validate reports
   findings as a *200* with `valid: false` and a `Fields` list
   (`ValidateCheckResponse`, `server/internal/handlers/checks/service.go:75`).
   Validate must therefore surface a read-only-field refusal as a **blocking
   finding** — `valid: false`, `fields[].name == "internal"`, same message
   constant `msgInternalNotWritable`
   (`server/internal/handlers/checks/handler.go:71`) — and must **not** start
   returning 422. `valid` is false exactly when `Fields` is non-empty, per the
   existing contract; keep that invariant.

4. **Decide, and write down, how far parity goes.** Bringing across the
   remaining fields in the list above is the natural end state, but each needs
   its create-time rule to be reachable without a write. If any cannot be
   validated dry, say so in the spec's Decisions rather than half-wiring it —
   a field that validate accepts and create rejects is the bug being fixed, and
   adding new instances of it would be worse than leaving those fields
   documented as out of scope.

### Tests

Table-driven, in `server/internal/handlers/checks/`:

- **The parity property is the point.** Drive a table of payloads through
  *both* endpoints and assert they agree on accept/reject, rather than asserting
  each in isolation — that is the invariant that regressed, and it is what keeps
  a future field from drifting the same way.
- **Positive control:** a payload with `internal` present must be rejected by
  both (validate: `valid: false` naming `internal`; create: 422 naming
  `internal`) — and the identical payload *without* `internal` must be accepted
  by both. Without the second half the test passes against a validate endpoint
  that simply rejects everything.
- Cover `internal` set to an explicit `false`, not just `true`: the create guard
  fires on any non-nil value, and `false` is the case that reads as harmless and
  is most likely to be let through by a partial fix.
- Assert the exact `fields[].name` and message, since clients key off them.

### Out of scope

`DisallowUnknownFields` on either decoder. It would catch this class of bug
wholesale but is a breaking change for existing clients that send extra
properties, and it belongs in its own spec with its own compatibility argument.

## Implementation Plan

1. Add a shared, side-effect-free `requestFieldValues` / `requestFieldFindings`
   pair in `server/internal/handlers/checks/validate.go` covering every
   request-level guard that is fully decidable from the request alone (no DB
   read, no write): `internal` (always refused), `regionSpread`'s
   `0 <= spread < period` bound, `tracerouteOnFailure`'s enum,
   `flappingWindowSeconds` / `flapBackoffFactor` / `maxRecoveryMultiplier`'s
   floors, and `confirmationPeriodSeconds` / `recoveryPeriodSeconds`'s
   `[0, 86400]` bound. The function returns every finding, in the same
   left-to-right order `CreateCheck` has always checked them in, each carrying
   the field name, a machine code, a message, and the exact `error` value
   `CreateCheck` used to return for it.
2. `CreateCheck` (`service.go`) calls this function at two points, replacing
   the ad hoc per-field checks with the same order/behavior: once at the top
   with only `Internal` populated (preserves the "refuse before the quota
   check" ordering spec 2026-08-27-01 relies on), and once after `check.Period`
   is resolved, with the rest of the fields. Both call sites take only the
   FIRST finding and return its `.Err` — the exact same error values as
   before, so the 422/400 shape, status code, and `errors.Is` switches in
   `handler.go` are unchanged.
3. `ValidateCheckRequest` gains `Internal *bool`, `RegionSpread *string`,
   `ConfirmationPeriodSeconds *int`, `RecoveryPeriodSeconds *int`,
   `TracerouteOnFailure *string`, `FlappingWindowSeconds *int`,
   `FlapBackoffFactor *int`, `MaxRecoveryMultiplier *int` — same JSON tags as
   `CreateCheckRequest`. `ValidateCheck` (`validate.go`) calls
   `requestFieldFindings` once and turns EVERY finding into a blocking
   `fields[]` entry (not just the first), consistent with the "report
   everything" contract spec 2026-08-26-05 already established for config
   findings.
4. `regionSpread`'s bound needs an effective period even when the request
   doesn't propose one: `CreateCheck` defaults to `models.NewCheck`'s 1-minute
   `Period` when `req.Period` is absent, so `ValidateCheck` computes the same
   fallback before calling the shared function, rather than reusing 0 (which
   would flag every non-empty `regionSpread` as invalid whenever period is
   omitted — a false positive parity would have introduced).
5. New machine codes: `CodeInternalNotWritable`, `CodeInvalidRegionSpread`,
   `CodeInvalidTracerouteOnFailure`, `CodeInvalidFlappingField`,
   `CodeInvalidIncidentPeriod`, alongside the existing `validate.go` `Code*`
   block.
6. Tests in `server/internal/handlers/checks/`, table-driven, driving payloads
   through both `CreateCheck` and `ValidateCheck` and asserting they agree —
   `internal` (true, false, and absent — positive control) is the primary
   case, plus one row each for the newly-parity'd fields.
7. `server/internal/mcp/tools_checktypes.go`'s `validate_check` tool builds its
   own `ValidateCheckRequest` from a fixed `{type, config}` schema and never
   sets any of the new fields — verified as needing no change (see Decisions).

## Decisions

Fields brought into `ValidateCheckRequest` for parity with `CreateCheckRequest`
(all have a create-time rule that is fully decidable from the request alone,
no DB read or write required):

- `internal` — the field this bug report is about. Always refused when
  non-nil (including explicit `false`), same `msgInternalNotWritable`
  constant and `fields[].name == "internal"` as create's 422.
- `regionSpread` — `0 <= spread < period` (spec 2026-07-20-05), pure duration
  arithmetic once the effective period is resolved (see plan item 4).
- `tracerouteOnFailure` — `inherit` / `on` / `off` enum via the existing
  `parseTraceroutePolicy`, pure string matching.
- `flappingWindowSeconds` (>= 0), `flapBackoffFactor` (>= 1),
  `maxRecoveryMultiplier` (>= 1) — pure integer floors.
- `confirmationPeriodSeconds`, `recoveryPeriodSeconds` — pure integer range
  `[0, 86400]`.

Fields deliberately left out, with why each cannot be validated dry today:

- `checkGroupUid`, `escalationPolicyUid` — **`CreateCheck` itself performs no
  existence check on these before writing.** It stores the FK verbatim
  (`service.go` around the `check.CheckGroupUID = req.CheckGroupUID` /
  `check.EscalationPolicyUID = req.EscalationPolicyUID` assignments) and
  relies on the database's foreign-key constraint to reject a dangling
  reference at insert time — which surfaces as an internal error, not a clean
  422. There is no create-time *rule* to mirror into validate without first
  adding one to create, which is a separate, larger change (and would need
  its own decision about whether a dangling reference should become a
  request-level 422 at all). Bringing "does this UID exist" into validate
  while create still doesn't check it would itself be a parity gap, just
  moved to a different pair of fields — so it stays out.
- `name`, `description`, `labels` — no create-time rule rejects any value for
  these; `checker.Validate`'s only interaction with `Name` is filling in a
  default when it is empty, never rejecting what's supplied. Nothing to
  pre-flight.
- `reopenCooldownMultiplier` — set on the model with no validation at all
  (`check.ReopenCooldownMultiplier = req.ReopenCooldownMultiplier`, any value
  accepted). Same reasoning as `name`/`description`/`labels`.

`server/internal/mcp/tools_checktypes.go`'s `validate_check` tool exposes only
`{type, config}` in its input schema and never forwards `internal` or any of
the other now-parity'd fields — a caller cannot use it to probe the bypass
this spec closes, and it needs no change.
