---
model: sonnet
effort: medium
pr: batch
automerge: false
---

# The `docker` check cannot watch the Docker daemon itself

## Problem

Issue #1044: a user makes every container check depend on a "Docker daemon is up" parent check, so a Docker restart raises one alert instead of one per container. The `docker` check cannot be that parent: it requires a container (`server/internal/checkers/checkdocker/config/config.go:146`). The only way left is a tcp or http check on `/var/run/docker.sock`, which means putting it in `checkers.unix_socket_paths`. Then anyone who can create a check can call the Docker API over that socket, which is root on the host.

Reply promising this change: https://github.com/fclairamb/solidping/issues/1044#issuecomment-7177369442

## Proposal

1. `server/internal/checkers/checkdocker/config/config.go:145`: drop the "containerName or containerId required" rule. No container means daemon mode. Host and timeout validation stay as they are.
2. `server/internal/checkers/checkdocker/checker.go:69`: when neither `containerName` nor `containerId` is set, call `cli.Ping(ctx, ...)` instead of `ContainerInspect`. Up if it answers, down otherwise, with the same timeout handling and `socketPermissionHint` as `handleInspectError` (`checker.go:128`). Put the daemon's API version (and OS type when the ping returns it) in the output, and a `ping_time_ms` metric. If version info needs `ServerVersion`, a second call is fine.
3. `createClient` (`checker.go:95`) does not change: same egress and dialer rules for both modes.
4. `server/internal/checkers/checkdocker/config/specdefaults.go`: with no container, default name `Docker daemon` and slug `docker-daemon`.
5. `server/internal/checkers/checkdocker/samples.go`: add a `Docker daemon` sample (no container).
6. dash0 `web/dash0/src/components/checks/form/types/infra.tsx:209`: remove the required-container validation. Help text under the container fields: "Leave empty to check the Docker daemon only." Add the key in all four `web/dash0/src/locales/*/checks.json`, and drop `containerRequired` if nothing else uses it.
7. Docs: describe daemon mode where the docker check is documented (see To verify), with the parent/child dependency use case, and say docker.sock does not need to be in `unix_socket_paths` for it.
8. `CHANGELOG.md` entry (conventions: `wiki/conventions/changelog.md`).

## Tests

- `server/internal/checkers/checkdocker/config/config_test.go`: a config without container now validates; a bad host still fails with no container.
- `server/internal/checkers/checkdocker/checker_test.go`: daemon mode against a fake Docker API (httptest on `/_ping`): up when it answers, down when it errors (500 or closed listener), timeout when it hangs. Container mode unchanged.
- `server/internal/checkers/checkdocker/egress_test.go`: daemon mode keeps the egress rules (local socket refused under a dialer, private tcp host denied when enforcing).
- Spec defaults test: no container gives name `Docker daemon`, slug `docker-daemon`.
- dash0: the form saves a docker check with both container fields empty (Playwright if a docker form spec exists, otherwise a unit test of the form's validate).

## To verify

- Where the docker check is documented in `web/docs/docs/` (no `checks/` folder found; maybe generated from checker metadata or under `features/`).
- Whether the moby client in `go.mod` returns `APIVersion` and `OSType` from `Ping`.
- Whether the checker metadata or JSON schema (if any) marks `containerName` as required.
