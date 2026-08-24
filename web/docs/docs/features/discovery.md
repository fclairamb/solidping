---
sidebar_position: 15
title: Network Discovery
---

# Network Discovery

Discovery scans a network for monitorable hosts and turns what it finds into
suggested checks — a faster start than adding checks one at a time. Find it
under **Discovery** in the dashboard (`/orgs/:org/discovery`).

Only **one scan can run per organization at a time**; starting a new one while
another is still running or pending is rejected until you stop or wait for
the first to finish.

## Sources

| Source | Scans | Requires |
|---|---|---|
| LAN | One or more CIDR ranges, e.g. `192.168.1.0/24` | Nothing extra |
| Containers | One or more Docker-compatible endpoints (`unix:///var/run/docker.sock` or `tcp://host:2375`) | Nothing extra |
| Freebox | The devices known to a paired Freebox | A granted Freebox integration channel |
| Kubernetes | Workloads and their endpoints in one or more namespaces | A Kubernetes cluster connection |

Freebox and Kubernetes only appear as selectable scan methods once a matching
integration/connection exists.

### LAN scan options

Besides the CIDR list, an **Advanced options** section exposes ports (comma
separated, defaults if left empty), per-probe timeout (e.g. `1s`, `500ms`,
default `1s`), and concurrency (default `64`, capped at `256`). A range
estimated at more than 4096 addresses shows a warning that the scan may take
hours to days, since large ranges are split into bounded chunks that scan
progressively.

### Kubernetes scan options

Pick a cluster connection and, optionally, a comma-separated list of
namespaces — leave it empty to scan every namespace the connection can see.

Every scan requires confirming *"I confirm I own or have permission to scan
the listed network(s)"* before it can start. Scans run on the in-process
worker; on a multi-site deployment, discovery always runs from the server
host, not from a remote agent.

## Lifecycle

A scan moves through `pending` → `running` → `success` or `failed`. While a
LAN scan (the chunked kind) is running, a progress card shows completed vs.
total chunks, plus running counts of groups and checks found so far, and it
can be stopped — chunks already in flight finish, queued chunks are dropped.

## Findings and promoting checks

Discovered hosts are grouped (by host, container, or Kubernetes workload) and
each group carries source-specific hints: open ports and ICMP reachability
for LAN/Freebox, image/state/health for containers, and kind, ready/desired
replica counts, and reachable endpoints for Kubernetes.

Within a group you can select individual suggested checks, or select the
whole group, then **Promote selected** to create them as real checks. A check
that has already been promoted can't be selected or dismissed again.
**Dismiss** removes a single suggested check, or an entire group, without
promoting it — it will not resurface for this scan.

## API

Full endpoint reference: `GET /discovery-types`, `GET /discovery-scans`,
`POST /discovery-scans`, `GET /discovery-scans/:jobUid`, and
`POST /discovery-scans/:jobUid/cancel` under `/api/v1/orgs/:org/...`. The
generated API reference documents every request and response schema under the
**Discovery** tag.
