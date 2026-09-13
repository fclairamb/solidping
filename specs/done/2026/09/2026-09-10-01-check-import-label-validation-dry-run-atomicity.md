---
model: opus
effort: high
---

# Check import: label keys pass API validation but fail the DB, dry-run doesn't check them, and a failed item still leaves a check behind while reporting `created: 0`

## Problem

One real bulk import of 47 checks against a live org on 2026-09-10 hit three
defects in sequence. Each one on its own is a bug; together they produced the
worst possible outcome — a dry run that said "all good", a real run that said
"nothing happened", and an org that silently gained 47 half-configured checks.

### 1. Label-key validation is more permissive than the DB constraint

There are three different rules for what a label key may look like, and the
one the import document is checked against is the odd one out:

| Where | Rule | Notes |
|---|---|---|
| Postgres `labels.key` CHECK — [001_v0_1_0.up.sql:403](server/internal/db/postgres/migrations/001_v0_1_0.up.sql:403) | `^[a-z][a-z0-9-]{2,50}$` | leading letter, 3–51 chars, no dots |
| dash0 `KEY_REGEX` — [label-shared.ts:9](web/dash0/src/components/shared/label-shared.ts:9) | `^[a-z][a-z0-9-]{2,50}$` | same as Postgres |
| Status-page selector — [status_page.go:441](server/internal/db/models/status_page.go:441) | `^[a-z][a-z0-9-]{2,50}$` | same, and its comment already says it "mirrors the label key rule" |
| Import/validate document `labelKeyRegex` — [validate_document.go:35](server/internal/handlers/checks/validate_document.go:35) | `^[a-z0-9]+(?:[-.][a-z0-9]+)*$` | **accepts 1–2 char keys, leading digits, and dots** — none of which Postgres can store |
| SQLite `labels.key` CHECK — [001_v0_1_0.up.sql:288](server/internal/db/sqlite/migrations/001_v0_1_0.up.sql:288) | `length(key) between 1 and 50` | **accepts anything** — diverges from Postgres |

So a key like `os` (2 chars) or `k8s.cluster` (dot) sails through document
validation, and the *check service* never validates label keys at all —
`CreateCheck` ([service.go:1517](server/internal/handlers/checks/service.go:1517))
and `UpdateCheck` ([service.go:1931](server/internal/handlers/checks/service.go:1931))
just loop `GetOrCreateLabel`, so on Postgres the first bad key surfaces as a
raw driver error, double-prefixed and leaking a SQLSTATE:

```
failed to create label: failed to create label: ERROR: new row for relation "labels" violates check constraint "labels_key_check" (SQLSTATE=23514)
```

On SQLite the same document *succeeds* and stores a key Postgres would refuse,
which is exactly the backend divergence `/sync-pg-to-sqlite` exists to prevent.

### 2. `POST /orgs/:org/checks/import?dryRun=true` gives false confidence

`importSingleCheck` ([service.go:3899](server/internal/handlers/checks/service.go:3899))
runs `validateImportedCheck` (slug/type/config present, type registered,
`internal` refused), looks up the slug to decide created-vs-updated, and then
**returns immediately on `dryRun`** — before group resolution, before the
`UpsertCheckRequest` is even built, and before anything that `UpsertCheck` /
`CreateCheck` would validate (label keys, config via the checker's `Validate`,
regions, period, entitlement quota, …). The dry run for the 47-check document
returned `{"created": 47, "updated": 0, "skipped": 0, "errors": []}` for a
document that could not possibly succeed.

The `ValidateDocument` lint ([validate_document.go:71](server/internal/handlers/checks/validate_document.go:71))
is a separate endpoint and, per (1), would not have caught it either.

A dry run is the thing you check before touching production. One that skips
the validation the real path performs is worse than none.

### 3. The real import is not atomic and misreports what it did

`CreateCheck` inserts the check row first
([service.go:1509](server/internal/handlers/checks/service.go:1509)) and only
then creates/links labels. When a label fails, the error is returned, the
import records it in `errors[]`, and the check row stays. The real run for the
same document returned `{"created": 0, "updated": 0, "skipped": 0, "errors": [47 × label error]}`
— while all 47 checks **had** been created, without their labels and (per the
report) without their group link. Re-running the corrected document reported
`updated: 47`, which is how the state was discovered.

