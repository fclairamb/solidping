---
model: opus
effort: high
---

# TCP and UDP checks can neither send a real payload nor wait for a reply matching a pattern

## Problem

A port probe that only completes a handshake proves a firewall forwards the
port, not that the service behind it works. The way to prove the service is to
**send it something and wait for the answer you expect** — an SMTP `EHLO` and
a `250`, a Redis `PING` and `+PONG`, a DNS query and an answer carrying your
transaction ID. The request is that the `tcp` and `udp` checks support exactly
that: a configurable payload, and a wait for a reply matching a pattern.

The backend has had a half of this since the original TCP spec, and it is
invisible, substring-only, single-read and text-only. Each of those is a
separate gap.

**1. The feature exists in the backend and nowhere else.** Both configs carry
`send_data` / `expect_data`
([checktcp/config.go:16-17](server/internal/checkers/checktcp/config.go:16),
[checkudp/config.go:14-15](server/internal/checkers/checkudp/config.go:14)).
But:

- the dashboard form for both types renders **host and port only** — the
  shared `tcpModule` owns `["host", "port"]` and nothing else
  ([network.tsx:26-40](web/dash0/src/components/checks/form/types/network.tsx:26)).
  A payload set through the API survives a dashboard save thanks to the
  unmodeled-key passthrough (spec 2026-09-11-01), but no user can set or see one;
- the published docs list neither key for TCP
  ([check-types.md:95-101](web/docs/docs/features/check-types.md:95)) nor for
  UDP ([check-types.md:112-116](web/docs/docs/features/check-types.md:112));
  the only documentation is the internal wiki
  ([checker-config.md:112-113](wiki/conventions/checker-config.md:112));
- no sample uses them. The two UDP samples — `8.8.8.8:53` and
  `pool.ntp.org:123` ([samples.go:16-35](server/internal/checkers/checkudp/samples.go:16))
  — send nothing, and a UDP dial that sends nothing cannot fail short of an
  ICMP port-unreachable. The original UDP spec says so itself and calls
  `send_data`/`expect_data` the "best practice" for reliable monitoring
  ([2026-03-22-udp-monitoring.md:120-121](specs/done/2026/03/2026-03-22-udp-monitoring.md:120)),
  then ships samples that ignore it. Our own catalog teaches users the
  tautological form of the check.

From the product's point of view the feature does not exist, which is why it
is being asked for.

**2. `expect_data` is a substring, not a pattern.** Both checkers match with
`strings.Contains`
([checktcp/checker.go:352](server/internal/checkers/checktcp/checker.go:352),
[checkudp/checker.go:222](server/internal/checkers/checkudp/checker.go:222)).
Every sibling checker that inspects a reply takes a regex: the WebSocket
`expect` ([checkwebsocket/config.go:26](server/internal/checkers/checkwebsocket/config.go:26)),
the SSH `expected_output_pattern`
([checkssh/config.go:181-187](server/internal/checkers/checkssh/config.go:181)),
the HTTP `bodyPattern`. `220` matches a banner, but `^220 .* ESMTP` or
`^HTTP/1\.[01] (200|301)` cannot be expressed.

**3. One `Read` is not "wait for a reply".** Each checker calls `conn.Read`
exactly once ([checktcp/checker.go:323](server/internal/checkers/checktcp/checker.go:323),
[checkudp/checker.go:202](server/internal/checkers/checkudp/checker.go:202))
and matches whatever that single call returned. Over TCP that is one segment:
a server that writes its banner in two `write()`s, a reply that straddles a
segment boundary, or a greeting that arrives *before* the answer to the
payload all fail to match, and they fail nondeterministically — the same
check flaps with the network. The WebSocket checker already does this right,
reading frames until the pattern matches or a cap is hit
([checkwebsocket/checker.go:211-232](server/internal/checkers/checkwebsocket/checker.go:211)).

**4. The match runs against the truncated copy.** Both checkers read up to
4 KB, then cut the reply to 1 KB *for the output field* and match the
expectation against that cut copy
([checktcp/checker.go:341-352](server/internal/checkers/checktcp/checker.go:341),
[checkudp/checker.go:214-222](server/internal/checkers/checkudp/checker.go:214)).
An expected string that arrives after byte 1024 never matches. This is a
plain bug.

**5. Payloads are text-only.** The payload goes out as `[]byte(cfg.SendData)`
([checktcp/checker.go:289](server/internal/checkers/checktcp/checker.go:289)).
A DNS query, an NTP request, an SNMP GET, an RCON packet — the protocols that
actually run over UDP — need arbitrary bytes, including NUL. The original TCP
spec called for hex encoding
([2025-12-14-tcp-check.md:106](specs/done/2025/12/2025-12-14-tcp-check.md:106))
and listed base64 as future work
([:184](specs/done/2025/12/2025-12-14-tcp-check.md:184)); neither shipped.
The same problem hits the dashboard in the other direction: a `<textarea>`
cannot produce a carriage return, so even a text protocol that wants
`EHLO x\r\n` cannot be configured from the form.

