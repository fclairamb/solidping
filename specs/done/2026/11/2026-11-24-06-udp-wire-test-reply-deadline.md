---
model: haiku
effort: low
pr: unix-socket-checks
automerge: false
---

# UDP heartbeat wire test: positive controls time out under load

## Problem

`server/internal/handlers/heartbeat/push_wire_test.go` reads every UDP reply with a 400 ms deadline
(`push_wire_test.go:55`). The positive controls, which expect `OK\n`, failed when the whole suite
ran under load (batch gate of PR #1003, closed, so the fix never reached `main`). 400 ms is right
for the no-reply assertions (a short silence is the expected outcome) but too tight when a reply is
expected.

## Proposal

In `push_wire_test.go`, as in commit `e85608bc` of branch `feat/todos-2026-11-23-2`:

1. Split `sendUDP` into `sendUDPWithin(t, payload, wait)`, keep `sendUDP` at 400 ms for the
   no-reply assertions, and add `sendUDPExpectReply` at 5 s.
2. Use `sendUDPExpectReply` for every call that asserts `OK\n`: `TestWireUDPAcceptsAndRecords`, the two
   positive controls of `TestWireUDPFailuresAreIndistinguishable`, and the positive control of
   `TestWireSP1RejectedOnRequireHMACCheck`.

## Tests

- `go test ./internal/handlers/heartbeat/ -run Wire -count=20` passes, including while
  `make test` runs in parallel to load the machine.
- The no-reply cases still use 400 ms, so the suite does not get slower when they pass.
