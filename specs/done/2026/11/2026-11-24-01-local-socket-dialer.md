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
   The two path rules (syntax, then allowlist) live in exported functions that `DialLocalSocket`
   calls, so the save-time gate (item 5) applies exactly the same rules.
3. **Config contract**: an optional interface in `checkerdef`,
   `type LocalSocketTarget interface { LocalSocket() (field, path string) }`. `field` is the config
   key that holds the path (`socket`, or `unix_socket` for `http`), so a validation error lands on
   the right form field. A check config that implements it and returns a non-empty path is a
   host-local check. Every gate below reads it, so a new check gets the rules by implementing one
   method.
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
     some configs are host-local; the capability flags of item 6 and the create error explain it
     instead.
5. **Refuse at save, not at the first run.** Without this, a check with a path outside the
   allowlist saves fine and fails on every run. In `configValidationErrors`
   (`handlers/checks/validate.go`, shared by create, update and `POST .../checks/validate`), a
   config with a non-empty socket path gets:
   - the syntax rules, always;
   - the allowlist rule when the server's own workers run it, meaning at least one resolved region
     is not private, or there is no region at all. A path outside the list (or an empty list) is a
     config error on `field` that names `checkers.unix_socket_paths`;
   - no allowlist rule when every resolved region is private. The allowlist is the agent's, the
     server does not know it, and the agent refuses at dial time.

   The dial-time check stays: the allowlist can change after the check is saved.
6. **Capability flags for clients**, same pattern as `SupportsTunnel`
   (`checkerdef/types.go:418`), served by the authenticated check-types listing:
   - `SupportsLocalSocket bool` (`json:"supportsLocalSocket"`) on `CheckTypeMeta`: the type's
     config accepts a socket path. Set for `tcp` here, and by each later spec of this PR for its
     type.
   - `LocalSocketOnServer bool` (`json:"localSocketOnServer"`) on `CheckTypeStatus`
     (`checkerdef/activation.go:129`), set in `ListAllWithStatus`: true when the type supports
     sockets, the server allowlist is not empty and the deployment is not SaaS. The paths
     themselves are never served, and nothing goes in the unauthenticated `GET /api/v1/config`.
   - Add both to the check-type schema of `openapi.yaml`.
7. **`tcp` accepts a socket**: add `Socket string` (`json:"socket,omitempty"`) to the `tcp` config.
   `socket` and `host`/`port`/`url` are mutually exclusive (declare it with
   `checkerdef.SchemaExclusiveGrouper`). The URL form `unix:///run/app/app.sock` sets `socket`;
   add `"unix": checkerdef.CheckTypeTCP` to `schemeMapping`
   (`checkers/urlparse/urlparse.go:42`). Send/expect works unchanged over the socket; TLS
   (`tcps`) is refused with a socket. The `tcp` config implements `LocalSocketTarget`.
8. Regenerate the schemas (`go generate ./internal/checkers/schemas/...`).
9. **Docs**: a "Unix sockets" section on the check-types page (`web/docs/docs/features/check-types.md`)
   and the setting on the configuration page: off by default, the allowlist, the per-check URL or
   field forms (a table this PR's later specs fill in), refused on SaaS shared regions and under
   the egress guard, needs read/write permission on the socket file, and a containerised SolidPing
   only sees sockets mounted into it. Warn that allowlisting `/var/run/docker.sock` or `/run/`
   gives every check author root on the host. Say that the dashboard only offers the socket target
   once the allowlist is set, or on a check pinned to private locations.
10. **dash0**: the `tcp` form gets a target switch "Host and port / Unix socket" with a socket path
    input (`data-testid="tcp-target-mode"`, `tcp-socket-path`), per the design reference.
    - Add `supportsLocalSocket` and `localSocketOnServer` to `CheckTypeInfo` (`api/hooks.ts`).
    - One shared hook decides whether the socket target is offered, for this form and the ones of
      the later specs. It is offered when `supportsLocalSocket` is true and either
      `localSocketOnServer` is true or the check is pinned to private regions only (every slug
      starts with `@`, as `check-form.tsx:851` tests it).
    - Never a hard-coded type list, same rule as `tunnel-select.tsx`.
    - A check that already stores a socket path always shows it when edited, even when the target
      is no longer offered. Otherwise the form would hide the stored value.
    - Errors from item 5 show on the socket input through the existing validate findings.
11. `CHANGELOG.md` entry linking issue #1000.

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
- `handlers/checks`, self-hosted with the allowlist `/run/app/`: a `tcp` check on
  `/run/app/a.sock` is accepted (positive control); `/run/other/a.sock` is a config error on
  `socket` that names `checkers.unix_socket_paths`; with an empty allowlist every path is
  refused; `/run/other/a.sock` pinned to a private region is accepted; `POST .../checks/validate`
  returns the same finding as create.
- `checkerdef`: for every registered type, `SupportsLocalSocket` equals "its config implements
  `LocalSocketTarget`", so a later spec cannot wire one and forget the other.
- `checktypes`: `tcp` has `supportsLocalSocket` true and `icmp` false; `localSocketOnServer` is
  true with an allowlist on self-hosted, false with an empty one, false on SaaS.
- `checkworker`: a shared SaaS worker refuses a socket `tcp` job without dialing; an agent worker
  runs it; a JS sub-check with a socket is refused where the top-level one is.
- dash0 Playwright: create a `tcp` check in socket mode, it is saved with `socket`. The E2E server
  runs with an allowlist (see To verify).
- dash0 component test (vitest): with `localSocketOnServer` false and no private region the switch
  is absent; selecting only private regions shows it (positive control); editing a check that
  stores a socket path shows it.

## To verify

- The env var for `checkers.unix_socket_paths`: `config.go:861` says snake_case keys are
  unreachable by koanf's env mapping. Follow how `AllowPrivateTargets` gets `SP_EGRESS_ALLOW_PRIVATE`
  (`config.go:852-874`) and name it `SP_CHECKERS_UNIX_SOCKET_PATHS` (comma-separated).
- `validateRegionGates` (`validate.go:520`) runs on a region-only PATCH with only the type: it must
  load the stored config to know whether the check is host-local.
- Whether the private-location agent loads `CheckersConfig` from its own env, or receives config
  from the server (the allowlist must be the agent's, never the server's).
- Where the E2E server gets its environment (CI and `wiki/testing/local-e2e.md`), to set
  `SP_CHECKERS_UNIX_SOCKET_PATHS` to a temp directory there.
- How the `ActivationResolver` gets the allowlist: it is built once at startup with the deployment
  mode (`NewActivationResolver`), the allowlist comes the same way.

## Resolved open questions

- Allowlist default: empty (feature off until the operator lists paths), or allow every path on a
  self-hosted server? **Recommended: empty**. An "allow all" default makes docker.sock reachable by
  any member who can create a check.

  **Decision:** the allowlist defaults to empty: the feature stays off until the operator lists paths. (autopilot: recommended answer)
