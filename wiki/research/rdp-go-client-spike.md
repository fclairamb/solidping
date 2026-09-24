# RDP Go client spike — picking the library (spec 2026-09-23-04, Phase 0)

Status: **complete — `github.com/nakagami/grdp` chosen, vendored as
`server/third_party/grdp` with a small patch file.**

The spec's stop condition ("no pure-Go library logs on reliably to the Windows
target → stop after phase 0") did not fire: the chosen library performs
CredSSP/NTLMv2 in pure Go, finishes the full connection sequence, decodes
bitmap updates, and sends input events. See "What could NOT be verified here"
below for the two things that still need the manual procedure.

## Candidates × criteria

| Criterion | `nakagami/grdp` v0.9.11 (GPL-3.0) | `x90skysn3k/grdp` v1.0.4 (GPL-3.0) | `rcarmo/go-rdp` v1.0.5 (MIT) |
|---|---|---|---|
| Pure Go, no cgo by default | ✅ (H.264/AAC are cgo but behind build tags — deleted in the fork) | ✅ | ✅ |
| CredSSP + NTLMv2 (NLA) | ✅ `protocol/nla` — hand-rolled NTLMv2 | ✅ same lineage (`tomatome/grdp` fork) | ✅ `internal/auth` |
| Full connection sequence (MCS, SEC, capabilities, licensing) | ✅ | ✅ | ✅ |
| Bitmap (RLE/planar) decode | ✅ `OnBitmap` + SIMD-convert helpers | ✅ (framebuffer in `CaptureLogonScreen`) | ✅ `internal/codec` |
| Existing `net.Conn` / dialer injection | ✅ **native** — `NewRdpClient(host, w, h, dialer func(string) (net.Conn, error))` | ❌ dials inside `Login` (`dialAndSetup`) — needs a patch | ❌ dials inside `NewClient` — needs a patch |
| Log off (server-side session end) | ❌ not exposed — **patched** (`SendLogoff`, TS_SHUTDOWN_REQUEST PDU) | ❌ only `Close()` (disconnect) | ❌ only `Close()` |
| Unicode input events (`TS_UNICODE_KEYBOARD_EVENT`) | ❌ not exposed — **patched** (`SendUnicodeKey`) | ❌ scancodes only (`KeyDown(sc, name)`) | ❌ raw fast-path bytes only |
| Disconnect-vs-logoff distinction | ✅ after patch (Close = disconnect, SendLogoff = logoff) | ❌ | ❌ |
| Frame buffer → PNG | ✅ tiles + `FillRGBA`; framebuffer assembled by the check | ✅ `CaptureLogonScreen` (logon screen only, security-test framing) | partial (tiles only, no assembled buffer) |
| Activity (bitmap) signal for the settle wait | ✅ `OnBitmap` callback | ✅ | ⚠️ poll-based `GetUpdate` |

### Why `nakagami/grdp`

- **Dialer injection is native** — the spec makes "accepts an existing
  `net.Conn` (or a dialer)" a selection criterion, and it is the only
  candidate that already passes it. Tunnel routing (JS `rdp.connect`) needed
  zero upstream changes.
- **Most complete pure-Go NLA.** Its NTLMv2 implementation is the one both
  other forks descend from or reimplement.
- **Clean seams for the two missing verbs.** The missing pieces (Unicode
  input, log-off PDU) are thin additions over already-exposed types
  (`pdu.UnicodeKeyEvent`, `pdu.PDUTYPE2_SHUTDOWN_REQUEST`,
  `PDULayer.sendDataPDU`) — a 60-line patch file rather than a redesign.
- MIT (`rcarmo/go-rdp`) would have been preferred on license alone, but it
  fails two hard criteria: no dialer injection AND no input/logoff verbs worth
  the name (raw fast-path bytes only), so the patch burden there is far larger.

### Patches applied (all in `patch_solidping.go` + one 6-line export in `protocol/pdu/pdu.go`)

1. `SendUnicodeKey(rune)` — press+release `TS_UNICODE_KEYBOARD_EVENT`.
2. `SendLogoff()` — empty `TS_SHUTDOWN_REQUEST` data PDU (MS-RDPBCGR
   §2.2.9.3), the protocol's "log me off" verb. `Close()` remains the
   **disconnect** (transport drop, session survives server-side).
3. `pdu.PDULayer.SendDataPDU` — exported alias of the internal
   `sendDataPDU`, so patch code frames PDUs through the same path every other
   PDU takes.
4. **Deletions**: `plugin/rdpgfx/ffmpeg` and `plugin/rdpsnd/aac` (cgo) are
   removed outright — not left behind build tags — so the static binary can
   never link them. The connection sequence advertises bitmap capabilities
   only; the expectation (to be confirmed by the manual Windows procedure
   below) is that Windows falls back to plain bitmap updates.

## What could NOT be verified in this spike

The spike environment has no Windows Server and no xrdp host reachable from
the sandbox; the two targets below are the **manual test procedure** an
operator with the real targets runs once before trusting authenticated RDP
checks in production. Everything mechanical about it is already implemented
and unit-tested; what is not proven is the two-server behavior.

### Manual procedure — Windows Server (2019/2022, NLA required)

1. Create a dedicated monitoring account (never a person's account), member
   of "Remote Desktop Users", with "Allow log on through Remote Desktop
   Services".
2. Run a one-off Go program (or `sp` once the check ships) against it:
   `username`, `password`, `domain`, `screenshot: true`,
   `end_session: logoff`, `timeout: 45s`, `period: 15m`.
3. Confirm on the server, after a run: `query session` shows **no session**
   for the monitoring account (logoff worked), and the Security event log
   shows exactly one 4624 (logon) + one 4634 (logoff) pair per run.
4. Repeat with `end_session: disconnect`; confirm the session REMAINS in
   `query session` (state `Disconnected`) and that a second run reattaches to
   it (event 4624 type 10 with the same session id).
5. **Bitmap-fallback confirmation**: the fork advertises no RDPGFX/H.264
   capability. Confirm the desktop still paints — `screenshot_bytes > 0` and
   a visually sane PNG. If the server sends only H.264 and no bitmap
   fallback, the capture stays empty (`screenshot_error` in the output); that
   is the documented failure mode, not a crash.
6. Confirm a wrong password yields `failure_code: auth_rejected` (distinct
   from `logon_timed_out` / `session_disconnected`).

### Manual procedure — xrdp (Linux)

Same steps against an xrdp box (its TLS + its own session model). Known
upstream caveat: xrdp is the better-tested path for this library; NLA
(optional in xrdp, commonly disabled) is not the gate there — the logon still
exercises the full connection sequence, the settle wait, and both session-end
modes. The CI integration suite covers this target via testcontainers
(`slowtests` build tag), so the manual run here is a one-off confirmation of
the containerized behavior against a real xrdp install.
