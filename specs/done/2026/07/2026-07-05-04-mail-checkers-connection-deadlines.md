# Mail checkers hang forever on silent sockets — set connection deadlines (imap/pop3/smtp)

## Problem

A `webingenia` IMAP check against `outlook.office365.com:993` (created
2026-07-04 23:13 UTC on the k8xp dev cluster) has been stuck in status
`created` since creation: not one result row was ever written. Diagnosis on
the live cluster showed it is much worse than one broken check — it is a
**fleet-killing hang**:

- The scheduler works fine: all three region jobs (`default`, `us-1`, `eu-2`)
  show `leaseStarts` 13–16 and derived state `crashLooping` in
  `GET /orgs/:org/check-jobs`. Workers claim the job every ~3.5 min
  (180s period + 30s lease slack), never submit a result, the lease expires,
  another runner claims it.
- Worker pods have **0 restarts** — nothing crashes. The runners *hang*: on
  `solidping-checks-eu2`, every one of the 25 runner goroutines' **last log
  line ever** is `Executing check job` for an outlook IMAP/POP3 check, and
  `Processing stats` reports `freeRunners=0`. The eu2 worker executes nothing
  at all anymore; `us-1` had 8 unrelated jobs 10+ min overdue and degrading.
- A sibling `SMTP: smtp.office365.com:587 starttls` check from the same org is
  healthy (`leaseStarts=0`) — control case.

Root-cause chain:

1. `checkimap.Execute` wraps the whole check in
   `context.WithTimeout(ctx, params.timeout)` (default 10s) —
   `server/internal/checkers/checkimap/checker.go:104`. DNS resolution
   (`LookupIPAddr`), TCP dial (`DialContext`) and TLS handshakes
   (`HandshakeContext`) honor that context.
2. **No code path ever calls `conn.SetDeadline`**. After the dial, all
   protocol I/O goes through `textproto.Conn` raw blocking reads/writes:
   greeting `ReadLine` (`checker.go:150`), STARTTLS response
   (`checker.go:305`), LOGIN response (`checker.go:280`). A context cannot
   interrupt a blocked socket read.
3. The check's config is `{host: outlook.office365.com, port: 993}` — implicit
   TLS **not** enabled on an implicit-TLS-only port. O365 accepts the TCP
   connection, waits silently for a ClientHello, never sends a plaintext
   greeting, and keeps the session alive (verified live: a raw `nc` to `:993`
   receives zero bytes; Go's default TCP keepalive is answered by the LB so
   the read never errors). The greeting `ReadLine` blocks **forever**.
4. The worker calls `checker.Execute(execCtx, ...)` synchronously
   (`server/internal/checkworker/worker.go:692`) with no outer guard
   (see companion spec `2026-07-05-05`), so each hung execution permanently
   consumes one of `poolSize` runner goroutines until the pool is empty.

`checkpop3` and `checksmtp` are copies of the same shape (textproto over a
raw conn, `WithTimeout` at the top, zero `SetDeadline` calls):
`checkpop3/checker.go` reads at lines 151/279/294/319, `checksmtp/checker.go`
reads after `textproto.NewConn` at line 147 and in EHLO/STARTTLS/AUTH flows.
The org's `POP3 :995` (no tls) check hangs identically. A survey of all
`checkers/check*` packages found only these three with raw line-protocol
reads and no deadline calls (`checkssh`, `checktcp`, `checkudp`,
`checkicmp`, `checkminecraft`, `checksip` already set deadlines; the rest
use context-aware client libraries).

## Current state (verified 2026-07-05 on v0.2.0; re-verify at build)

- `checkimap/checker.go`, `checkpop3/checker.go`, `checksmtp/checker.go`:
  no `SetDeadline`/`SetReadDeadline`/`SetWriteDeadline` anywhere.
- Timeout config: `defaultTimeout = 10s`, `maxTimeout = 60s` in each
  package's `config.go`; runner-side cost-aware exec ceiling clamps at 30s
  (`worker.go:687`).
