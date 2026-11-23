---
model: opus     # the allowlist and the host-local gates are the only thing keeping a check off the host's docker.sock
effort: high
pr: unix-socket-checks
automerge: false
---

# Let existing checks reach a service on a Unix socket, behind one shared, allowlisted dialer

## Problem

Issue #1000. A self-hosted SolidPing often runs next to services that only listen on a Unix socket
(a web app listening on `unix:/run/app/app.sock`, Redis on `/run/redis/redis.sock`, Postgres on
`/var/run/postgresql`). No check can open a socket today: `tcp` dials `host:port`
(`server/internal/checkers/checktcp/config/config.go:20-22`), and `docker` refuses `unix://` as
soon as an outbound dialer applies (`server/internal/checkers/checkdocker/checker.go:97-110`).

PR #1003 (closed, not merged) added a dedicated `unix_socket` check type. That approach is
replaced: a bare "the socket accepts a connection" proves little (the socket file can outlive a
hung process), and a new type loses what each protocol check already does (Redis `PING`, Postgres
`SELECT 1`, HTTP status and body). Instead, the existing checks take a socket path in place of
`host`/`port`. This spec builds the shared piece and wires the first check (`tcp`); the following
specs of this PR wire `http`, `redis`, `postgres`, `mysql`, `mongodb` and `smtp`.

The catch is security. SolidPing often runs as root, and one request on `/var/run/docker.sock` is
root on the host (`POST /containers/create` with a privileged container). So socket access must be
decided in one place, never by each check.

## Proposal

1. **Server config: an allowlist, empty by default.** In `CheckersConfig`
   (`server/internal/config/config.go:740`), add `UnixSocketPaths []string`
   (`koanf:"unix_socket_paths"`): absolute paths or directory prefixes ending in `/`
   (e.g. `/run/gotify/`, `/var/run/postgresql/`). An empty list means the feature is off: every
   socket target is refused with a config error that names the setting. Env var: see To verify.
   A private-location agent reads the same setting from its own config: the allowlist is the one
   of the process that dials.
2. **One dialer**: `checkerdef.DialLocalSocket(ctx, path string, timeout time.Duration) (net.Conn, error)`
   in a new `server/internal/checkers/checkerdef/localsocket.go`. In order, before any dial:
   - tunneled check (`checkerdef.TunnelDialerFrom(ctx) != nil`): refuse, a socket path means
     nothing on the far side of a tunnel;
   - enforcing egress guard (`egress.FromContext(ctx).Enforcing()`): refuse with an
     `egress.DeniedError` and `egress.RecordDenial`, exactly like `checkdocker/checker.go:100-106`;
   - path not absolute, not `filepath.Clean`, contains a NUL byte, or starts with `@` (abstract
     sockets are out of scope): refuse;
   - path not covered by the allowlist (exact match, or under a `/`-terminated prefix, compared
     after `filepath.Clean` so `..` cannot escape a prefix): refuse;
   - then `net.Dialer{}.DialContext(ctx, "unix", path)`.

   Map dial errors to a readable output naming the errno: `ENOENT` (socket file missing),
   `ECONNREFUSED` (nobody listening), `EACCES` (SolidPing lacks permission on the file).
   Also export `checkerdef.SocketHTTPTransport(ctx, path)` returning an `*http.Transport` whose
   `DialContext` ignores the requested address and calls `DialLocalSocket`; the `http` spec uses it.
3. **Config contract**: an optional interface in `checkerdef`,
   `type LocalSocketTarget interface { LocalSocket() string }`. A check config that implements it
   and returns a non-empty path is a host-local check. Every gate below reads it, so a new check
   gets the rules by implementing one method.
