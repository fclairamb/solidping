# Label key min length 3 + Playwright E2E for label definition on check create

## Context

The label-input component (`web/dash0/src/components/shared/label-input.tsx:11`) gates label keys on the regex `^[a-z][a-z0-9-]{3,50}$` — a leading letter plus **3–50 more chars**, i.e. **4–51 total**. The Postgres CHECK constraint mirrors that exactly (`server/internal/db/postgres/migrations/001_initial.up.sql:266`). SQLite is already permissive: `length(key) >= 1 and length(key) <= 50` (`server/internal/db/sqlite/migrations/001_initial.up.sql:190`), so it will happily store 3-char keys today.

That 4-char minimum rules out the most natural ops keys: `env`, `app`, `dev`, `qa`, `db`, `os`. The integration test in `server/test/integration/checks_test.go:113` already uses `env` as a key and only passes because the test stack runs on SQLite — the same payload would 23 against Postgres prod.

The label-input UI also has **no Playwright coverage** today. Spec `2026-05-02-22-labels-input-check-form.md` listed a Playwright test in scope but it was never written; spec `2026-05-04-07-fix-edit-check-labels-not-saved.md` listed `check-labels.spec.ts` and that file is also missing from `web/dash0/e2e/`. Since labels are now load-bearing for filtering and dashboards, we need real coverage of the create path.

This spec does two small, related things in one PR:
1. Lower the label key minimum to 3 chars (frontend regex + Postgres migration). SQLite already accepts it.
2. Add a Playwright E2E that exercises label definition on the check create form, including the new 3-char minimum.

## Scope

**In:**
- Frontend regex bump in `label-input.tsx`, plus the human-readable error message currently saying "4–51".
- Postgres forward + down migration that swaps the CHECK constraint to `^[a-z][a-z0-9-]{2,50}$`.
- Playwright spec `web/dash0/e2e/check-labels.spec.ts` covering: add valid label, add 3-char key (`env`), reject too-short key (1–2 chars), reject invalid chars, save and reload, remove a chip.

**Out:**
- SQLite migration. The existing length-only constraint already permits 3-char keys; tightening it to enforce the regex would be a separate hygiene task and is not needed to unblock this change.
- Backend Go-side validation. There is no Go-level label key regex today; the DB is the source of truth and that is fine for this scope.
- Editing label on existing check via the edit page — covered separately by spec 07's reload-and-persist case. Keep this Playwright suite focused on the **create** flow as the user requested.
- i18n for the validation message. The current message is hardcoded in `label-input.tsx`; turning it into a translation key is a worthwhile but unrelated cleanup.

## Approach

### 1. Frontend regex

`web/dash0/src/components/shared/label-input.tsx`:

- Line 11: `const KEY_REGEX = /^[a-z][a-z0-9-]{3,50}$/;` → `/^[a-z][a-z0-9-]{2,50}$/;`
- Line 115: `Use 4–51 lowercase letters, digits, or hyphens, starting with a letter.` → `Use 3–51 lowercase letters, digits, or hyphens, starting with a letter.`

That's the entire frontend change. The duplicate-check, value-length, and suggestion logic are unaffected.

### 2. Postgres migration

Next available pair number is `015` (current head is `014_unique_active_group_incident`). Add:

`server/internal/db/postgres/migrations/015_label_key_min_3.up.sql`:

```sql
-- Allow 3-char label keys (e.g. "env", "app", "qa") that were previously
-- rejected by the {3,50} repeat. Keep the leading-letter requirement and
-- the 50-char cap; only the lower bound moves from 4 total to 3 total.

ALTER TABLE labels DROP CONSTRAINT IF EXISTS labels_key_check;
ALTER TABLE labels ADD CONSTRAINT labels_key_check
  CHECK (key ~ '^[a-z][a-z0-9-]{2,50}$');
```

`server/internal/db/postgres/migrations/015_label_key_min_3.down.sql`:

```sql
ALTER TABLE labels DROP CONSTRAINT IF EXISTS labels_key_check;
ALTER TABLE labels ADD CONSTRAINT labels_key_check
  CHECK (key ~ '^[a-z][a-z0-9-]{3,50}$');
```

Confirm the constraint name first — Postgres auto-names it `labels_key_check` by convention but the inline CHECK in `001_initial.up.sql:266` does not assign one explicitly, so check `\d labels` against a live dev DB before merging. If the auto-name differs, adjust both files.

