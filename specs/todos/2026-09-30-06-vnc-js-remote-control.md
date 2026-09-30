---
model: sonnet
effort: medium
---

# Expose a `vnc` JS object for scripted VNC remote control, mirroring `rdp`

## Problem
`js` checks can drive an RDP desktop through the `rdp` global (`rdp.connect()` then click, type, key, pixel, regionHash, waitForStable, waitForChange, screenshot). Nothing equivalent exists for VNC, so a user cannot script a VNC-based flow (log into an app on a kiosk, check a pixel, capture the screen).

Depends on spec `2026-09-30-05` (the `checkvnc` package and its authenticated session).

## Proposal
1. In `server/internal/checkers/checkvnc/session.go`, add an exported `VNCSession` interface with the same method set as `checkrdp.RDPSession` (`server/internal/checkers/checkrdp/session.go:547`) where it applies: `WaitForStable`, `WaitForChange`, `Click`, `RightClick`, `DoubleClick`, `Move`, `Type`, `Key`, `Pixel`, `RegionHash`, `ScreenshotPNG`, `Closed`, and an end method. Add an `OpenVNCSession` test seam like `checkrdp.OpenRDPSession`.
2. Implement it on the real session:
   - keep the connection reading after the first frame: request incremental `FramebufferUpdate`s in a loop and paint into the frame buffer; stability and change detection work like RDP's bitmap timestamps (`checkrdp/session.go:329-360`, `:865`);
   - pointer: `PointerEvent` (button mask bits 1/2/3), double-click = two press/release pairs;
   - `Type`: one `KeyEvent` down/up per rune; Latin-1 runes map to their keysym, others to `0x01000000 + codepoint`;
   - `Key`: parse the same combo syntax as `parseKeyCombo` (`checkrdp/session.go:738`) but map names to X keysyms (Return, Escape, Tab, Control_L, Alt_L, Super_L, F1-F12, arrows...).
   - No logoff concept: the end method just closes the connection.
3. New `server/internal/checkers/checkjs/vnc.go`, modelled on `server/internal/checkers/checkjs/rdp.go`: `registerVNC()` exposing `vnc.connect({host, port, password, timeout})`, the one-session-per-execution rule, a shared action budget like `rdpActions`, failure mapping, and `closeVNC()` deferred like `closeRDP` (`server/internal/checkers/checkjs/checker.go:146`). Register it next to `registerRDP()` (`checker.go:349`) and add `"vnc"` next to `"browser", "rdp"` at `checker.go:500` if that list is the reserved-globals list.
4. Docs: a `vnc` section in `web/docs/docs/features/javascript-checks.md` next to the `rdp` one, and a CHANGELOG entry.

## Tests
- `checkjs/vnc_fake_test.go` + `checkjs/vnc_test.go`, against a fake `VNCSession` like `rdp_fake_test.go` / `rdp_test.go`:
  - connect + click + type + screenshot script succeeds and records the calls in order;
  - second `vnc.connect()` in one execution is refused;
  - action budget exhausted gives the budget error;
  - a script that throws still closes the session (fake records the close);
  - `vnc.connect` without host is refused.
- `checkvnc/session_test.go` (fake RFB server): `Type("é€")` sends keysyms `0x00e9` and `0x010020ac`; `Key("ctrl+alt+delete")` sends the three down events then the three up events in reverse order; `Click` sends mask 1 then mask 0 at the right coordinates; `WaitForChange` times out with the sentinel error when no update arrives.
- The `slowtests` live test from spec 05 gains a scripted case: type into an xterm and assert the region hash changes.

## To verify
- What `checker.go:500` (`"browser", "rdp"`) is used for, and whether `vnc` belongs there.
- Whether `JSConfig`'s `MinPeriodHint` (`checkerdef/types.go:17-30`) scans the script for `rdp.connect`; it needs no `vnc` equivalent if spec 05 decides VNC has no period floor.
