# Container discovery — enumerate containers on a host and suggest checks

> Builds on the existing discovery feature (`2026-05-18-01-network-discovery.md`,
> `2026-05-24-08-freebox-lan-discovery.md`) and the unified scan-method selector
> (`2026-05-25-09-discovery-unified-scan-method-selector-frontend.md`). Adds a
> third `DiscoverySource` alongside `lan` and `freebox`. Ships only after the
> `docker` check type (already in `server/internal/checkers/checkdocker/`) — it is
> the load-bearing dependency, so no new checker is needed.

## Context

solidping already has two discovery sources, both modelled as background jobs
that write into the shared `discovered_hosts` table and feed one promote/dismiss
UX:

- **`lan`** — a CIDR scan (`server/internal/discovery/scanner.go`) that ICMP-pings
  and TCP-probes every address in a range, then maps open ports to suggested
  checks via the authoritative `defaultPorts` table (`ports.go`) and
  `SuggestChecks` (`suggest.go`).
- **`freebox`** — pulls the Freebox router's LAN host list and runs it through the
  *same* probe engine (`job_freebox_lan_discovery.go`, `disc.ScanHosts`).

The sources are unified by `models.DiscoverySource` (`server/internal/db/models/discovered_host.go:13-21`),
a closed string enum, and by the unified "Start new scan" selector on the frontend
(LAN / Freebox today). Promotion (`server/internal/handlers/discovery/service.go:561`,
`PromoteHost`) turns any discovered row's `suggested_checks` into real `checks`,
tagging them `auto-discovery: true` + `discovery-job: <jobUid>`.

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
containers, and writes one `discovered_hosts` row per container (`source = container`)
carrying the container name, image, state, published ports, and a set of suggested
checks. The user reviews the list and **promotes** containers into real checks —
primarily a `docker` check (the HEALTHCHECK analog), optionally HTTP/TCP checks on
published ports — through the existing promote flow, with the existing
`auto-discovery` / `discovery-job` labels.

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

Four vertical slices, each independently committable. The work deliberately reuses
the `discovered_hosts` table and the promote/dismiss UX rather than building a
parallel "discovered_containers" surface — keeping the unified-discovery investment
intact.

### 1. Model + DB: the `container` source and per-container identity

The `discovered_hosts` table is IP-centric: `ip INET NOT NULL` plus a partial unique
index on `(organization_uid, ip, source)`. Many containers share one host IP, so
that index would collide. The container's stable identity is its **container ID**.

`server/internal/db/models/discovered_host.go`:

- Add the source constant:
  ```go
  // DiscoverySourceContainer marks a container found on a configured
  // Docker-compatible host (Docker or Podman).
  DiscoverySourceContainer DiscoverySource = "container"
  ```
- Add two nullable fields (no-ops for `lan`/`freebox`):
  ```go
  ContainerID *string         `bun:"container_id"            json:"containerId,omitempty"`
  Metadata    json.RawMessage `bun:"metadata,type:jsonb"     json:"metadata,omitempty"`
  ```
  `hostname` reuses the existing column for the **container name** (mirrors how the
  `docker` checker derives its name from `ContainerName`). `metadata` holds
  `{image, state, healthStatus, dockerHost}` — `dockerHost` is the endpoint the
  promoted `docker` check needs.

Migration (new `NNN_*.up.sql` + `.down.sql`, Postgres + SQLite mirror, following the
consolidated-per-release convention in `server/CLAUDE.md`):

```sql
ALTER TABLE discovered_hosts ADD COLUMN container_id TEXT;
ALTER TABLE discovered_hosts ADD COLUMN metadata     JSONB;

-- Scope the existing IP-uniqueness to the IP-based sources only…
DROP INDEX idx_discovered_hosts_org_ip_active;
CREATE UNIQUE INDEX idx_discovered_hosts_org_ip_active
  ON discovered_hosts (organization_uid, ip, source)
  WHERE deleted_at IS NULL AND promoted_to_check_uid IS NULL
        AND source <> 'container';

-- …and give containers their own identity index on container_id.
CREATE UNIQUE INDEX idx_discovered_hosts_org_container_active
  ON discovered_hosts (organization_uid, container_id)
  WHERE deleted_at IS NULL AND promoted_to_check_uid IS NULL
        AND source = 'container';
```

`ip` for a container row is the endpoint host's resolved IP (or `127.0.0.1` for a
local `unix://` socket) — it stays populated so the `NOT NULL` constraint and the
host-list UI keep working.

