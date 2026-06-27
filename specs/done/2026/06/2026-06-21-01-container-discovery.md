# Container discovery — enumerate containers on a host and suggest checks

> **Lands after `2026-06-21-00-discovery-check-model-and-generic-scan-api.md`**
> (the check-centric, grouped `discovered_checks` model + the generic
> `{type, parameters}` scan API + the `scantypes` registry). This spec adds a third
> `DiscoverySource` alongside `lan` and `freebox` by **registering a discovery type**
> against that foundation — **no schema migration, no new endpoint**. It still ships on
> top of the existing `docker` check type (already in
> `server/internal/checkers/checkdocker/`) — that is the load-bearing dependency, so
> **no new checker is needed**.
>
> **Order:** first of the discovery-source siblings to land (container → kubernetes,
> `2026-06-21-03`). Both reuse the foundation's `discovered_checks` table unchanged;
> each new source is "register a `scantypes.Definition` + a `JobDefinition`", and adds
> only its own Go-level `source` constant.

## Context

After the foundation spec (`2026-06-21-00`), discovery is **check-centric and
grouped**: a scan writes one `discovered_checks` row per *suggested check*
(`{group_key, group_label, name, slug, type, config, metadata}` + `source`,
`job_uid`, `promoted_to_check_uid`), and the frontend renders them grouped by
`group_key`. Two reference sources already register against it:

- **`lan`** — a CIDR scan (`server/internal/discovery/scanner.go`) that ICMP-pings
  and TCP-probes every address in a range, then maps open ports to grouped suggested
  checks via the authoritative `defaultPorts` table (`ports.go`) and `SuggestChecks`
  (`suggest.go`); one group per host (`group_key = ip`).
- **`freebox`** — pulls the Freebox router's LAN host list and runs it through the
  *same* probe engine (`job_freebox_lan_discovery.go`, `disc.ScanHosts`).

Sources are unified by `models.DiscoverySource` (a Go-level string enum in
`server/internal/db/models/discovered_check.go`), by the generic
`POST /discovery/scans` body `{type, parameters}` routing through the `scantypes`
registry (`server/internal/discovery/scantypes/`), and by the grouped promote/dismiss
UX. Promotion (`POST /discovery/checks/promote {uids:[…]}`) turns selected
`discovered_checks` rows into real `checks`, tagging them `auto-discovery: true` +
`discovery-job: <jobUid>`.

Separately, solidping already ships a **`docker` check type**
(`server/internal/checkers/checkdocker/`). It connects to a Docker-compatible API
endpoint — `unix:///var/run/docker.sock` by default, or `tcp://host:port`
(`config.go:11`, `Validate` at `:137-145`) — via the official Docker SDK
(`client.NewClientWithOpts(client.WithHost(...), client.WithAPIVersionNegotiation())`,
`checker.go:110-115`), then `ContainerInspect`s one named container and reports
`up/down/warning` from its running state and `State.Health.Status`
(`checker.go:140-198`). It already understands Docker's own `HEALTHCHECK`: when the
image declares one, `info.State.Health` drives the verdict; when it does not,
running-vs-exited does. Podman exposes the same Docker-compatible API, so this all
works against Podman unchanged.

What is missing is the **bootstrap**: a user with a host running 30 containers must
today add 30 `docker` checks by hand, typing each container name. "Point me at the
host and tell me what is running" — the discovery story, applied to containers
instead of IPs — does not exist.

## Honest opinion — the key open question: *how do we find containers on a network?*

This is the decision that shapes the whole feature. Three approaches:

**A. Port-scan the LAN for an exposed Docker API (2375/2376), then enumerate.**
Reuse the existing CIDR scanner, add 2375/2376 to the probe set, and for every host
that answers, call `ContainerList`.
- *Pro:* most literal reading of "discover all containers on a network"; reuses the
  scan engine wholesale.
- *Con:* port **2375 is the *unauthenticated*, root-equivalent Docker socket** — a
  well-known critical misconfiguration that essentially nobody leaves open, and
  rightly so. **2376 is TLS-only and needs client certificates** solidping does not
  hold. So in practice this scan finds nothing on a correctly-run network, and
  where it *does* find 2375 it is rewarding an insecure setup. Building the primary
  mechanism on a port that should never be open is the wrong default.