**6. A binary reply is unreadable.** `received_data` is the raw bytes cast to
a string; `encoding/json` replaces every invalid UTF-8 sequence with U+FFFD,
so the operator sees `�����` where the diagnostic bytes were.

**7. Three timeouts hide in one.** `timeout` bounds the dial via
`ctxWithTimeout`, then the write deadline is set to `now + timeout` again
([checktcp/checker.go:279](server/internal/checkers/checktcp/checker.go:279)),
then the read deadline to `now + timeout` a third time
([:311](server/internal/checkers/checktcp/checker.go:311)). A check configured
with `timeout: 10s` can legitimately run for close to 30 s, and `conn.Read`
is not context-aware, so the overall context expiring does not interrupt it.

**8. The UDP checker has no tests at all.** `checkudp/` holds `checker.go`,
`config.go` and `samples.go` — there is no `_test.go`, and the only
references from tests are the shared IP-version suites. The TCP checker has
one send/expect case, against an `httptest` server whose whole reply fits in
one segment ([checker_test.go:425-440](server/internal/checkers/checktcp/checker_test.go:425)),
which is exactly the case that hides gaps 3 and 4.

## Proposal

Make the send/expect half of the `tcp` and `udp` checks real, and the same on
both: encoded payloads, a regex reply pattern, a read-until-match loop under a
single deadline, and a dashboard form, docs and samples that show it.

### Config

Existing keys keep their names and meaning; the additions are optional and
default to today's behaviour, so every stored check is unaffected.

| Key | Type | Default | Meaning |
|---|---|---|---|
| `send_data` | string | — | Payload to send after connect (and after the TLS handshake for `tcps`). Unchanged. |
| `send_encoding` | `text` \| `escaped` \| `hex` | `text` | How `send_data` is decoded into bytes. `text`: byte-for-byte (today). `escaped`: C-style escapes — `\r`, `\n`, `\t`, `\0`, `\\`, `\xNN` — the Nagios `check_tcp --escape` convention, and what a `<textarea>` needs to express CRLF. `hex`: hex digits, whitespace ignored, odd length rejected. |
| `expect_data` | string | — | Substring the reply must contain. Unchanged. |
| `expect_encoding` | `text` \| `escaped` \| `hex` | `text` | Same decoder, applied to `expect_data`, so a binary reply can be asserted with a hex substring (`53508180`). |
| `expect_pattern` | string | — | RE2 regex the reply must match. Compiled in `Validate()` like the SSH pattern; an invalid one is a `VALIDATION_ERROR` on save, never a runtime error. |
| `timeout` | duration | `5s` | Now bounds the **whole** exchange: dial, TLS, write and the wait for the reply, together. |

`expect_data` and `expect_pattern` may both be set; both must hold. Either
without `send_data` stays allowed (banner protocols speak first). The regex
runs over the reply bytes with `regexp.Match`; document that a raw byte above
`0x7F` is not valid UTF-8 and must be asserted with a hex `expect_data`, not
with `\xNN` in the pattern.

One decoder, one place: `checkerdef` gets `DecodePayload(s, encoding string)
([]byte, error)` and the matcher, and both checkers use them. Their
send/read/match blocks are near-identical today and should become one call.

### Behaviour

After the payload is written (or immediately, if there is none but an
expectation is set), the checker **reads until one of**:

1. the accumulated reply satisfies every expectation set → `Up`. The output
   reports `bytes_received` (total), `received_data` (see below) and
   `read_count` (segments or datagrams it took);
2. the accumulated reply reaches 4 KB without matching → `Down`,
   `error: "no match in the first 4096 bytes of the reply"`;
3. the peer closes (`io.EOF`) without matching → `Down`,
   `error: "connection closed after N bytes without a matching reply"`;
4. the deadline passes → `Timeout`, `error: "no matching reply within
   <timeout> (N bytes received)"`. Today this is reported as `Down` with a
   generic read error; a silent service behind an open port is the one thing
   this check is for, and it deserves its own status.

`send_data` without any expectation keeps today's semantics: one best-effort
read for diagnostics, no failure on silence.

The read deadline is the context deadline (`SetReadDeadline(deadline)`), not
`now + timeout`. So is the write deadline. That closes gap 7 without a new
knob.

Over UDP each `Read` is one datagram; datagrams are appended to the same
buffer and matched the same way, so a multi-datagram reply works and the code
path is shared.

