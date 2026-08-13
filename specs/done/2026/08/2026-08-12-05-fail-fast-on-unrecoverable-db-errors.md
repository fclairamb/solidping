---
model: opus
effort: high
---

# The server retries unrecoverable schema errors forever instead of failing

## Problem

A `solidping-e2e serve` process ran for seventeen hours with every job query
failing on:

```
SQL logic error: no such table: jobs (1)
```

It never stopped, never reported unhealthy, and never exited. It logged the
same error until the host disk was full (see spec `2026-08-12-04` for the
retry loop that amplified it — that spec bounds the *volume*; this one is about
the process staying alive at all).

The point worth dwelling on: **no number of retries will make a missing table
appear.** The server spent seventeen hours doing something that could not
possibly succeed, while presenting itself as a running service.

### The schema did not fail to migrate — it vanished

`main.go:189` calls `server.Initialize(ctx)` at startup, which runs migrations
(`internal/app/server.go:2621` → `internal/db/service.go:63` →
`internal/db/sqlite/sqlite.go:253`). So the `jobs` table existed when the
process booted. It disappeared **underneath a live process**.

For SQLite that has a small set of plausible causes, and they need to be
distinguished before choosing a fix:

- an e2e teardown deleted or replaced the database file while the server was
  still running (the process was an `-e2e` binary left over from a test run,
  which makes this the leading hypothesis);
- the DB pointed at a temporary path that something else reclaimed;
- a connection was re-established against a fresh, unmigrated file.

`internal/db/sqlite/sqlite.go:210` already carries a comment about the hazards
of dropping the file, which suggests this failure mode has been considered
before in a different context.

## Proposal

Treat "the schema is gone" as what it is: a terminal condition for the process,
not a transient error to be retried.

Three questions to settle, in order:

**1. Which of the three causes above actually happened?** Reproduce it before
designing around it. If the e2e harness deletes the DB file out from under a
still-running server, that is a harness bug worth fixing on its own — and it
also means the production-shaped fix below is defence in depth rather than the
primary remedy.

**2. Where does the classification live?** A missing table is not a job-worker
concern; any component touching the database can hit it. The natural home is
the db layer: a helper that classifies a driver error as transient (retry) or
structural (do not retry), so both the job worker and future callers share one
definition. Resist putting a string match on `no such table` in `jobworker` —
that is the kind of check that silently stops matching when the driver's
wording changes.

**3. What should the process do?** Options, roughly in order of preference:

- *Shut down.* A server whose schema is missing cannot serve; exiting lets a
  supervisor restart it, which re-runs migrations and may well fix it outright.
  Loud, unambiguous, and it makes the failure visible in seconds instead of
  never.
- *Report unhealthy and keep serving reads.* Gentler, but it needs a health
  endpoint that actually reflects this state; a process that answers "healthy"
  while every job query fails is the situation we already have.
- *Retry with a hard cap, then escalate to one of the above.* Useful only if
  investigation shows a genuinely recoverable variant (e.g. a brief window
  during file replacement where the table legitimately reappears).

Whichever is chosen, the outcome must be that a schema-level failure becomes
**visible within seconds and terminal within minutes**, rather than a silent
seventeen-hour spin.

Also worth deciding: whether the periodic schema check should be active
(cheap probe) or purely reactive (classify errors as they occur). Reactive is
simpler and sufficient if the classification is centralised; an active probe
mainly buys earlier detection on an idle server.

## Acceptance criteria

- The root cause is identified in writing, with a reproduction, not a guess —
  specifically whether the e2e harness removes the database file while the
  server is running.
- Structural database errors are classified in one place in the db layer, with
  a test per classified driver error, for both SQLite and PostgreSQL.
- A server whose schema has disappeared reaches a terminal, externally visible
  state within minutes — the exact mechanism follows from question 3 above.
- The condition is distinguishable from an ordinary outage in the logs: one
  clear message naming the structural fault, not an undifferentiated stream of
  query errors.
- If the e2e harness is at fault, it no longer tears down the database while a
  server it started is still running; if a leftover server is possible by
  design, it dies with its parent rather than being orphaned to PPID 1.
- Existing behaviour for genuinely transient errors is unchanged: a dropped
  connection still retries and recovers without killing the process.

## Notes

The orphaned process is a related but separable problem. It had PPID 1 and
outlived the session that started it by seventeen hours. Even with everything
above fixed, an e2e sidecar that cannot die with its parent will find another
way to be a nuisance — worth handling wherever that binary is spawned, though
nothing in the repository creates the `sidecar.log` file, so the redirect came
from an ad-hoc command rather than committed code.