**B. Connect to a *configured* container host (Docker-compatible endpoint).**
The user supplies one or more endpoints — `unix:///var/run/docker.sock` (the local
host, the common self-hosted case) or `tcp://10.0.0.5:2375` (a remote host) — and
we `ContainerList` each.
- *Pro:* reuses the **exact** Docker client the `docker` checker already uses; works
  with the realistic default (the local socket); produces high-quality, named,
  actionable rows (real container names, images, health status, published ports);
  the promoted checks are `docker` checks — closing the loop with HEALTHCHECK
  semantics; no insecure assumptions. Engine-agnostic: a Podman socket works too.
- *Con:* not literally "scan the whole network" — but enumerating containers
  *requires* API access regardless, and only this model gives clean API access.

**C. mDNS / container labels self-advertise.** Containers announce themselves.
- *Pro:* zero-config in theory.
- *Con:* containers do **not** advertise via mDNS by default; this needs a sidecar
  or agent per host — a large build for a niche topology. Not how Docker works.

**Decision: B (configured container host) is the mechanism. A and C are out for
v1.** The primary (and only, in v1) input is an explicit list of Docker-compatible
endpoints. The "flag 2375/2376 during a LAN scan" aid from Approach A is a tidy
follow-up — it satisfies the literal "find them on the network" framing without
making an insecure, almost-always-empty port scan load-bearing — but it is **not**
in this scope; keeping v1 to the configured-endpoint path keeps the change focused
and ships the high-value path first. C (mDNS/labels) is dropped entirely.

For v1, support `unix://` and **plaintext `tcp://`** endpoints only — matching what
the `docker` checker itself supports today (it does not configure TLS client certs
either). mTLS (`2376`) is deferred to a follow-up that stores certs via the existing
encrypted-credentials system (`internal/crypto/credentials/`).

## Goal

