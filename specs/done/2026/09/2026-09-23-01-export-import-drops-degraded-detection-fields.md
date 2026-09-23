---
model: sonnet
effort: medium
---

# Config-as-code export/import silently drops a check's degraded-detection configuration

## Problem

The export/import (config-as-code) path for checks in
[server/internal/handlers/checks/service.go](server/internal/handlers/checks/service.go)
does not carry the six degraded-detection fields. A check that has been
customized with a non-default degraded configuration loses that
configuration on an export/import round-trip and silently reverts to the
code defaults.

Evidence, all in `service.go`:
- `ExportCheck` (around line 3343) carries `ReopenCooldownMultiplier`,
  `FlappingWindowSeconds`, `FlapBackoffFactor`, `MaxRecoveryMultiplier`,
  `TracerouteOnFailure` — but none of `DegradedFailures`,
  `DegradedFailuresWindow`, `DegradedSlow`, `DegradedSlowWindow`,
  `SlowThresholdMs`, `DegradedEnabled`.
- `exportCheck`'s construction (around line 3591) populates the five fields
  above from `check.*` but has no equivalent lines for the degraded fields.
- `UpsertCheckRequest` (around line 1662) likewise lacks all six fields, and
  `UpsertCheck`'s `UpdateCheckRequest`/`Check` construction (around line
  2055-2100) does not forward them.
- `buildImportUpsertRequest` (line 4304) builds an `UpsertCheckRequest` from
  an `ExportCheck` and forwards the five existing tunables (lines
  4317-4320) — it has no way to forward the degraded fields because
  `ExportCheck` never carried them in the first place.

The five numeric degraded columns are nullable (`*int` in
[server/internal/db/models/check.go:199-203](server/internal/db/models/check.go:199),
`DegradedFailures`, `DegradedFailuresWindow`, `DegradedSlow`,
`DegradedSlowWindow`, `SlowThresholdMs`) where `NULL` means "use the code
default", resolved at read time by `EffectiveDegradedFailures()` and its
four siblings (check.go:449-479). `DegradedEnabled` (check.go:215) is a
plain non-nullable `bool`.

This matters because the export document must carry the **raw pointer
value** (`nil` → omit the field from the JSON document), not the resolved
effective value. `ExportCheck.TracerouteOnFailure`'s own doc comment
(service.go:1712, repeated at the export struct) explains the same
principle for that field: "IT MUST TRAVEL … an explicit opt-out quietly
becoming an opt-in on restore, with no diff to notice, is the worst
direction this field could fail in." Exporting the *effective* (defaulted)
degraded values instead of the raw pointers would have the mirror-image
failure: every export freezes today's code defaults into the document, and
re-importing it on a different code version (or after the defaults change)
would pin every previously-unconfigured check to stale values, plus every
diff/plan would report a spurious change the moment `DefaultDegradedFailures`
& co are tuned.

## Proposal

1. Add the six fields to `ExportCheck` as pointers mirroring the DB model
   shape (`DegradedFailures *int`, `DegradedFailuresWindow *int`,
   `DegradedSlow *int`, `DegradedSlowWindow *int`, `SlowThresholdMs *int`,
   all `omitempty`), plus `DegradedEnabled bool` (non-nullable, matching the
   model — but consider whether it needs `omitempty` given `false` is a
   meaningful non-default value; check how `Enabled` on the check itself is
   handled for precedent).
2. Populate them in the `exportCheck` construction directly from
   `check.DegradedFailures` etc. — copy the raw pointer, do **not** call the
   `Effective*` accessors.
3. Add the same six fields to `UpsertCheckRequest`, and thread them through
   `UpsertCheck`'s request-to-model construction the same way the existing
   `ReopenCooldownMultiplier` et al. are threaded (nil-pointer means leave
   unset / use default, matching the `intPtr`/pointer-passthrough pattern
   already used for the other nullable fields).
4. Update `buildImportUpsertRequest` to forward all six fields from the
   `ExportCheck` into the `UpsertCheckRequest`, matching the existing
   pattern for `ReopenCooldownMultiplier` & co (lines 4317-4320).
5. Check whether the plan/diff path (`CheckFieldChange` /
   `ImportPlanEntry.Changes`) needs the same six fields added to its
   comparison set so `/plan` and `/apply` report degraded-config drift
   instead of silently ignoring it — search for where the existing five
   tunables are compared for the diff and mirror it.
6. Consult [wiki/features/degraded-detection.md](wiki/features/degraded-detection.md)
   for the full field contract before finalizing names/semantics.

### Test coverage to add

- An export/import (or export/plan/apply) round-trip test where a check has
  a **customized** degraded config (e.g. `DegradedFailures=10`,
  `DegradedEnabled=false`, a non-default `SlowThresholdMs`) — assert the
  round-tripped check's raw DB columns match the original customization
  exactly, not just the effective/resolved values.
- A companion test where a check has **no** degraded customization (all five
  numeric columns `NULL` in the DB) — assert that after export/import the
  columns are still `NULL`, not written as the resolved defaults
  (`5/60/3/6/0`). This is the regression this bug would otherwise reintroduce
  silently: a naive fix that exports `Effective*()` values would pass a
  "does it round-trip" test but fail this one.
- Confirm the export document itself omits the degraded fields entirely for
  an unconfigured check (raw pointers are `nil` → `omitempty` drops them),
  not just that the DB ends up NULL after import.
