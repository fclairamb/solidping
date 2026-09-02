---
model: opus
effort: high
---

# The passive-check evaluation re-anchors on its own previous row, so overdue and stale-run detection are a jitter coin-flip and `lastSignalAt` drifts

## Problem

A heartbeat (or email) check produces two kinds of raw result rows:

- **Signal rows** — written at ingest by `recordBeat`
  (`server/internal/handlers/heartbeat/service.go:289`, result built at
  `:329-341`) and by the email ingest
  (`server/internal/handlers/emailcheck/handler.go:~418`). They carry the
  caller metadata and have **no `worker_uid` and no `region`** — nothing in
  those constructors sets either.
- **Evaluation rows** — written every check period by `executePassiveJob`
  (`server/internal/checkworker/worker.go:1488`) through
  `DirectBackend.SubmitResult`, which stamps `period_start = time.Now()`,
  `worker_uid = <worker>` and `region` (`backend/direct.go:211-223`).

`executePassiveJob` decides the check's status by reading "the last result"
(`worker.go:1493` → `backend.LastResults` → `GetLastResultForChecks`,
`db/postgres/postgres.go:2685`, `db/sqlite/sqlite.go:2668`): the newest raw
row of **any origin**, excluding only `status = created`. The evaluation's own
previous row is a raw row with status Up (or Running), so from the second
evaluation after a beat onwards, **the evaluation reads its own predecessor
rather than the beat**.

Timeline with period `P`, beat at `T0`, phase-locked ticks
(`calculateNextScheduledAt`), claim jitter `j`, submit overhead `s`:

| evaluation | newest raw row it reads | `elapsed` it computes |
|---|---|---|
| E1 at `T0 + δ` | the beat | `δ` → Up, `lastSignalAt = T0` ✔ |
| E2 at `T0 + P + j2` | **E1** (Up, `period_start = T0 + δ + s1`) | `P + (j2 − δ − s1)` — decided by jitter |
| E3 … | **E2** | `≈ P ± jitter` — coin flip again |

Consequences, all deterministic from the code (no dev query needed):

1. **Missing-beat detection is randomized.** A heartbeat that stops beating
   stays Up for a geometric number of extra periods — each evaluation is a
   ~50 % coin flip on `elapsed <= period` (`worker.go:1506`) — with an
   unbounded tail. The one thing a heartbeat check exists to assert
   ("no ping in time ⇒ incident") has a random delay.
2. **When it does flip, the diagnostics are wrong.** The row that opens the
   incident says `Heartbeat overdue`, `overdueBy: 4ms` (the jitter
   difference) and `lastSignalAt = <previous evaluation's timestamp>` — not
   the beat's. That row is what the incident's `first_result` snapshot and
   every notification carry.
3. **The next evaluation forgets the beat entirely.** It reads the Down
   evaluation row, matches no branch and writes `No heartbeat received`
   (`worker.go:1502`) — even though a beat was received one period ago —
   and `lastSignalAt` disappears from the output.
4. **Stale-run detection can never fire in production.** A `status=running`
   beat makes E1 write a Running row (`worker.go:1523-1528`); E2 reads E1
   (Running, `elapsed ≈ P ≤ 2P`) and writes Running again, re-anchoring
   `runStarted` on itself every period. The `Run started but never
   completed` / `StatusTimeout` branch (`worker.go:1531-1537`) is only
   reachable when *no* evaluation ran in the last `2P` — i.e. never.
   `TestExecuteHeartbeatJob_RunningStatus` (`worker_test.go:756`) passes only
   because it seeds a single old Running row with no evaluation in between.
5. **The common case is right by accident.** When beats arrive more often
   than the period, the newest raw row is almost always a beat, which is why
   the bug hides in the healthy state and surfaces exactly in the failure
   state.

Observed on dev (the reporter's example, check
`1893d40c-6d72-4378-84b3-18b88a3e5d6b`): beat row
`01a0621f-10fd-7062-9892-a4b0749b84d9` at 12:36:38Z with caller metadata,
evaluation row `01a0621f-2f86-7abf-8e7d-92b70dac7156` at 12:36:46Z with
`lastSignalAt` — the next evaluation of that check reads the 12:36:46 row,
not the 12:36:38 beat. (The check lives in an org the implementer's API key
may not read; the reproduction below does not depend on it.)

## Proposal

Make the evaluation read **the newest signal row**, never a worker-written
row. The discriminator already exists structurally — `worker_uid IS NULL` —
so no output-shape heuristics and no schema change to `results` are needed.

### D1. `LastResults` becomes `LastSignals`

