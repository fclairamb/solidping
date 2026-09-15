---
model: sonnet
effort: low
---

# The UDP heartbeat push listener answers an accepted SP1 beat with `OK` (2 bytes) instead of `OK\n` (3 bytes)

## Problem

An embedded device that beats over UDP and TCP gets two different answers for
the same accepted line. TCP answers `OK\n`
([listener.go:444](../../server/internal/heartbeatpush/listener.go:444)),
UDP answers a bare `OK`
([listener.go:268](../../server/internal/heartbeatpush/listener.go:268)).
Both come from the same `okReply` constant
([listener.go:38](../../server/internal/heartbeatpush/listener.go:38)); the
TCP path appends the newline by hand and the UDP path does not.

That asymmetry is a trap for firmware. A device that treats the reply as a
line (`readline()` on a socket, `nc -u` piped into a shell, a `strncmp` against
`"OK\n"`) works on TCP and silently fails on UDP, or hangs waiting for a
terminator that never comes. The protocol line the device *sends* is
newline-terminated on both transports, so the reply should be too.

The reply is version-agnostic: the UDP path writes `okReply` for SP1 and SP2
alike, so the fix covers both.

Everything that pins the current 2-byte shape:

- [listener.go:35-38](../../server/internal/heartbeatpush/listener.go:35) —
  the constant and its "Two bytes" comment.
- [listener.go:267-270](../../server/internal/heartbeatpush/listener.go:267) —
  the write and the `len(payload) >= len(okReply)` amplification guard.
- [config/heartbeat.go:103](../../server/internal/config/heartbeat.go:103) —
  the `UDPReplyOK` field doc says "sends a two-byte `OK`".
- [embedded-push.md:194-195](../../web/docs/docs/features/embedded-push.md:194)
  — the UDP section documents a bare `OK` while the TCP section documents
  `OK\n`.
- Tests asserting `"OK"` on UDP:
  [listener_test.go:110](../../server/internal/heartbeatpush/listener_test.go:110),
  [push_wire_test.go:76](../../server/internal/handlers/heartbeat/push_wire_test.go:76),
  [push_wire_test.go:95](../../server/internal/handlers/heartbeat/push_wire_test.go:95),
  [push_wire_test.go:116](../../server/internal/handlers/heartbeat/push_wire_test.go:116),
  [push_wire_test.go:176](../../server/internal/handlers/heartbeat/push_wire_test.go:176).

## Proposal

Make the accepted-beat reply `OK\n` on both transports, from one place.

- Change the constant to `okReply = "OK\n"` and drop the `+"\n"` at the TCP
  write site, so the two paths cannot drift again. Update the comment: three
  bytes, still strictly shorter than any valid beat (the shortest SP1 line is
  `SP1 a/b <token>`, dozens of bytes).
- Keep the amplification guard exactly as written — `len(payload) >=
  len(okReply)` — it becomes `>= 3` automatically. Keep the
  failure-is-silence rule untouched: still no bytes at all on any rejection.
- Update the `UDPReplyOK` field doc in `config/heartbeat.go` ("two-byte" →
  "`OK\n`", three bytes).
- Update `embedded-push.md`: the UDP section says `OK\n`, matching the TCP
  section. Mention in the same sentence that it is 3 bytes and never longer
  than the datagram received.
- Tests: flip the five UDP assertions above to `"OK\n"`. Keep the
  `r.Less(len(reply), len(validLine))` amplification assertion in
  `TestUDPRepliesOKOnlyWhenAccepted`; it must still pass with the longer
  reply. Add one explicit assertion that the UDP reply is byte-identical to
  the TCP reply for the same accepted line, so the symmetry is the tested
  property rather than a coincidence of two constants.
- CHANGELOG entry under the user-facing section: UDP heartbeat replies are
  now `OK\n`, same as TCP. Call it out as a wire change so anyone matching on
  exactly two bytes knows to loosen the comparison.

Not in scope: any change to what is sent on failure (silence stays silence),
to `SP_HEARTBEAT_UDP_REPLY_OK` semantics, or to the SP1/SP2 request format.
