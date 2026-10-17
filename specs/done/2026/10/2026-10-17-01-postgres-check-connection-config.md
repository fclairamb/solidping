---
model: opus
effort: medium
---

# Build the PostgreSQL check connection explicitly: no conninfo string, no ambient worker credentials

## Problem

Found by the security scan of 2026-10-17 (findings G1 and G2). Severity: low. Any member who can write checks (`user` role and above) can trigger both; on SaaS that is anyone, since any account can create its own org.

**G1: the conninfo escaper lets a field add driver options.**
- `checkpostgres/config/config.go:187` `BuildConnStr` builds a lib/pq keyword/value string from `host`, `username`, `database`, `ssl_mode` and `password`, each passed through `escapePQParam` (`config.go:222`).
- `escapePQParam` quotes a value only when it contains a space, a quote or a backslash (`:227`). It then prefixes each quote with a backslash (`:232`), but it never escapes a backslash already in the value.
- lib/pq (v1.12.3, `connector.go` quoted-value loop) treats a backslash inside a quoted value as "take the next character literally". A backslash supplied by the user can therefore cancel the escape that `escapePQParam` inserted. The quoted value then closes early, and the rest of the field is parsed as more `key=value` options.
- `Validate` (`config.go:152`) checks none of these characters, and `host` has no hostname check. The same config reaches the worker through a top-level check and through a JS `solidping.check("postgres", ...)` sub-check (`checkjs` `parseSubCheckConfig` runs the same `Validate`).
- Impact: the caller picks any lib/pq option. Two examples are an `sslmode` outside the validated enum, and file-path options the worker reads from its own disk: `sslcert` / `sslkey` / `sslrootcert`, `passfile`, `service`.
  - It is **not** an egress bypass. Under an enforcing policy every dial still goes through `checkerdef.OutboundDialer` (`checkpostgres/checker.go:45-61`), whatever host the options name.
  - It is not as severe as the MySQL `allowAllFiles` case either, because lib/pq has no option that uploads an arbitrary file to the server.
- The audit in `specs/done/2026/10/2026-10-16-04-mysql-dsn-injection.md:32` classed PostgreSQL as "safe (quote-doubling)". The code does not double quotes; it backslash-escapes them and leaves backslashes alone.
- Side effect: a legitimate password containing a backslash or a quote can be mangled today.

**G2: fields the check leaves empty fall back to the worker's own Postgres credentials.**
- `openDB` (`checker.go:45`) calls `sql.Open("postgres", connStr)` or `pq.NewConnector(connStr)`. Both go through lib/pq `NewConfig`, which loads the process environment first (`fromEnv(os.Environ())`) and lets the DSN override it.
- `password` is optional in `PostgreSQLConfig`. When a check omits it, lib/pq takes `PGPASSWORD` from the worker's environment. If that is unset, lib/pq falls back to a matching line of the worker's `~/.pgpass` or `PGPASSFILE` (`conn.go:270`). The same applies to:
  - the client certificate: `PGSSLCERT` / `PGSSLKEY`, or by default `~/.postgresql/postgresql.crt` (`ssl.go:176`);
  - `PGSERVICE`.
- Attack: a member points a postgres check, with no password, at a server they control that asks for cleartext password authentication. If the worker process has `PGPASSWORD` set, that credential is sent to the attacker's server. The worker's client certificate is presented the same way. `PGPASSWORD` is often set when the API runs next to a Postgres it administers: a Postgres sidecar, a platform that injects `PG*` variables, or an operator shell.
- This is the same class as N1 (`${env:}` reading the server environment, fixed by `SP_CHECK_ENV_ALLOWLIST`) through a different door: the driver reads the environment itself, so the allowlist never sees it.
- Why low: SolidPing's own deployment files set no `PG*` variable, so the leak depends on the operator's environment.

## Proposal

1. In `checkpostgres`, stop building a conninfo string.
   - Build a `pq.Config` value directly from the parsed fields (`Host`, `Port`, `User`, `Password`, `Database`, `SSLMode`, connect timeout) and open it with `pq.NewConnectorConfig(cfg)`. That constructor reads no environment and parses no string, so no field can add options (G1) and nothing is inherited from `PG*` variables (G2).
   - Keep `connector.Dialer(...)` for the tunnel/egress dialer, and use the same connector, with lib/pq's default dialer, when `OutboundDialer` is nil. Drop the `sql.Open` branch.
2. Close the remaining file fallbacks for G2 in the same place:
   - make sure an empty password never consults a pgpass file;
   - make sure no client certificate is loaded from the worker's home directory unless the check config explicitly carries one (it cannot today).
   See "To verify" for how lib/pq lets you express that.
3. Delete `BuildConnStr` and `escapePQParam` if nothing else uses them. If a display string is still needed (logs, output), build it from the fields without the password and never feed it back to the driver.
4. Leave the done MySQL spec as written (done specs are history). Say in the changelog entry that PostgreSQL check credentials containing a backslash or a quote now work.

## Tests

- `checkpostgres/config` (or a new `connector_test.go`): for each of `host`, `username`, `database` and `password`, a value made of backslashes, quotes, spaces and `=` signs reaches the built `pq.Config` field byte for byte. Add no other option, and `SSLMode` stays the validated value. Fails before the fix (today the string round-trips through lib/pq's parser with extra keys).
- Positive control: an ordinary config (host, user, password, database, `ssl_mode: require`) builds a `pq.Config` with exactly those values, and the existing `tunnel_test.go` still proves the dial goes through the tunnel dialer.
- Environment isolation: with `PGPASSWORD`, `PGSSLCERT` and `PGPASSFILE` set in the test process (`t.Setenv`) and a check config without a password, the built config carries an empty password and no client certificate path. Against a local fake server that requests cleartext auth, no password is received. Fails before the fix.
- Positive control for the above: the same fake server receives the check's own password when the config sets one.

## To verify

- How to disable lib/pq's pgpass lookup (`conn.go:270`, `internal/pqutil/path.go` `Pgpass`) and its `~/.postgresql/postgresql.crt` default (`ssl.go:176-200`) for a `pq.Config` built in code. Candidates: set `Passfile` and `SSLCert` / `SSLKey` to a path that cannot exist, or use the `set` tracking `NewConnectorConfig` relies on. Pick whichever lib/pq documents, and pin the behaviour with the environment-isolation test above.

Already checked: the check timeout travels on the context (`checker.go:89-96`), not in the DSN. `checkpostgres` is the only checker that hands a user-built string to lib/pq. `internal/notifier/postgres.go` and `internal/db/dbfault` use lib/pq for the server's own database, with operator config.