The backend interface method `LastResults` (`backend/backend.go`,
`backend/direct.go:313`, `backend/ws.go:389`, test stub in
`fetcher_fatal_test.go:78`) has exactly one caller, `executePassiveJob`.
Rename it to `LastSignals(ctx, orgUID, checkUIDs)` with the contract
"newest raw row per check that was written by an inbound signal". The WS
backend keeps returning `ErrPassiveUnsupported` (passive checks never
dispatch to private-region agents, so there is no agent-protocol impact).
`GetLastResultForChecks` is **not** touched — it serves the API's
`lastResult`, where "newest row of any origin" is the right answer.

### D2. New DB query `GetLastSignalForChecks` (PG + SQLite)

Same shape as `GetLastResultForChecks` (LATERAL descent on PG, correlated
subquery on SQLite — sync-pg-to-sqlite convention) with two predicates added:

```sql
AND res.worker_uid IS NULL
AND (res.status IS NULL OR res.status != <created>)
```

`created` stays excluded: CreateCheck's one-time `Check created` marker
(`sqlite.go:1438`) has no worker_uid either and must not count as a signal.
Reaper rows (`abandoned`) are worker-written and fall out naturally.

### D3. Partial index for the signal descent

`results_raw_idx (organization_uid, check_uid, period_start DESC) WHERE
period_type = 'raw'` does not carry `worker_uid`, so for a dead heartbeat the
descent walks every evaluation row inside raw retention (24 h default,
`config.go:806` — 1 440 heap fetches per evaluation at a 1-minute period,
every minute, per dead check). Add, in the next migration number of **both**
dialects:

```sql
CREATE INDEX results_raw_signal_idx
  ON results (organization_uid, check_uid, period_start DESC)
  WHERE period_type = 'raw' AND worker_uid IS NULL;
```

It contains only ingest rows and creation markers — tiny — and makes the
signal lookup O(1) regardless of how long the check has been silent.

### D4. `executePassiveJob` semantics after the fix

The switch at `worker.go:1500-1538` is unchanged in structure; its input is
now guaranteed to be a signal, so:

- `lastSignalAt` / `runStarted` are always the beat's own timestamp;
- `overdueBy` is `elapsed − period` measured from the beat;
- Running beats time out after `2P` as designed;
- the default branch (no signal within raw retention, or newest signal is a
  `down`/`error` beat) keeps its status and message, but when a signal
  exists it now also carries `lastSignalAt` so the UI can say when. (Message
  wording for that branch is owned by spec 2026-09-02-04.)

A beat older than raw retention has been deleted, so the check reads "No
heartbeat received" — identical to today's post-flip behaviour; note it in
the docs paragraph of 2026-09-02-04 rather than here.

### Tests (must prove the negative)

- `worker_test.go` — `TestExecutePassiveJob_IgnoresItsOwnEvaluationRows`:
  seed a beat (Up, `WorkerUID: nil`, caller keys) at `now − (P + 30 s)` and
  an evaluation row (Up, `WorkerUID: &worker.UID`, `lastSignalAt`) at
  `now − 10 s`; run; expect `Heartbeat overdue`, `lastSignalAt ==` the beat's
  `period_start` (RFC3339) and `overdueBy ≈ 30 s`. State in the test comment
  that the pre-fix code returns Up here; the test must fail on current
  `main`.
- `TestExecutePassiveJob_StaleRunDetectedDespiteEvaluationRows`: Running
  beat at `now − (2P + 30 s)`, Running evaluation row at `now − P`; expect
  `StatusTimeout`, `Run started but never completed`, `runStarted ==` the
  beat's timestamp. Fails on current `main` (returns Running).
- Email variant of the first test (noun = "Email"), since
  `passiveSignalNoun` shares the path.
- DB tests, PG (testcontainers) and SQLite, for `GetLastSignalForChecks`:
  skips worker rows, skips `created`, returns no entry for a check that has
  only evaluation rows, returns one entry per requested check, and the
  Postgres plan uses `results_raw_signal_idx` (EXPLAIN assertion like the
  existing spec 2026-08-09-07 test, if one exists; otherwise a comment).
- Existing `TestExecuteHeartbeatJob_RunningStatus`, the express-hint and
  region-fallback tests (`worker_test.go:551, 613, 722`) keep passing
  unchanged.

### Out of scope

- Whether evaluation rows should count toward availability at all — they do
  today, and nothing here changes availability math, rollups or SLOs.
- Distinguishing evaluation rows from beats in the API/dashboard — spec
  2026-09-02-04, which assumes this spec has landed so that "the signal this
  evaluation saw" is a real beat.
