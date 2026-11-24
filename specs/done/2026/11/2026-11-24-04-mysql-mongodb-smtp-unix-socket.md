---
model: sonnet
effort: medium
pr: unix-socket-checks
automerge: false
---

# MySQL, MongoDB and SMTP checks over a Unix socket

## Problem

Issue #1000, after `redis-postgres-unix-socket`. MySQL/MariaDB (`/run/mysqld/mysqld.sock`), MongoDB
(`/tmp/mongodb-27017.sock`) and a local MTA's submission socket are the other services commonly
bound to a Unix socket. Depends on spec `local-socket-dialer` of this PR.

## Proposal

Same shape as the Redis/Postgres spec, for each of `checkmysql`, `checkmongodb` and `checksmtp`
(their `config/config.go`):

1. `Socket string` (`json:"socket,omitempty"`), mutually exclusive with `host`/`port`; the config
   implements `checkerdef.LocalSocketTarget`, and the type sets `SupportsLocalSocket`.
2. Dial through `checkerdef.DialLocalSocket` via each driver's dial hook, never the driver's own
   socket support:
   - MySQL: register a dial function with `mysql.RegisterDialContext` under a private network name
     and set `Net` to it, `Addr` to the socket path;
   - MongoDB: `options.Client().SetDialer(...)` with a dialer calling `DialLocalSocket`;
   - SMTP: the check's existing connection setup dials the socket; STARTTLS and implicit TLS are
     refused with a socket.
3. URL forms (`mysql+unix://`, `mongodb+unix://`, `smtp+unix://`) only where the type already has a
   URL form.
4. dash0: the same target switch on the three forms, with `data-testid`s, offered through the same
   shared hook.
5. Docs: three rows in the Unix sockets table.

## Tests

- For each type: a fake server on an allowlisted socket gives up (MySQL handshake, a MongoDB `hello`
  reply, an SMTP `220` banner, whatever the check needs as its minimum); a path outside the
  allowlist is refused without dialing (positive control: the allowlisted path works); `socket`
  with `host` is a validation error.
- dash0 Playwright: create one of the three in socket mode.

## To verify

- Whether the MySQL driver version in `go.mod` has `RegisterDialContext` (since 1.5).
- What the `smtp` check sends after the banner (EHLO, auth), to size the fake server.
- Whether each fake server is simpler than a socket-mounted testcontainer. Pick the simpler one per
  type.
