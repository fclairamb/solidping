---
model: opus
effort: high
---

# Build the MySQL check connection from a struct, not an interpolated DSN string

## Problem
Found by the security scan of 2026-10-12 (finding N2). **Critical**, triggerable by any role ≥ `user` — on a SaaS instance with open signup, anyone who registers (any authenticated user can create their own org, `server/internal/app/server.go:915-918`). The DSN parsing was reproduced with the real driver (go-sql-driver v1.10.1, confirmed in `server/go.mod:254`) and the real `BuildDSN`; the end-to-end file read was not run.

`MySQLConfig.BuildDSN` interpolates user-controlled fields straight into the DSN string with no escaping (`server/internal/checkers/checkmysql/config/config.go:157-182`):

```
dsn := fmt.Sprintf("%s:%s@tcp(%s:%d)/", c.Username, c.Password, c.Host, port)  // :164-169
dsn += c.Database                                                              // :172
dsn += fmt.Sprintf("?timeout=%s", timeout)                                     // :180
```

`Validate` (`config.go:130-154`) puts no character rules on `Username`, `Password` or `Database`. The result is parsed by the driver's `mysql.ParseDSN`, which splits on the **last** `@` and the **last** `/` and reads `?`-delimited params (`dsn.go:402-470`, `parseDSNParams` at `:496`), so a crafted field changes what the driver connects to and how.

**(a) Driver-parameter injection → arbitrary file read, every mode.** A `database` field like `x?allowAllFiles=true&z=` lands verbatim after the `/`, and `parseDSNParams` sets `AllowAllFiles=true` (`dsn.go:496`; the flag gates the `LOCAL INFILE` path in `infile.go:125`, driven by `packets.go:570`). Pointed at an attacker-run MySQL server on a public host (the egress guard allows public destinations), the server answers with a `LOCAL INFILE` request and the driver streams back the worker's files — `/proc/self/environ` (the same platform secrets as N1), config files, the service-account token.

**(b) Egress-guard bypass → internal scanning, SaaS.** On an enforcing worker the checker rewrites only the **first** `@tcp(` to the guarded/tunnel network (`checkmysql/checker.go:50-52`, `tunneledDSN`; applied at `:129-132` when `OutboundDialer(ctx) != nil`). A password such as `X@tcp(<internal-host>:3306)/e` injects its own `@tcp(`, so after `BuildDSN` the string is `u:X@tcp(<internal>:3306)/e@tcp(<realhost>:3306)/d?timeout=10s`; `tunneledDSN` consumes its one rewrite on the password's `@tcp(`, and `ParseDSN`'s last-`@`/last-`/` rule reads `Net=tcp Addr=<realhost>:3306` from the genuine suffix. `tcp` has no registered dialer, so the driver falls through to a plain `net.Dialer{}` (`connector.go:98-109`) — no guard, no `egress_denied` trace. Result: internal port scanning / MySQL fingerprinting from a shared worker (or, on a tunneled check, from the worker instead of the bastion).

## Proposal
Stop round-tripping a DSN string. Build a `*mysql.Config` with structured fields, force `AllowAllFiles=false`, and set `DialFunc` so the guard/tunnel always governs the dial.

1. **Replace `BuildDSN` string assembly with a `*mysql.Config`** in `server/internal/checkers/checkmysql`. Add a builder (e.g. `buildConfig(cfg *MySQLConfig) *mysql.Config`) that sets `User`, `Passwd`, `Net="tcp"`, `Addr=net.JoinHostPort(host, port)`, `DBName=database`, `Timeout`, and `Params`/`Loc` as needed, then explicitly sets `AllowAllFiles=false`. Field assignment cannot inject driver params or shift the parse — the values never pass through `ParseDSN`. Because `config.go` must stay driver-free (the light sub-package the offline validator links — `config.go:1-9`), build the `*mysql.Config` in the parent `checkmysql` package (`checker.go`), not in `config`.
2. **Open through `mysql.NewConnector` with a `DialFunc`.** In `checker.go` `Execute` (`:100-145`), replace `sql.Open("mysql", dsn)` with `mysql.NewConnector(cfg)` + `sql.OpenDB(connector)`. Set `cfg.DialFunc` to route through `checkerdef.OutboundDialer(ctx)` when non-nil (tunnel or enforcing guard) — `connector.go:98-99` calls `DialFunc` for every dial regardless of `Net`, so the guard can no longer be bypassed by a crafted `Net`. This also retires the fragile `tunneledDSN` string replace (`checker.go:46-52`) and the `solidping-tunnel` dial-network registration can stay or be simplified (keep the `RegisterDialContext` init if other code relies on it, but the per-check dial no longer depends on the DSN carrying the network name). Keep the untunneled path (`OutboundDialer == nil`) dialing with the driver default so behavior is unchanged where no guard/tunnel applies.
3. **Defense in depth in `Validate`.** Add a rule in `config.go:130-154` rejecting control characters that have no business in these fields — at minimum reject `@`, `/`, `(`, `)`, `?`, `&`, `:` in `Username`, `Password` and `Database`. This is a secondary guard; step 1 is the real fix. (A password legitimately containing such characters is rare for a monitoring probe account; note it in the field docs / schema.)
4. **Other DB checkers — audited, and the result.** Keep each as-is except where noted:
   - **PostgreSQL** — safe: `BuildConnStr` escapes every field with `escapePQParam` (quote-doubling + quoting, `checkpostgres/config/config.go:187-234`), and the tunneled path uses `pq.NewConnector` + a dialer (`checkpostgres/checker.go:44-62`). No change.
   - **MSSQL, Oracle, RabbitMQ** — safe: built via `url.URL{User: url.UserPassword(...)}` which percent-escapes, and opened via `mssql.NewConnector` / `go_ora.NewConnector` / `amqp.DialConfig` with a dialer (`checkmssql/config/config.go:206-211` + `checkmssql/checker.go:37-54`; `checkoracle/config/config.go:181-200` + `checkoracle/checker.go:21-35`; `checkrabbitmq/config/config.go:475-477`). No change.
   - **Redis, ClickHouse** — safe: structured `redis.Options{}` / `clickhouse.Options{}` with explicit `Addr`/`Auth` and a per-connection `Dialer`/`DialContext` (`checkredis/checker.go:63-81`, `checkclickhouse/checker.go:42-80`). No change.
   - **MongoDB** — has unescaped URI interpolation (`fmt.Sprintf("mongodb://%s:%s@%s", c.Username, c.Password, hostPort)`, `checkmongodb/config/config.go:146`), so a crafted username/password *can* inject URI options. But its dialer is always the guarded/tunnel one (`checkmongodb/checker.go:54-77` sets `clientOpts.SetDialer(...)` under tunnel or enforcing guard), so it is **not** an egress bypass, and MongoDB has no `LOCAL INFILE` analogue. Lower severity than MySQL. Recommended follow-up (see Open questions): switch to `url.URL{User: url.UserPassword(...)}` for consistency, but it is not required by this spec.

