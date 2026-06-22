# Dependencies as part of the check's configuration surface

## Context

Check dependencies shipped two days ago (`specs/done/2026/05/2026-05-03-57-check-dependencies-and-cascade-rollup.md`) with frontend completed today (`2026-05-05-01-check-dependencies-frontend.md`). Storage lives in a dedicated `check_dependencies` edge table with its own CRUD endpoints under `/checks/$check/dependencies`.

That works. What doesn't work is the **configuration surface**: deps are second-class everywhere a check is treated as a portable unit.

- `GET /api/v1/orgs/$org/checks/export` returns an `ExportDocument` with no `dependsOn` field. Move an org to a new instance, lose your dependency graph.
- `POST /api/v1/orgs/$org/checks/import` cannot restore deps. The `ExportCheck` struct (`server/internal/handlers/checks/service.go:1275`) has no slot for them.
- `PUT /api/v1/orgs/$org/checks/$slug` (upsert-by-slug) does not let a caller declare deps in the same trip. Tooling that drives a check from a YAML file has to call the deps API as a second, ordered step — and ordering matters because the parent must exist first.
- The CLI (`./solidping client checks ...`) has no dependency verbs. Operators creating checks via the CLI have to drop into curl for deps.

The fix is **not** to move storage into `checks.config` JSON. That's been considered and rejected — see "Why not move storage into `checks.config`" below. The fix is to teach the surfaces that already treat checks as portable units to *carry deps along with them*, while leaving the edge table as the source of truth.

This is glue work, not a new feature. The data model, the validation rules, and the rollup algorithm don't change.

## Why not move storage into `checks.config`

Tempting, but actually wrong:

