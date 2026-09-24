# SolidPing patches to the vendored grdp fork

This directory is a vendored fork of `github.com/nakagami/grdp` v0.9.11
(GPL-3.0 — compatible with SolidPing's AGPL-3.0; the license file is kept at
`LICENSE`). The fork exists for three reasons:

1. **No cgo, ever.** The upstream ffmpeg H.264 decoder plugin
   (`plugin/rdpgfx/ffmpeg`) and the Darwin AAC decoder (`plugin/rdpsnd/aac`)
   are cgo. SolidPing ships a single static binary, so both are DELETED from
   this tree rather than left behind build tags. The bitmap codecs that remain
   are pure Go (RLE / planar / NSCodec / RemoteFX), which is exactly what the
   check needs: the connection sequence advertises bitmap capabilities only,
   and Windows falls back to plain bitmap updates when no RDPGFX/H.264
   capability is offered.

2. **Tunnel dialing.** `grdp.NewRdpClient` already accepts a
   `dialer func(string) (net.Conn, error)` instead of dialing itself — no
   patch needed. SolidPing's session hands it a one-shot dialer returning the
   caller's pre-dialed `net.Conn` (an SSH tunnel), which is what makes
   `rdp.connect` in JS scripts work through the tunnel (spec 2026-09-23-04).

3. **Session-end and input verbs SolidPing needs.** `grdp.RdpClient` did not
   expose Unicode typing or a log-off PDU. The patches live in
   `patch_solidping.go` (this fork's own file, clearly separated from
   upstream code):
   - `SendUnicodeKey(rune)` — TS_UNICODE_KEYBOARD_EVENT press+release, so
     `type()` works regardless of the target's keyboard layout;
   - `SendLogoff()` — the TS_SHUTDOWN_REQUEST data PDU, the protocol's
     "log me off" verb, which is what `end_session: logoff` sends;
   - `eventReady`-gated helpers for the check's stability wait.

Patches are confined to files this fork adds (`patch_solidping.go`) or are
one-line import-path rewrites. If you upgrade the fork, re-apply the patch
file on top — it touches no upstream function bodies.
