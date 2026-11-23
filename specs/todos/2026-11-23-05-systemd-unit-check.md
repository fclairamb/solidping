---
model: opus     # runs a host command from user input: argument validation is the security boundary
effort: medium
pr: batch
automerge: false
---

# Monitor the state of a systemd unit

## Problem

Issue #1000. A self-hosted SolidPing installed as a root systemd service can see the host's
services, but no check type reports whether a systemd unit is running. A service that listens on
nothing reachable (or only on a Unix socket) cannot be watched at all. The user wants "the systemd
status (active-running)" of a unit.

## Proposal

New check type `systemd` (constant `CheckTypeSystemd`), package
`server/internal/checkers/checksystemd/`, following the "Adding New Checkers" steps of
`server/internal/checkers/AGENTS.md`.

1. **Config** (`checksystemd/config/config.go`):
   - `unit` (required): unit name. A bare name gets `.service` appended. Validation: matches
     `^[A-Za-z0-9:_.@-]+$`, at most 256 characters, does not start with `-` (so it can never be
     read as a `systemctl` flag).
   - `expected_state` (optional): `active` (default). Allowed values: `active`, `inactive`. The
     second covers "this unit must not run".
   - `timeout`.
2. **Execute**: run `systemctl show <unit> --property=LoadState,ActiveState,SubState,Result,ActiveEnterTimestamp --no-pager`
   with `exec.CommandContext` (args passed as a slice, no shell, `--` before the unit name), and
   parse its `KEY=value` lines. No new Go dependency (no D-Bus client): `systemctl` is present
   wherever systemd is.
   - `LoadState=not-found`: down, output "unit not found".
   - `ActiveState` equals `expected_state`: up. For `active`, `activating` and `reloading` are up
     too (a unit restarting under `Restart=` is not an outage); `failed`, `inactive`,
     `deactivating` are down.
   - Output: `ActiveState/SubState` (e.g. `active/running`, `failed/failed`) and `Result`.
   - Metric: `active_since_s` from `ActiveEnterTimestamp` when active.
   - `systemctl` missing (macOS, Alpine container): error status with a clear message, not down.
3. **Never on a shared node.** Refuse when the egress guard is enforcing
   (`egress.FromContext(ctx).Enforcing()`) or the check is tunneled, like
   `checkdocker/checker.go:100-106`: a SaaS worker must not report on its own host's units.
4. **Meta** in `checkerdef/types.go`: labels `labelUnsafe`, `labelCatInfrastructure`, a new
   `labelReqSystemd = "requires:systemd"`; `SupportsTunnel: false`, `SupportsIPVersion: false`.
   Description: "Check the state of a systemd unit".
5. **SaaS placement**: same rule as `docker` (private location only), sharing the advisory with
   the `unix_socket` spec if it lands first (`checkerdef/activation.go:119`).
6. Register it (`registry/registry.go`, `configregistry`), regenerate
   `internal/checkers/schemas/systemd.json`, bump the advertised count and the docs that
   `registry/check_type_count_test.go` names.
7. **Docs**: a `systemd` section on the check-types page, anchor `systemd`. Say that it reads the
   systemd of the host SolidPing runs on, so it does not work from a container without the host's
   D-Bus socket mounted, and that `systemctl show` needs no root.
8. **dash0**: the form (unit, expected state) with `data-testid`s, per the design reference.
9. `CHANGELOG.md` entry, with a link to issue #1000.

## Tests

- `checksystemd/parse_test.go` (table-driven, no systemd needed): `systemctl show` outputs for
  active/running, activating/auto-restart, failed/failed, inactive/dead, not-found; each maps to
  the expected status and output; `expected_state: inactive` inverts up/down.
- `checksystemd/checker_test.go`: the command runner is injectable; assert the exact argv
  (`--` before the unit, `.service` appended), and a missing `systemctl` gives error status.
- `checksystemd/config/validate_test.go`: `-H`, `--host=x`, `a b`, `a;b`, empty, 300 characters
  are rejected; `nginx`, `app@1.service`, `foo.timer` pass.
- `checksystemd/egress_test.go`: refused under an enforcing guard and under a tunnel, and the
  runner is never called (positive control: called with no guard).
- Registry tests pass with the new type in their tables.
- dash0 Playwright: create a `systemd` check from the form.

## To verify

- Where docker checks are rejected on shared SaaS regions (`checkerdef/activation.go:124`), to add
  `systemd` to the same rule.
- Whether the private-location agent image ships `systemctl` (if not, the docs say so).

## Resolved open questions

- **Decision:** accept any unit type (`.service`, `.timer`, `.socket`, `.mount`, ...). A bare name
  gets `.service` appended.
- **Decision:** `activating` stays up however long it lasts. No threshold.
