---
model: sonnet
effort: medium
pr: batch
automerge: false
---

# Native host resource check (disk / memory / CPU) without an exporter

Source: issue fclairamb/solidping#1083.

## Problem

SolidPing cannot alert on the resources of a bare host. CPU, memory and disk are only
reachable through an SNMP daemon, a Prometheus exporter, Docker or Kubernetes.
`wiki/competitors/checkmate.md:28` lists this as a gap ("No dedicated agent, nearest:
`prometheus` check on node_exporter") and `wiki/competitors/statuscake.md` lists server
monitoring as a paid feature there. The competitor issue cited in #1083 is said to have
61 comments (figure not verified).

The pattern for a check that reads the host running it already exists:
`server/internal/checkers/checksystemd/checker.go` (issue #1000). It never runs through a
tunnel, and an enforcing egress guard refuses it (`checker.go:180`).

SolidPing ships and tests binaries for Linux, macOS and Windows (`.github/workflows/ci.yml`:
`windows-service` job at line 659, `macos-service`, cross-builds at line 989 with
`CGO_ENABLED=0`). The check must work on the three.

This check does not fix false positives caused by an overloaded monitoring box: a stalling
box stalls this check too. It only explains them afterwards. Do not write that claim in the
docs or the changelog.

## Proposal

1. New check type `host` in `server/internal/checkers/checkerdef/types.go` (next to
   `CheckTypeSystemd`, `types.go:247` and the `checkTypeInfos` entry at `types.go:512`):
   labels `unsafe` and the infrastructure category. No `requires:` label, it runs on every
   supported system.
2. Add it to `IsHostLocalType` (`server/internal/checkers/checkerdef/localsocket.go:84`) so
   it never runs on a SaaS shared worker. It runs on any other worker that executes it: the
   self-hosted main server and private agents alike.
3. New package `server/internal/checkers/checkhost/` with the same layout as `checksystemd`
   (`checker.go`, `alias.go`, `config/config.go`, `config/validate.go`, tests), registered in
   `server/internal/checkers/registry/registry.go:103`.
4. Config, all optional, camelCase. Thresholds are `*float64` with the same semantics as the
   prometheus check (`server/internal/checkers/checkprometheus/config/config.go:110-144`):
   warning yields `StatusWarning`, critical yields `StatusDown`, comparison is `>=`.
   - `diskPath`, `diskWarningPercent`, `diskCriticalPercent`. Default path: `/`, and the
     system drive (`%SystemDrive%\`, usually `C:\`) on Windows.
   - `memoryWarningPercent`, `memoryCriticalPercent`
   - `cpuWarningPercent`, `cpuCriticalPercent`
   - Validation: percents in `(0, 100]`, warning below critical when both are set, `diskPath`
     absolute and clean with no NUL byte (`filepath.IsAbs` and `filepath.Clean` of the
     system that validates; a Windows drive path must be accepted on Windows).
5. Execution through `github.com/shirou/gopsutil/v4`, promoted from indirect to direct in
   `server/go.mod` (line 161, `v4.26.6`). Its macOS files carry no cgo in that version
   (`mem_darwin.go`, `cpu_darwin*.go`, `disk_darwin.go`, backed by `purego`), so the
   `CGO_ENABLED=0` builds keep working. Hand-rolled per-system code is not worth it: macOS
   memory needs a Mach call and Windows has no load average.
   - disk: `disk.UsageWithContext(ctx, diskPath)`, `UsedPercent` and `Free`.
   - memory: `mem.VirtualMemoryWithContext(ctx)`, used % = `(Total - Available) / Total`,
     and `Available`.
   - CPU: `cpu.PercentWithContext(ctx, time.Second, false)`, the whole-host busy % over one
     second. The check therefore takes at least one second. Load average is not used: it
     does not exist on Windows.
   - Call the three behind a small interface so tests inject fakes.
   - Enforcing egress guard: refuse before reading anything, as `checksystemd/checker.go:180`
     does (`egress.DeniedError{Host: "host:" + diskPath}`).
6. Result: the worst status of the configured thresholds. Every metric is stored even when
   it has no threshold: `disk_used_percent`, `disk_free_bytes`, `memory_used_percent`,
   `memory_available_bytes`, `cpu_percent`. The output names the metric(s) that breached and
   their value. A read failure of one source (missing path, unsupported system) is
   `StatusError` with the source in the message.
7. dash0: `web/dash0/src/components/checks/form/types/host.tsx` modelled on `systemd.tsx`,
   added to `common.ts:55` and `index.ts`, with the labels in
   `web/dash0/src/locales/{en,fr,de,es}/checks.json`. Reuse the primitives of the design
   reference page, add `data-testid` on every input.
8. Docs: a `### Host resources {#host}` section in
   `web/docs/docs/features/check-types.md` (next to systemd, line 1424), covering:
   - containers, disk: inside a container `/` is the overlay filesystem. Mount the host path
     read-only (e.g. `-v /:/host:ro`) and set `diskPath` to `/host`.
   - containers, memory and CPU: `/proc` is the host's under plain Docker, but a runtime
     that virtualises `/proc` (lxcfs) shows the container's limits.
   - Kubernetes agents: the figures are the node's, not the pod's.
   - macOS: memory used % stays high by design (the system keeps RAM full of cache), set
     the memory thresholds accordingly or leave them unset.
   - Windows: `diskPath` is a drive or folder path such as `D:\`.
9. `openapi.yaml` and the Terraform / Ansible check schemas if check types are enumerated
   there (see To verify).
10. `CHANGELOG.md` entry, and update the "Hardware metrics" row of
    `wiki/competitors/checkmate.md:28`.

Out of scope: dashboards, per-process data, network I/O, load average, per-core CPU,
several disks in one check (one check per path).

## Tests

- `checkhost/config/validate_test.go`: defaults, percent out of range, warning >= critical
  rejected, relative or unclean `diskPath` rejected, no threshold at all accepted.
- `checkhost/checker_test.go` (table-driven, the three sources faked): all below thresholds
  is up; warning only; critical wins over warning across metrics; metrics present with no
  threshold set; a failing source gives `StatusError`.
- `checkhost/checker_real_test.go`: one test against the real host with no threshold, which
  asserts every metric is present and within range (percents in `[0, 100]`, bytes > 0). It
  runs in the Linux backend job and must also run in the `windows-service` and
  `macos-service` CI jobs (see To verify).
- `checkhost/egress_test.go`: an enforcing guard refuses the check and records the denial,
  and nothing is read (positive control: the same check passes with a non-enforcing guard).
- `checkerdef`: `IsHostLocalType(CheckTypeHost)` is true.
- `web/dash0/src/components/checks/form/types/host.test.ts`: modelled on `systemd.test.ts`.

## To verify

- Every place the `systemd` type is enumerated outside the files above (openapi, Terraform
  provider schema, Ansible modules, sample checks, generated schemas in
  `server/internal/checkers/schemas`): follow the commit that closed issue #1000.
- Whether `StatusWarning` is available to any checker or wired specifically for prometheus.
- That the five cross-build targets of `ci.yml:989` (`linux/arm64`, `linux/riscv64`,
  `windows/amd64`, `darwin/amd64`, `darwin/arm64`) still build with `CGO_ENABLED=0` once
  gopsutil's `cpu`, `mem` and `disk` packages are imported.
- Whether the `windows-service` and `macos-service` CI jobs run Go tests or only the built
  binary. If they only run the binary, exercise the check there through the CLI or the API
  instead of `go test`.
- The binary size increase from gopsutil, to state in the PR description.

## Decisions (Florent, 2026-11-27)

- Linux, macOS and Windows are all supported, through gopsutil.
- The CPU metric is CPU % sampled over one second, not load average.
- One check carries the three metrics (one incident per host, fewer checks against plan
  limits), not one check per metric.