## Tests
- `server/internal/checkers/checkmysql/config/config_test.go` (table-driven): **regression (a)** — a config with `database: "x?allowAllFiles=true&z="` must not yield a connection with `AllowAllFiles` set. Assert on the built `*mysql.Config` (new builder): `AllowAllFiles == false` and `DBName == "x?allowAllFiles=true&z="` (the value is data, not params). This fails against today's `BuildDSN`+`ParseDSN` round-trip, which sets `AllowAllFiles=true`.
- `server/internal/checkers/checkmysql/config/config_test.go`: **regression (b)** — a config with `password: "X@tcp(10.0.0.5:3306)/e"` and a benign host must produce a `*mysql.Config` whose `Addr`/`Net` are the configured host and `tcp`, with the whole crafted string kept intact in `Passwd` (never re-parsed into `Addr`).
- `server/internal/checkers/checkmysql/config/config_test.go`: **positive control** — an ordinary config (`host`, `port`, `username`, `password`, `database`, `timeout`) builds a `*mysql.Config` with the expected `Addr`, `User`, `Passwd`, `DBName`, `Timeout` and `AllowAllFiles == false`.
- `server/internal/checkers/checkmysql/tunnel_test.go`: keep `TestExecuteThroughTunnelSkipsLocalResolution` / `TestExecuteWithoutTunnelStillResolvesLocally` green through the `DialFunc` path (they assert the bastion saw the verbatim `host:port`, proving the dial went through the injected dialer, not a DSN-encoded network). Add a case: with a crafted `password` carrying `@tcp(...)`, the tunnel dialer still receives the **configured** target, so the guard/tunnel cannot be shed.
- `server/internal/checkers/registry/egress_test.go`: the MySQL row of `TestCheckTypesRefuseLoopbackTargetsUnderAnEnforcingPolicy` (`:86`) already proves a MySQL check is refused under an enforcing guard and allowed under a permissive one; add a **regression** row for MySQL with a `password` that injects `@tcp(<loopback>)/` and assert the enforcing guard still records a refusal (today the crafted `Net=tcp` escapes the guard).
- `server/internal/checkers/checkmysql/config/config_test.go`: a `Validate` case rejecting `@`/`?`/`/` in username/password/database (step 3).

## To verify
- That retiring or keeping the `solidping-tunnel` `RegisterDialContext` init (`checker.go:29-44`) has no other dependents once `DialFunc` is set per connector (grep showed only `checkmysql` uses `tunnelNetwork`).
- That `mysql.NewConnector(cfg)` + `cfg.DialFunc` is the v1.10.1 shape (confirmed: `NewConnector` at `dsn.go`/`driver.go:101`, `DialFunc func(ctx, network, addr)` at `dsn.go:58-59`, consumed at `connector.go:98-99`).

## Open questions
1. **Fold MongoDB's URI interpolation into this spec or a separate one?** Recommended: separate (or a `## Related` follow-up). It is not an egress bypass (its dialer is always guarded) and has no file-read primitive, so it is lower severity and this spec stays focused on the MySQL critical. Note it so it is not lost.

## Resolved open questions
Decided 2026-10-16 (all recommendations accepted):
1. Keep MongoDB URI interpolation out of this spec. Add a `## Related` follow-up note (or a backlog spec) so it is not lost.

## Related
- N1 (same scan): `${env:}` leaks the same platform secrets by a different route; the MySQL file-read reaches `/proc/self/environ` — spec `2026-10-16-03-env-secret-reference-allowlist.md`.
- MongoDB unescaped URI interpolation (`checkmongodb/config/config.go:146`) — same interpolation class, not covered here (see Open questions).