An admin selects **Container** as the scan method, enters one or more
Docker-compatible endpoints (prefilled with `unix:///var/run/docker.sock`), and
confirms. A `container_discovery` job connects to each endpoint, lists running
containers, and writes — per container — a **group** of `discovered_checks` rows
(`source = container`, `group_key = container ID`, `group_label = container name`,
`metadata = {image, state, healthStatus, dockerHost}`): always a `docker` check, plus
one HTTP/TCP check per published port. The user reviews the list **grouped by
container** and **promotes** any subset (or a whole container's group) into real
checks — primarily the `docker` check (the HEALTHCHECK analog), optionally HTTP/TCP
checks on published ports — through the existing grouped promote flow, with the
existing `auto-discovery` / `discovery-job` labels.

## Non-goals

- Scanning a CIDR for exposed Docker APIs (Approach A), including the "flag
  2375/2376 during a LAN scan" aid — deferred to a follow-up so v1 stays focused on
  the configured-endpoint path.
- mTLS (`2376`) endpoints / client-certificate management (follow-up).
- Kubernetes / containerd / CRI discovery. v1 is the Docker-compatible API only
  (Docker + Podman).
- Continuous / scheduled re-discovery or keeping checks in sync as containers come
  and go. On-demand only, same as LAN/Freebox.
- Auto-creating checks. The label is applied at promote time, as today.
- Discovering `EXPOSE`-only ports that are not published to the host (they are not
  reachable by a worker, so there is nothing to HTTP/TCP-probe — the `docker` check
  covers those containers instead).

## Design

Four vertical slices, each independently committable. The work reuses the
foundation's `discovered_checks` table and the grouped promote/dismiss UX rather than
building a parallel "discovered_containers" surface — keeping the unified-discovery
investment intact.

### 1. Model + DB: the `container` source (no schema change)

Consumes the `discovered_checks` model from `2026-06-21-00` **unchanged — NO schema
migration.** The check-centric model already carries everything a container needs:
`group_key`, `group_label`, `metadata` (jsonb), and a Go-level `source` enum with no DB
`CHECK` constraint, so a new source value is Go-only. There is no per-container
identity column to add and no IP unique index to rework — the foundation's
`(organization_uid, source, group_key, slug)` partial unique index is keyed on the
*group*, so many containers behind one host IP never collide.

Add **only** the Go source constant to
`server/internal/db/models/discovered_check.go`:

```go
// DiscoverySourceContainer marks a container found on a configured
// Docker-compatible host (Docker or Podman).
DiscoverySourceContainer DiscoverySource = "container"
```

A `container` row maps onto the model as:

- `group_key` = the **Docker container ID** (the stable grouping identity; upsert keys
  on it, so a re-scan updates in place).
- `group_label` = the **container name** (mirrors how the `docker` checker derives its
  name from `ContainerName`).
- `metadata` = `{image, state, healthStatus, dockerHost}` — denormalized group-display
  hints, identical across the group's rows; `dockerHost` is the endpoint the promoted
  `docker` check needs.

### 2. Discovery engine + job

No CIDR fan-out is needed — the endpoint list is small and explicit, so this mirrors
the **single-job** Freebox model (`job_freebox_lan_discovery.go`), not the
plan/child LAN model.

- Job type in `server/internal/jobs/jobdef/types.go`:
  ```go
  // JobTypeContainerDiscovery connects to one or more Docker-compatible API
  // endpoints, lists running containers, and records them in discovered_checks
  // (source='container', one group per container) for operator review and promotion.
  JobTypeContainerDiscovery JobType = "container_discovery"
  ```
  Register the `JobDefinition` in `server/internal/jobs/jobtypes/registry.go` — which
  **also** registers the matching `scantypes.Definition` (slice 4) so the generic
  `POST /discovery/scans` can reach it; the registry wiring keeps the job-layer and
  `scantypes` registrations in lockstep (one source registers in both).

- Implementation `server/internal/jobs/jobtypes/job_container_discovery.go`:
  ```go
  type ContainerDiscoveryConfig struct {
      Hosts   []string `json:"hosts"`             // required, ≥1 Docker endpoints
      Timeout string   `json:"timeout,omitempty"` // per-endpoint, default "10s"
  }
  ```
  `Run` iterates `Hosts`; for each it builds a client exactly as the checker does
  (`client.NewClientWithOpts(client.WithHost(host), client.WithAPIVersionNegotiation())`),
  calls `ContainerList(ctx, container.ListOptions{All: false})` (running containers;
  `All:false` keeps the suggestion set to things worth monitoring), runs each container
  through `SuggestContainerChecks` (slice 3) to build its group of `SuggestedCheck`
  rows, and persists them via the foundation's shared
  `UpsertDiscoveredChecks(ctx, db, orgUID, jobUID, source, rows)` (upsert key
  `(organization_uid, source, group_key, slug)`). A per-endpoint failure is logged and
  skipped (never aborts the run) — same resilience contract as the foundation's
  per-item log-and-continue in `UpsertDiscoveredChecks`.

  Each `types.Container` summary gives `ID`, `Names`, `Image`, `State`, `Status`,
  and `Ports []Port{ PrivatePort, PublicPort, Type }`. **Published** ports are those
  with `PublicPort != 0` — only those are reachable by a worker and become HTTP/TCP
  suggestions; everything else relies on the `docker` check.

The discovery scan engine itself lives next to the existing one. Add
`server/internal/discovery/container.go` exposing
`ListContainers(ctx, endpoint string, timeout time.Duration) ([]DiscoveredContainer, error)`
so the engine is unit-testable against a fake Docker API independently of the job
plumbing (mirrors how `Scan`/`ScanHosts` are testable in isolation).

### 3. Suggested checks — grouped, mirroring Docker's HEALTHCHECK

New `SuggestContainerChecks` in `server/internal/discovery/suggest.go`. It returns the
foundation's grouped `SuggestedCheck` rows (`{GroupKey, GroupLabel, Name, Slug, Type,
Config, Metadata}`), reusing the shared `checkName`/`checkSlug` helpers and the
`defaultPorts` port→scheme mapping. Every row of a container's group shares
`group_key = containerID`, `group_label = containerName`, and
`metadata = {image, state, healthStatus, dockerHost}`:

- **Primary, always emitted — a `docker` check.** This *is* the HEALTHCHECK mirror:
  ```json
  { "type": "docker", "config": { "host": "<dockerHost>", "containerName": "<name>" } }
  ```
  Name/slug are discovery-generated (`group_label · Docker` / a deduped slug). When the
  container's image declares a `HEALTHCHECK`, the promoted `docker` check surfaces
  `State.Health.Status`; when it does not, it reports running-vs-exited. This is the
  universal suggestion — it works even for containers that publish no ports (the common
  "internal service behind a reverse proxy" case).

- **Secondary — one check per *published* port** (`PublicPort != 0`), using the
  **host-published** port and the endpoint host's IP/hostname (what a worker can
  actually reach), with the scheme decided by the container-side port via the existing
  `defaultPorts` table:
  - container port 80 / 8080 → `http` `{ "url": "http://<hostIP>:<publicPort>" }`
  - container port 443 / 8443 → `http` `{ "url": "https://<hostIP>:<publicPort>" }`
  - anything else → `tcp` `{ "host": "<hostIP>", "port": <publicPort> }`

Extend the `checkType*` constants in `suggest.go` with `checkTypeDocker = "docker"`.
`normalizeCheckType` in the promote service already passes unrecognised types through
unchanged, so `docker` → `docker` needs no special case; only `ping` → `icmp` is
remapped. Promotion (`POST /discovery/checks/promote`) then creates checks straight
from each row's stored `config`/`name`/`slug` (with any overrides) — no promote-path
changes beyond the rows existing.

### 4. API + frontend

**Backend** (`server/internal/discovery/scantypes/`):

- Register a `container` discovery type (`scantypes.Definition`):
  `Type() = "container"`, `Source() = DiscoverySourceContainer`. `BuildJob` validates
  the parameters `{ "hosts": []string, "timeout?": string }` — ≥1 endpoint, each one
  `unix://`/`tcp://` (reuse the checker's prefix check) — and returns
  `("container_discovery", cfg)`. A bad endpoint scheme returns the foundation's
  `DISCOVERY_INVALID_PARAMETERS`. No dedicated route and no per-source service method:
  it is reached through the generic
  `POST /api/v1/orgs/:org/discovery/scans` (admin-only) with body:
  ```json
  { "type": "container",
    "parameters": { "hosts": ["unix:///var/run/docker.sock"], "timeout": "10s" } }
  ```
  `Service.StartScan` routes via the registry, applies the existing
  `checkAlreadyRunning(orgUID, "container_discovery")` guard
  (→ `DISCOVERY_ALREADY_RUNNING`), and enqueues the job — no handler/service edits
  beyond registering the type.