SQLite needs no migration: its existing `length(key) >= 1 and length(key) <= 50` already accepts `env`. We deliberately do not retrofit a regex constraint here — that's a separate, larger change (SQLite needs the table-rebuild dance shown in spec `2026-05-04-03`).

### 3. Playwright E2E

Create `web/dash0/e2e/check-labels.spec.ts`. Follow the patterns in `e2e/checks.spec.ts` and the `authenticatedPage` fixture from `e2e/fixtures.ts`. All tests run as the `test@test.com / test` user against `org=test` (test mode credentials, per top-level `CLAUDE.md`).

Test ids already exist on the component:
- `label-key-input` and `label-value-input` for the two comboboxes
- `label-chips` for the chip strip
- `label-chip-remove-${key}` for each chip's `×` button
- `label-key-error` for the regex validation message
- `label-key-duplicate` for the duplicate-key message
- `label-key-use-typed` and `label-value-use-typed` for the cmdk "Use '<typed>'" item that appears when the typed value isn't in suggestions

Cases to cover (one `test()` block each):

1. **Add a multi-char label and persist.**
   - New check → fill name/URL.
   - Type `service` in `label-key-input`, click `label-key-use-typed`.
   - Focus jumps to value input; type `payments`.
   - Press Enter (commits via the value input's `onEnterCommit`).
   - Assert chip `service: payments` is in `label-chips`.
   - Submit the form, wait for navigation to detail.
   - Reload; navigate to edit; assert chip is still there.

2. **3-char key works (the regression we're guarding).**
   - Same flow, but key = `env` and value = `prod`.
   - Critical: assert `label-key-error` is **not visible** while typing `env`, and the Add button becomes enabled before commit.
   - Save; reload edit; assert `env: prod` chip persists.

3. **Too-short key is rejected inline.**
   - Type `e` in key input → `label-key-error` visible, Add disabled.
   - Type `en` → still visible, still disabled.
   - Type `env` → error disappears, Add enabled. (This is the same threshold as case 2 but tested as a UI assertion rather than a round-trip.)

4. **Invalid characters rejected.**
   - Type `Env` (capital) → error visible.
   - Clear, type `env_x` (underscore) → error visible.
   - Clear, type `1env` (leading digit) → error visible.

5. **Remove a chip.**
   - Add `team: ops`, then click `label-chip-remove-team`.
   - Assert the chip is gone before submit (no save needed for this case).

Each test should generate a unique check name with `Date.now()` to avoid slug collisions across parallel workers, matching the pattern in `checks.spec.ts:128`.

Run with the standard config:

```bash
cd web/dash0 && bun run test:e2e --grep "check-labels"
```

Or against a running `make dev-test` server:

```bash
cd web/dash0 && bun run test:e2e:dev --grep "check-labels"
```

## Verification

1. Apply migration on a fresh dev Postgres: `make dev` boots cleanly, `\d labels` shows the new constraint.
2. Backend in test mode (SQLite): create check via curl with `{"labels":{"env":"prod"}}` succeeds — already does today, no regression.
3. Backend with Postgres: same payload succeeds (would have failed before this spec).
4. `bun run test:e2e --grep "check-labels"` passes locally.
5. `make lint-dash` clean.
6. Manual: open `/dash0/orgs/test/checks/new`, type `env` in the label key combobox, observe no validation error and the Add button enabled.

## Files touched

- `web/dash0/src/components/shared/label-input.tsx` — regex + error message.
- `server/internal/db/postgres/migrations/015_label_key_min_3.up.sql` — new.
- `server/internal/db/postgres/migrations/015_label_key_min_3.down.sql` — new.
- `web/dash0/e2e/check-labels.spec.ts` — new.

No backend Go change. No SQLite migration. No new dependencies.

## Implementation plan

1. Drop the regex from `{3,50}` to `{2,50}` and adjust the user-visible error message.
2. Add the Postgres migration pair (verify constraint name on a real DB first).
3. Run `make migrate` against a fresh Postgres dev DB to confirm both up and down migrations succeed.
4. Write the Playwright spec; run against `make dev-test`.
5. `make lint-dash`, `make build-dash0`, full `bun run test:e2e` to confirm no regressions in the existing `checks.spec.ts`.
