---
model: sonnet
effort: medium
---

# `go test -race ./internal/integrations/discord/...` reports data races in the gateway test harness

## Problem

Running `go test -race ./internal/integrations/discord/...` in `server/` fails with
several "WARNING: DATA RACE" reports. This is unrelated to the
`CloseIdleConnections`/`http.DefaultTransport` issue fixed by
`specs/done/` (see the archived
`2026-09-24-09-shared-integration-http-transport.md`).

The race is a genuine concurrency bug in the discord package's test harness, not in
production code:

- `gatewayHarness` (`server/internal/integrations/discord/gateway_test.go`, see
  `newGatewayHarness` around line 160 and `start` around line 225) runs a
  `*GatewaySupervisor` in a background goroutine via
  `Run()` → `runOnce()` → `readLoop()` → `handleDispatch()` → `handleMessageCreate()`
  → `ingestThreadComment()` → `recordComment()`
  (`server/internal/integrations/discord/gateway_messages.go`, roughly lines 83-182).
- That background goroutine calls into the test double
  `fakeIncidents.AddCommentFromDiscord` / `AddCommentFromDiscordCommand`
  (`server/internal/integrations/discord/interactions_test.go`, roughly lines 71-82),
  which mutates fields on the fake with no synchronization.
- Meanwhile the test's main goroutine concurrently reads those same fields directly
  (`gateway_test.go`, roughly lines 342-344, 425, 441, 504) instead of only observing
  them through something safe like `assert.Eventually` backed by a lock.

Confirmed pre-existing: checking out commit `89af98d26` (the commit right before the
httpclientpool work started on this branch) into a scratch worktree reproduces the
same 6 data races there. This is not something introduced by the httpclientpool
switch — it's an existing bug in test synchronization.

## Proposal

Root-cause and fix the test harness, not production code:

1. Add a `sync.Mutex` to `fakeIncidents` in `interactions_test.go` guarding its
   recorded-comment fields (whatever `AddCommentFromDiscord` /
   `AddCommentFromDiscordCommand` currently mutate unsynchronized).
2. Add locked getter methods on `fakeIncidents` for those fields instead of exposing
   them for direct reads.
3. Update the racy direct field reads in `gateway_test.go` (around lines 342-344, 425,
   441, 504) to go through the new getters, using `assert.Eventually` (or an
   equivalent poll/wait) where the read is waiting on the background goroutine to
   finish processing rather than asserting on already-settled state.

Verify with:
- `go test -race ./internal/integrations/discord/...` — must be clean, no data races.
- `make lint-back`
- `make test`

Follow the repo's git workflow in `CLAUDE.md` (batch branch rules — this branch is
currently `batch/2026-09-25`, so integrate there rather than switching branches;
commit conventions with the `Co-Authored-By` line) if a batch branch is in use.
