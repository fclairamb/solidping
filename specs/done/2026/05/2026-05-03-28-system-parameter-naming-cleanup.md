# System parameter naming cleanup

## Context

System parameter keys live in two places today and follow two different conventions:

`server/internal/systemconfig/systemconfig.go:24-68`:

```go
KeyJWTSecret                ParameterKey = "jwt_secret"           // top-level snake_case
KeyJobWorkers               ParameterKey = "job_workers"          // top-level snake_case
KeyCheckWorkers             ParameterKey = "check_workers"        // top-level snake_case
KeyBaseURL                  ParameterKey = "base_url"             // top-level snake_case
KeyNodeRole                 ParameterKey = "node_role"            // top-level snake_case
KeyNodeRegion               ParameterKey = "node_region"          // top-level snake_case
KeyEmailHost                ParameterKey = "email.host"           // dot-namespaced
KeyEmailFromName            ParameterKey = "email.from_name"      // dot-namespaced + snake within segment
KeyGoogleClientID           ParameterKey = "auth.google.client_id"
KeyAggregationRetentionRaw  ParameterKey = "aggregation.retention_raw"
// ... rest are dot-namespaced
```

`server/internal/jmap/manager.go:18`:

```go
const SystemParameterKey = "email_inbox"  // top-level snake_case, ought to be email.inbox
```

`server/internal/jobs/jobtypes/job_startup.go:143`:

```go
paramKey := "samples.loaded"  // dot-namespaced (good)
```

Two conventions coexist:

1. **Top-level snake_case** — `jwt_secret`, `job_workers`, `check_workers`, `base_url`, `node_role`, `node_region`, `email_inbox`. All are early entries that predate the dot convention.
2. **Dot-namespaced + snake_case within each segment** — `email.from_name`, `auth.google.client_id`, `aggregation.retention_raw`, `samples.loaded`. This is the convention every later key follows.

The most jarring outlier is `email_inbox`: every other email setting sits under `email.*`, so `email_inbox` looks like a typo when you read the list. But the same problem exists for `jwt_secret` (sibling of `auth.*` keys) and `base_url` / `node_*` / `*_workers` (no namespace at all).

## Honest opinion

Pick one rule, codify it, fix the outliers in one shot.

The rule worth codifying — because it's already what most of the code does — is:

> **Param keys mirror the config struct path.** Use `.` for hierarchy, `_` for word breaks within a segment.

Under that rule, `email_inbox` is wrong because the config namespace is `email`. `jwt_secret` is wrong because the config struct is `cfg.Auth.JWTSecret`. `base_url` is wrong because it's `cfg.Server.BaseURL`. The right names are mechanical, not subjective.

Don't ship a permanent read-fallback (try new key, fall through to old) — that's the tempting "safe" option but it freezes the inconsistency in the code forever and the next person who adds a key won't know which side to write to. A one-shot SQL migration that renames the rows in the `parameters` table is cleaner: after one deploy, the database matches the constants and the code can pretend the old names never existed.

The env vars (`SP_AUTH_JWT_SECRET`, `SP_BASE_URL`, etc.) **stay as they are** — they're a separate namespace from the DB keys, users may have them set in their environment, and renaming them is a real breaking change. Only the DB-side parameter keys move.

## Scope

**In:**
- Document the naming rule in `server/CLAUDE.md` (one paragraph, near the parameters-table section).
- Rename the seven outlier constants in `systemconfig.go` and the one constant in `jmap/manager.go`.
- Add migration `008_rename_legacy_param_keys` (postgres + sqlite) that renames the rows in `parameters` where `organization_uid IS NULL`.
- Update any tests that hardcode old key names.

**Out:**
- Renaming env vars (`SP_*`) — kept as-is; they're a separate user-facing surface.
- Renaming URL paths (`/email_inbox/public` vs `/email-inbox/config` — there's a separate URL inconsistency in `server/internal/app/server.go:535-545`, but that's a different namespace and a different spec).
- Moving `email_inbox` config out of `jmap/manager.go` into the `getKnownParameters` table — it doesn't fit the simple `ApplyFunc(cfg, value)` shape because its value is a JSON config blob. Leave the storage layer alone, just rename the key.
- Adding a permanent read-fallback. If someone deploys this on a DB that already migrated, the migration is a no-op (`UPDATE … WHERE key = '<old>'` matches zero rows).

## Renames

The mapping, derived mechanically from each parameter's env var and config struct path:

| Old key         | New key               | Env var (unchanged)              | Config struct                    |
|-----------------|-----------------------|----------------------------------|----------------------------------|
| `jwt_secret`    | `auth.jwt_secret`     | `SP_AUTH_JWT_SECRET`             | `cfg.Auth.JWTSecret`             |
| `job_workers`   | `server.job_workers`  | `SP_SERVER_JOB_WORKER_NB`        | `cfg.Server.JobWorker.Nb`        |
| `check_workers` | `server.check_workers`| `SP_SERVER_CHECK_WORKER_NB`      | `cfg.Server.CheckWorker.Nb`      |
| `base_url`      | `server.base_url`     | `SP_BASE_URL`                    | `cfg.Server.BaseURL`             |
| `node_role`     | `node.role`           | `SP_NODE_ROLE`                   | `cfg.Node.Role`                  |
| `node_region`   | `node.region`         | `SP_NODE_REGION`                 | `cfg.Node.Region`                |
| `email_inbox`   | `email.inbox`         | (none — JMAP-only)               | (JSON blob, parsed in `jmap`)    |

