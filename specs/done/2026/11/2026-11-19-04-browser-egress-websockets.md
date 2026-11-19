---
model: opus
effort: high
---

# The browser egress watch never sees WebSocket connections, so a page can read an internal WebSocket service

## Problem

Found by the security scan of 2026-11-19 (finding AR3). Severity: medium. Who can trigger it: any `user`-role member who can create or test-run a browser or JS check. On SaaS, that is anyone who signs up. It also concerns AI authoring and MCP probe runs that open a browser.

This is a variant of the still-open T3 (scan of 2026-10-13): browser checks send requests to internal hosts and only the reads are blocked. T3 was rated as blind: requests reach internal services, but the session is poisoned once Chrome reports a response, so no data comes back. WebSockets take away that last guarantee.

- `watchEgress` (`server/internal/checkers/checkbrowser/session.go:479-501`) listens only for `network.EventResponseReceived` and the `RedirectResponse` of `network.EventRequestWillBeSent`.
- A WebSocket handshake produces `Network.webSocketCreated`, `webSocketWillSendHandshakeRequest` and `webSocketHandshakeResponseReceived`. None of them reach `checkRemote` (`session.go:505-521`), so `egressDenied` is never set for a WebSocket.
- Page JavaScript has no same-origin barrier on WebSockets (no CORS), so a page the check navigates to can open a socket to an internal host and read its frames. The navigation pin (`--host-resolver-rules`, `checker.go:575`) covers the navigation host only. The remote-CDP sidecar path has no pin at all.
- The frames can then reach:
  - the check's own result (`page.text()`, `page.title()`, `browser.evaluate()`), because the session is never poisoned;
  - or the page's own origin, which is public and allowed.

Impact: a read SSRF against internal WebSocket services, seen from the network of the node running Chrome. That is the checks worker or its sidecar, and for test runs and MCP probes the API node. Examples: cluster-internal dashboards, message brokers' WebSocket ports, dev servers.

I couldn't build a chain to the Chrome DevTools socket itself, even though the documented sidecar runs with `--remote-allow-origins=*` (`web/docs/docs/installation/kubernetes.md:372-380`, `docker-compose.md:212-220`): the socket path needs the browser's unguessable id, and Chrome refuses DevTools HTTP requests whose Host is not an IP or localhost. Consider this under To verify.

## Proposal

The fix must stop the connection, not just observe it. That also closes T3.

1. Deny by default at name resolution, for both launch modes:
   - local launch: extend the `--host-resolver-rules` pin (`session.go:405`, `checker.go:575`) to `MAP * ~NOTFOUND` with an `EXCLUDE` for the pinned navigation host. Only hosts the guard has resolved and approved can be reached.
   - Remote CDP: flags can't be set per session there. Use one of:
     - an egress proxy the server runs (a local guarded HTTP/CONNECT proxy, with the sidecar started with `--proxy-server` pointing at it), which carries WebSocket upgrades through the same guard;
     - or refuse internal targets via `Fetch.enable` + `Fetch.requestPaused` for HTTP(S), plus closing any WebSocket whose `webSocketCreated` URL resolves to a non-public address. Fetch interception does not pause WebSocket handshakes, so the proxy is preferred.
2. Meanwhile (or in addition), extend `watchEgress` so a WebSocket at least poisons the session:
   - handle `network.EventWebSocketCreated` (URL) and `network.EventWebSocketHandshakeResponseReceived`;
   - resolve the URL host and run it through `guard.CheckAddr`, as `checkRemote` does;
   - set `egressDenied` and record the denial on the execution's recorder.
3. Update the documented sidecar setup (`kubernetes.md`, `docker-compose.md`) if step 1 needs a proxy flag. Keep the CDP port on localhost only.
4. Note in `wiki/` (browser checks / egress) that WebSockets are covered, and close T3 in the scan's known list once this ships.

## Tests

- `server/internal/checkers/checkbrowser` (Chrome available, slowtests layer if it needs a real browser), with the guard in enforcing mode:
  - a page served from a test HTTP server (treated as public by a test guard) opens a WebSocket to a second listener the guard classes as private. The check result must be the egress refusal, and the private listener must never see a handshake (step 1) or at least the result must not carry the frame (step 2). This fails before the fix.
  - Positive control: a WebSocket to an allowed host still connects, and its frame is readable by the script.
- Unit test for the event handling (no browser): feeding a `network.EventWebSocketCreated` with a private address into the listener sets `egressDenied`.
- Resolver-rules builder test: the rule string maps every host to `~NOTFOUND` except the pinned one.

## To verify

- Whether any Chrome flag or CDP domain in the current setup already blocks page WebSockets to private ranges (Private Network Access does not cover WebSockets in headless-shell builds, as far as I know).
- That the DevTools socket of the sidecar is not reachable from a page (unguessable `/devtools/browser/<id>` and the DevTools Host check), and whether `--remote-allow-origins=*` can be narrowed to the server's own origin.
