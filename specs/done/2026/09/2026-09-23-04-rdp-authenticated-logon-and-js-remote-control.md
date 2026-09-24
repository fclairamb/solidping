---
model: opus
effort: high
---

# RDP checks stop at the handshake: no way to prove a user can log on, or to drive the desktop from a script

## Problem

`checkrdp` only does the pre-auth X.224 negotiation (MS-RDPBCGR §2.2.1.1/§2.2.1.2): no credentials, no session (`server/internal/checkers/checkrdp/checker.go:1-6`). It proves the listener is alive and which security protocol it picks. It says nothing about whether a user can actually log on. A Windows server with a full disk, a broken profile service, an expired licence server or a hung logon script still answers the handshake and reads green.

For web apps we already solved this with the `browser` check and the `browser` object in `js` scripts (`checkjs/browser.go:92`, `checkbrowser/session.go`). There is no equivalent for a Windows desktop or a RemoteApp.

## Proposal

Three phases. Phase 0 decides whether phases 1 and 2 happen with a pure-Go library or not at all.

### Phase 0: spike (throwaway, report in `wiki/`)

Pick the library by running a headless client against two real targets:

- one Windows Server (2019 or 2022) with NLA required;
- one xrdp box (Linux).

For each: connect, pass NLA (CredSSP + NTLMv2), wait for the desktop, decode the frame buffer and save a PNG, send one click and one Unicode keystroke, then end the session both ways: **log off** (the session is gone from `query session` on the server) and **disconnect** (the session stays, and the next connect reattaches to it).

The library must also accept an existing `net.Conn` (or a dialer) instead of dialing itself, so phase 2 can route it through the tunnel. Both are selection criteria: a library that can only disconnect, or only dial on its own, needs a patch or loses.

Candidates:

| Repo | License | Notes |
| --- | --- | --- |
| `github.com/rcarmo/go-rdp` | MIT | Active (Jul 2026), code under `pkg/`, RemoteFX/NSCodec/RLE, NLA "with limitations", mostly tested against xrdp. Codecs are decoded in WASM in its own frontend: check they are plain Go we can call server-side. |
| `github.com/nakagami/grdp` | GPL-3.0 | Active (Sep 2026), fork of `tomatome/grdp`. Its H.264 path needs FFmpeg (cgo): must not be linked. |
| `github.com/x90skysn3k/grdp` | GPL-3.0 | Has `CaptureLogonScreen` returning PNGs: good reference for the frame-buffer part. |

Constraints:

- **No cgo.** The single static binary is non-negotiable.
- **License.** SolidPing is AGPL-3.0, so GPL-3.0 is legally fine. MIT is preferred when the two are otherwise equal.
- **Codecs.** Advertise only bitmap (RLE / planar) capabilities, no RDPGFX, no RemoteFX, no H.264. The expectation is that Windows falls back to plain bitmap updates. This is unverified: the spike must confirm it on the Windows target.
- Vendoring or forking the chosen library is acceptable if upstream needs fixes.

Output: `wiki/research/rdp-go-client-spike.md` with the result per library × target, the chosen library, and any patches needed.

**Stop condition.** If no pure-Go library logs on reliably to the Windows target, stop after phase 0 and write that up. The fallback (Guacamole `guacd` sidecar, built on FreeRDP) breaks the single-binary model and needs its own decision. Do not start it from this spec.

### Phase 1: authenticated logon in `checkrdp`

Add optional fields to `checkrdp/config/config.go`, following the SSH checker's credential handling (`checkssh/config/config.go:27`):

- `username`, `password`, `domain` (optional);
- `screenshot` (bool): capture the desktop once the logon settles;
- `end_session`: `logoff` (default) or `disconnect`.

Behaviour:

- No credentials: exactly today's pre-auth check, unchanged.
- With credentials: after the handshake, run CredSSP/NLA, finish the connection sequence, wait until the screen is stable (no bitmap update for \~2 s, bounded by the timeout), optionally capture a PNG, then end the session per `end_session`. A failed log off is reported in the result details but does not turn an `up` run down: the logon itself succeeded.
- New failure states with distinct error codes: auth rejected, logon timed out, session disconnected by the server (licensing, policy, "another user is logged on").
- Screenshots go through the same `Diagnostics` path and size cap as browser screenshots (`checkjs/browser.go:439-460`, `checkbrowser.MaxScreenshotBytes`).
- `timeout` max goes up from 30 s for authenticated runs (a Windows logon with profile load routinely takes 10-30 s). Suggest 90 s max, 45 s default when credentials are set.
- When credentials are set, `RDPConfig` implements `checkerdef.MinPeriodHint`(`checkerdef/types.go:27`) and returns **15 minutes**. The pre-auth check keeps the global floor.
- The `rdp` type's labels (`checkerdef/types.go:380`) stay `labelSafe`. The UI should still say clearly that credentials turn it into a real interactive logon.

### Phase 2: `rdp` object in `js` scripts

Mirror the `browser` object (`checkjs/browser.go:92-310`):

```js
const s = rdp.connect({
  host: "rdp.acme.com",
  username: "svc-monitor",
  password: secrets.RDP_PASSWORD,
  width: 1280, height: 800,        // optional, default 1280x800
});
s.waitForStable({ quietMs: 2000 }); // no screen update for quietMs
s.click(640, 400);                  // also rightClick, doubleClick, move
s.type("hello");                    // Unicode input events (TS_UNICODE_KEYBOARD_EVENT)
s.key("enter");                     // named keys + combos: "ctrl+alt+end", "win+r"
s.pixel(10, 10);                    // -> { r, g, b }
s.regionHash(0, 0, 200, 50);        // stable hash of a rectangle
s.screenshot();                     // recorded like browser screenshots, last wins
s.logoff();                         // or s.disconnect(): leave the session running
```

