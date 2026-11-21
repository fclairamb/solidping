---
model: opus
effort: medium
---

# A page in a check's browser can send WebRTC traffic to internal addresses, around the egress proxy

## Problem

Found by the security scan of 2026-11-20 (finding AS1). Severity: low. Who can trigger it: any `user`-role member who can create or test-run a browser or JS browser check, or call a browser MCP probe. On SaaS, that is anyone who signs up. It is a gap left by the AR3 fix (#946, spec `specs/done/2026/11/2026-11-19-04-browser-egress-websockets.md`).

- Under an enforcing egress policy, the session's Chrome sends its traffic through the guarded per-session proxy (`server/internal/checkers/checkbrowser/egressproxy.go`). Its header comment promises that "a non-public destination is never contacted at all, whatever the protocol" (`egressproxy.go:28`).
- WebRTC does not go through an HTTP proxy. With Chrome's default WebRTC IP handling policy, ICE sends its STUN checks directly over UDP.
- The launch flags set only `--proxy-server`, `--proxy-bypass-list` and `--host-resolver-rules` (`checker.go:561-591`). The remote-CDP context gets only a proxy (`checker.go:656-672`). No WebRTC flag or policy is set anywhere in the repo, and the shipped sidecar launch (`docker-compose.yml:51-53`) has none either.
- The resolver rules don't help: a page can name an IP literal as a STUN server, or put IP-literal remote candidates in an SDP it sets itself, so no name is resolved.
- Chrome reports no CDP network event for this traffic, so `onEgressEvent` (`session.go:537`) never poisons the session and the egress Recorder logs no denial.

Impact:
- A page loaded by a check makes the node running Chrome send STUN packets to internal IP:port pairs (cloud metadata, RFC 1918 neighbours, the pod itself).
- The payload is a fixed STUN format (the page controls only the ICE username fragment and password). The page learns only which targets answer STUN, through the ICE state.
- So this is blind reconnaissance. It doesn't read internal data. But it breaks the guarantee AR3 set out to give, and the page can report what it learned in its DOM, which reaches the check result since nothing was refused.

## Proposal

1. Exec Chrome (`allocator`, `checker.go:576-591`): when a proxy is set, add `--force-webrtc-ip-handling-policy=disable_non_proxied_udp`. WebRTC then only uses TCP through the proxy, so it meets the guard. If that is not enough in practice (see To verify), use `--webrtc-ip-handling-policy` plus the flag that disables WebRTC entirely. A monitoring browser has no use for it.
2. All sessions, including the remote CDP context, which takes no per-check flags: install a `Page.addScriptToEvaluateOnNewDocument` script when the guard is enforcing. It deletes `RTCPeerConnection`, `webkitRTCPeerConnection` and `RTCDataChannel` from the window before any page script runs. It runs in every frame, including iframes.
3. Ship the same WebRTC policy flag in the sidecar launches: `docker-compose.yml:51-53`, the Kubernetes and Compose install docs (`web/docs/docs/installation/kubernetes.md`, `docker-compose.md`), and the Helm chart if it starts one.
4. Fix the `egressproxy.go:28` comment to list what the proxy covers.

## Tests

- `server/internal/checkers/checkbrowser/egress_live_test.go`, next to `TestBrowserWebSocketToAPrivateHostIsRefused` (`:90`): an enforcing session loads a page whose script creates an `RTCPeerConnection` with a STUN server on a loopback UDP listener the test opens. The test asserts the listener receives no packet within the ICE gathering timeout. It fails before the fix.
- Positive control: the same page with a non-enforcing guard makes the listener receive a STUN binding request. This proves the test can see the packet.
- Unit test of the allocator options: an enforcing session's exec options contain the WebRTC policy flag, and a non-enforcing one's don't.
- Unit test: an enforcing session installs the new-document script, and in the live test `typeof RTCPeerConnection` evaluates to `"undefined"` in the page and in an iframe.

## To verify

- Whether Chrome's ICE-TCP active candidates open TCP connections to remote passive candidates outside the proxy under the default policy. That would make this a TCP connect oracle, so Medium. The fix above covers it either way.
- Whether `disable_non_proxied_udp` also sends TURN-over-TCP through the `--proxy-server` proxy in the Chrome version the sidecar pins (`docker-compose.yml:29`).
- Whether a dedicated or shared worker can construct `RTCPeerConnection` (it shouldn't, but check it in the live test).