- The generic `GET /scans`, `GET /scans/:jobUid`, `POST /scans/:jobUid/cancel`,
  `GET /discovery/checks`, `POST /discovery/checks/promote`, `DELETE
  /discovery/checks/:uid` all work for `container` unchanged — they are type-agnostic
  and the source filter accepts any registered enum value.

**Frontend** (`web/dash0/`):

- `discovery.new.tsx` — add **Container** to the registry-driven type list
  (`DISCOVERY_TYPES`): a parameter sub-form with a "Container host(s)" textarea (one
  endpoint per line, prefilled `unix:///var/run/docker.sock`) + the shared confirmation
  checkbox. Submit dispatches the generic `useStartDiscoveryScan({ type: "container",
  parameters: { hosts, timeout } })` and navigates to the scan detail.
- `discovery.index.tsx` — `container` appears in the registry-driven source filter.
- Scan detail (grouped render) — a `container` group renders the container **name**
  (`group_label`), an image + state badge from `metadata` (`{image, state,
  healthStatus}`), and the group's suggested checks (the `docker` check + published-port
  http/tcp) with per-check checkboxes, "select all in group", **Promote selected**, and
  per-check/per-group dismiss. The grouped list already ships from the foundation; only
  the `container`-source image/state badge is new.
- Promote — unchanged; selection is inline on the grouped list, the `docker` check is
  just another promotable row (config prefilled with `host` + `containerName`).
- `web/dash0/src/api/hooks.ts` — no new hook (the generic `useStartDiscoveryScan`
  covers it); extend the `DiscoverySource` / `canSource` helpers with `container`.
- i18n — add `methodContainer`, `containerHosts`, `containerHostsHelp` to
  `web/dash0/src/locales/{en,fr,de,es}/discovery.json`.

## Decisions (applied 2026-06-21)

1. **Discovery mechanism → B (configured endpoint list).** Connect to an explicit
   list of Docker-compatible endpoints; no network port-scan in v1. The 2375/2376
   LAN-scan flag (Approach A) is deferred to a follow-up.
2. **Source name → `container`.** Engine-agnostic (covers Podman and any future
   Docker-compatible runtime); the promoted *check* type stays `docker`. Keeping the
   two names distinct avoids implying the source is Docker-only.
3. **Container set → running only (`All:false`) in v1.** Stopped/exited containers
   (which would surface something that *should* be up but is down) are a follow-up;
   v1 suggests checks only for what is currently worth monitoring.