`received_data`: unchanged when the reply is valid UTF-8; otherwise the
`\xNN`-escaped form of the (still 1 KB-capped) reply. Matching always runs
on the full buffer, never the capped copy — that is the fix for gap 4.

### Dashboard

`tcpModule` ([network.tsx](web/dash0/src/components/checks/form/types/network.tsx))
gains, below host/port, a collapsible **Payload & reply** section (the
WebSocket form's `send` / `expect` pair in
[web.tsx:18-35](web/dash0/src/components/checks/form/types/web.tsx:18) is the
precedent, extended with the encodings):

- **Send** — `Textarea`, with an encoding `Select` (`Text` / `Escaped` /
  `Hex`). New checks default to **Escaped** so `\r\n` can be typed; a stored
  check loads with whatever `send_encoding` it has (absent → Text).
- **Expect** — one input plus a `Select` for how it is read: `Contains`
  (→ `expect_data`, with the same encoding select) or `Matches regex`
  (→ `expect_pattern`). Both keys enter `ownedKeys` so clearing the input
  really deletes them (the omit-to-clear contract in
  [common.ts](web/dash0/src/components/checks/form/types/common.ts)).
- Help text under the section, one line: *"Set an expectation to prove the
  service answers, not just that the port is open."* — for UDP add that
  without one the check can only detect port-unreachable.

Every primitive needed (`Textarea`, `Select`, collapsible section) is in the
design reference; nothing new to add there. Locale keys go into all four of
`de`/`en`/`es`/`fr` `checks.json` — `locale-parity.test.ts` fails otherwise.

### Samples

Replace the tautological UDP samples with ones that assert an answer, and add
one TCP sample that speaks:

- **UDP · Google DNS (53)** — `send_encoding: hex`, a query for
  `solidping.io A` with transaction ID `0x5350`:
  `5350 0100 0001 0000 0000 0000 09 736f6c696470696e67 02 696f 00 0001 0001`;
  `expect_encoding: hex`, `expect_data: 53508180` (our ID echoed, QR/RD/RA
  set, RCODE 0).
- **UDP · NTP pool (123)** — `send_encoding: hex`, the 48-byte client request
  `1b` + 47 × `00`; `expect_pattern: ^[\x1c\x24]` (mode 4 server reply,
  version 3 or 4). Verify the exact expectation against a live pool server in
  the `slowtests` layer before shipping it — leap-indicator bits change the
  first byte.
- **TCP · solidping.io HTTPS** — existing demo host, `tls: true`,
  `send_encoding: escaped`, `send_data: HEAD / HTTP/1.0\r\nHost: solidping.io\r\n\r\n`,
  `expect_pattern: ^HTTP/1\.[01] (200|301|302)`. Demo catalog rule from spec
  2026-09-06-02 still holds: solidping-owned hosts only.

### Docs

- `check-types.md` TCP and UDP tables gain the four options with one example
  each, plus a short "Send a payload and wait for a reply" paragraph shared
  by both sections (Redis `PING`/`+PONG` for TCP, the DNS hex query for UDP),
  and the UDP caveat that a check with no expectation only detects
  port-unreachable.
- `wiki/conventions/checker-config.md` tables updated.
- `CHANGELOG.md` entry under Unreleased.

### Tests

Backend, table-driven, all against local listeners (`127.0.0.1:0`) so they
run in `make test`:

- `checkerdef`: `DecodePayload` for the three encodings — escapes incl.
  `\x00` and `\\`, hex with whitespace, odd-length hex and a bad escape
  rejected; `expect_pattern` that does not compile is refused by `Validate`
  for both checkers.
- `checktcp`: a banner written in **two `write()`s 100 ms apart** matches
  (gap 3); a match whose expected bytes sit past offset 1024 of a 3 KB reply
  succeeds (gap 4, with the reverse as the positive control: today's code
  must fail it); a listener that accepts and never writes → `Timeout` within
  `timeout` ± 200 ms, and the whole check (dial + write + read) with a
  2-second timeout finishes under 3 s (gap 7); peer closes without match →
  `Down` with the EOF message; 5 KB of non-matching data → `Down` with the
  4096-byte message; a `\xff\xfe` reply renders as escaped `received_data`;
  hex `expect_data` against a binary reply.
- `checkudp` — a new `checker_test.go`: a UDP echo listener answering a hex
  payload with a binary reply matched by hex `expect_data` and by
  `expect_pattern`; a reply split across two datagrams; a listener that never
  answers → `Timeout`; the DNS sample's exact bytes decode to a well-formed
  query (unpack it with `github.com/miekg/dns`, already in `go.mod`, or check
  the header fields by hand).
- `slowtests`: each new sample executes `Up` against its real target.