4. **Host-local gates, single source.** Today four places hard-code `CheckTypeDocker`: the create
   gate (`handlers/checks/validate.go:495-518`), the worker gate (`checkworker/worker.go:1450`),
   the JS sub-check gate (`checkworker/worker.go:2001-2012`, `checkjs.DeploymentGateFunc` at
   `checkjs/checker.go:76`) and the catalog advisory (`checkerdef/activation.go:119-126`).
   Add `checkerdef.IsHostLocal(checkType CheckType, cfg Config) bool`: true for `docker` (and for
   `systemd`, see spec `systemd-unit-check` of this PR) or when `cfg` is a `LocalSocketTarget` with a
   non-empty path. Rewire the four places to it:
   - create/update: on SaaS, a host-local check must be pinned to private regions only, the
     docker rule generalised (`ErrDockerNotAvailableInSaaS` gets a generic sibling
     `ErrHostLocalNotAvailableInSaaS`; keep the docker message unchanged for docker);
   - worker: a SaaS shared worker refuses the job before execution, an agent-mode worker runs it
     (same as `dockerBlockedOnThisWorker`, `worker.go:1997`);
   - JS sub-checks: the gate receives the sub-check's config too;
   - advisory: the `tcp` (and later `http`, `redis`…) catalog entries get no advisory, since only
     some configs are host-local; the create error explains it instead.
5. **`tcp` accepts a socket**: add `Socket string` (`json:"socket,omitempty"`) to the `tcp` config.
   `socket` and `host`/`port`/`url` are mutually exclusive (declare it with
   `checkerdef.SchemaExclusiveGrouper`). The URL form `unix:///run/app/app.sock` sets `socket`;
   add `"unix": checkerdef.CheckTypeTCP` to `schemeMapping`
   (`checkers/urlparse/urlparse.go:42`). Send/expect works unchanged over the socket; TLS
   (`tcps`) is refused with a socket. The `tcp` config implements `LocalSocketTarget`.
6. Regenerate the schemas (`go generate ./internal/checkers/schemas/...`).
7. **Docs**: a "Unix sockets" section on the check-types page (`web/docs/docs/features/check-types.md`)
   and the setting on the configuration page: off by default, the allowlist, the per-check URL or
   field forms (a table this PR's later specs fill in), refused on SaaS shared regions and under
   the egress guard, needs read/write permission on the socket file, and a containerised SolidPing
   only sees sockets mounted into it. Warn that allowlisting `/var/run/docker.sock` or `/run/`
   gives every check author root on the host.
8. **dash0**: the `tcp` form gets a target switch "Host and port / Unix socket" with a socket path
   input (`data-testid="tcp-target-mode"`, `tcp-socket-path`), per the design reference.
9. `CHANGELOG.md` entry linking issue #1000.

## Tests

- `checkerdef/localsocket_test.go`, with a listener on `t.TempDir()/s.sock` and the allowlist set:
  - allowed exact path and allowed prefix connect (positive control for every refusal below);
  - empty allowlist, a path outside it, `<prefix>/../etc/x.sock`, relative path, NUL byte,
    `@abstract` are refused and never dial;
  - enforcing egress guard: refused with a recorded denial; tunnel dialer set: refused;
  - missing file gives `ENOENT`, a closed listener's leftover file gives `ECONNREFUSED`, a
    `0000` file gives `EACCES` (skip that case when running as root).
- `checktcp`: connect-only and send/expect over a socket are up; `socket` with `host` is a
  validation error; `tcps` with a socket is rejected.
- `urlparse`: `unix:///run/app/app.sock` parses to `tcp` with that socket.
- `handlers/checks`: on SaaS, a `tcp` check with `socket` on a shared region is rejected and
  accepted on a private region; the same check with `host`/`port` is accepted on a shared region
  (positive control); self-hosted accepts both. Docker cases keep passing unchanged.
- `checkworker`: a shared SaaS worker refuses a socket `tcp` job without dialing; an agent worker
  runs it; a JS sub-check with a socket is refused where the top-level one is.
- dash0 Playwright: create a `tcp` check in socket mode, it is saved with `socket`.

## To verify

- The env var for `checkers.unix_socket_paths`: `config.go:861` says snake_case keys are
  unreachable by koanf's env mapping. Follow how `AllowPrivateTargets` gets `SP_EGRESS_ALLOW_PRIVATE`
  (`config.go:852-874`) and name it `SP_CHECKERS_UNIX_SOCKET_PATHS` (comma-separated).
- `validateRegionGates` (`validate.go:520`) runs on a region-only PATCH with only the type: it must
  load the stored config to know whether the check is host-local.
- Whether the private-location agent loads `CheckersConfig` from its own env, or receives config
  from the server (the allowlist must be the agent's, never the server's).

## Open questions

- Allowlist default: empty (feature off until the operator lists paths), or allow every path on a
  self-hosted server? **Recommended: empty**. An "allow all" default makes docker.sock reachable by
  any member who can create a check.