4. **`unix://` worker-locality → document, don't block.** A `docker` check promoted
   from a `unix://` discovery only runs on a worker sharing that socket. v1 is
   in-process-worker-only anyway (same as the LAN scan), so this is fine for the
   common single-host self-hoster. The `containerHostsHelp` field text recommends
   `tcp://` endpoints for remote hosts; no blocking UI warning.
5. **Identity → the foundation's `group_key`; no schema change.** The check-centric
   model from `2026-06-21-00` already provides a non-IP grouping identity, so a
   container uses `group_key` = the Docker container ID and is keyed by the foundation's
   `(organization_uid, source, group_key, slug)` partial unique index. This dissolves
   the old "many containers collide on one IP" problem at the model level — no bespoke
   identity column and no migration in this spec (the migration is owned entirely by the
   foundation).

## Files to create / modify

### New (backend)
- `server/internal/discovery/container.go` + `container_test.go` — `ListContainers`
  engine against a fake Docker API.
- `server/internal/jobs/jobtypes/job_container_discovery.go` + test — the
  `container_discovery` job.
- `server/internal/discovery/scantypes/container.go` + test — the `container`
  `scantypes.Definition` (`BuildJob`, `{hosts, timeout}` validation).

> **No migration here** — `discovered_checks` and the `003` migration are owned by the
> foundation (`2026-06-21-00`).

### Modified (backend)
- `server/internal/db/models/discovered_check.go` — add the
  `DiscoverySourceContainer` source constant only.
- `server/internal/discovery/suggest.go` — `SuggestContainerChecks` (grouped rows),
  `checkTypeDocker`.
- `server/internal/jobs/jobdef/types.go` + `jobtypes/registry.go` — register the
  `JobDefinition` and (via the registry wiring) the matching `scantypes.Definition`.

> The generic `POST /discovery/scans` handler/service need **no edits** — the type is
> reached entirely through the registry.

### New / modified (frontend)
- `discovery.new.tsx` (add the `container` type + its parameter sub-form),
  `discovery.index.tsx`, the grouped scan-detail component (container-source
  image/state badge).
- `web/dash0/src/api/hooks.ts` — extend `DiscoverySource` / `canSource` with
  `container` (no new hook; reuses `useStartDiscoveryScan`).
- `web/dash0/src/locales/{en,fr,de,es}/discovery.json`.
- `web/dash0/e2e/discovery.spec.ts` — container method coverage via the generic
  endpoint.

## Verification

