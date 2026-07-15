---
model: sonnet
effort: low
---

# The default embedded-PostgreSQL port should be 5433

## Problem

The `postgres-embedded` database type (used by `make dev` and anyone running
SolidPing with a self-managed, bundled Postgres) listens on a hard-coded default
port of **5434**. We want the default to be **5433** instead.

The port is currently defined as a `embeddedPostgresPort = 5434` constant,
duplicated across three sites that must stay in sync:

- [`server/main.go:31`](server/main.go:31) — `embeddedPostgresPort = 5434`
  (consumed at [`main.go:366`](server/main.go:366) and
  [`main.go:392`](server/main.go:392))
- [`server/internal/app/server.go:113`](server/internal/app/server.go:113) —
  `embeddedPostgresPort = 5434`, used at
  [`server.go:200`](server/internal/app/server.go:200) and baked into the
  notifier connection string at
  [`server.go:244`](server/internal/app/server.go:244)
  (`postgres://postgres:postgres@localhost:%d/solidping_test?...`)
- [`server/internal/db/postgres/embeddedpg/embeddedpg.go:152`](server/internal/db/postgres/embeddedpg/embeddedpg.go:152)
  — the `port = 5434` fallback used when `Options.Port` is zero

`5433` is currently unused anywhere in the repo (grep confirms no existing
reference), so the new default does not collide with any other in-repo port.
The standard system Postgres port `5432` and the docker-compose mapping
(`55432:5432`) are both left untouched.

## Proposal

Change the embedded-PostgreSQL **default** port from `5434` to `5433` at all
three sites above, keeping them consistent:

1. `server/main.go` — `embeddedPostgresPort = 5433`
2. `server/internal/app/server.go` — `embeddedPostgresPort = 5433` (the notifier
   DSN at `server.go:244` derives from the same constant, so it follows
   automatically)
3. `server/internal/db/postgres/embeddedpg/embeddedpg.go` — the `port = 5434`
   fallback → `5433`

Notes / scope guardrails:

- **Do not touch the PostgreSQL _check type_ default port (5432)** — that is the
  monitored-target default (`specs/done/2026/03/2026-03-22-postgresql-monitoring.md`),
  a completely separate concern from SolidPing's own embedded storage DB.
- **Leave test-only ports alone.** Integration/unit tests allocate their own
  embedded-PG ports explicitly (e.g. `5435`, `15434`, `15436`, and per-file
  values noted in the "port-numbering note" comments across
  `server/internal/db/postgres/*_test.go`). Those pass an explicit `Options.Port`
  and never rely on the default, so they are unaffected — but re-run the
  embedded-PG suites to confirm nothing regressed.
- Consider deduplicating the constant later (it lives in both `main.go` and
  `server.go`); not required for this change, but call it out if a shared home is
  trivial.
- Update the constant's doc comment only if it names the number; the current
  comment ("the default port for embedded PostgreSQL") stays accurate.

### Open question

Is there a reason the default was set to `5434` rather than `5433` originally
(e.g. avoiding a conflict on a specific dev setup)? Grep shows `5433` is free
in-repo; if the implementer finds an external rationale, surface it before
flipping the value.

### Verification

- `make build` + `make dev` (or run with `SP_DB_TYPE=postgres-embedded`) and
  confirm the embedded server binds `localhost:5433` and the app connects
  through the notifier DSN.
- Run the embedded-Postgres backend test suites to confirm the explicit-port
  tests still pass.

## Implementation Plan

1. Change `embeddedPostgresPort = 5434` → `5433` in `server/main.go:31`.
2. Change `embeddedPostgresPort = 5434` → `5433` in
   `server/internal/app/server.go:113` (notifier DSN at `server.go:244`
   derives from the constant automatically, no separate edit).
3. Change the `port = 5434` fallback → `5433` in
   `server/internal/db/postgres/embeddedpg/embeddedpg.go:152`.
4. Update the stray explanatory comment in
   `server/internal/db/postgres/postgres_headroom_postgres_test.go:19`
   ("5434 is the app default") to say 5433, so the port-numbering note stays
   accurate. No other doc/README/docker-compose references to the default
   were found (grep-checked).
5. History check: `git log --all --oneline | grep 5433` and grepping for a
   rationale turned up nothing — no evidence 5434 was deliberately chosen to
   avoid a conflict. Proceeding with the change as specced.
6. `make fmt`, then `make build-backend lint-back test` (backend-only scope).
7. Explicit-port test suites (5435, 15434, 15436-15452 range) are unaffected
   since they pass `Options.Port` directly — re-run to confirm no regression.