Dashboard:

- `bun run test:unit`: `tcpModule.fromConfig`/`toConfig` round-trip all six
  keys; a cleared expect input omits both `expect_*` keys.
- Playwright (`web/dash0/e2e/checks.spec.ts`, alongside the RDP/IMAP form
  round-trips at [:682](web/dash0/e2e/checks.spec.ts:682)): create a TCP
  check with an escaped payload and a regex expectation, reopen it, assert
  every field round-trips; switch the expect mode and assert the other key
  is gone from the saved config.

### Decisions

- **Keep `expect_data` and add `expect_pattern`** rather than reinterpreting
  `expect_data` as a regex: stored checks with a `.` or `+` in their
  expectation must not silently change meaning.
- **Encodings are explicit, `text` stays the default**: an API client already
  sends real CR/LF through JSON escapes, and a stored `send_data` with a
  literal backslash must keep sending that backslash. Only the *dashboard*
  defaults new checks to `escaped`.
- **Hex, not base64, for binary**: hex is what the original spec asked for,
  is human-checkable in a config diff, and payloads are tens of bytes.
  Base64 stays deferred.
- **Silence is `Timeout`, not `Down`**: "port open, service mute" is a
  distinct failure and the status set already has the word for it.
- **4 KB reply cap stays**: this is a liveness probe, not a body fetch; the
  HTTP check is the tool for large responses.
- **Out of scope**: multi-step conversations (send, expect, send again),
  STARTTLS, and a reply-pattern *reject* (the `bodyPatternReject`
  counterpart). All three are natural follow-ups once the single exchange is
  solid.

## Implementation Plan

1. **`checkerdef` — one decoder, one matcher, one exchange** (new
   `payload.go` + `exchange.go`, new `payload_test.go`):
   - `DecodePayload(s, encoding string) ([]byte, error)` for `text` (default,
     byte-for-byte), `escaped` (`\r \n \t \0 \\ \xNN`, unknown escape and a
     trailing lone `\` rejected) and `hex` (whitespace ignored, odd length and
     non-hex digits rejected). Any other encoding name is rejected.
   - `RenderReplyData(b []byte) string` — 1 KB cap, raw when the capped copy is
     valid UTF-8, `\xNN`-escaped otherwise.
   - `Exchange` (decoded `Send`, `ExpectData`, compiled `ExpectPattern`,
     `Timeout`, `Deadline`) with `HasExpectation()`, `Matches(buf)` (both
     expectations must hold, matched on the FULL buffer) and
     `Run(conn, metrics, output) *Result` — the write + read-until-match loop
     under the single context deadline, returning `nil` on success or the
     failure `Result` verbatim.
   - `ValidateExchangeConfig(...)` returns the `VALIDATION_ERROR`-shaped
     `ConfigError` for a bad encoding, a bad payload or an uncompilable
     `expect_pattern`; both checkers call it from `Validate()`.
2. **`checktcp`** — config gains `send_encoding`, `expect_encoding`,
   `expect_pattern` (FromMap/GetConfig/Validate); `connect()` drops its three
   independent deadlines and its single `conn.Read` in favour of
   `Exchange.Run` under the context deadline.
3. **`checkudp`** — same config additions, same `Exchange.Run` call, so the
   datagram path and the segment path share one implementation.
4. **Samples** — UDP Google DNS sends a hex `solidping.io A` query
   (ID `0x5350`) and expects hex `53508180`; UDP NTP sends the 48-byte client
   request and expects `^[\x1c\x24]`; TCP gains a speaking `solidping.io`
   HTTPS sample (escaped `HEAD / HTTP/1.0`, `^HTTP/1\.[01] (200|301|302)`),
   also applied to the demo catalog entry.
5. **Dashboard** — `tcpModule` gains a collapsible "Payload & reply" section
   (Send textarea + encoding select; Expect input + `Contains`/`Matches regex`
   mode select + encoding select), the six new keys in `ownedKeys`, and locale
   keys in `de`/`en`/`es`/`fr`.
6. **Docs** — `check-types.md` TCP and UDP tables + a shared "Send a payload
   and wait for a reply" section; `wiki/conventions/checker-config.md` tables;
   `CHANGELOG.md` Unreleased entry.
7. **Tests** — `checkerdef/payload_test.go`; `checktcp/checker_test.go` grows
   the split-write, past-1 KB (with the reverse as positive control), silent
   listener (`Timeout`, whole check under 3 s), EOF, 5 KB-no-match and
   binary-reply cases; a brand-new `checkudp/checker_test.go`; a `slowtests`
   `samples_live_test.go` per checker; `network.test.ts` for the form module
   round-trip; a Playwright round-trip in `web/dash0/e2e/checks.spec.ts`.
