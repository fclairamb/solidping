---
model: opus     # the local-socket gates are what keeps a check off the host's sockets
effort: medium
---

# The docker check dials any `unix://` path, bypassing `checkers.unix_socket_paths`

## Problem

Found by the security scan of 2026-11-27 (finding AZ2). Severity: Low. Who can exploit it: any member
who may create checks (role `user`) on a self-hosted server or a private agent. SaaS shared workers
refuse docker checks (`config.DockerChecksBlocked`, `server/internal/config/node_role.go:154`), and an
enforcing egress guard refuses every non-`tcp` docker host (`checkdocker/checker.go:103-115`).

`checkerdef/localsocket.go:17-22` is meant to be "the ONE place that decides whether a check may open a
Unix socket". The operator's `checkers.unix_socket_paths` is empty by default, which turns socket
targets off. The docker check is a documented exception for the Docker socket: daemon mode and
container mode reach `/var/run/docker.sock` "through the docker client, so it does not need to be in
`checkers.unix_socket_paths`" (`web/docs/docs/features/check-types.md:1403-1411`).

That exception is not limited to the Docker socket:

- `DockerConfig.Validate` only requires the host to start with `unix://` or `tcp://`
  (`server/internal/checkers/checkdocker/config/config.go:145-150`). Any path is accepted.
- With no guard or tunnel on the context, `createClient` hands the host to the moby client, which dials
  it with its own transport (`checkdocker/checker.go:99-131`). No `ValidateLocalSocketPath`,
  `LocalSocketAllowed` or `CheckLocalSocket` check runs.
- The save path does not see it either: `validateLocalSocketConfig`
  (`server/internal/handlers/checks/validate.go:558`) only applies to configs that implement
  `LocalSocketTarget`, and `DockerConfig` has no socket field.

So a check author can make the server (or agent) send an HTTP request to any Unix socket the process
can open: `GET /_ping` in daemon mode (fc6309d0, #1057), and
`GET /v…/containers/<containerName>/json` in container mode, where the container name is theirs to
choose. The error text, which can carry part of the peer's answer, is stored in the result
(`handleError`, `checker.go:175-200`). Every other socket-capable check type is held to the
allowlist. Only GET requests on fixed path shapes are possible, so this is a bypass of the allowlist
plus a probe of local sockets, not command execution.

Inspecting the host's own Docker daemon from any org on a self-hosted server is by design (a
host-local type) and is not part of this finding.

## Proposal

1. Restrict a docker `unix://` host to Docker sockets. Add `checkers.docker_socket_paths`
   (`SP_CHECKERS_DOCKER_SOCKET_PATHS`, comma-separated, bound by hand in `Load()` like
   `EnvCheckersUnixSocketPaths`, `server/internal/config/config.go:481-488`). Default:
   `/var/run/docker.sock,/run/docker.sock`. Same matching rules as `LocalSocketAllowed` (exact path,
   or a directory prefix ending in `/`).
2. Dial time: in `createClient`, for a `unix://` host on the no-guard path, run
   `checkerdef.ValidateLocalSocketPath` and check the path against that list before
   `client.New`. A refusal is a down result naming the setting, like the other socket refusals.
3. Save time: the same check in `validateLocalSocketConfig` (or a docker-specific sibling), for
   checks placed on regions this server runs. A private agent applies its own list, as it does for
   `unix_socket_paths`.
4. Docs: `check-types.md` (Docker section) and `configuration/index.md` name the new setting, its
   default, and the rootless Docker and Podman socket paths an operator may add.

## Tests

- `server/internal/checkers/checkdocker`: a `unix://` host outside the list (a socket in a temp dir)
  is refused before any dial. A test listener on that socket sees no connection. Fails before the fix:
  the client dials it today (`socket_permission_test.go:47-49` relies on this).
- Positive control: the same temp socket added to `docker_socket_paths` is dialed and a fake
  `/_ping` answer gives an up result in daemon mode.
- Default list: `unix:///var/run/docker.sock` passes validation with no configuration.
- `handlers/checks` save path: a docker check with `host: unix:///run/other.sock` is rejected with a
  `VALIDATION_ERROR` naming `checkers.docker_socket_paths`. The default host is accepted.
- The existing egress tests (`tcp://` through the guard, `unix://` refused when enforcing) keep passing.

## To verify

- Whether the moby client path-escapes `ContainerInspect`'s container reference. If `../` reaches the
  wire, also validate `containerName` / `containerId` against Docker's name and ID syntax
  (`[a-zA-Z0-9][a-zA-Z0-9_.-]*`, hex IDs) in `Validate`.
- The socket permission hint and its test (`socket_permission_test.go`) need a listed path after the
  change.
