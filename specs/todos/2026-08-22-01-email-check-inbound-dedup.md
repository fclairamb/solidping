---
model: opus
effort: xhigh
---

# One inbound email mints up to 4 duplicate results because every server replica consumes the same JMAP inbox with no claim and no idempotency

## Problem

The `email` check type is passive: the server watches a JMAP mailbox and each
inbound mail whose recipient matches a check token mints a raw result
(`server/internal/handlers/emailcheck/handler.go`). On a multi-replica
deployment this produces duplicate results for a single email.

Observed on the k8xp dev deployment (check `aabab130-ba81-483d-9bba-c613f2b0f21d`,
an SMTP-relay delivery inbox check): the same `Message-ID` was recorded as 3–4
separate raw results within ~2 seconds, several times in one day:

```
22:17:15.206 / .217 / .225 / 22:17:16.934  → CAJKrVea...@mail.gmail.com   (×4)
14:44:44.962 → 14:44:45.922                → 20d4f70b-...@...outlook.com  (×4)
```

All rows have `region=NULL, worker_uid=NULL, duration=0` — the signature of the
inbound-email path. Two server pods logged the losing side of the race 18 ms
apart:

```
solidping                 22:17:15.717 WARN "JMAP move-to-processed failed" messageId=lmaaaac9 error="jmap: not all emails updated: 1 emails"
solidping-checks-default  22:17:15.735 WARN "JMAP move-to-processed failed" messageId=lmaaaac9 error="jmap: not all emails updated: 1 emails"
```

Three compounding root causes:

1. **The JMAP manager runs in every server process unconditionally.**
   `runJMAPManager` is started in `server/internal/app/server.go:2757-2764`
   without any `Node.Role` gate — unlike the job worker (`server.go:2805`) and
   check worker (`server.go:2820`). Every replica sharing the `email_inbox`
   system parameter polls the same mailbox concurrently.
2. **Process-then-archive ordering.** `jmap.Manager.syncEmails`
   (`server/internal/jmap/manager.go:370-424`) queries the inbox, runs the
   handlers (which record the result), and only *afterwards* moves the message
   to the Processed mailbox (`manager.go:418`). Every consumer that sees the
   message before a move lands records it. Worse, when *both* movers fail (as
   above), the message stays in the inbox and is reprocessed on the next
   event/poll — that is the 4th row at +1.7 s.
3. **No idempotency.** The inbound `Message-ID` is stored in `output.messageId`
   (`handler.go:320`) but never consulted before inserting, so nothing
   downstream catches a duplicate.

Consequences: inflated result counts and misleading result lists (bursts of
identical "Up" rows), skewed aggregations, and N concurrent JMAP sessions
hammering the mailbox provider for no benefit.

**Constraint: no DB migration.** No new tables, columns, or unique
constraints — the fix must work with the existing schema.

## Proposal

Three layers, all migration-free. Layers 1 and 3 provide correctness; layer 2
stops the redundant polling.

### 1. Claim-by-archive (CAS on the mailbox itself)

Invert the ordering in `syncEmails`: attempt the move-to-Processed *first* and
treat it as the claim. JMAP `Email/set` responses distinguish `updated` from
`notUpdated` ids — a consumer only processes (records a result for) the
messages *it* successfully moved; ids that come back `notUpdated` were claimed
by someone else (or already archived) and are skipped silently, downgrading
today's WARN to a debug-level "lost claim" log. This gives at-most-once
processing across any number of consumers with no shared state beyond the
mailbox itself.

Crash-safety: a crash between the successful move and the result insert would
drop that email's signal. Mitigate by having the manager, on startup (and
optionally on each fallback poll), re-scan the most recent messages in the
Processed mailbox (bounded, e.g. last 50 or last 24 h) and record any whose
`Message-ID` has no matching result — idempotent thanks to layer 3.

### 2. Single active consumer via Postgres advisory lock

Real leader election is overkill — all replicas already share one Postgres. In
`runJMAPManager`, acquire a session-scoped `pg_try_advisory_lock` on a
dedicated constant key (follow whatever key-numbering convention exists; grep
first — none found as of writing). Holder runs the JMAP manager; non-holders
retry acquisition every ~30 s, giving automatic failover when the leader dies.
Release on graceful shutdown. Hold the lock on a dedicated long-lived
connection (advisory session locks die with their connection — do not take it
through the pool and let the pool recycle it).

On SQLite deployments there is a single server process by construction — skip
the lock and always run the manager.

This layer is an optimization (fewer JMAP sessions, less provider load), not
the correctness mechanism: even a single leader re-processes a message when its
own move fails, so layers 1 and 3 are still required.

Non-goal: gating `runJMAPManager` by `Node.Role`. The advisory lock supersedes
it (static role config would have no failover), but the asymmetry with
`server.go:2805/2820` is worth a comment at the call site.

### 3. Query-based dedup backstop (no schema change)

Before inserting a result in `emailcheck` handling, check for an existing raw
result for the same `check_uid` whose `output.messageId` matches, scoped to a
recent window (e.g. `period_start > now() - interval '7 days'`) so the JSONB
scan stays bounded by the check's recent-results range. If found, skip the
insert and log at debug. Works on both Postgres and SQLite (JSON extraction
exists in both query layers). Emails lacking a `Message-ID` header skip this
layer (rare; layers 1–2 still cover them).

### Tests

- Fake JMAP server: two managers against one inbox → exactly one result per
  email; the loser sees `notUpdated` and records nothing (prove the negative:
  assert result count == 1, with a positive control that a lone manager
  records exactly 1).
- Move-fails path: JMAP server rejects the move → no result recorded by that
  consumer, message reprocessed later without producing a duplicate (dedup
  layer catches the retry).
- Startup re-scan: message present in Processed with no result → result
  recorded once; run twice → still one result.
- Advisory lock (testcontainers PG): two connections, one wins; kill the
  winner's connection → the other acquires within the retry interval.
- Dedup query unit test on both PG and SQLite: same messageId twice → one row;
  different messageId → two rows.

### Acceptance

One inbound email = exactly one raw result for the matching check, regardless
of replica count, move failures, or event/poll overlap. At most one replica
holds an active JMAP session at a time (PG deployments).
