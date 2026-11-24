---
model: opus     # runs a host command from user input: argument validation is the security boundary
effort: medium
pr: unix-socket-checks
automerge: false
---

# Monitor the state of a systemd unit

## Problem

Issue #1000 also asks for "the systemd status (active-running)" of a unit. A self-hosted SolidPing
installed as a systemd service can see the host's units, but no check reports whether one is
running. This is a separate need from the socket specs of this PR, and gets its own check type.

Recreated from PR #1003 (closed, not merged), which implemented it as specced in
`2026-11-23-05-systemd-unit-check` with the two open questions answered as below. The
implementation on branch `feat/todos-2026-11-23-2` (commits `30542dd6`..`7be87ad7`) can be reused,
adapted to the `checkerdef.IsHostLocal` gate of spec `local-socket-dialer`.

## Proposal

New check type `systemd` (constant `CheckTypeSystemd`), package
`server/internal/checkers/checksystemd/`, following the "Adding New Checkers" steps of
`server/internal/checkers/AGENTS.md`.

1. **Config** (`checksystemd/config/config.go`):
   - `unit` (required): any unit type (`.service`, `.timer`, `.socket`, `.mount`…); a bare name gets
     `.service` appended. Validation: matches `^[A-Za-z0-9:_.@-]+$`, at most 256 characters, does
     not start with `-`.
   - `expected_state`: `active` (default) or `inactive` ("this unit must not run").
   - `timeout`.
2. **Execute**: `exec.CommandContext("systemctl", "show", "--property=LoadState,ActiveState,SubState,Result,ActiveEnterTimestamp", "--no-pager", "--", unit)`,
   args as a slice, no shell; parse the `KEY=value` lines. No D-Bus dependency. The runner is
   injectable for tests.
   - `LoadState=not-found`: down, "unit not found".
   - For `active`: `active`, `activating`, `reloading` are up (no threshold on `activating`);
     `failed`, `inactive`, `deactivating` are down. `expected_state: inactive` inverts.
   - Output `ActiveState/SubState` (e.g. `active/running`) and `Result`; metric `active_since_s`.
   - `systemctl` missing: error status with a clear message, not down.
3. **Never on a shared node**: refuse under a tunnel or an enforcing egress guard, and add
   `CheckTypeSystemd` to `checkerdef.IsHostLocal`, so the SaaS create gate, the worker gate, the JS
   sub-check gate and the catalog advisory treat it like `docker` (private locations only).
4. **Meta** in `checkerdef/types.go`: labels `labelUnsafe`, `labelCatInfrastructure`, a new
   `labelReqSystemd = "requires:systemd"`; `SupportsTunnel: false`, `SupportsIPVersion: false`.
   Description "Check the state of a systemd unit".
5. Register it (`registry/registry.go`, `configregistry`), regenerate
   `internal/checkers/schemas/systemd.json`, add it to the OpenAPI check-type enums
   (`server/internal/app/openapi/openapi.yaml`) and to the enum of
   `server/pkg/client/client_generated.go`, and bump the advertised check-type count and the docs
   that `registry/check_type_count_test.go` names.
6. **Docs**: a `systemd` section on the check-types page, anchor `systemd`: it reads the systemd of
   the host the check runs on, does not work from a container without the host's D-Bus socket, and
   `systemctl show` needs no root.
7. **dash0**: the form (unit, expected state) with `data-testid`s, per the design reference.
8. `CHANGELOG.md` entry linking issue #1000.

## Tests

- `checksystemd/parse_test.go` (table-driven): active/running, activating/auto-restart,
  failed/failed, inactive/dead, not-found map to the expected status and output;
  `expected_state: inactive` inverts.
- `checksystemd/checker_test.go`: exact argv (`--` before the unit, `.service` appended); missing
  `systemctl` gives error status.
- `checksystemd/config/validate_test.go`: `-H`, `--host=x`, `a b`, `a;b`, empty, 300 characters
  rejected; `nginx`, `app@1.service`, `foo.timer` pass.
- `checksystemd/egress_test.go`: refused under an enforcing guard and under a tunnel, the runner is
  never called (positive control: called with no guard).
- `handlers/checks` and `checkworker`: a `systemd` check on a SaaS shared region is rejected, on a
  private region accepted; a shared SaaS worker refuses the job, an agent worker runs it.
- Registry tests pass with the new type in their tables.
- dash0 Playwright: create a `systemd` check from the form.

## To verify

- Whether the private-location agent image ships `systemctl` (if not, the docs say so).
- `client_generated.go` is stale against oapi-codegen v2.9.0 (noted in #1003): add the enum value
  by hand, as #1003 did, unless a full regeneration is cheap.