Silently-partial writes plus `created: 0` is the worst combination: a caller
that trusts the response will retry (duplicating work, or tripping quota), or
assume a clean slate.

Note the obstacle to the obvious fix: the DB layer never consults
`dbctx.GetDB`/`TxFromContext` — `postgres.go` and `sqlite.go` each have ~260
direct `s.db.New*` call sites and zero `dbctx.GetDB` uses, and
`dbctx.RunInTx` has **no production callers**. Wrapping `importSingleCheck` in
`RunInTx` would compile, run, and roll back nothing.

## Proposal

### 1. One canonical label-key rule, enforced before any write

- Canonical rule is the Postgres/dash0 one: **`^[a-z][a-z0-9-]{2,50}$`**.
  Rationale: it is what production already stores, what the dashboard already
  enforces on authoring, and what status-page selectors already assume. Dots
  were never storable, so nothing in the wild depends on them; the import
  regex's comment ("mirrors the reference workflow's LABEL_KEY_RE") describes a
  rule the platform never actually honoured.
- Add `models.LabelKeyPattern` + `models.ValidateLabelKey(key string) error`
  (next to `sectionSelectorKeyPattern`, which should then reuse it) returning
  a stable, human-readable message naming the key and the rule, e.g.
  `label key "os" is invalid: must match ^[a-z][a-z0-9-]{2,50}$ (lowercase, starts with a letter, 3-51 chars, letters/digits/hyphens)`.
  Also enforce the existing `value` rule (non-empty, ≤ 200) in the same helper
  so the value CHECK can't leak either.
- Use it in **every** path that reaches `GetOrCreateLabel`:
  `CreateCheck` and `UpdateCheck` label loops (validate the whole map *before*
  the check insert / update, not inside the loop), `validate_document.go`
  (replace `labelKeyRegex`), and any label-authoring endpoint under
  `handlers/labels` that accepts a key. Surface it as `VALIDATION_ERROR` (400)
  from the HTTP layer, and as a per-item `errors[]` entry from import.
- Defense in depth: map a residual `labels_key_check` / value CHECK violation
  from the DB into the same validation error (never a raw SQLSTATE), and fix
  the doubled `failed to create label:` prefix.
- **SQLite parity**: add migration `021` for SQLite that rebuilds `labels` with
  an equivalent CHECK (`length(key) between 3 and 51 and key glob '[a-z]*' and
  key not glob '*[^a-z0-9-]*'`), following the repo's existing
  SQLite table-rebuild pattern if one exists; if a rebuild is judged too risky,
  the Go-level gate is the enforcement and the divergence must be called out
  in the migration/`wiki` notes rather than left implicit. Postgres needs no
  migration. Do not edit the `001` baselines.
- Update the dash0 `KEY_HINT` copy / docs only if the message changes; the
  regex itself is already correct there.

### 2. Dry run validates everything the real path validates

Restructure `importSingleCheck` into **plan** and **apply** phases:

- *Plan* (always runs): `validateImportedCheck`, slug lookup, group resolution
  (existing-or-would-create — on dry run, record "would create group X" without
  writing), build the `UpsertCheckRequest`, then run the same request
  validation `UpsertCheck` → `CreateCheck`/`UpdateCheck` performs — label
  keys/values, checker `Validate` on the config, regions
  (`NormalizeRegionsForOrg`), period/alerting bounds, `internal` refusal, and
  the entitlement quota (`CheckCreateAllowed`) for would-create items. Extract
  that validation from the create/update paths into a reusable
  `validateUpsertRequest` so dry-run and real run cannot drift again.
- *Apply* (real run only): the writes.

Dry run returns the same `created/updated/errors` the real run would, with the
same per-item error strings. Where the real path has a validation the plan
cannot reproduce without writing (a genuine DB uniqueness race, say), document
it in the response — not silently omit it.

### 3. A failed item leaves no check behind, and the counts are honest

Preferred: make create+labels+group a unit at the service level without
relying on the (non-functional) ctx-transaction plumbing:

- With (1) in place, a label failure after the insert can no longer happen for
  validation reasons; the remaining post-insert failures are infrastructure
  (DB down mid-item, unique race). For those, `CreateCheck` performs a
  **compensating hard delete** of the check it just inserted (and its
  `check_jobs` / `check_labels` / `check_connections` rows — `DeleteCheck` is a
  soft delete and is *not* sufficient, the slug would stay claimed) before
  returning the error, and does not emit `check.created` / activation events
  for it.
- Import counts a check as `created` **only if `CreateCheck` returned
  success**. If the compensating delete itself fails, the item is reported as
  created *and* errored — add a per-item `state: "created-incomplete"` (or
  equivalent) to `ImportError` so the caller can see exactly which rows exist —
  never `created: 0` with rows on disk.
- Verify the "no group link" part of the report: `CreateCheck` does copy
  `req.CheckGroupUID` ([service.go:1401](server/internal/handlers/checks/service.go:1401)),
  so either the auto-created group was lost on the failure path or the report
  observed something else. Find out, fix it, and cover it in the regression
  test rather than assuming.
- Pass 2 (`importDependencies`) already skips items in `pass1Failed`; keep
  that contract intact.
- `ImportResult.Skipped` is declared but never incremented — either use it
  (e.g. for items skipped because of pass-1 failure) or drop it from the
  response and OpenAPI so it stops implying a semantics that doesn't exist.

Alternative if the compensating delete proves too entangled with side effects:
teach the DB layer to honour `dbctx.GetDB` for the handful of methods the
create path uses (`CreateCheck`, `GetOrCreateLabel`, `SetCheckLabels`,
`CreateCheckGroup`, job reconciliation) and wrap the per-item apply in
`dbctx.RunInTx`. This is the more correct long-term fix but a bigger blast
radius (both backends, ~260 call sites each); pick it only if the compensating
approach can't be made airtight, and say which was chosen and why.

### Regression tests (all three are required, not optional)

Unit level in `server/internal/handlers/checks/` on the in-memory SQLite
harness already used by `import_yaml_test.go` (`newImportRouter`), plus a
Postgres integration case where the CHECK constraint is what fails:

1. **Label key the DB rejects fails validation up front** — a document with
   keys `os`, `1abc`, `k8s.cluster` (and an empty value) yields per-item
   `VALIDATION_ERROR`-shaped entries naming the key and the rule, with no
   SQLSTATE text and no doubled prefix; nothing is written; the same keys are
   rejected by `POST /checks` and `PATCH /checks/$uid` with 400
   `VALIDATION_ERROR`. Run the same table against both backends (there is an
   existing PG testcontainer suite; assert the SQLite migration 021 CHECK
   rejects the keys at the SQL level too).
2. **Dry run rejects that same document** — `?dryRun=true` returns
   `created: 0` with the same `errors[]` the real run would, and the org's
   check count is unchanged. Add positive controls: a valid document dry-runs
   to `created: N, errors: []` and the real run then produces the identical
   counts; a document with an invalid *config* (checker `Validate` failure) and
   one over the check quota are also caught by the dry run.
3. **A mid-item failure leaves no check behind and reports zero creations** —
   inject a failure *after* the check insert using a `db.Service` wrapper
   (precedent: `barrierIncidentsDB` in
   `server/internal/handlers/availability/service_test.go:725`) that makes
   `SetCheckLabels` (or `GetOrCreateLabel`) fail for one slug in a 3-check
   document. Assert: `created: 2`, one `errors[]` entry for that slug, the
   failed slug does **not** resolve via `GetCheckByUidOrSlug`, no `check_jobs`
   row exists for it, and a re-import of the corrected document reports
   `created: 1, updated: 2`. Add the "compensating delete also fails" branch
   asserting the item is reported as created-incomplete, not silently counted
   as zero.

### Out of scope

- Changing the label key rule itself (e.g. allowing dots) — that is a product
  decision with a Postgres migration and a dash0 change behind it; this spec
  only makes every layer agree on the rule that already exists.
- `POST /checks/apply` — it shares `UpsertCheck`, so it benefits from (1) and
  (3) automatically; its own dry-run planning is a separate code path and is
  not re-audited here beyond confirming the existing
  `server/test/integration/checks_apply_test.go` still passes.

## Implementation Plan