### 2. Discovery engine + job

No CIDR fan-out is needed — the endpoint list is small and explicit, so this mirrors
the **single-job** Freebox model (`job_freebox_lan_discovery.go`), not the
plan/child LAN model.

- Job type in `server/internal/jobs/jobdef/types.go`:
  ```go
  // JobTypeContainerDiscovery connects to one or more Docker-compatible API
  // endpoints, lists running containers, and records them in discovered_hosts
  // (source='container') for operator review and promotion.
  JobTypeContainerDiscovery JobType = "container_discovery"
  ```
  Register in `server/internal/jobs/jobtypes/registry.go`.

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
  `All:false` keeps the suggestion set to things worth monitoring), and for each
  container builds a `DiscoveredHost`. A per-endpoint failure is logged and skipped
  (never aborts the run) — same resilience contract as
  `FreeboxLanDiscoveryJobRun.persistHosts`. Persist via upsert keyed on
  `(organization_uid, container_id)`.

  Each `types.Container` summary gives `ID`, `Names`, `Image`, `State`, `Status`,
  and `Ports []Port{ PrivatePort, PublicPort, Type }`. **Published** ports are those
  with `PublicPort != 0` — only those are reachable by a worker and become HTTP/TCP
  suggestions; everything else relies on the `docker` check.

The discovery scan engine itself lives next to the existing one. Add
`server/internal/discovery/container.go` exposing
`ListContainers(ctx, endpoint string, timeout time.Duration) ([]DiscoveredContainer, error)`
so the engine is unit-testable against a fake Docker API independently of the job
plumbing (mirrors how `Scan`/`ScanHosts` are testable in isolation).

### 3. Suggested checks — mirroring Docker's HEALTHCHECK

New `SuggestContainerChecks` in `server/internal/discovery/suggest.go`, reusing the
existing `SuggestedCheck` struct and the `defaultPorts` port→scheme mapping:

- **Primary, always emitted — a `docker` check.** This *is* the HEALTHCHECK mirror:
  ```json
  { "type": "docker", "config": { "host": "<dockerHost>", "containerName": "<name>" } }
  ```
  When the container's image declares a `HEALTHCHECK`, the promoted `docker` check
  surfaces `State.Health.Status`; when it does not, it reports running-vs-exited.
  This is the universal suggestion — it works even for containers that publish no
  ports (the common "internal service behind a reverse proxy" case).

- **Secondary — one check per *published* port**, using the **host-published** port
  and the endpoint host's IP/hostname (what a worker can actually reach), with the
  scheme decided by the container-side port via the existing `defaultPorts` table:
  - container port 80 / 8080 → `http` `{ "url": "http://<hostIP>:<publicPort>" }`
  - container port 443 / 8443 → `http` `{ "url": "https://<hostIP>:<publicPort>" }`
  - anything else → `tcp` `{ "host": "<hostIP>", "port": <publicPort> }`

Extend the `checkType*` constants in `suggest.go` with `checkTypeDocker = "docker"`.
`normalizeCheckType` in the promote service (`service.go:659`) already passes
unrecognised types through unchanged, so `docker` → `docker` needs no special case;
only `ping` → `icmp` is remapped. `PromoteHost` then builds the check config from the
suggested `docker` check's config merged with any `extraConfig` — no promote-path
changes beyond the suggestion existing.

### 4. API + frontend

**Backend** (`server/internal/handlers/discovery/`):

- New route `POST /api/v1/orgs/:org/discovery/container-scans` (admin-only),
  mirroring the Freebox `POST /freebox-scans` registration in `handler.go:58-67`.
  Body: `{ "hosts": ["unix:///var/run/docker.sock"], "timeout": "10s" }`.
