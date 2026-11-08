---
model: sonnet
effort: medium
pr: batch
automerge: false
---

# A remote agent losing its server connection floods ERROR lines and abandons checkers that should have stopped

GitHub Issue: https://github.com/fclairamb/solidping-business/issues/55

## Source

The daily pod-log review (item 3 of the issue): during a ~5 minute network or restart event on
one remote region's agent, the agent's WebSocket went stale (keepalive unanswered) and the logs
show 18 `Checker abandoned: did not honor context` (baseline ~8), 15 `failed to submit result
... failed to acquire lock: context deadline exceeded / use of closed network connection`, and
`Failed to claim jobs: agent connection lost`, then the server's `Region went dark`. It recovered
on its own. The reviewer asks to look at the abandon count and at ERROR for a closed connection.

## Problem

- `server/internal/checkworker/worker.go:797-800` logs every claim failure at ERROR except
  `context.Canceled`; an "agent connection lost" error is an expected transient state of a
  remote agent, not a server bug.
- `worker.go:1460-1463` wraps a submit failure; when the cause is a closed connection or a
  deadline during a reconnect, the job's result is lost and it is reported as an execution error
  at ERROR upstream.
- `worker.go:1695` (`abandonCheckerExecution`) fires for checkers whose context was cancelled
  by the connection loss itself. Whether those checkers really ignore their context (a checker
  bug worth the ERROR) or are simply cut off while blocked on the dead connection (expected) is
  not known: the 18 vs 8 count suggests the second for part of them.

## Why a spec

Correctness hinges on concurrency (the worker's claim/execute/submit pipeline during a
reconnect), and the right level per error needs a decision backed by reproduction, not a
blanket downgrade.

## Proposal

1. Reproduce: run an agent against a local server, cut the WebSocket mid-batch (kill the
   connection, or a test backend whose `SubmitResult`/`ClaimJobs` return the connection-lost
   error), and record what each path logs.
2. Introduce a sentinel (or reuse the existing one) for "agent connection lost" and log claim
   and submit failures carrying it at WARN, once per outage (first failure, then a summary with
   the count when the connection comes back), instead of one ERROR per job.
3. Abandon: when the abandon was triggered by a connection-loss cancellation, count it under a
   distinct reason (metric label + WARN); keep ERROR for a checker that ignores a plain
   timeout.
4. Tests in `internal/checkworker` with a fake backend: a connection loss during a batch yields
   no ERROR line, one WARN, and the jobs are left for re-claim.

## Closing the issue

This spec closes fclairamb/solidping-business#55 together with
`2026-11-08-03-email-job-invalid-recipient.md`. The issue was closed when the specs were
written; when the spec is archived to `specs/done/`, check it is closed and close it by hand
otherwise:

    gh issue close 55 --repo fclairamb/solidping-business --comment "Implemented by <PR or commit>; spec: <archived spec path>"
