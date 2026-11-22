---
model: opus
effort: high
---

# A popup opened by a check's page escapes the WebRTC removal script and the egress watch

## Problem
Found by the security scan of 2026-11-22 (finding AU3). Severity: Low. Who can trigger it: a `user`-role member writing a browser or JS check, or the checked page itself. It is a gap in the AS1 fix (79bdc829, spec `specs/done/2026/11/2026-11-20-01-browser-egress-webrtc.md`).

Two of the three layers of that fix are bound to the session's own page target:
- `disableWebRTC` installs `disableWebRTCScript` with `Page.addScriptToEvaluateOnNewDocument` on the session's target only (`server/internal/checkers/checkbrowser/session.go:960-985`).
- `watchEgress` listens with `chromedp.ListenTarget(s.browserCtx, …)`, which only sees that target's events (`session.go:552-556`).

A page opened with `window.open` (or by clicking a `target=_blank` link, which a JS check can do) is a separate top-level CDP target. It gets a fresh document without the script, and its responses, redirects and WebSockets never reach `onEgressEvent`. Nothing in `checkbrowser` handles new or attached targets.

What still contains it:
- On the exec path, the WebRTC policy flags, `--proxy-server` and the deny-by-default `--host-resolver-rules` are process-wide (`server/internal/checkers/checkbrowser/checker.go:603-623`).
- On a remote sidecar, the popup inherits the isolated browser context's proxy (`checker.go:687-707`).

The residual: a remote Chrome whose launch flags do not carry `--force-webrtc-ip-handling-policy=disable_non_proxied_udp`. That is every sidecar deployed before 79bdc829 whose manifest was not updated: the flags live in the operator's own manifests (`web/docs/docs/installation/kubernetes.md:381-384`). On such a Chrome:
- The removal script is the only WebRTC defence, and a popup's `RTCPeerConnection` sends ICE/STUN UDP to any internal address.
- No refusal is recorded and the result stays clean. It is the blind internal probe AS1 closed.

The docs promise more than the code does: "every page, iframes included, loses `RTCPeerConnection`" (`web/docs/docs/configuration/egress-policy.md:191-193`).

## Proposal
1. In the session setup (`session.go`, where `disableWebRTC` and `watchEgress` are wired), on an enforcing session, watch `Target.targetCreated` for the session's browser context. Close at once any page target that is not the session's own (`target.CloseTarget`), and record a refusal that poisons the session like an egress denial.
   - Simplest and safest: a check never needs a second tab.
   - If closing breaks a supported flow, the alternative is `Target.setAutoAttach` (with `waitForDebuggerOnStart`) plus, on `attachedToTarget`, installing `disableWebRTCScript` and routing the new session's network events into `onEgressEvent` before resuming it.
2. Make the same choice for the JS runtime's browser API (`server/internal/checkers/checkjs/browser.go`), which shares the session.
3. Fix the docs (`egress-policy.md:191-200`): popups are closed (or instrumented). State that the sidecar flags are required, not optional, for WebRTC containment on a remote Chrome.

## Tests
- `server/internal/checkers/checkbrowser/webrtc_live_test.go` (live, existing layer):
  - A page that opens a same-origin popup (`window.open` after a click step), in which `RTCPeerConnection` is used against the existing loopback STUN listener.
  - Run it with the sidecar-style flags off, to model an un-updated remote Chrome.
  - Expect no STUN packet, and a session that is poisoned or reports the popup closed. It fails before the fix: the listener receives a packet.
- Positive control, same file: the existing top-frame, iframe and worker cases still pass, and a check that never opens a popup gives a normal result.
- Unit (`webrtc_test.go` or a fake-CDP test): a `targetCreated` event for a foreign page in the session's browser context leads to `CloseTarget` and a refusal. A target in another browser context is ignored.

## To verify
- Whether headless-shell's popup blocker stops `window.open` without a user gesture on the remote path (a click step gives one, so the gesture path must be covered anyway).
- Whether Chromium still gathers ICE-TCP candidates under `disable_non_proxied_udp`, which the AS1 spec left open. If it does, extend the live test with a TCP listener.