Investigation note that reshapes step 2 below: **`/apply`'s reserved managed
label key `solidping.io/managed` ([apply.go:29](server/internal/handlers/checks/apply.go:29))
cannot be stored on Postgres today** — it carries a dot and a slash, so the
`labels_key_check` CHECK has always refused it. It only ever "worked" because
every apply/importer test runs on the lax in-memory SQLite backend. Making
every layer agree on the rule that already exists therefore *requires* moving
that key to a storable spelling; it is not an optional extra.

1. **Canonical rule in `models`.** New `server/internal/db/models/label.go`
   holding `LabelKeyPatternSource` / `LabelKeyPattern` (`^[a-z][a-z0-9-]{2,50}$`),
   `LabelValueMaxLen`, `ErrLabelKeyInvalid` / `ErrLabelValueInvalid`, and
   `ValidateLabelKey` / `ValidateLabelValue` / `ValidateLabels` (sorted-key
   iteration so the first error is deterministic). `sectionSelectorKeyPattern`
   is deleted and its two call sites reuse `LabelKeyPattern`.

2. **Reserved managed key becomes storable.** `ManagedLabelKey` →
   `solidping-managed`. Update the four `web/docs/docs/features/migrate/*.md`
   pages plus `migrate/index.md`, and carry existing SQLite rows over in the
   migration (step 4).

3. **Enforce before any write.** `CreateCheck` and `UpdateCheck` validate the
   whole label map *before* the insert/update (not inside the
   `GetOrCreateLabel` loop); `validate_document.go` drops `labelKeyRegex` for
   the shared helper; `isCheckFieldValidationError` learns the two new
   sentinels so create/update/upsert answer `400 VALIDATION_ERROR`. The
   doubled `failed to create label:` prefix is removed, and a new
   `db.IsLabelConstraintViolation` maps a residual driver-level CHECK
   violation back onto `ErrLabelKeyInvalid` / `ErrLabelValueInvalid` so no
   SQLSTATE can reach a caller.

4. **SQLite migration `021_v0_28_0`.** Rebuild `labels` with the
   Postgres-equivalent CHECK using the established `*_new` +
   `PRAGMA foreign_keys` pattern (005/009). Before the swap: rename
   `solidping.io/managed` → `solidping-managed`, then drop the remaining
   non-conformant rows (their `check_labels` go with them via cascade) —
   they are rows Postgres could never have held and the Go gate will never
   mint again. Down-migration restores the lax CHECK. Postgres needs no
   migration.

5. **Plan / apply split for import.** Extract the request-level validation
   `CreateCheck` performs into `validateCreateRequest` (returning the
   resolved regions / normalized config / validated spec the write path then
   uses, so there is exactly one copy), and add
   `Service.PlanUpsert(ctx, org, slug, req) error` that runs it for a
   would-create item and the update-side equivalent (labels, period bounds,
   region resolution, merged-config checker `Validate`, request-field
   findings) for a would-update one. `importSingleCheck` becomes
   plan-then-apply: the plan phase always runs (group resolution included —
   on dry run the group is recorded as would-create without writing), the
   apply phase only on a real run. Dry run therefore returns the same
   `created/updated/errors` the real run does.

6. **No check left behind.** Add `PurgeCheck(ctx, uid)` to `db.Service` and
   both backends (a real `DELETE`, not the soft `DeleteCheck`, so the slug is
   released; `check_jobs` / `check_labels` / `check_connections` / `results`
   all cascade). `CreateCheck` calls it when anything after the insert fails,
   and returns the original error without emitting `check.created` or the
   activation event. When the purge itself fails, the error is wrapped in a
   new `ErrCheckCreatedIncomplete` sentinel that import turns into
   `ImportError.State = "created-incomplete"`. `ImportResult.Skipped` is
   given a real meaning: the number of pass-2 dependency applications skipped
   because the item's pass-1 upsert failed.

7. **Regression tests** (the three the spec requires, plus the negative and
   positive controls it names), in
   `server/internal/handlers/checks/label_validation_test.go`,
   `import_dryrun_test.go` and `import_atomicity_test.go`, plus a Postgres
   testcontainer case in `server/internal/db/` proving the CHECK is what
   fails there and a SQLite migration-021 SQL-level case.

8. **Docs.** `CHANGELOG.md` entry, `wiki/api-specification/checks.md` (dry-run
   semantics, `skipped`, `state`), and the label-key rule noted where the
   migration lives.