- **Ending the session.** `logoff()` and `disconnect()` are both explicit. `close()`is kept as an alias for `logoff()` for parity with `browser`. A script that ends without calling either gets a log off, same as the check's default.

- **Tunnel.** `rdp.connect` dials through the `js` check's tunnel when one is configured, exactly like the other socket APIs in `checkjs/socket.go`.

- **Typing uses Unicode input events**, not scancodes, so the target's keyboard layout does not matter. `key()` uses scancodes for named keys and combos.

- **Session lifecycle** reuses the `checkbrowser` patterns: a process-wide semaphore (its own, e.g. `MaxConcurrentRDP = 4`, not shared with Chrome slots), a slot timeout that maps to a timeout verdict, infra-vs-target error classification (`checkbrowser/session.go:30,238`), and a forced close on script end or timeout. Factor the shared parts out of `checkbrowser` rather than copying them.

- **Minimum period.** Extend `JSConfig.MinPeriodHint` (`checkjs/config/config.go:190`) with an `rdp.connect(` regex, same heuristic and caveats as `browserOpenRE`, returning the 15-minute floor.

- **Assertions v1** are pixels only: `pixel`, `regionHash`, `waitForStable`, plus a `waitForChange(timeoutMs)`. Template matching and OCR come later (see below).

### Docs

- `web/docs/`: extend the RDP check page with the logon mode, and add an `rdp`section to the JS scripting reference next to `browser`.
- Both pages must carry the operational caveats below, in plain words.

### Operational caveats (must be in the docs and the UI help text)

Every authenticated run is a **real interactive Windows logon**:

- it loads a user profile and runs logon scripts / GPOs;
- it may consume an RDS client access licence;
- on a single-session server (or with "restrict to one session per user") it can **disconnect a real logged-in user**;
- it shows up in the Security event log (4624/4634) every run.

So: use a dedicated monitoring account, never a person's account, and keep the interval long. The 15-minute floor enforces the last part.

Authentication is NTLM through CredSSP only. Kerberos is not supported, so domains that disable NTLM will fail at NLA with a clear error.

## Testing

- Unit: config validation (credentials pairs, timeout bounds, `MinPeriodHint` both paths), JS regex heuristic, key-combo parsing, region hash stability.
- Integration: an xrdp container via testcontainers (`slowtests` build tag), covering logon, screenshot, click, type, auth failure, log off (no session left) vs disconnect (session reattached on the next run), and `rdp.connect` through a tunnel. Windows Server cannot run in CI: document a manual test procedure in the wiki spike page.
- Existing pre-auth `checkrdp` tests must pass unchanged.

## Out of scope

- OCR (`waitForText`): needs Tesseract (cgo or sidecar).
- Template image matching (`waitForImage`): candidate follow-up spec.
- RemoteApp-specific launch, clipboard, drive/audio redirection, H.264/GFX codecs.
- The Guacamole fallback.

## Decisions

- **Log off and disconnect are both offered**, in the check and in JS (see phases 1 and 2). Log off is the default. Disconnect leaves the session running on the server until its idle policy ends it, which is sometimes what you want (reconnect to the same session next run, faster checks).
- `rdp.connect` **goes through the** `js` **check's tunnel** like the other sockets.
- **The floor is 15 minutes** for every authenticated run, in the check and in JS.1s 21     
## Implementation Plan

- **Phase 0 (done)**: spike in `wiki/research/rdp-go-client-spike.md`;
  `nakagami/grdp` v0.9.11 chosen, vendored at `server/third_party/grdp` with
  `patch_solidping.go` (Unicode input, log-off PDU, exported PDU writer;
  cgo-only ffmpeg/AAC plugins deleted). Dialer injection was native — no patch.
- **Phase 1**:
  1. `checkrdp/config`: `username`/`password`/`domain` (pair-validated,
     `password` secret via `SecretFields`), `screenshot`, `end_session`
     (logoff default), authenticated timeout default 45s / max 90s (pre-auth
     unchanged 5s/30s), `AuthenticatedMinPeriod` = 15m via
     `checkerdef.MinPeriodHint`, distinct `failure_code`s (auth_rejected /
     logon_timed_out / session_disconnected).
  2. `checkrdp/session.go`: framebuffer + grdp session, settle wait (2s
     without bitmap updates, bounded by timeout), PNG capture, logoff vs
     disconnect, `MaxConcurrentRDP` = 4 semaphore with timeout verdict.
  3. `checkrdp/checker.go`: `executeAuthenticated` path — slot → dial (or
     caller conn for tunnel) → logon → settle → capture → session end;
     logoff/screenshot failures are output details, never a Down.
  4. JSON schemas regenerated (`go generate ./internal/checkers/schemas/...`).
  5. Unit tests: config pairs/bounds/MinPeriodHint both paths.
- **Phase 2**: `checkjs` `rdp` object — connect (tunnel dialer like
  socket.go), waitForStable/waitForChange, click/rightClick/doubleClick/move,
  type (Unicode), key (scancodes + combos), pixel, regionHash, screenshot
  (browser path, last wins), logoff/disconnect/close alias; own action
  budget; `JSConfig.MinPeriodHint` regex for `rdp.connect(`; 15-minute floor.
- **Docs**: `web/docs` RDP check page + JS scripting `rdp` section, with the
  operational caveats verbatim in plain words.
- **Integration (slowtests)**: xrdp testcontainers suite.
