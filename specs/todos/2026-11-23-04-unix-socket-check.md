---
model: opus     # the check must stay behind the egress guard: a local socket is a host-internal target
effort: medium
pr: batch
automerge: false
---

# Monitor a service that only listens on a Unix domain socket

## Problem

Issue #1000. A self-hosted SolidPing runs on the same host as services that listen only on a Unix
socket file (a web app configured with a `unix:/run/app/app.sock` listen address, no TCP port).
Today the only way to watch them is an external HTTP check through a reverse proxy, which tests the
proxy as much as the service. No check type can open a Unix socket: `tcp` dials `host:port`
(`server/internal/checkers/checktcp/config/config.go:20-31`) and `docker` refuses `unix://` hosts
as soon as an outbound dialer applies (`server/internal/checkers/checkdocker/checker.go:91-111`).

## Proposal

New check type `unix_socket` (constant `CheckTypeUnixSocket`), in its own package
`server/internal/checkers/checkunixsocket/`, following the "Adding New Checkers" steps of
`server/internal/checkers/AGENTS.md`.

1. **Config** (`checkunixsocket/config/config.go`, snake_case JSON like `checktcp`):
   - `path` (required): absolute path of the socket file. Validation: absolute, cleaned, no NUL
     byte. Abstract sockets (leading `@`) are out of scope.
   - `timeout` (same semantics as `tcp`).
   - Raw mode, same fields and meaning as `tcp`: `send_data`, `send_encoding`, `expect_data`,
     `expect_encoding`, `expect_pattern`. Reuse the `tcp` exchange code (`checktcp/exchange*`,
     `checkerdef/readuntil.go`) rather than copying it; move the shared part into `checkerdef` if it
     is package-private today.
   - HTTP mode: `http_path` (e.g. `/healthcheck`), `expected_status` (default: any 2xx or 3xx),
     `body_expect`. When `http_path` is set, the check sends `GET <http_path>` over the socket with
     an `http.Transport` whose `DialContext` dials the socket, `Host: localhost`. `http_path` and
     `send_data` are mutually exclusive (declare it with `checkerdef.SchemaExclusiveGrouper`).
2. **Execute**: dial `unix` on `path` with the timeout.
   - Connect only (no send, no `http_path`): up when the dial succeeds.
   - Down on `ENOENT` (socket file missing), `ECONNREFUSED` (nobody listening), `EACCES`, with the
     errno in the output so the user can tell them apart. Timeout status on deadline.
   - Metrics: `connect_time_ms`, plus `response_time_ms` and `status_code` in HTTP mode.
3. **Never reachable from a shared node.** Before dialing:
   - tunneled check (`checkerdef.TunnelDialerFrom(ctx) != nil`): refuse with an error, a socket
     path means nothing on the far side of a tunnel;
   - enforcing egress guard (`egress.FromContext(ctx).Enforcing()`): refuse with an
     `egress.DeniedError` and `egress.RecordDenial`, exactly like `checkdocker/checker.go:100-106`.

   This is the whole security model: a SaaS shared worker must never become a probe of its own
   host's sockets.
4. **Meta** in `checkerdef/types.go` (`checkTypesRegistry`): labels `labelUnsafe`,
   `labelCatInfrastructure`, a new `labelReqLocalSocket = "requires:local-socket"`;
   `SupportsTunnel: false`, `SupportsIPVersion: false`. Description: "Check a Unix domain socket".
5. **SaaS placement**: on a SaaS deployment the type only runs pinned to a private location, like
   `docker`. Extend `dockerAdvisoryFor` (`checkerdef/activation.go:119`) into an advisory for both
   types, and apply the same placement rule as docker (see To verify).
6. Register it: `registry/registry.go` `builtinCheckers`, `configregistry` `ParseConfig` and
   `ValidateSpec`, regenerate `internal/checkers/schemas/unix_socket.json`
   (`go generate ./internal/checkers/schemas/...`), bump the advertised count and the docs that
   `registry/check_type_count_test.go` names.
7. **Docs**: a `unix_socket` section on the check-types page (`web/docs/docs/`), anchor `unix-socket`,
   with two examples (connect only, HTTP mode). Say that the SolidPing process needs read/write
   permission on the socket file, and that a SolidPing running in a container only sees sockets
   mounted into it.
8. **dash0**: the check form for the new type (path, mode, expectations), with `data-testid`s.
   Follow the design reference page.
9. `CHANGELOG.md` entry, with a link to issue #1000.

## Tests

- `checkunixsocket/checker_test.go`, with a listener on `t.TempDir()/s.sock`:
  - connect only: up when listening; down with the errno when the file is missing, and when the
    file exists but nobody listens (closed listener leaves the file, or a plain file);
  - raw mode: `send_data` + `expect_data` match is up, mismatch is down;
  - HTTP mode: `httptest`-style server on the socket, 200 is up, 500 is down, `body_expect` miss
    is down, `expected_status` honoured;
  - timeout: a listener that accepts and never answers yields timeout status.
- `checkunixsocket/egress_test.go`: under an enforcing guard the check is refused with a recorded
  denial and never dials (positive control: the same config is up with no guard); under a tunnel
  dialer it is refused.
- `checkunixsocket/config/validate_test.go`: relative path, empty path, NUL byte, `http_path`
  together with `send_data` are rejected; a valid config passes.
- The registry tests (`registry_test.go`, `check_type_count_test.go`, `egress_test.go`,
  `configregistry_agree_test.go`) pass with the new type added to their tables.
- dash0 Playwright: create a `unix_socket` check from the form, it is saved with the path.

## To verify

- Where docker checks are rejected on shared SaaS regions ("shared regions reject them",
  `checkerdef/activation.go:124`): find that rule and add `unix_socket` to it, rather than relying
  only on the egress guard.
- Whether the `tcp` send/expect exchange is reusable as is from another package.

## Open questions

- Name: `unix_socket` or `file_socket` (both floated in the issue)? **Recommended: `unix_socket`**,
  the standard term, and the one Go and systemd use.
- HTTP over a socket: a mode of `unix_socket` (this spec), or a `socket_path` field on the `http`
  check so every HTTP assertion applies? **Recommended: the mode in `unix_socket`** with status and
  body only; move to `http` later if users ask for the full HTTP assertions.