After this change, every system parameter key contains a `.`.

## Implementation

### 1. Migration

`server/internal/db/postgres/migrations/008_rename_legacy_param_keys.up.sql`:

```sql
-- Rename legacy top-level snake_case system parameter keys to the
-- dot-namespaced convention used everywhere else. System parameters
-- live with organization_uid IS NULL; per-org rows (if anyone has
-- happened to set one of these names per-org) are left alone.
UPDATE parameters SET key = 'auth.jwt_secret'      WHERE key = 'jwt_secret'    AND organization_uid IS NULL;
UPDATE parameters SET key = 'server.job_workers'   WHERE key = 'job_workers'   AND organization_uid IS NULL;
UPDATE parameters SET key = 'server.check_workers' WHERE key = 'check_workers' AND organization_uid IS NULL;
UPDATE parameters SET key = 'server.base_url'      WHERE key = 'base_url'      AND organization_uid IS NULL;
UPDATE parameters SET key = 'node.role'            WHERE key = 'node_role'     AND organization_uid IS NULL;
UPDATE parameters SET key = 'node.region'          WHERE key = 'node_region'   AND organization_uid IS NULL;
UPDATE parameters SET key = 'email.inbox'          WHERE key = 'email_inbox'   AND organization_uid IS NULL;
```

`server/internal/db/postgres/migrations/008_rename_legacy_param_keys.down.sql`: the reverse mapping (mirror image).

`server/internal/db/sqlite/migrations/008_*.sql`: identical SQL — these UPDATEs are portable.

If `(organization_uid, key)` has a unique index and a deployment somehow already has both the old and the new name, the UPDATE will fail. That's the right failure: the operator should look at the row, decide which value to keep, delete the loser, and re-run. A silent winner-picks-loser-loses migration would be worse. (In practice the new keys don't exist anywhere yet, so this can't happen on first deploy.)

### 2. Constants

`server/internal/systemconfig/systemconfig.go:25-30`: change the seven string literals on the right-hand side. Constant names (`KeyJWTSecret`, etc.) stay — they're the public Go API.

`server/internal/jmap/manager.go:18`:

```go
const SystemParameterKey = "email.inbox"
```

Also update the error message at `manager.go:24` and the doc comment at `manager.go:493` (search for `email_inbox` in that file).

### 3. Test fixtures

Grep for the seven old string literals across test files and replace:

```bash
rg -l '"jwt_secret"|"job_workers"|"check_workers"|"base_url"|"node_role"|"node_region"|"email_inbox"' --glob '*_test.go'
```

`server/internal/handlers/system/service_test.go` is the most likely hit (touched any time a system param test fixture is built). Also check `server/internal/jmap/*_test.go`.

User-visible strings — log messages mentioning "email_inbox", error messages, doc comments — should be updated for clarity but aren't load-bearing.

### 4. Documentation

Add to `server/CLAUDE.md`, in the parameters-table section:

> **Parameter key convention.** Param keys mirror the config struct path: dots for hierarchy (`email.host`, `auth.google.client_id`), snake_case within a segment for word breaks (`email.from_name`, `aggregation.retention_raw`). New keys must follow this — never add a top-level snake_case key.

## Verification

```bash
# Before deploy: check current state
psql -c "SELECT key FROM parameters WHERE organization_uid IS NULL ORDER BY key;"

# Apply migration
./solidping migrate

# After: no top-level snake_case keys remain
psql -c "SELECT key FROM parameters WHERE organization_uid IS NULL AND key NOT LIKE '%.%';"
# (zero rows)

# Functional check: server starts, JWT secret loads, email config loads, JMAP inbox loads
./solidping serve
# Look for: no warnings about missing JWT secret on a previously-configured DB
# Look for: JMAP inbox supervisor starts (or stays idle if email.inbox unset) — same as before
```

In the dash UI, the system-parameters admin page should now list all keys in dot form.

## Files touched

- `server/internal/db/postgres/migrations/008_rename_legacy_param_keys.{up,down}.sql` — new
- `server/internal/db/sqlite/migrations/008_rename_legacy_param_keys.{up,down}.sql` — new
- `server/internal/systemconfig/systemconfig.go` — string literals on lines 25-30
- `server/internal/jmap/manager.go` — line 18 + comment/error string updates
- `server/internal/handlers/system/service_test.go` — fixture key updates (if any hardcode old names)
- `server/internal/jmap/*_test.go` — same check
- `server/CLAUDE.md` — add the convention paragraph

No code outside the systemconfig / jmap / migrations packages should care: every consumer reads via `systemconfig.Key*` constants or `jmap.SystemParameterKey`, and those identifiers don't change.

## Implementation Plan

> **Migration number adjustment**: `008_add_discord_provider_type` already exists in both postgres and sqlite migration directories, so the rename migration ships as `009_rename_legacy_param_keys` instead of `008_*`. The renames themselves are unchanged.

1. Write the migration SQL as `009_rename_legacy_param_keys.{up,down}.sql` (postgres + sqlite). Confirm migration runs cleanly against a DB that has the old keys set.
2. Update the seven string literals in `systemconfig.go` and the constant in `jmap/manager.go`.
3. `rg` for any test fixtures or string references to the old key names; update them.
4. Add the convention paragraph to `server/CLAUDE.md`.
5. `make gotest` + `make lint-back` clean.
6. Smoke-test on a dev DB that has e.g. `jwt_secret` and `email_inbox` set: run migrate, restart server, confirm everything still works.
