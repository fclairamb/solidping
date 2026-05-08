# Raise check slug max length from 20 to 50

## Context

Check slugs are currently capped at 20 characters by the Go validator (`server/internal/handlers/checks/service.go:67,120,982`), but the SQLite migration's CHECK constraint allows up to 40 (`server/internal/db/sqlite/migrations/001_initial.up.sql:159`) — already inconsistent. Users want longer, more descriptive slugs (e.g. `prod-eu-west-payment-api` is 24 chars). Bump the cap to **50**.

This applies only to `Check.slug`. Organization (still 20), check group (still 40), and status page (still 40) slugs are unaffected — those are deliberate constraints (organization slug appears in URLs, etc.) and changing them is a separate conversation.

## Scope

**In scope:**
- Go regex + constants in `server/internal/handlers/checks/service.go`.
- New forward DB migration (SQLite + Postgres) that bumps the CHECK constraint on `checks.slug` to 50.
- Unit test asserting 50-char accepted, 51-char rejected.
- API spec docs (`docs/api-specification.md`) if it documents slug length.

**Out of scope:**
- Other slug fields (organization, check_group, status_page).
- Frontend `maxLength` attribute on the slug input — backend rejection is sufficient; the form already surfaces backend errors.

## Approach

### 1. Go validation

`server/internal/handlers/checks/service.go`:
- Line 67: `var slugRegex = regexp.MustCompile(\`^[a-z][a-z0-9-]{2,19}$\`)` → `{2,49}$`
- Line 120: `const maxSlugLen = 20` → `50`
- Line 982: `const maxSlugLength = 20` (used by `ensureUniqueSlug`) → `50`
- Line 98 comment: update "20 chars" reference to "50 chars".

Sanity-grep the rest of the file and the package for `20` near "slug" — there may be log messages or error strings to update.

### 2. Database migration

Do **not** edit `001_initial.up.sql` in place — it has been applied. Add a new pair:

- `server/internal/db/sqlite/migrations/0NN_check_slug_50.up.sql`
- `server/internal/db/sqlite/migrations/0NN_check_slug_50.down.sql`

(`NN` = next available migration number; check the directory.)

SQLite doesn't support `ALTER TABLE … DROP CONSTRAINT`. The standard recipe is:
1. `CREATE TABLE checks_new (...)` with the new constraint.
2. `INSERT INTO checks_new SELECT * FROM checks;`
3. `DROP TABLE checks;`
4. `ALTER TABLE checks_new RENAME TO checks;`
5. Recreate any indexes on `checks` that were dropped with the table.

Same migration filename pair under `server/internal/db/postgres/migrations/`. Postgres supports `ALTER TABLE checks DROP CONSTRAINT … ; ALTER TABLE checks ADD CONSTRAINT … CHECK (length(slug) >= 3 AND length(slug) <= 50);` — much simpler. Constraint name: inspect the existing one (likely `checks_slug_check` by Postgres default).

### 3. Test

Add to `server/internal/handlers/checks/service_test.go`:
- `TestSlugValidation_50Chars_Accepted`: 50-char slug in `CreateCheck` returns no error.
- `TestSlugValidation_51Chars_Rejected`: 51-char slug returns a `VALIDATION_ERROR`.

### 4. Docs

If `docs/api-specification.md` mentions slug length, update it.

## Verification

1. `make migrate` runs cleanly on a fresh SQLite db and on a Postgres dev db.
2. `./solidping client checks create --slug=$(printf 'a%.0s' {1..50}) ...` succeeds; same with 51 fails.
3. `make test` passes.
4. Manual: log in to dash0, create a check with a 45-char slug, save, reload, slug intact.