- **Unit (table-driven, `testify/require`, `t.Parallel()`):** `SuggestContainerChecks`
  — returns grouped rows sharing one `group_key` (container ID), `group_label`
  (container name), and `metadata`; the `docker` check always present; 80/443/8080/8443
  → http(s) on the *published* port; other published ports → tcp; EXPOSE-only (no
  `PublicPort`) → no port suggestion; slugs deduped within the group.
  `ListContainers` against an `httptest` fake Docker `/containers/json` — name strip,
  image/state/ports mapping, per-endpoint error skip. The `container` `scantypes`
  definition: `BuildJob` validates `{hosts, timeout}` and returns
  `("container_discovery", cfg)`; a bad endpoint scheme → `DISCOVERY_INVALID_PARAMETERS`.
  (No migration round-trip here — `discovered_checks` is the foundation's.)
- **End-to-end** (`make dev-test`, real local Docker): start a container scan via
  `POST /discovery/scans { "type": "container", "parameters": { "hosts":
  ["unix:///var/run/docker.sock"] } }`; confirm running containers render **grouped**
  with a `docker` suggestion + published-port suggestions, select a group and promote,
  confirm the resulting checks carry `auto-discovery: true` and run green.
- **Guards:** invalid endpoint scheme → 400 `DISCOVERY_INVALID_PARAMETERS`; second
  concurrent container scan → 409 `DISCOVERY_ALREADY_RUNNING`; non-admin → 403.
- `make lint && make test && make test-dash`.

## Risk log

| Risk | Mitigation |
|---|---|
| 2375 is an unauthenticated root socket — basing discovery on scanning for it rewards a critical misconfig | v1 mechanism is a *configured* endpoint (B) only; the LAN-scan 2375/2376 flag is deferred to a follow-up, never the engine |
| A `unix://`-discovered `docker` check only runs on a worker sharing that socket | v1 in-process-worker-only (as LAN scan); recommend `tcp://` for remote hosts; documented + optional UI note (decision 4) |
| TLS (2376) endpoints not supported | Out of scope for v1 (matches the checker today); follow-up using `internal/crypto/credentials/` |
| EXPOSE-only ports look monitorable but aren't worker-reachable | Only `PublicPort != 0` ports get HTTP/TCP suggestions; the `docker` check covers the rest |
| Container names/IDs churn between scans | Upsert on the stable `group_key` (the container ID) via the foundation's group identity index; promotion snapshots config into the check, which is then independent (no sync), matching the Freebox stance |

**Status**: Done | **Created**: 2026-06-21 | **Rebased**: 2026-06-27 — rebased onto the check-centric `discovered_checks` model + generic `{type, parameters}` scan API (`2026-06-21-00`): dropped the bespoke identity column + its migration, the dedicated scan route and per-source service method, and the type-specific endpoint error code; the source now registers a `scantypes.Definition`, reuses the foundation's `DISCOVERY_INVALID_PARAMETERS`, and emits grouped suggested-check rows. No schema change in this spec.

## Implementation Plan

Each step is independently committable. Build on the foundation
(`2026-06-21-00`), already on this branch.

### Slice 1 — model source constant
- Add `DiscoverySourceContainer = "container"` to
  `server/internal/db/models/discovered_check.go`. No migration.

### Slice 2 — suggester (`SuggestContainerChecks`)
- In `server/internal/discovery/suggest.go`: add `checkTypeDocker = "docker"`;
  add `SuggestContainerChecks(containerID, name, image, state, healthStatus,
  dockerHost string, ports []ContainerPort) []SuggestedCheck`.
- Always emit the `docker` check (`{host, containerName}`); for each published
  port (`PublicPort != 0`) emit http (80/8080), https (443/8443), or tcp (else),
  targeting the host IP (or the endpoint host) + published port.
- Group identity: `group_key = containerID`, `group_label = name`,
  `metadata = {image, state, healthStatus, dockerHost}`, shared on every row;
  slugs deduped within the group via `checkSlug`/`dedupSlug`.
- Tests in `suggest_container_test.go` (table-driven) covering the verification
  matrix.

### Slice 3 — discovery engine (`ListContainers`)
- New `server/internal/discovery/container.go`: `DiscoveredContainer` +
  `ContainerPort` types; `ListContainers(ctx, endpoint, timeout)` builds the
  Docker client exactly like the checker (`WithHost` + version negotiation),
  calls `ContainerList(All:false)`, maps each summary (name strip, image, state,
  status→healthStatus, published ports).
- `container_test.go`: `httptest` fake Docker API (`/_ping` + `/containers/json`)
  asserting mapping; a non-listening endpoint → error.

### Slice 4 — job + registry + scantype
- `jobdef/types.go`: `JobTypeContainerDiscovery = "container_discovery"`.
- `jobtypes/registry.go`: case for the new definition.
- `jobtypes/job_container_discovery.go`: `ContainerDiscoveryConfig{Hosts,
  Timeout}`; `Run` iterates hosts (per-endpoint log-and-continue), builds rows
  via `SuggestContainerChecks`, persists via `UpsertDiscoveredChecks` (source
  `container`). Test with a fake Docker API + in-memory sqlite.
- `scantypes/container.go`: `ContainerDefinition` (`Type()="container"`,
  `Source()=DiscoverySourceContainer`, `BuildJob` validates `{hosts, timeout?}`
  — ≥1 endpoint, each `unix://`/`tcp://`, else `CodeInvalidParameters`). Register
  in `RegisterDefaults`. Test in `container_test.go` (scantypes pkg).
- Extend `service.go scanJobTypes()` with `container_discovery`; extend the
  activation-guard test map with `container`.

### Slice 5 — frontend
- `discovery.new.tsx`: container parameter sub-form (hosts textarea prefilled
  `unix:///var/run/docker.sock`), builds `{type:"container", parameters:{hosts,
  timeout?}}`.
- `discovery.index.tsx`: `scanSource("container_discovery") → "container"`.
- `discovery.$jobUid.index.tsx`: container metadata badges (image, state).
- i18n: `containerHosts`, `containerHostsHelp`, `methodContainer` in en/fr/de/es
  (`sourceLabel.container` + `method.container` already present).
- `e2e/discovery.spec.ts`: Container method visible + sub-form coverage.

### Slice 6 — QA + audit + archive
- `make build-backend lint-back test` + `make build-dash0 lint-dash`; fix green.
- Inline completeness audit; archive the spec.
