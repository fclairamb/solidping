# Dependencies in check config — Phase 2: CLI verbs and frontend

## Context

Phase 1 (`specs/done/2026/05/2026-05-05-03-dependencies-in-check-config.md`) shipped the load-bearing API: `dependsOn` on export, import, PUT-by-slug, and validate, plus docs. The CRUD API and on-detail-page `<DependenciesCard>` already cover the operator UX. This spec covers the two surfaces deliberately deferred from Phase 1.

Phase 1 references this follow-up by name and authorizes the split via its own "if review pressure hits" guidance — the API change is the load-bearing piece; CLI and frontend are convenience layers on top.

## Scope

In scope:

1. **CLI verbs** under `sp checks deps`:
   - `sp checks deps list <check-slug>` — list parents of a check.
   - `sp checks deps add <check-slug> <parent-slug> [--kind hard|soft] [--description "…"]` — add one edge.
   - `sp checks deps remove <check-slug> <parent-slug>` — drop one edge.
   - `sp checks deps set <check-slug> [--from <yaml/json file>]` — replace the full set of edges for a check (PUT-by-slug semantics).
   - `sp checks export --with-deps[=true]` — already returns deps from the server; add an opt-out flag (`--with-deps=false` strips the field client-side).
   - `sp checks import [--with-deps[=true]] <file>` — same opt-out for the import direction.

2. **OpenAPI spec extension** to expose the per-check dependency CRUD endpoints (`/checks/:check/dependencies` GET/POST/PATCH/DELETE) so the generated client gets typed methods. Run `make generate` after.

3. **Frontend (dash0)**: the check create/edit form (`web/dash0/src/components/shared/check-form.tsx`) gets a "Dependencies" section near the Configuration section.
   - Reuse `<CheckPicker>` from `components/shared/check-picker.tsx`.
   - On *create*: call `POST /checks/validate` first to surface cycle errors inline; on submit, call `POST /checks` to create the check, then `POST /checks/$check/dependencies` for each declared edge.
   - On *edit*: serialize `dependsOn` as part of the existing PUT-by-slug payload (one round-trip).
   - The standalone `<DependenciesCard>` on the detail page stays as the canonical single-edge-edit surface.
   - Extract the cycle-path renderer from `<DependenciesCard>` to a shared helper so the form can render the same error visualization.

4. **Atomicity fix on PUT-by-slug** (carried over from Phase 1): wrap the check upsert and the dep apply in a single DB transaction so a cycle failure doesn't leave the check itself updated. Today's behavior is documented in the Phase 1 spec under "Atomicity follow-up".

5. **Integration tests** for the Phase 1 server work that didn't ship in Phase 1:
   - Export-with-deps snapshot (golden file).
   - Import idempotency: re-importing the same payload is a no-op.
   - Import pass-2 errors land in the result struct with the right shape.
   - PUT-by-slug: absent vs `[]` vs non-empty array semantics.
   - PUT-by-slug cycle returns 400 `DEPENDENCY_CYCLE` and (post-atomicity-fix) does not update the check.
   - Validate cycle returns `valid: false` with field-level path.

Out of scope:

- Anything Phase 1 already shipped (export/import/PUT/validate field handling, slug resolution, cycle DFS).
- Bulk dep editing UX outside the create/edit form. The detail page card stays single-edge.
- A reverse `dependedOnBy` editing field on the form. Same invariant as Phase 1.
- Frontend cycle-path renderer rewrite — extract the existing renderer, don't redesign it.
- A new feature flag.

## Implementation Plan

1. **OpenAPI extension** for `/checks/:check/dependencies` CRUD. Run `make generate`. Verify `apiClient.ListDependenciesForCheck` (or equivalent generated name) compiles in a tiny smoke test before touching CLI.

2. **CLI**: `sp checks deps list|add|remove|set` in `server/pkg/cli/`. Wire to the freshly generated client methods. `--from` flag on `set` reads YAML (preferred) or JSON. Output uses the existing `output.NewTable` for `list`, success/error helpers elsewhere.

3. **CLI**: `--with-deps` opt-out on existing `sp checks export` and `sp checks import`. Default true (server-side behavior is "always include"; the flag strips client-side on export, drops the field on import).

4. **Frontend helper**: extract the cycle-path renderer from `<DependenciesCard>` to `components/shared/dependency-cycle-path.tsx`. Verify the detail-page card still renders unchanged.

5. **Frontend**: add the "Dependencies" section to `<CheckForm>`. Local state for declared edges. Inline validation via `POST /checks/validate` (debounced, on `dependsOn` change). On submit:
   - Edit path: include `dependsOn` in PUT-by-slug payload.
   - Create path: POST check, then per-edge POST. Disable submit button while in flight.

6. **Atomicity fix**: refactor `UpsertCheck` (or its `UpdateCheck` callee) to accept a `*bun.Tx` so the dep apply runs in the same transaction. Failure rolls back both. Update tests.

7. **Integration tests** for the Phase 1 surface — table-driven against testcontainers (postgres + sqlite), per the existing pattern.

8. **QA**: `make build lint test` clean. Manual verification per the Phase 1 spec's verification checklist (now applies again because the frontend wires up).

## Verification

Inherits the Phase 1 verification checklist (curl smoke tests for export/import/PUT/validate). Add:

- `./bin/sp checks deps list <slug>` — table view of parents.
- `./bin/sp checks deps add a b --kind hard --description "x"` then `./bin/sp checks deps list a` shows the new edge.
- `./bin/sp checks export --with-deps=false` produces a doc with no `dependsOn` fields.
- Frontend: create a check with two declared parents in the form, observe both edges materialized after save.
- Frontend: edit an existing check, drop one parent, save — the dropped edge is gone.
- Frontend: try to add a cycle in the form — inline validate flags it before submit.

## Risks

- **OpenAPI regeneration churn**: existing client code may have name collisions. Compare the generated diff before merging.
- **CLI `set` from YAML**: pulls in a yaml dep. If the codebase doesn't already have one, JSON-only is fine for V1 — flag it during impl.
- **Frontend create path is two trips** (POST check, then per-edge POST). Mid-flight failures (check created, edges not) leave a check with no parents — surface a clear retry CTA rather than rolling back the create. A future "POST /checks with dependsOn" would close this.
- **Atomicity refactor radius**: passing `*bun.Tx` through `UpdateCheck` may touch a lot of callers. If radius blows the timebox, ship CLI + frontend + tests first and put atomicity in its own PR — the visible bug (check updated despite cycle error) is rare and recoverable by re-saving.
