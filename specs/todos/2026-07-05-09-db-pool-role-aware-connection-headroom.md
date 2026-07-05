# DB connection pool — role-aware sizing and headroom under the Postgres role limit

## Context

Found while diagnosing spec `2026-07-05-08` on the k8xp dev cluster: the
`solidping_dev` Postgres role could not accept a single additional
connection — `psql` (and anything else: migrations, a debugging session, a
rolling-deploy surge pod, a second replica) was rejected with
`FATAL: too many connections for role "solidping_dev"` on every attempt.

The fleet itself was the culprit, at steady state and fully idle:

```
rolconnlimit(solidping_dev) = 30        (server max_connections = 100)
pg_stat_activity            = 30 connections, ALL idle, all solidping_dev
```

30 = 3 pods × `MaxIdleConns` 10. The idle floor of a healthy, quiet fleet
exactly saturates the role cap.

## Current state (verified 2026-07-05; re-verify at build)

- Pool defaults are global — `MaxOpenConns: 25`, `MaxIdleConns: 10`,
  `ConnMaxLifetime: time.Hour` (`server/internal/config/config.go:645-651`),
  applied in `applyPoolLimits`
  (`server/internal/db/postgres/postgres.go:74-85`). There is **no
  `ConnMaxIdleTime`**, so idle connections are held up to an hour regardless
  of load.
- Every node role gets the same pool. `SP_NODE_ROLE=checks` workers
  (config.go:24-27) run only the check worker — batched `ClaimJobs` polls
  and result writes — yet hold the same 25/10 pool as the full API server.
  The k8xp fleet is 1 × `all` + 2 × `checks`, all sharing one DB URL/role.
- Worst-case demand is 3 × 25 = **75** against a role cap of 30: under
  burst the app competes with itself for connections and third parties are
  locked out entirely.
- Env overrides exist (`SP_DB_MAX_OPEN_CONNS` / `SP_DB_MAX_IDLE_CONNS`,
  manual koanf readers at config.go:1082/1087), but nothing warns when the
  configuration cannot fit the role limit — the failure surfaces later, as
  connection refusals in whatever connects last.

## Design decisions

### D1 — Role-aware pool defaults

A `checks` or `jobs` node does not need an API-sized pool. Defaults become
per-role (still overridable by the existing env keys):

| Node role | MaxOpenConns | MaxIdleConns |
|---|---|---|
| `all`, `api` | 25 | 10 |
| `checks`, `jobs` | 8 | 2 |

Applied where config defaults are resolved (after `SP_NODE_ROLE` is known),
so `applyPoolLimits` stays unchanged. The k8xp fleet's worst case drops from
75 to 25 + 2×8 = 41, and its idle floor from 30 to 14.

### D2 — Reap idle connections

Add `ConnMaxIdleTime` (koanf `conn_max_idle_time`, default **5m**) alongside
`ConnMaxLifetime` in `DatabaseConfig` and `applyPoolLimits`
(`sqldb.SetConnMaxIdleTime`). A quiet fleet then decays toward zero idle
connections instead of pinning `MaxIdleConns` × pods forever. SQLite ignores
it, same as the other pool bounds.

### D3 — Startup headroom check

After the Postgres connection is established, run once:

```sql
SELECT rolconnlimit FROM pg_roles WHERE rolname = current_user;
SELECT count(*) FROM pg_stat_activity WHERE usename = current_user;
```

If `rolconnlimit > 0` and `MaxOpenConns >= rolconnlimit`, log a WARN naming
both numbers and the current usage, stating that the limit must cover
*every* process sharing the role (API pods + region workers + migrations +
surge pods during rolling deploys) plus operator headroom. One process
cannot know the fleet size, so this is a warning, not a hard failure — but
it turns a silent time bomb into a startup log line. Skip silently on
permission errors (some managed Postgres setups hide `pg_roles`).

### D4 — Ops note (k8xp, outside this repo)

The dev cluster's `rolconnlimit = 30` should be raised (or per-pod
`SP_DB_MAX_OPEN_CONNS` lowered) so that
`Σ MaxOpenConns + headroom ≤ rolconnlimit`. With D1 defaults the current
fleet needs ~45–50. Tracked here for visibility; the change itself lives in
`~/code/fclairamb/k8xp`.

## Non-goals

- Connection multiplexing / pgbouncer.
- Retry/backoff semantics on `53300 too_many_connections` errors beyond
  existing driver behavior.
- Changing SQLite behavior (single-writer pinning stays as is).

## Acceptance criteria

1. A node started with `SP_NODE_ROLE=checks` and no pool overrides gets
   MaxOpenConns 8 / MaxIdleConns 2; `all`/`api` keep 25/10; explicit
   `SP_DB_MAX_OPEN_CONNS` / `SP_DB_MAX_IDLE_CONNS` still win for any role.
2. Idle connections are closed after `conn_max_idle_time` (observable via
   `pg_stat_activity` dropping back near zero on an idle instance).
3. Startup against a role whose `rolconnlimit` ≤ configured MaxOpenConns
   emits the D3 warning with both values and current usage; startup against
   an unlimited role (`rolconnlimit = -1`) emits nothing.
4. Tests: table-driven config tests for per-role defaults and override
   precedence; `applyPoolLimits` test covering the new idle-time bound;
   headroom-check test against the Postgres testcontainer (role with a low
   `CONNECTION LIMIT`).

## Verification on k8xp after deploy

With all three pods redeployed: `pg_stat_activity` for `solidping_dev`
settles well under 30 when quiet, `psql` connects as the app role without
retries, and a rolling restart (surge pod) no longer trips the role limit.

## Implementation Plan

### D1 — Role-aware pool defaults (`server/internal/config/config.go`)

