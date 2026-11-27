---
model: sonnet
effort: medium
pr: batch
automerge: false
---

# Native host resource check (disk / memory / load) without an exporter

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

This check does not fix false positives caused by an overloaded monitoring box: a stalling
box stalls this check too. It only explains them afterwards. Do not write that claim in the
docs or the changelog.

## Proposal

1. New check type `host` in `server/internal/checkers/checkerdef/types.go` (next to
   `CheckTypeSystemd`, `types.go:247` and the `checkTypeInfos` entry at `types.go:512`):
   labels `unsafe`, infrastructure category, plus a `requires:linux` label if such a label
   mechanism fits (see `labelReqSystemd`, `types.go:380`).
2. Add it to `IsHostLocalType` (`server/internal/checkers/checkerdef/localsocket.go:84`) so
   it never runs on a SaaS shared worker. It runs on any other worker that executes it: the
   self-hosted main server and private agents alike.
3. New package `server/internal/checkers/checkhost/` with the same layout as `checksystemd`
   (`checker.go`, `alias.go`, `config/config.go`, `config/validate.go`, tests), registered in
   `server/internal/checkers/registry/registry.go:103`.
4. Config, all optional, camelCase. Thresholds are `*float64` with the same semantics as the
   prometheus check (`server/internal/checkers/checkprometheus/config/config.go:110-144`):
   warning yields `StatusWarning`, critical yields `StatusDown`, comparison is `>=`.
   - `diskPath` (default `/`), `diskWarningPercent`, `diskCriticalPercent`
   - `memoryWarningPercent`, `memoryCriticalPercent`
   - `loadWarning`, `loadCritical` (1-minute load average divided by the CPU count)
   - Validation: percents in `(0, 100]`, warning below critical when both are set, `diskPath`
     absolute and clean with no NUL byte (same rule as `ValidateLocalSocketPath`,
     `localsocket.go:88`).
5. Execution, Linux only, no new direct dependency (`gopsutil` and `procfs` are only
   indirect in `server/go.mod`, do not promote them):
   - disk: `syscall.Statfs(diskPath)`, used % = `(blocks - bfree) / (blocks - bfree + bavail)`
     (what `df` shows).
   - memory: `/proc/meminfo`, used % = `(MemTotal - MemAvailable) / MemTotal`.
   - load: first field of `/proc/loadavg` divided by `runtime.NumCPU()`.
   - Put the Linux code behind a build tag (`host_linux.go`), with a `host_other.go` that
     returns `StatusError` and a clear "the host check needs Linux" message.
   - Enforcing egress guard: refuse before reading anything, as `checksystemd/checker.go:180`
     does (`egress.DeniedError{Host: "host:" + diskPath}`).
6. Result: the worst status of the configured thresholds. Every metric is stored even when
   it has no threshold: `disk_used_percent`, `disk_free_bytes`, `memory_used_percent`,
   `memory_available_bytes`, `load1`, `load1_per_cpu`. The output names the metric(s) that
   breached and their value. A read failure of one source (missing path, no `/proc`) is
   `StatusError` with the path in the message.
7. dash0: `web/dash0/src/components/checks/form/types/host.tsx` modelled on `systemd.tsx`,
   added to `common.ts:55` and `index.ts`, with the labels in
   `web/dash0/src/locales/{en,fr,de,es}/checks.json`. Reuse the primitives of the design
   reference page, add `data-testid` on every input.
8. Docs: a `### Host resources {#host}` section in
   `web/docs/docs/features/check-types.md` (next to systemd, line 1424), with the container
   caveat spelled out:
   - disk: inside a container `/` is the overlay filesystem. Mount the host path read-only
     (e.g. `-v /:/host:ro`) and set `diskPath` to `/host`.
   - memory and load: `/proc/meminfo` and `/proc/loadavg` are the host's under plain Docker,
     but a runtime that virtualises `/proc` (lxcfs) shows the container's limits.
   - Kubernetes agents: the figures are the node's, not the pod's.
9. `openapi.yaml` and the Terraform / Ansible check schemas if check types are enumerated
   there (see To verify).
10. `CHANGELOG.md` entry, and update the "Hardware metrics" row of
    `wiki/competitors/checkmate.md:28`.

Out of scope: dashboards, per-process data, network I/O, CPU % sampled over an interval,
several disks in one check (one check per path).

## Tests

- `checkhost/config/validate_test.go`: defaults, percent out of range, warning >= critical
  rejected, relative or unclean `diskPath` rejected, no threshold at all accepted.
- `checkhost/checker_test.go` (table-driven, sources injected as readers / a statfs func):
  all below thresholds is up; warning only; critical wins over warning across metrics;
  metrics present with no threshold set; malformed `/proc/meminfo` and missing path give
  `StatusError`.
- `checkhost/egress_test.go`: an enforcing guard refuses the check and records the denial,
  and nothing is read (positive control: the same check passes with a non-enforcing guard).
- `checkerdef`: `IsHostLocalType(CheckTypeHost)` is true.
- `web/dash0/src/components/checks/form/types/host.test.ts`: modelled on `systemd.test.ts`.

## To verify

- Every place the `systemd` type is enumerated outside the files above (openapi, Terraform
  provider schema, Ansible modules, sample checks, generated schemas in
  `server/internal/checkers/schemas`): follow the commit that closed issue #1000.
- Whether `StatusWarning` is available to any checker or wired specifically for prometheus.
- Whether the build must stay green on darwin and windows for the agent binary (build tags
  in step 5 assume yes).

## Open questions

- macOS and Windows hosts: support them now? Recommended: no, Linux only, with the clear
  error on other systems. Revisit if someone asks.
- CPU metric: normalised 1-minute load, or CPU % sampled over one second? Recommended:
  load, it is one read and needs no sampling window.
- One check with the three metrics, or one check per metric? Recommended: one check with
  the three metrics (one incident per host, fewer checks against plan limits).
