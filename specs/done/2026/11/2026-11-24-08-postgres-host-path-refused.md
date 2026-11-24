---
model: opus
effort: high
pr: own
automerge: false
---

# Refuse a filesystem-path `host` on the PostgreSQL check, so lib/pq never opens a Unix socket outside the allowlist

## Problem

From security advisory GHSA-88fc-w4jm-9fcx.

- **Severity:** High in the documented own-database-over-socket setup, Medium elsewhere.
- **Who can trigger it:** any org member who can create or edit a check, on a self-hosted instance, or on a check pinned to a private-location agent. SaaS shared workers are not affected: their enforcing egress guard refuses a path address.
- **Where:** `server/internal/checkers/checkpostgres/connector.go:25-65` (`buildPQConfig`) and `:78-96` (`openDB`), plus `config/config.go:277-290` (`validateTarget`).

Spec 2026-11-24-01 made Unix sockets deny-by-default. Every socket dial is supposed to pass through `checkerdef.DialLocalSocket`, which checks `checkers.unix_socket_paths` (empty by default). The postgres check bypasses that gate:

- `buildPQConfig` copies `cfg.Host` into `pq.Config.Host` as is.
- lib/pq v1.12.3 (`connector.go:654-664`, `Config.network`) dials `unix` at `<host>/.s.PGSQL.<port>` when the host is an absolute path or starts with `@`.
- `openDB` only installs a dialer for the `socket` field, a tunnel or an enforcing guard. Otherwise pq keeps its own `net.Dialer`.
- `validateTarget` only requires `host` to be non-empty.
- `validateLocalSocketConfig` (`server/internal/handlers/checks/validate.go`) reads only `LocalSocketOf`, which means the `socket` field.

So `host` set to a socket directory connects whatever the allowlist says.

**Impact.** The check runs the author's query and returns its first row (`checker.go:139-149`). The docs present reaching SolidPing's own database over `/var/run/postgresql` as a supported setup (`web/docs/docs/configuration/database.md` "Unix-domain socket", `web/docs/docs/installation/docker.md`). Its `pg_hba.conf` `local` lines usually use `peer` or `trust`:

- With `peer`, a check whose username is the role of the SolidPing OS user (a bare-metal service install runs as `solidping`) authenticates without a password.
- With `trust`, any role works.

Either way the check author can read and rewrite every org's rows, which means taking over the instance. On an agent host, an org writer reaches the host's local PostgreSQL although the agent operator left the allowlist empty.

## Proposal

1. **Validate the host.** In `checkpostgres/config/config.go` `validateTarget`, when `Socket` is empty, refuse a `host` for which `filepath.IsAbs` is true, that starts with `@`, or that contains `/`. The config error goes on `host` and says to use the `socket` field (`.s.PGSQL.<port>` path) with `checkers.unix_socket_paths`.
2. **Refuse `unix` at dial time.** In `connector.go` `openDB`, never leave pq on its default dialer. Without a tunnel or guard, install a `pqTunnelDialer` over a `net.Dialer` that refuses network `unix` with `checkerdef.ErrLocalSocketNotAllowed`. A row stored before step 1, or a config path that skips `Validate`, then still cannot dial a socket. The `socket` path keeps `LocalSocketDialer`.
3. **Same backstop in the other drivers.** Apply the same `unix` refusal to the default dialers of the other checkers whose driver picks the network from the address:
   - go-redis switches to `unix` for an `Addr` starting with `/` (`checkredis/checker.go:68`, `setRedisDialer`). The redis check appends `:port` today, so this is hardening only.
   - The mongodb check is covered by its own advisory spec, 2026-11-24-07.
4. **Docs.** Add a line under "Unix sockets" in `web/docs/docs/features/check-types.md`: a postgres `host` must be a hostname or an IP, and a local socket goes through `socket` plus the allowlist.

## Tests

- `checkpostgres/config`: `Validate` refuses `host` values `/var/run/postgresql`, `/tmp` and `@pg` (error on `host`), and accepts `db.acme.com`, `10.0.0.5` and `::1`. It must fail on the current code.
- `checkpostgres` dial test:
  - Setup: a context with an empty socket allowlist and no guard, plus a `PostgreSQLConfig` built directly (skipping `Validate`) with `Host: t.TempDir()`. A `net.Listen("unix", <dir>/.s.PGSQL.5432)` listener accepts in a goroutine.
  - Assert: the check returns an error result naming the refusal, and the listener never accepts a connection.

  It must fail on the current code, where pq connects.
- Positive control: the same listener through `Socket: <dir>/.s.PGSQL.5432` with `WithLocalSocketAllowlist(ctx, []string{<dir>/})` reaches the listener, and a TCP `host: 127.0.0.1` against a test listener still dials.

## To verify

- Whether existing rows with a path `host` are worth a startup WARN. A data migration is not worth it: such checks fail on their next run with a clear error.
