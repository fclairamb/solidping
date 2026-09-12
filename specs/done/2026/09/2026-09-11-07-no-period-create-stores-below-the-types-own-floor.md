---
model: sonnet
effort: high
---

# A check created with no period gets a flat 1m — below its own type's floor, and its export no longer imports

## Problem

`models.NewCheck` hands every check the same period regardless of type
([check.go:388](server/internal/db/models/check.go:388)):

```go
Period: timeutils.Duration(time.Minute), // default to 1 minute
```

`CreateCheck` only overrides it when the request actually carried one
([service.go:1350](server/internal/handlers/checks/service.go:1350)):

```go
// Set period (default is 1 minute from NewCheck)
if req.Period != nil && *req.Period != "" {
```

But several check types declare a **higher** `MinPeriod` in
`checkerdef.checkTypeMetas` ([types.go:319-356](server/internal/checkers/checkerdef/types.go:319)):

| Type | MinPeriod | DefaultPeriod |
|---|---|---|
| `ssl` | 1h | 6h |
| `domain` | 6h | 24h |
| `dnsbl` | 15m | 1h |
| `js` | 30s | 1m |
| `browser` | 1m | 5m |

`validatePeriodForType` deliberately exempts a zero/absent period on create —
"A zero period means *not provided* (the default applies) and always passes"
([service.go:305-314](server/internal/handlers/checks/service.go:305)) — and
`planPeriod` returns the **raw** period (zero when none was proposed), so
nothing downstream ever re-checks the value that actually lands in the row
([plan.go:276-298](server/internal/handlers/checks/plan.go:276)).

The exemption's premise — that "the default applies" — is simply false: the
default it falls through to is `NewCheck`'s flat constant, not the type's own
`DefaultPeriod`. So:

```
POST /orgs/:org/checks {"type":"ssl","name":"cert", ...}   # no period
→ 201, stored period = 1m       # the type's own floor is 1h
```

An SSL certificate re-fetched every minute is 60× the intended probe volume on
a target that changes monthly, and the same is true for `domain` (6h floor,
stored at 1m = 360×) and `dnsbl` (15m floor). It is also a period the same
server would reject outright had the caller typed it.

### The visible consequence: a broken config-as-code round trip

`GET /orgs/:org/checks/export` emits `period: "1m"` for that check, and
re-importing the very same document fails:

```
POST /orgs/:org/checks/import?dryRun=true
→ period for ssl checks must be at least 1h
```

An org's own export is not a document the server will take back. Reproduce:
create an `ssl` or `dnsbl` check with no period, export the org, import the
result.

`TestExportRoundTripsThroughValidateAndImport`
([export_roundtrip_test.go:95](server/internal/handlers/checks/export_roundtrip_test.go:95))
already works around this — it creates every sample with an explicit
`defaultPeriodFor(checkType)` and says so in a comment
([:139-155](server/internal/handlers/checks/export_roundtrip_test.go:139)).
That workaround exists only because of this bug and should be deleted as part
of the fix.

### Who is actually affected

- **Not the dashboard.** `check-form.tsx` already resolves the type's
  `defaultPeriodSeconds` before submitting
  ([check-form.tsx:396](web/dash0/src/components/shared/check-form.tsx:396)),
  so UI-created checks are fine.
- **API, MCP, client-library and import callers** that omit `period` — i.e.
  exactly the config-as-code audience — get the wrong value.
- **The third-party importers already do this right**: `converter.go` raises a
  below-floor period to `meta.DefaultPeriod` (clamped to `MinPeriod`) and warns
  ([converter.go:285-310](server/internal/handlers/checks/importers/converter.go:285)).
  The first-party create path is the outlier.

## Proposal

### 1. Resolve the type's own default when the request supplies none

In `CreateCheck`, replace the `NewCheck` constant with a resolution off
`checkerdef.GetCheckTypeMeta`:

