---
model: sonnet
effort: medium
pr: unix-socket-checks
automerge: false
---

# HTTP check over a Unix socket

## Problem

Issue #1000. The services in the issue (a notification server, a git forge, a feed reader) speak
HTTP on their socket. The useful check is the full `http` check (status code, body, headers,
latency) sent through the socket, like `curl --unix-socket`. Depends on spec
`local-socket-dialer` of this PR (`checkerdef.DialLocalSocket`, `SocketHTTPTransport`,
`LocalSocketTarget`, the allowlist and the host-local gates).

## Proposal

1. **Config**: add `UnixSocket string` (`json:"unix_socket,omitempty"`) to the `http` config
   (`server/internal/checkers/checkhttp/config/config.go:84`). The `url` stays required: its path
   and query are the request, its host is the `Host` header (typically `http://localhost/health`).
   The config implements `checkerdef.LocalSocketTarget`.
2. **URL form** for quick create: `http+unix:///run/app/app.sock:/health` sets `unix_socket` to the
   part before `:/` and `url` to `http://localhost/health`. Add `http+unix` to `schemeMapping`
   (`checkers/urlparse/urlparse.go:42`). `https+unix` is refused (no TLS over a local socket).
3. **Execute**: when `unix_socket` is set, build the client with
   `checkerdef.SocketHTTPTransport(ctx, path)` instead of the usual transport. Every HTTP assertion
   applies unchanged.
   - Redirects: a redirect is followed only when it stays on the same host as `url`. Any other
     redirect stops and is reported. Otherwise a `Location: http://other/` would be sent down the
     socket.
   - Settings that make no sense on a socket are rejected at validation when `unix_socket` is set:
     IP version, resolver and proxy settings, and an `https` URL (see To verify for the exact
     field names).
4. **dash0**: the `http` form gets an optional "Unix socket path" input under an advanced toggle
   (`data-testid="http-unix-socket"`), per the design reference.
5. **Docs**: an `http` row in the Unix sockets table of the check-types page, with a curl
   equivalent.

## Tests

- `checkhttp`: `httptest`-style server on `t.TempDir()/s.sock` (allowlisted): 200 is up, 500 is
  down, a body assertion miss is down, the `Host` header comes from `url`; a same-host redirect
  is followed; a redirect to another host is not followed and is reported.
- Validation: `unix_socket` with an `https` URL, with an IP version or with a proxy is rejected;
  a valid config passes.
- `urlparse`: `http+unix:///run/app/app.sock:/health?x=1` parses to `unix_socket=/run/app/app.sock`,
  `url=http://localhost/health?x=1`; `https+unix://…` is rejected.
- Gate: on SaaS, an `http` check with `unix_socket` on a shared region is rejected (positive control:
  the same check without it is accepted).
- dash0 Playwright: create an `http` check with a socket path, it is saved.

## To verify

- The names of the `http` config fields for IP version, resolver and proxy
  (`checkhttp/config/config.go`).
- How the `http` checker builds its transport today, so the socket transport keeps the timeouts and
  the response-size limits.