- `timeoutResult()` helpers already exist in each checker and are returned
  when `ctx.Err() != nil` after an I/O error.

## Design decisions

### D1 — Propagate the context deadline to the socket, once, right after dial

In each of the three checkers' `dial()` (and nowhere else), after a
successful `DialContext`:

```go
if dl, ok := ctx.Deadline(); ok {
    _ = conn.SetDeadline(dl)
}
```

One call bounds **every** subsequent read and write on that conn for the
whole check lifetime: the deadline lives on the underlying `net.Conn`, so it
survives `tls.Client` wrapping (STARTTLS upgrade) and `textproto.NewConn`
wrapping. `Execute` already installs `WithTimeout` before dialing, so a
deadline is always present. No per-read juggling, no behavior change for
fast paths.

### D2 — Map deadline errors to the existing timeout result

A read that trips the conn deadline returns an error wrapping
`os.ErrDeadlineExceeded`, racing the context timer that expires at the same
instant — `ctx.Err()` may still be nil when the read error surfaces. Extend
the existing error handling so both signals mean timeout:

```go
if ctx.Err() != nil || errors.Is(err, os.ErrDeadlineExceeded) {
    return timeoutResult(start), nil
}
```

Apply at every `ReadLine`/`PrintfLine` error branch in the three checkers
(greeting, STARTTLS, EHLO/AUTH/LOGIN, USER/PASS). Result: a silent server
yields a clean `StatusTimeout` result within the configured timeout instead
of an eternal hang.

### D3 — No API/config surface change

`timeout`, `tls`, `starttls` keep their semantics. Fixing the *configuration*
trap (tls unset on 993/995) is spec `2026-07-05-06`; runner-level defense in
depth is spec `2026-07-05-05`. This spec only makes the checkers honor their
timeout.

## Implementation

1. `checkimap/checker.go` — deadline in `dial()`; `os.ErrDeadlineExceeded`
   branch in greeting/STARTTLS/LOGIN error paths.
2. `checkpop3/checker.go` — same (greeting, STARTTLS, USER, PASS paths).
3. `checksmtp/checker.go` — same (greeting, EHLO, STARTTLS, AUTH paths).
4. Tests per checker (table-driven, `t.Parallel()`, `require`):
   - **Silent listener**: local `net.Listener` that accepts and never writes,
     holding the conn open. `Execute` with `timeout: "1s"` must return within
     ~1.5s with `StatusTimeout` — this is the regression test for the o365
     hang; it fails (test times out) on current code.
   - **Mid-protocol silence**: listener sends a valid greeting then goes
     silent; LOGIN/USER/EHLO path must also time out, not hang.
   - Existing happy-path tests stay green (deadline far in the future).
5. Run `make lint` + backend tests.

## Ops follow-up (dev cluster, after deploy)

The three `solidping-dev` deployments (`solidping`,
`solidping-checks-us1`, `solidping-checks-eu2`) are carrying dozens of
permanently-stuck runner goroutines; the eu2 pool is fully drained. Rolling
out the fixed image restarts them anyway — verify afterwards that the
webingenia outlook checks produce `timeout`/`down` results and
`freeRunners` stays > 0. Independently, the checks themselves should get
`tls: true` (or be deleted) — they are misconfigured regardless of this fix.

## Out of scope

- Runner-level watchdog around `checker.Execute` — spec `2026-07-05-05`.
- Defaulting `tls` from well-known implicit-TLS ports — spec `2026-07-05-06`.
- Auditing context-awareness of library-based checkers (mysql, redis, …).

## Verification

- `make test` — new silent-listener tests fail before the fix, pass after.
- `make lint` clean.
- Manual: `nc -l 1993` locally, create an IMAP check
  `{host: localhost, port: 1993, timeout: "2s"}` → result rows with
  `timeout` status appear each period; worker `freeRunners` stable.

## Key files