- Add four named constants (`dbPoolMaxOpenConnsDefault` = 25,
  `dbPoolMaxIdleConnsDefault` = 10, `dbPoolMaxOpenConnsChecksDefault` = 8,
  `dbPoolMaxIdleConnsChecksDefault` = 2) next to the `NodeRole*` constants, and
  use the `api/all` pair in the `Load()` defaults struct literal.
- Add `applyNodeRolePoolDefaults(cfg *DatabaseConfig, nodeRole string)`: for
  `checks`/`jobs` roles, swap `MaxOpenConns`/`MaxIdleConns` to the smaller pair
  **only if** each field still equals the api/all struct-literal default —
  that equality check is how a `config.yml`/`config.local.yml` override
  (already folded into `cfg` at this point, since file providers load before
  `Unmarshal`) is distinguished from "untouched."
- Call it in `Load()` right after `cfg.Node.Role` becomes available
  (post-`Unmarshal`, alongside the other manual env-reader calls) and
  **before** `applyDatabasePoolEnv(&cfg.Database)`, so
  `SP_DB_MAX_OPEN_CONNS`/`SP_DB_MAX_IDLE_CONNS` always win for any role.
  `applyPoolLimits` (`postgres.go`) is untouched — it only ever reads the
  already-resolved `cfg.Database.MaxOpenConns/MaxIdleConns`.

### D2 — Reap idle connections

- Add `ConnMaxIdleTime time.Duration` to `config.DatabaseConfig` (koanf key
  `conn_max_idle_time`) and to `postgres.Config`, default 5m in both the
  `Load()` defaults struct and wired through `server.go`'s `postgres.Config{}`
  construction.
- Apply via `sqldb.SetConnMaxIdleTime(cfg.ConnMaxIdleTime)` in
  `applyPoolLimits`, guarded by `> 0` like the other bounds (so an unset value
  behaves as before — no reap). SQLite already ignores every field on
  `postgres.Config`/`DatabaseConfig` it doesn't use (separate `sqlite.Config`
  type), so no SQLite-side change needed.
- Also added `SP_DB_CONN_MAX_IDLE_TIME` to `applyDatabasePoolEnv` alongside
  the existing `SP_DB_CONN_MAX_LIFETIME` reader, for symmetry — not explicitly
  required by the acceptance criteria but the natural companion to the
  existing lifetime override and consistent with "existing env overrides
  still win."

### D3 — Startup headroom check (`server/internal/db/postgres/postgres.go`)

- New unexported `checkRoleConnLimitHeadroom(ctx, bunDB *bun.DB,
  maxOpenConns int)`, called once from `New()` — after pool limits, query
  hook, and Prometheus registration are wired, right before returning the
  `Service` — but only on the real (non-embedded) path; the `Embedded` branch
  early-returns via `NewEmbedded` before reaching this call, so embedded/test
  Postgres instances never run it (avoids noise in every test that spins one
  up).
- Query 1: `SELECT rolconnlimit FROM pg_roles WHERE rolname = current_user`
  via bun's select builder. Any error (permission-restricted `pg_roles`,
  transient failure) → return silently, no warning, no propagated error.
- Skip (no warning) when `rolConnLimit <= 0` (unlimited) or `maxOpenConns <
  rolConnLimit` (fits) — this also covers `maxOpenConns <= 0` (unbounded
  local pool) via an early return, since comparing "unbounded" against a
  limit is meaningless.
- Query 2 (only reached when the above warns): `SELECT count(*) FROM
  pg_stat_activity WHERE usename = current_user`. Same silent-skip-on-error
  handling.
- `slog.WarnContext` with `maxOpenConns`, `rolConnLimit`, `currentUsage`
  fields plus a `hint` explaining the limit must cover every process sharing
  the role.

### D4 — Ops note

Out of scope for this repo (lives in `~/code/fclairamb/k8xp`) — no code
change here; referenced only in the final report.

### Tests (acceptance criterion 4)

- `server/internal/config/config_test.go`: table-driven cases for
  `applyNodeRolePoolDefaults` — `all`/`api`/unset role keep 25/10; `checks`/
  `jobs` get 8/2; a role with a pre-set non-default `MaxOpenConns` (simulating
  a config.yml override) is left alone; and an end-to-end `Load()`-level case
  (via `t.Setenv("SP_NODE_ROLE", ...)` /
  `t.Setenv("SP_DB_MAX_OPEN_CONNS", ...)`) proving the env override still wins
  for a `checks` node.
- `server/internal/db/postgres/postgres_test.go` (new file): unit test for
  `applyPoolLimits` asserting `ConnMaxIdleTime` is applied via
  `sql.DB.Stats()`-adjacent behavior or by constructing a `*sql.DB` against a
  stub driver and checking no panic / correct call — matched to whatever is
  practical without a live connection (likely just asserting the setter is
  invoked conditionally, mirroring the existing bounds).
- `server/internal/db/postgres/postgres_test.go` or a new
  `_postgres_test.go` file (matching the `costdist_postgres_test.go`
  self-skipping embedded-Postgres pattern — this repo has no
  `testcontainers-go` dependency despite `server/CLAUDE.md`'s wording, so
  "testcontainer" here means the real `embedded-postgres` binary already used
  elsewhere): spin up an embedded instance, `CREATE ROLE ... CONNECTION
  LIMIT n` (and a second unlimited role, `CONNECTION LIMIT -1`), connect as
  that role, and assert `checkRoleConnLimitHeadroom` warns for the low-limit
  role and stays silent for the unlimited one (acceptance criterion 3).
  Capturing the slog output requires swapping in a test handler (e.g.
  `slog.New(slog.NewTextHandler(&buf, nil))` set via `slog.SetDefault` for the
  duration of the subtest, restored after).