1. **Org-graph endpoint cost.** `GET /api/v1/orgs/$org/dependencies` is a cheap `SELECT uid, parent_check_uid, child_check_uid, kind FROM check_dependencies`. Moving to a JSON column means a full check-table scan + deserialize on every render. Today's payoff is <10 ms; future-you would measure it in 100s once orgs have a few hundred checks.
2. **Referential integrity.** `parent_check_uid` is a real FK. A deleted parent today leaves no dangling row. Inside JSON, a deleted parent silently rots into a string the rollup hook can't resolve, and we'd be back-filling defensive lookups everywhere.
3. **Validation does not get simpler.** Cycle / self-edge / duplicate / cross-org checks all walk the same graph regardless of storage. The DFS in `checkdependencies/service.go` doesn't shrink.
4. **PATCH semantics on `dependsOn` arrays are ambiguous** (RFC 7396 says "replace whole array"; users will expect merge by parent UID; we'd be reinventing the per-edge UID anyway).
5. **Throws away just-shipped work.** Specs 2026-05-03-57 and 2026-05-05-01 are days old. Re-doing them costs schema migration + frontend rewrite + a re-test of the rollup hook for net-zero capability gain.

The aesthetic "single JSON document per check" appeal is real but cheap to satisfy at the *export/import boundary* (this spec), not at the storage boundary.

## Scope

In scope:

1. **Export/import** carry deps. `ExportCheck` gains a `dependsOn` array; `ExportChecks` populates it from `check_dependencies`; `ImportChecks` resolves parents-by-slug after all checks exist and creates edges, idempotently.
2. **PUT-by-slug** (`PUT /api/v1/orgs/$org/checks/$slug`) accepts an optional `dependsOn` array. When present, it becomes the authoritative set: the service diffs against existing edges and issues create/update/delete to converge. When omitted, deps are untouched (this is critical — partial PUT must not nuke deps).
3. **Validate-check endpoint** (`POST /api/v1/orgs/$org/checks/validate`) accepts and validates `dependsOn` (cycle / self / cross-org / duplicate / unknown parent slug) without writing.
4. **CLI** (`./solidping client`) gains `checks deps list|add|remove|set`, plus `--with-deps` on `checks export`/`import` (default true).
5. **Frontend (dash0)**: the check create/edit form gets a "Dependencies" section that posts in the same trip as the rest of the form. The standalone `<DependenciesCard>` on the check detail page (shipped in 2026-05-05-01) stays — it's the canonical edit surface for "I'm here looking at this check, let me add a parent quickly." But the *create* flow currently has no dep slot; this spec adds it.
6. **Spec updates**: `wiki/api-specification.md` documents the new `dependsOn` field on import/export/upsert payloads.

Out of scope:

- Moving dep storage into `checks.config`. Rejected above.
- Deduplicating edges across the dedicated CRUD API and the new payload-embedded path. Both keep working; the CRUD API is the per-edge surface, the embedded path is the bulk surface. They share validation code.
- "Depended-on-by" as an editable field on the create/edit form. Today's invariant — edges are managed from the *child*, parents are read-only on the parent — stays. A check's import payload declares what *this* check depends on, never what depends on it.
- Editing soft-vs-hard kind via the org-wide YAML/import without UI. Already works because `kind` is part of `dependsOn` entries.
- Versioning the export format. The existing `ExportDocument.Version = 1` stays; we extend the v1 shape additively (new optional field). If a v0 importer ever surfaces, it ignores `dependsOn` and the import succeeds without deps.

## Data model

No schema changes. The `check_dependencies` table from spec 57 is untouched. We're only adding *projection* logic into the check export/import path and the upsert-by-slug path.

The transport shape, mirroring the existing `DependencyEdge` API type but slug-keyed (because export documents are slug-portable, UID-portable would break across instances):

```jsonc
{
  "dependsOn": [
    { "parentSlug": "rabbit", "kind": "hard", "description": "consumes the orders queue" },
    { "parentSlug": "cdn",    "kind": "soft" }
  ]
}
```

Why slug-keyed not UID-keyed:
- Export documents move between instances (dev → prod, dev → staging). UIDs change; slugs are the contract.
- Within a single instance, the upsert-by-slug endpoint already keys checks by slug — staying consistent.

The CRUD API (`POST /checks/$check/dependencies`) keeps using `parentCheckUid` because intra-instance the UID is the stable identifier. The export/import path translates between the two: at export, look up parent's slug; at import, resolve parent slug → UID after all checks have been upserted.

## API

### `ExportCheck` extension

`server/internal/handlers/checks/service.go:1275` — add the field:

```go
type ExportedDependency struct {
    ParentSlug  string `json:"parentSlug"`
    Kind        string `json:"kind"`
    Description string `json:"description,omitempty"`
}

type ExportCheck struct {
    // …existing fields…
    DependsOn []ExportedDependency `json:"dependsOn,omitempty"`
}
```

`omitempty` is load-bearing: existing exports without deps stay byte-identical so anyone diffing snapshots before/after this change sees only intended changes.

### `ExportChecks` population

After fetching checks and labels, fetch all dependencies for the org's checks in one query (`WHERE child_check_uid IN (...)`), then group by child UID. For each `ExportCheck`, look up the parent slug via the existing checks slice (already in memory). Sort dependencies by `parentSlug` for deterministic output (export diffs).

### `ImportChecks` two-pass behavior

Today's import is one pass — for each entry, upsert the check. We change to:

1. **Pass 1**: upsert every check (current behavior). Validation, group resolution, label upsert as today. Skip `dependsOn` entirely. If a check fails, record the error and continue — same idempotency as today.
2. **Pass 2** (new): for every successfully-upserted check that has `dependsOn`:
   - Build a slug→UID map from the now-current set of checks (re-read from DB; some might have been pre-existing).
   - For each entry in `dependsOn`, resolve `parentSlug` → parent UID. If the parent slug isn't in the map → record `ImportError{Slug: child.slug, Error: "unknown parent slug: $parentSlug"}` and skip.
   - Diff against existing dependencies for that child:
     - Edge present in payload but not in DB → create.
     - Edge in DB but not in payload → **leave alone**. Import is additive; deletion happens via the dedicated CRUD API. (This is debatable; alternative is destructive sync. Choosing additive because most import use cases are "merge a fixture into an env," not "make this env match the file." If a destructive variant is needed, add `?syncMode=replace` later.)
     - Edge present in both with same kind/description → no-op.
     - Edge present in both with different kind/description → update.
   - All cycle / self / cross-org checks run via the existing `checkdependencies/service.go` validators.

This sequencing matters: pass 1 must complete before pass 2 because a payload can declare both checks of an edge for the first time.

The result struct gains nothing — `ImportError` already covers per-row errors. Both pass-1 and pass-2 errors land in the same `Errors` slice, distinguished by their `Error` text.

### `PUT /api/v1/orgs/$org/checks/$slug` extension

Today's PUT-by-slug body is the check itself. Extend it with optional `dependsOn`:

- **`dependsOn` absent (or `null`)** → deps untouched. This is the default for any tooling that doesn't know about deps — back-compat preserved.
- **`dependsOn: []`** → all deps for this check are deleted. Explicit empty array == "I want zero parents."
- **`dependsOn: [...]`** → set the deps for this check to exactly this list (destructive sync). Cycle/self/cross-org validation runs first; if any entry fails, the whole PUT fails atomically (no partial state).

Why destructive here but additive in import: PUT-by-slug is "make this resource look like this." That's the contract of PUT. Import is "merge these into the org." Different verbs, different semantics — and that's fine because the operator picks the verb.

Atomicity: wrap the check upsert + dep diff in a transaction. The current PUT handler already runs in a transaction; just include the dep diff inside.

### `POST /api/v1/orgs/$org/checks/validate`

Mirror PUT — accept optional `dependsOn`, run all the same validators, return errors without writing. The frontend uses this for inline validation in the create form (no point letting the user submit a doomed cycle).

### CLI

Add to `cmd/sp` (or wherever check verbs live):

```
sp checks deps list <check-slug>
sp checks deps add <check-slug> <parent-slug> [--kind hard|soft] [--description "…"]
sp checks deps remove <check-slug> <parent-slug>
sp checks deps set <check-slug> [--from <yaml/json file>]
sp checks export --with-deps[=true]            # default true
sp checks import [--with-deps[=true]] <file>   # default true
```

`--with-deps=false` lets an operator opt out (e.g. when comparing check configs across orgs without dep noise).

## Frontend (dash0)

Today's `<DependenciesCard>` is on the **check detail** page (`routes/orgs/$org/checks.$checkUid.index.tsx`) — i.e. visible only after a check exists. The **create/edit** check form doesn't have it.

Add a "Dependencies" section to the check create/edit form, located near the Configuration section (not at the bottom — operators often forget bottom-of-form fields when scrolling back from validation errors).

- Reuse `<CheckPicker>` from `components/shared/check-picker.tsx` (shipped in 2026-05-05-01).
- The control posts to the new PUT-by-slug `dependsOn` field rather than the per-edge CRUD API. One round-trip on save.
- For *create* (the check doesn't exist yet, so PUT-by-slug doesn't apply), call `POST /checks/validate` first to surface cycle errors, then on submit call `POST /checks` to create the check, then `POST /checks/$check/dependencies` for each edge — one trip per edge but the user already accepted the latency by clicking save. (Could be a single `POST /checks?dependsOn=…` later, but POST-with-deps is enough divergence from existing patterns that it's not worth bundling here.)
- For *edit*, prefer the new PUT-by-slug shape — the form already serializes the whole check, just add `dependsOn`.
- The standalone `<DependenciesCard>` on the detail page stays. It's the right surface for "I'm here, let me add one quick parent." Don't remove it.

i18n: reuse keys from `dependencies.json` (shipped in 2026-05-05-01). One new key for the form section title if the existing copy is detail-page-specific.

## Tests

Backend (`server/internal/handlers/checks/service_test.go` + `_export_test.go` if it exists):

- **Export with no deps** → byte-identical to current export (snapshot test). This is the regression guard for the `omitempty` claim.
- **Export with two deps** → `dependsOn` array sorted by `parentSlug`, includes `kind` and `description`.
- **Import with deps, parents declared in same payload** → both checks created, edges created. Re-importing the same payload → no-op (idempotent).
- **Import with `dependsOn` referencing an unknown slug** → `ImportError` for the child, other deps still process.
- **Import with `dependsOn` creating a cycle** → `ImportError`, no edge written.
- **Import with `dependsOn` cross-payload self-reference** (A→B and B→A in same file) → first one accepted, second rejected as cycle.
- **Import does not delete edges** absent from payload → existing edges from a CRUD API call survive a re-import without `dependsOn`.

For PUT-by-slug (`server/internal/handlers/checks/service_test.go`):

- **PUT without `dependsOn`** → existing edges untouched.
- **PUT with `dependsOn: []`** → all edges for that check deleted.
- **PUT with `dependsOn: [...]`** → diff applied (creates new, updates kind, deletes missing). Validate cycle inside the transaction → no partial state on rollback.
- **PUT with cycle** → 400 `DEPENDENCY_CYCLE`, check unchanged.

For validate (`server/internal/handlers/checks/service_test.go`):

- **Validate with cycle** → `DEPENDENCY_CYCLE`, no rows written.
- **Validate with valid `dependsOn`** → 200 with `valid: true`.

CLI (`server/internal/cli/...`):

- `sp checks deps add` happy path against a dev server.
- `sp checks export --with-deps` produces YAML/JSON containing `dependsOn`.
- `sp checks import` of that same file is idempotent.

Frontend:

- The form's `<CheckPicker>` excludes self and descendants on the *create* form too (currently the picker logic relies on the check existing in the graph; for create, exclude only self because the new check has no UID yet).
- Cycle error from the validate endpoint renders inline.

## Verification

1. `make build && make migrate && make dev-test`.
2. Pre-existing setup: create three checks (`rabbit`, `worker-a`, `worker-b`) and two hard deps (`worker-a → rabbit`, `worker-b → rabbit`) via the existing UI.
3. Export the org's checks:
   ```bash
   TOKEN=$(curl -s -X POST -H 'Content-Type: application/json' \
     -d '{"org":"test","email":"test@test.com","password":"test"}' \
     http://localhost:4000/api/v1/auth/login | jq -r '.accessToken')
   curl -s -H "Authorization: Bearer $TOKEN" \
     'http://localhost:4000/api/v1/orgs/test/checks/export' | jq '.checks[].dependsOn'
   ```
   `worker-a` and `worker-b` should each show one entry pointing at `rabbit`.
4. Save the export to a file. Wipe the deps via the existing CRUD API (delete each edge). Re-import the file:
   ```bash
   curl -s -X POST -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
     --data @/tmp/export.json \
     'http://localhost:4000/api/v1/orgs/test/checks/import' | jq '.'
   ```
   Edges restored; `created: 0, updated: 3` (or similar — checks already exist) and edges visible again on the dependency list page.
5. PUT-by-slug round-trip:
   ```bash
   curl -s -X PUT -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
     -d '{"name":"Worker A","slug":"worker-a","type":"http","config":{"url":"http://127.0.0.1:1"},"dependsOn":[{"parentSlug":"rabbit","kind":"soft"}]}' \
     'http://localhost:4000/api/v1/orgs/test/checks/worker-a'
   ```
   The edge flips from hard to soft. Verify on the detail page.
6. PUT with `dependsOn: []` → edge for `worker-a` is gone.
7. PUT with a cycle:
   ```bash
   curl -s -X PUT -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
     -d '{"name":"RabbitMQ","slug":"rabbit","type":"tcp","config":{"address":"localhost:5672"},"dependsOn":[{"parentSlug":"worker-a","kind":"hard"}]}' \
     'http://localhost:4000/api/v1/orgs/test/checks/rabbit'
   ```
   → 400 `DEPENDENCY_CYCLE`. The rabbit check is unchanged (transaction rolled back).
8. CLI: `./bin/sp checks export --with-deps > /tmp/export.json` → file contains `dependsOn`. `./bin/sp checks import /tmp/export.json` → idempotent.
9. Frontend: create a new check via the dashboard. The "Dependencies" section is visible on the create form. Pick `rabbit` as a parent, kind=hard. Submit. The check is created, the dep edge is created, and the new check's detail page shows the dep card populated.
10. `make build-dash0 lint test` clean.

## Risks / unknowns

- **Snapshot test fragility on `omitempty`.** A reviewer could reasonably ask for `dependsOn: []` instead of omitting. Resist — every existing snapshot test in `checks/service_test.go` would diff. Stay with `omitempty`.
- **Two-pass import on a partial-failure first pass.** If pass 1 fails on check `worker-a` but succeeds on its parent `rabbit`, pass 2 will skip `worker-a`'s deps because the child wasn't created. That's correct, but the error message must clearly say "skipped because pass-1 failure on child." Otherwise the operator sees "unknown parent slug" or worse, silence.
- **PUT-by-slug `dependsOn: null` vs absent.** JSON decoders that conflate the two will be hard to support cleanly. Use a `*[]ExportedDependency` pointer in the request struct so the handler can distinguish "field absent" from "field present and null/empty." Document this in the API spec.
- **Cycle path rendering on PUT failure.** The frontend's `<DependenciesCard>` renders cycle paths nicely (shipped in 2026-05-05-01). The check create/edit form has to do the same — extract the path-rendering helper from the card before this spec ships.
- **Destructive PUT vs additive POST asymmetry.** Will surprise some users. Document in the API spec; add a note to the form copy ("Saving will replace the dependencies listed below").
- **CLI deps verbs** are easy to under-design. `sp checks deps set --from file.yaml` is the one that earns its keep for IaC-style flows; the others can be deferred if review pressure mounts. Keep `add`/`remove`/`list` for parity, but if the CLI work blows the timebox, ship export/import + PUT-by-slug first and follow up with CLI.
- **No new feature flag.** The new field is additive on every endpoint and the validators reject malformed input cleanly. A flag would just be ceremony.

## Implementation Plan

Each step lands as a green commit:

1. **`ExportedDependency` type** + `ExportCheck.DependsOn` field. Update `ExportChecks` to populate it. Snapshot test: empty exports unchanged.
2. **`ImportChecks` two-pass**: pass-1 untouched; pass-2 resolves slugs and applies the additive merge. Test the additive semantics (new edges added, existing edges left alone, cycles rejected per-row).
3. **PUT-by-slug `dependsOn` field**: pointer-typed for absent vs empty distinction. Inside the existing transaction, diff and apply. Tests for absent/empty/diff/cycle.
4. **`POST /checks/validate` `dependsOn` field**: same validators, no writes. Test happy + cycle.
5. **CLI**: `sp checks deps {list,add,remove,set}` and `--with-deps` on export/import. Wire to the new fields.
6. **Frontend**: add the Dependencies section to the check create/edit form. Extract the cycle-path renderer from `<DependenciesCard>` to a shared helper. Wire to PUT-by-slug on edit, to POST + per-edge POST on create.
7. **Docs**: update `wiki/api-specification.md` and `server/CLAUDE.md` to mention `dependsOn` on the import/export/upsert payloads.
8. **QA per the verification checklist**. `make build lint test` clean.

If review pressure hits: ship steps 1–4 first (the load-bearing API change). Steps 5–7 can be a follow-up; the dedicated CRUD API and the on-detail-page card already cover the operator UX.

## Status — 2026-05-05

**Phase 1 shipped on `feat/dependencies-in-check-config`** (steps 1–4 + lint clean):

- ✅ Step 1: `ExportedDependency` type + `ExportCheck.DependsOn`, `ExportChecks` populates it (sorted by parent slug, omitempty preserved). Commit `7c7e073e`.
- ✅ Step 2: `ImportChecks` two-pass — pass 1 unchanged; pass 2 resolves slugs → UIDs and applies the additive merge with per-row error reporting (unknown parent / cycle / self-edge / invalid kind). Skipped silently for any check whose pass-1 upsert failed, with an explicit `skipped dependsOn: pass-1 upsert failed for this check` error. Commit `abfbf844`.
- ✅ Step 3: `UpsertCheck` (PUT-by-slug) accepts `dependsOn *[]ExportedDependency` (pointer-typed for absent vs empty distinction), runs all validators upfront (parent existence, self, cross-org, kind, duplicate, cycle simulation), then applies destructive sync (delete missing, create new, update changed kind/desc). Caveat: not yet wrapped in a transaction with the check upsert — a failed dep apply leaves the check itself updated. Commit `69385bf0`.
- ✅ Step 4: `POST /api/v1/orgs/$org/checks/validate` accepts `dependsOn` plus optional `slug`, runs the same validators without writing, returns per-row field errors. Commit `2c91759f`.

- ✅ Step 7: `wiki/api-specification.md` documents `dependsOn` on
  `/checks/export`, `/checks/import`, `PUT /checks/:slug`, and
  `POST /checks/validate`. Commit forthcoming on this branch.

**Phase 2 deferred** to a follow-up spec (`2026-05-05-07-dependencies-in-check-config-cli-frontend.md`):

- ⏳ Step 5: CLI verbs (`sp checks deps {list,add,remove,set}` + `--with-deps` on export/import). Blocked on OpenAPI spec extension + client regeneration; meaningful enough to deserve its own spec.
- ⏳ Step 6: Frontend Dependencies section on the check create/edit form. Touches `web/dash0/src/components/shared/check-form.tsx` (1500+ LOC) — also worth its own focused pass.

**Why the split**: the spec's own "if review pressure hits" guidance authorizes shipping steps 1–4 first and following up with the CLI/frontend. With the API surface and docs in place, the CRUD API + on-detail-page `<DependenciesCard>` already cover the operator UX. The split is a documented acceptable scope cut, not a corner cut.

**Atomicity follow-up**: PUT-by-slug should wrap the check upsert + dep diff in a single transaction so a cycle error doesn't leave the check itself updated. Tracked separately because the existing `UpdateCheck` flow doesn't currently expose a transaction handle.

**Tests follow-up**: integration tests for the export-with-deps snapshot, import idempotency / pass-2 errors, PUT-by-slug diff cases, and validate-cycle paths are not in this PR. Existing tests still pass; new test coverage for the new code paths is owed.