- `server/internal/checkers/checkimap/checker.go` (dial ~line 327, reads 150/280/305)
- `server/internal/checkers/checkpop3/checker.go` (dial ~line 341, reads 151/279/294/319)
- `server/internal/checkers/checksmtp/checker.go` (dial ~line 428, reads from 147)
- `server/internal/checkers/check{imap,pop3,smtp}/checker_test.go`

## Risk log

- **Deadline vs slow-but-alive servers**: the deadline equals the existing
  context deadline, so nothing that previously succeeded within `timeout`
  changes behavior.
- **`PrintfLine` writes** also become bounded — intended; a wedged write path
  hung identically before.
- **Race between ctx timer and conn deadline** is absorbed by D2 (either
  signal → `StatusTimeout`); without D2 the same race would surface as
  `StatusDown` with an `i/o timeout` error string — misleading but harmless.

## Implementation Plan

1. **`checkimap/checker.go`**
   - In `dial()`, right after `dialer.DialContext` succeeds (before the
     implicit-TLS branch, so the deadline covers the TLS handshake too), add:
     `if dl, ok := ctx.Deadline(); ok { _ = conn.SetDeadline(dl) }`.
   - Extend every `ctx.Err() != nil` timeout check to also match
     `errors.Is(err, os.ErrDeadlineExceeded)`:
     - greeting `ReadLine` (~line 152)
     - STARTTLS branch, `c.doSTARTTLS` error (~line 209) — note the read
       happens inside `doSTARTTLS` (~line 305), error bubbles up to this
       existing `ctx.Err()` check at the call site
     - LOGIN branch, `c.doLogin` error (~line 238) — same shape, read is in
       `doLogin` (~line 280)
   - Add `"os"` to imports.
2. **`checkpop3/checker.go`** — identical shape:
   - `dial()` deadline.
   - greeting `ReadLine` (~line 153).
   - STARTTLS branch (~line 208), read inside `doSTARTTLS` (~line 319).
   - USER/PASS (auth) branch (~line 237), reads inside `doAuth` (~lines
     279/294 — both USER response and PASS response reads feed the same
     `err` returned to the one `ctx.Err()` check at the call site).
   - Add `"os"` to imports.
3. **`checksmtp/checker.go`** — identical shape, but `ReadResponse`/`Cmd`
   instead of `ReadLine`/`PrintfLine`:
   - `dial()` deadline.
   - greeting `ReadResponse(220)` (~line 154).
   - EHLO branch (~line 193), read inside `sendEHLO` (~line 349).
   - STARTTLS branch (~line 219), read inside `doSTARTTLS` (~line 407).
   - AUTH branch (~line 258), read inside `doAUTH` (~line 323).
   - Add `"os"` to imports.
4. **Tests** — `checkimap` and `checkpop3` currently have *no* test file at
   all (only `checksmtp/checker_test.go` exists); create
   `checkimap/checker_test.go` and `checkpop3/checker_test.go` following the
   `checksmtp` fake-server pattern (`net.ListenConfig` on `127.0.0.1:0`,
   `t.Cleanup` closing the listener, a `handleFake{IMAP,POP3}` goroutine per
   accepted conn). For all three packages add, alongside existing/new
   happy-path cases:
   - `TestXxxChecker_Execute_SilentListener` — listener accepts and never
     writes a byte; `Execute` with `Timeout: 1 * time.Second` must return
     `StatusTimeout` and the test itself must complete within ~2s (guarded
     with `context.WithTimeout` around the test or a `select`/timer so a
     regression hangs the test process visibly rather than the whole `go
     test` run silently).
   - `TestXxxChecker_Execute_MidProtocolSilence` — listener sends a valid
     greeting (and, for smtp, a valid EHLO response) then stops writing;
     exercise the LOGIN/USER-PASS/AUTH path (config has `Username` set so
     the post-greeting code path is reached) and assert `StatusTimeout`.
   - Both use `t.Parallel()` and `require`, matching the existing style.
5. Run `make fmt` after each checker file's edits; commit per-package
   (imap, pop3, smtp) since they are independent, self-contained diffs.
6. QA: `make build-backend lint-back test`, iterate until clean.