- `Service.StartContainerScan(ctx, orgUID, cfg)` mirrors `StartFreeboxScan`
  (`service.go:146`): validate ≥1 endpoint and each is `unix://`/`tcp://`
  (reuse the checker's prefix check), guard with the existing per-type
  `checkAlreadyRunning(orgUID, JobTypeContainerDiscovery)` (`service.go:192`), then
  `jobSvc.CreateJob`. New error code `DISCOVERY_INVALID_ENDPOINT` for a bad scheme.
- The existing `GET /scans`, `GET /scans/:jobUid`, `GET /hosts`, `POST
  /hosts/:uid/promote`, `DELETE /hosts/:uid` all work unchanged — they key on
  job type / `source`, and the source filter already accepts an enum value.

**Frontend** (`web/dash0/`):

- `discovery.new.tsx` — add **Container** as a third option to the existing
  scan-method `Select` (the unified selector from spec `2026-05-25-09`). When
  selected: a "Container host(s)" textarea (one endpoint per line, prefilled
  `unix:///var/run/docker.sock`) + the shared confirmation checkbox; submit
  dispatches a new `useStartContainerScan` hook and navigates to the scan detail.
- `discovery.index.tsx` — add `container` to the source-filter `Select`.
- Scan detail / host list — render container rows: container **name** (from
  `hostname`), an image + state badge from `metadata`, published ports from
  `open_ports`, and the suggested checks. The list already renders
  hostname/open_ports/suggested_checks, so containers slot in; only the
  image/state badge is new.
- Promote page — unchanged; `docker` simply appears as a selectable suggested type,
  prefilled with `host` + `containerName`.
- `web/dash0/src/api/hooks.ts` — add `useStartContainerScan`; extend the
  `DiscoverySource` / `canSource` helpers with `container`.
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

## Files to create / modify

### New (backend)
- `server/internal/discovery/container.go` + `container_test.go` — `ListContainers`
  engine against a fake Docker API.
- `server/internal/jobs/jobtypes/job_container_discovery.go` + test.
- Migration up/down (Postgres + SQLite) adding `container_id` / `metadata` and the
  reworked unique indexes.

### Modified (backend)
- `server/internal/db/models/discovered_host.go` — `DiscoverySourceContainer`,
  `ContainerID`, `Metadata`.
- `server/internal/discovery/suggest.go` — `SuggestContainerChecks`,
  `checkTypeDocker`.
- `server/internal/jobs/jobdef/types.go` + `jobtypes/registry.go` — register the job.
- `server/internal/handlers/discovery/{handler,service}.go` + tests —
  `container-scans` route, `StartContainerScan`, endpoint validation.

### New / modified (frontend)
- `discovery.new.tsx`, `discovery.index.tsx`, host-list / scan-detail components.
- `web/dash0/src/api/hooks.ts` — `useStartContainerScan`, `canSource`.
- `web/dash0/src/locales/{en,fr,de,es}/discovery.json`.
- `web/dash0/e2e/discovery.spec.ts` — container method coverage.

## Verification

- **Unit (table-driven, `testify/require`, `t.Parallel()`):** `SuggestContainerChecks`
  — docker check always present; 80/443/8080/8443 → http(s) on the *published* port;
  other published ports → tcp; EXPOSE-only (no `PublicPort`) → no port suggestion.
  `ListContainers` against an `httptest` fake Docker `/containers/json` — name strip,
  image/state/ports mapping, per-endpoint error skip.
- **Migration round-trip** (Postgres + SQLite): apply + `migrate down 1` + up;
  confirm both unique indexes and that a re-scan upserts on `container_id` (not IP).
- **End-to-end** (`make dev-test`, real local Docker): start a container scan against
  `unix:///var/run/docker.sock`, confirm running containers appear with a `docker`
  suggestion + published-port suggestions, promote one, confirm the resulting check
  carries `auto-discovery: true` and runs green.
- **Guards:** invalid endpoint scheme → 400 `DISCOVERY_INVALID_ENDPOINT`; second
  concurrent container scan → `DISCOVERY_ALREADY_RUNNING`.
- `make lint && make test && make test-dash`.

## Risk log

| Risk | Mitigation |
|---|---|
| 2375 is an unauthenticated root socket — basing discovery on scanning for it rewards a critical misconfig | v1 mechanism is a *configured* endpoint (B) only; the LAN-scan 2375/2376 flag is deferred to a follow-up, never the engine |
| A `unix://`-discovered `docker` check only runs on a worker sharing that socket | v1 in-process-worker-only (as LAN scan); recommend `tcp://` for remote hosts; documented + optional UI note (decision 4) |
| Many containers per host would collide on the IP-based unique index | Container rows use a separate `(org, container_id)` partial unique index; IP index scoped to `source <> 'container'` |
| TLS (2376) endpoints not supported | Out of scope for v1 (matches the checker today); follow-up using `internal/crypto/credentials/` |
| EXPOSE-only ports look monitorable but aren't worker-reachable | Only `PublicPort != 0` ports get HTTP/TCP suggestions; the `docker` check covers the rest |
| Container names/IDs churn between scans | Upsert on stable `container_id`; promotion snapshots config into the check, which is then independent (no sync), matching the Freebox stance |