- request supplies a period → parse and use it (unchanged; `planPeriod`
  already validated it against the type's bounds);
- request supplies none → `meta.DefaultPeriod`, falling back to the current
  `time.Minute` when the meta declares none (several types do: `http`, `tcp`,
  `icmp`, …).

Clamp the resolved value up to `MinPeriod` if a meta ever declares a
`DefaultPeriod` below its own floor — the importers' `converter.go` already
carries exactly that guard, and the invariant should hold by construction
rather than by the metas happening to be consistent today. A unit test over
`checkerdef.ListCheckTypes(nil)` asserting `DefaultPeriod == 0 ||
DefaultPeriod >= MinPeriod` would catch a future meta that violates it.

Put the resolution in **one** exported-within-package helper (e.g.
`defaultPeriodForType(checkType) time.Duration`) so create, the plan, and the
validate path can't drift apart.

### 2. Make `defaultCheckPeriod`'s two other users type-aware

`defaultCheckPeriod` ([validate.go:113](server/internal/handlers/checks/validate.go:113))
is documented as "mirrors `models.NewCheck`'s default Period" and is used to
compute the **effective** period that `regionSpread` is validated against, in
two places:

- [plan.go:148-150](server/internal/handlers/checks/plan.go:148) (create), and
- [validate.go:533-535](server/internal/handlers/checks/validate.go:533)
  (`POST /checks/validate`).

Both must use the same new resolution, or `POST /checks/validate` will answer
about a 1m check while `POST /checks` writes a 6h one — the exact
create/validate divergence spec 2026-08-28-14 exists to prevent. Update the
constant's comment (or replace it with the helper) so the "mirrors NewCheck"
claim stops being load-bearing.

Decide explicitly whether `NewCheck` itself should keep its 1m constant. It is
the right call to leave it (it has no meta access and is used by the clone path
at [service.go:4190](server/internal/handlers/checks/service.go:4190), which
overwrites the period from the source check anyway) — but say so in a comment
pointing at the resolver, so the next reader doesn't "fix" it in the wrong
layer.

### 3. Confirm the other create doors go through the same resolution

- **Upsert / `PUT`-by-slug** delegates to `CreateCheck` on the create branch
  ([service.go:1996](server/internal/handlers/checks/service.go:1996)) — should
  be covered for free; assert it with a test rather than by reading.
- **Import** builds upsert requests; verify the created-check branch lands on
  the same path and that a document omitting `period` produces the type's
  default, not 1m.
- **The seeded catalog / startup job** and any test fixtures that call
  `models.NewCheck` directly bypass the resolver — check whether any of them
  create a type with a `MinPeriod` and, if so, whether that matters (they are
  `Internal`, which `validatePeriodForType` exempts, so this may be a
  deliberate non-issue — state the conclusion either way).

### 4. Tests

- **The headline test**: for every type in `checkerdef.ListCheckTypes(nil)`,
  create a check with **no** period and assert the stored `Period` satisfies
  `validatePeriodForType(type, stored, false)`. This is the test that would
  have caught the bug, and it stays honest as new types are registered.
- A round-trip assertion that an `ssl` (or `dnsbl`) check created with no
  period exports a document that `ValidateDocument` + `import?dryRun=true`
  accept.
- Create/validate parity: `POST /checks/validate` and `POST /checks` agree on
  the effective period for a no-period request (drive it through the
  `regionSpread` bound, which is where the effective period is observable).
- **Delete the `defaultPeriodFor` workaround** in
  `export_roundtrip_test.go` and let the samples be created with no period —
  the round-trip test then covers this bug for free, which is the whole point.

### 5. Backfill: decide, don't default

Existing rows created before this fix carry a below-floor period. There is a
real tension to resolve rather than paper over:

- `validatePeriodForType`'s own comment states the standing decision:
  *"Existing rows are grandfathered: this runs only on create/update writes,
  never via migration."*
- But these rows are not the grandfathered case that decision was written for.
  They were never a value the user chose — the user supplied nothing, and the
  server picked wrong. And while they stay below the floor, the org's export
  keeps failing its own import, which is the defect this spec is about.

Recommended: backfill **only** rows whose period is below their type's
`MinPeriod` **and** equal to the 1m constant (i.e. the fingerprint of "the
server picked this, nobody asked for it"), raising them to the type's
`DefaultPeriod`. A row at, say, 30m on a `dnsbl` check was typed by a human
through some earlier path and must be left alone. If the audit finds that no
API path could ever have produced a below-floor period other than the 1m
default, say so and simplify the predicate accordingly.

Mechanically: **add a new `SECTION:` to the existing, still-unreleased
`021_v0_28_0` migration** — do not create a new numbered file. That file
already carries the `check-name-backfill` section from spec 2026-09-11-02 and
its header documents the one-consolidated-migration-per-release rule
([021_v0_28_0.up.sql](server/internal/db/postgres/migrations/021_v0_28_0.up.sql),
and the SQLite twin). Both dialects, both directions; the header's warning
applies — a dev database that already ran an earlier draft of 021 must be
**reset**, never `migrate repair`'d.

The per-type floors have to be expressed in SQL (the metas live in Go), so
write the `CASE`/`WHERE` over the handful of types that declare a `MinPeriod`
and add a Go test that the SQL's type list matches
`checkerdef`'s — otherwise a future type with a floor gets silently missed.

If the backfill is judged too risky to run unattended, the fallback is to skip
it and note it in the changelog as "new checks are fixed; existing ones can be
corrected with a PATCH" — but make that an explicit, stated decision, not an
omission.

### 6. Changelog

User-visible behaviour change on a documented API: a create that omits
`period` now stores a different (larger) value for `ssl`, `domain`, `dnsbl`,
`js` and `browser`. Note the round-trip fix as the motivation, and the backfill
(or its absence) as the operator-facing consequence. Conventions in
`wiki/conventions/changelog.md`.

## Implementation Plan

1. **One resolver — `defaultPeriodForType`.** Add a package-internal helper in
   `server/internal/handlers/checks/validate.go`, next to `defaultCheckPeriod`
   (whose comment gets rewritten so "mirrors `models.NewCheck`" stops being
   load-bearing): resolves `checkerdef.GetCheckTypeMeta(type).DefaultPeriod`,
   clamped up to that type's own `MinPeriod`, falling back to the flat
   `defaultCheckPeriod` (1m) when the type declares no default. Three call
   sites switch to it:
   - `CreateCheck` (`service.go`, "Set period" block) — else-branch now
     resolves the type default instead of relying on `NewCheck`'s constant.
   - `plan.go:148-150` (`planCreateCheck`'s effective-period computation for
     the `regionSpread` bound).
   - `validate.go:533-535` (`validateRequestFieldFindings`, used by
     `POST /checks/validate`).
   `NewCheck` itself (`check.go:388`) keeps its flat 1m constant — it has no
   meta access and the clone path (`service.go:4365`) overwrites `Period` from
   the source check regardless — with a comment pointing at the resolver so
   the next reader doesn't "fix" it in the wrong layer.

2. **Confirm the other create doors** (assert with tests, not by reading):
   - Upsert/`PUT`-by-slug create branch → delegates to `CreateCheck`, covered
     for free.
   - Import → `buildImportUpsertRequest` leaves `Period` nil when the document
     omits it, which lands on the same `CreateCheck`/`UpsertCheck` path.
   - Direct `models.NewCheck` callers outside `checks.Service`
     (`checkworker/worker.go`, `jobworker/worker.go`,
     `jobtypes/job_startup.go`, `jobtypes/job_startup_demo.go`,
     `testapi/generate_data.go`, `testapi/bulk_checks.go`): the two worker
     paths set `check.Internal = true` (exempted by `validatePeriodForType`);
     the other four always assign `check.Period` explicitly from their own
     sample/param period before insert, so none of them ever surface
     `NewCheck`'s bare constant. State this conclusion in the self-review
     rather than adding redundant tests for code that never reaches the bug.

3. **Tests**
   - `checkerdef`: `TestCheckTypeMetaDefaultPeriodNeverBelowMinPeriod` — for
     every registered meta, `DefaultPeriod == 0 || DefaultPeriod >= MinPeriod`.
   - `checks` package: headline test — for every type in
     `checkerdef.ListCheckTypes(nil)`, `CreateCheck` with no period, assert the
     stored `Period` satisfies `validatePeriodForType(type, stored, false)`.
   - Round-trip: an `ssl` (and `dnsbl`) check created with no period exports a
     document that `ValidateDocument` + `import?dryRun=true` accept.
   - Create/validate parity: for a no-period request on a type with a
     `MinPeriod` (e.g. `ssl`), `POST /checks/validate`'s accepted `regionSpread`
     bound and `POST /checks`'s stored effective period agree.
   - Upsert-create and import-create both land the type's default period, not
     1m, via a dedicated assertion.
   - Delete `defaultPeriodFor` from `export_roundtrip_test.go` and stop passing
     an explicit `Period` in `seedSampleChecks`.

4. **Migration** — append a new `SECTION: period-below-floor-backfill` to the
   *existing* `021_v0_28_0.{up,down}.sql` in both dialects (never a new `022`).
   Backfill predicate: `type` is one of every checkerdef type that declares a
   `MinPeriod` (all 5: `ssl`, `domain`, `dnsbl`, `js`, `browser` — listed in
   full even though `js`/`browser`'s floor sits at/below the flat 1m constant,
   so their branch of the `WHERE` never matches; that's what makes the parity
   test below non-trivial), `period` equals the flat 1-minute fingerprint, AND
   `period` is below that type's own `MinPeriod` → raised to that type's
   `DefaultPeriod`. A `PERIOD_BACKFILL_TYPES:` marker comment in both `up.sql`
   files pins the literal type list. `down.sql` gets a new top section
   (reverse order) documenting the backfill is deliberately NOT reversed, same
   rationale as the existing `check-name-backfill` section.
   - Go test (`checkerdef` package, reads both migration files by relative
     path): the `PERIOD_BACKFILL_TYPES:` marker in each dialect's `up.sql`
     matches, sorted, the set of `checkerdef` types with `MinPeriod > 0`.
   - SQLite functional test: pre-021 schema + seeded checks (flat 1m on a type
     with a floor, a human-typed below-floor value on a different type, a 1m
     value on a type whose floor is at/below 1m, a 1m value on a type with no
     floor) → apply `021_v0_28_0.up.sql` → assert exactly the fingerprinted
     rows moved.
   - Postgres functional test (embedded postgres, skipped under `-short`):
     `Initialize()` a fresh DB (harmless no-op backfill), insert the same
     fixture rows directly, re-run just the backfill SQL block (kept in a
     `const` mirroring the migration, same pattern as
     `owner_backfill_migration_postgres_test.go`), assert the same outcome.

5. **Backfill decision** — as stated above: backfill only rows at the flat
   1-minute fingerprint AND below their own type's floor. This is the
   "narrower than grandfathering" case the spec recommends, not a departure
   from `validatePeriodForType`'s "existing rows are grandfathered" doc
   comment (which stays true for every other below-floor row).

6. **Changelog** — one entry under the next unreleased version noting: a
   create/import that omits `period` for `ssl`/`domain`/`dnsbl`/`js`/`browser`
   now stores that type's own default period instead of a flat 1 minute, and
   that existing affected rows are backfilled on this release (state the
   backfill's own predicate briefly, since it changes stored data on upgrade).
