---
model: sonnet
effort: medium
pr: unix-socket-checks
automerge: false
---

# Redis and Postgres checks over a Unix socket

## Problem

Issue #1000. Redis and Postgres are often configured to listen only on a local socket
(`/run/redis/redis.sock`, `/var/run/postgresql/.s.PGSQL.5432`). Their checks only take
`host`/`port` (`checkredis/config/config.go:30-31`, `checkpostgres/config/config.go:46-47`).
Depends on spec `local-socket-dialer` of this PR.

## Proposal

1. **Config**: add `Socket string` (`json:"socket,omitempty"`) to both configs, mutually exclusive
   with `host`/`port` (`checkerdef.SchemaExclusiveGrouper`). Both implement
   `checkerdef.LocalSocketTarget`, and both types set `SupportsLocalSocket`.
   - Redis: `socket` is the socket file path.
   - Postgres: `socket` is the socket file path (`/var/run/postgresql/.s.PGSQL.5432`), not libpq's
     directory form, so the allowlist check stays an exact file path.
2. **Dial through the shared dialer**, never through the driver's own socket support, so the
   allowlist and refusal rules apply:
   - go-redis: set `Network: "unix"`, `Addr: socket` and a `Dialer` that calls
     `checkerdef.DialLocalSocket`.
   - Postgres: give the driver a custom dial function calling `checkerdef.DialLocalSocket` (see To
     verify for the driver's hook); TLS is refused with a socket.
3. **URL forms** for quick create, added to `schemeMapping` (`checkers/urlparse/urlparse.go:42`)
   if `redis`/`postgres` URLs are parsed there (To verify):
   - `redis+unix:///run/redis/redis.sock?db=0`
   - `postgres+unix:///var/run/postgresql/.s.PGSQL.5432?dbname=app&user=monitor`
4. **dash0**: the Redis and Postgres forms get the same "Host and port / Unix socket" switch as `tcp`
   (`data-testid="redis-target-mode"`, `redis-socket-path`, `postgres-target-mode`,
   `postgres-socket-path`), offered through the same shared hook.
5. **Docs**: Redis and Postgres rows in the Unix sockets table of the check-types page.

## Tests

- `checkredis`: a minimal RESP server on an allowlisted socket answering `PING` gives up; a missing
  socket gives down with `ENOENT`; a path outside the allowlist is refused without dialing
  (positive control: the allowlisted path works).
- `checkpostgres`: the dial function receives the socket path and goes through `DialLocalSocket`
  (refused outside the allowlist). A real server on a socket goes in the testcontainers layer if
  it can share a socket with the host; otherwise unit-level only, and say so.
- Validation: `socket` together with `host` is rejected for both.
- `urlparse`: both URL forms parse to the socket and the query parameters.
- dash0 Playwright: create a Redis check in socket mode.

## To verify

- Which Postgres driver `checkpostgres` uses (pgx: `pgconn.Config.DialFunc`; lib/pq: a `Dialer`)
  and how to give it a unix dial function with a full file path.
- Whether `redis://` and `postgres://` URLs go through `urlparse` today or through each check's own
  URL field.
