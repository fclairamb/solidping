# Kubernetes discovery — enumerate Deployments/ReplicaSets on a cluster and suggest checks

> Sibling of the container-discovery spec (`2026-06-21-01-container-discovery.md`), which
> it deliberately mirrors: both add a new `DiscoverySource` alongside `lan` and `freebox`
> and reuse the shared `discovered_hosts` table + promote/dismiss UX.
>
> **Depends on `2026-06-21-02-kubernetes-checker.md`** (the prerequisite, which must land
> first). That spec builds the two foundations this one assumes already exist: the
> `kubernetes` check type (replica-health) and the encrypted **cluster connection**
> (`clusterUid`) plus its `kubernetesClient(clusterUid)` resolver. This spec adds *only*
> the discovery story on top — it does not build a checker or a connection.
>
> **Order:** third of three (container-discovery → kubernetes-checker → **kubernetes-
> discovery**). It reuses the shared `resource_uid` + `metadata` columns and the
> `003_discovery_resources` migration introduced by container-discovery (`01`) — see
> slice 1.

## Context

solidping has two discovery sources, both background jobs that write into the shared
`discovered_hosts` table and feed one promote/dismiss UX:

- **`lan`** — a CIDR scan (`server/internal/discovery/scanner.go`, `Scan`/`ScanHosts`)
  that ICMP-pings and TCP-probes every address, then maps open ports to suggested checks
  via the authoritative `defaultPorts` table (`ports.go:20-37`) and `SuggestChecks`
  (`suggest.go:18`).
- **`freebox`** — pulls a paired Freebox router's LAN host list through the *same* probe
  engine (`job_freebox_lan_discovery.go`). It is driven by a **stored, granted
  integration**: the job config carries only a `channelUid` (`FreeboxLanDiscoveryConfig`,
  `job_freebox_lan_discovery.go:25-27`), and the credential behind it is resolved at run
  time (`server/internal/integrations/freebox/lanlookup.go:34-124`).

The sources are unified by `models.DiscoverySource` (a closed string enum,
`server/internal/db/models/discovered_host.go:16-21`) and by the "Start new scan" selector
on the frontend (`discovery.new.tsx:38,165-178`, LAN / Freebox today). Promotion
(`handlers/discovery/service.go:561`, `PromoteHost`) turns any discovered row's
`suggested_checks` into real `checks`, tagging them `auto-discovery: true` +
`discovery-job: <jobUid>`. The per-source start endpoint, validation, and concurrency
guard are all per-job-type (`checkAlreadyRunning`, `service.go:192`; source filter,
`service.go:490-536`), so a new source slots in cleanly.

Two prerequisites are now in place when this spec runs:
- **From `02` (kubernetes-checker):** the `kubernetes` check type and the cluster
  connection (`clusterUid` + `kubernetesClient` resolver). Discovery's promoted checks are
  `kubernetes` checks; its scan job authenticates via the same `clusterUid`.
- **From `01` (container-discovery):** the shared `resource_uid` + `metadata` columns on
  `discovered_hosts` and the `idx_discovered_hosts_org_resource_active` identity index —
  the model pressure (many resources behind one API-server IP) is identical, so this spec
  reuses them rather than adding its own column.

What is missing is the **enumeration**: a user running 40 Deployments must add 40 checks
by hand. "Point me at the cluster and tell me what is running" — the discovery story
applied to **workloads** — does not exist.

## Non-goals

- **Building the `kubernetes` checker or the cluster connection** — those are `02`.
- **Discovering ClusterIP-only workloads as HTTP/TCP targets.** Pod IPs and ClusterIP
  Services are not reachable from an out-of-cluster worker, so there is nothing to
  HTTP/TCP-probe — the `kubernetes` replica-health check covers those. Only `NodePort` /
  `LoadBalancer` Services and `Ingress` hosts yield endpoint suggestions (the analog of
  container-discovery's "published ports only").
- **Workload kinds beyond Deployment / bare ReplicaSet** — a follow-up on the same code
  path (and the checker already only supports those two).
- **Continuous / scheduled re-discovery or keeping checks in sync** as Deployments scale
  and roll. On-demand only, same as LAN/Freebox.
- **Writing to the cluster.** Strictly read-only `list`/`get`.
- **Auto-creating checks.** The `auto-discovery` label is applied at promote time, as today.

## Design

Four vertical slices, each independently committable, mirroring container-discovery
slice-for-slice. The work reuses the `discovered_hosts` table and promote/dismiss UX
rather than building a parallel surface — keeping the unified-discovery investment intact.

### 1. Model + DB: the `kubernetes` source and per-workload identity

The `discovered_hosts` table is IP-centric (`ip INET NOT NULL` + the partial unique index
`idx_discovered_hosts_org_ip_source_active` on `(organization_uid, ip, source)`). Many
workloads share one API-server IP, so that index would collide. A workload's stable
identity is its Kubernetes **`metadata.uid`**.

This is the *same* model pressure container-discovery hit (many containers, one host IP),
so **reuse the shared identity column** rather than adding a `kubernetes_uid`:

- Add the source constant in `server/internal/db/models/discovered_host.go` (enum at
  `:16-21`):
  ```go
  // DiscoverySourceKubernetes marks a Deployment/ReplicaSet found on a configured
  // Kubernetes cluster connection.
  DiscoverySourceKubernetes DiscoverySource = "kubernetes"
  ```
- **Reuse** the generic `ResourceUID *string` (`resource_uid`) and `Metadata
  json.RawMessage` fields introduced by container-discovery (`01`). For a `kubernetes`
  row, `resource_uid` holds the workload's `metadata.uid`; `hostname` reuses the existing
  column for the workload's `namespace/name` (the human label, e.g. `prod/api-server`);
  `metadata` holds `{clusterUid, kind, namespace, name, images, desiredReplicas,
  readyReplicas, availableReplicas, conditions, endpoints}` —
  `clusterUid`+`kind`+`namespace`+`name` are what the promoted `kubernetes` check needs.

**Migration — reused, not re-added.** Container-discovery's `003_discovery_resources`
migration already adds `resource_uid` + `metadata` and the reworked indexes
(`idx_discovered_hosts_org_ip_source_active` scoped to `source IN ('lan','freebox')` plus
`idx_discovered_hosts_org_resource_active` on `(organization_uid, source, resource_uid)`).
Since `01` lands first, **this slice adds no migration** — only the Go `DiscoverySourceKubernetes`
constant. *(If `01` has not shipped for some reason, create `003_discovery_resources` exactly
as specified there — the two specs share one migration by design; never add a second
identity column.)* `ip` for a Kubernetes row is the API server's resolved IP (or
`127.0.0.1` for an in-cluster connection) so the `NOT NULL` constraint and host-list UI keep
working.

### 2. Discovery engine + job

No CIDR fan-out and no active network probe — the cluster API returns everything needed as
metadata, so this mirrors container-discovery's metadata-derived approach (not Freebox's
`ScanHosts` probe), wrapped in the **single-job** Freebox shape
(`job_freebox_lan_discovery.go`), not the plan/child LAN model.

- Job type in `server/internal/jobs/jobdef/types.go` (Freebox at `:39`):
  ```go
  // JobTypeKubernetesDiscovery connects to a configured Kubernetes cluster, lists
  // Deployments and bare ReplicaSets, and records them in discovered_hosts
  // (source='kubernetes') for operator review and promotion.
  JobTypeKubernetesDiscovery JobType = "kubernetes_discovery"
  ```
  Wire into the registration switch in `jobtypes/registry.go:8-39` (Freebox at `:32-33`).
- Engine `server/internal/discovery/kubernetes.go` exposing
  ```go
  func ListWorkloads(ctx context.Context, client kubernetes.Interface,
      namespaces []string, timeout time.Duration) ([]DiscoveredWorkload, error)
  ```
  so it is unit-testable against a `client-go` `fake.Clientset` independently of the job
  plumbing (mirrors how `Scan`/`ScanHosts` and `01`'s `ListContainers` are testable in
  isolation). It lists Deployments + ReplicaSets (skipping ReplicaSets with a Deployment
  `ownerReference`), and lists `NodePort`/`LoadBalancer` Services + Ingresses, best-effort
  matching them to workloads by label selector to populate `endpoints`. The `client` is
  built by `02`'s `kubernetesClient(clusterUid)` resolver — this engine takes the interface,
  not a `clusterUid`, keeping it pure.
- Implementation `server/internal/jobs/jobtypes/job_kubernetes_discovery.go`, mirroring
  `FreeboxLanDiscovery*`:
  ```go
  type KubernetesDiscoveryConfig struct {
      ClusterUID string   `json:"clusterUid"`           // required (the 02 connection)
      Namespaces []string `json:"namespaces,omitempty"` // empty = all visible
      Timeout    string   `json:"timeout,omitempty"`    // default "30s"
  }
  ```
  `Run` builds the clientset via `02`'s `kubernetesClient`, calls `ListWorkloads`, and for
  each workload builds a `models.DiscoveredHost`
  (`NewDiscoveredHost(..., DiscoverySourceKubernetes)`, `discovered_host.go:40-51`).
  Per-workload build/upsert errors are logged `Warn` and `continue`d — never abort the run
  — the exact resilience contract of `FreeboxLanDiscoveryJobRun.persistHosts`
  (`job_freebox_lan_discovery.go:120-162`). Persist via upsert keyed on
  `(organization_uid, source, resource_uid)` (the shared identity index from `01`),
  parallel to the Freebox `ON CONFLICT (organization_uid, ip, source)` upsert.

### 3. Suggested checks — the workload health monitor + reachable endpoints

New `SuggestKubernetesChecks` in `server/internal/discovery/suggest.go`, reusing the
`SuggestedCheck` struct (`:12-15`) and the `defaultPorts` port→scheme mapping
(`ports.go:20-37`):

- **Primary, always emitted — a `kubernetes` check.** This *is* the replica-health monitor
  from `02`:
  ```json
  { "type": "kubernetes",
    "config": { "clusterUid": "<uid>", "namespace": "<ns>",
                "kind": "Deployment", "name": "<name>" } }
  ```
  Universal — it works even for a workload that exposes nothing externally (the common
  "internal service behind a ClusterIP" case).
- **Secondary — one check per externally-reachable endpoint** the engine matched in slice
  2, using the worker-reachable address and the scheme decided by the service/target port
  via `defaultPorts`:
  - `LoadBalancer` → `status.loadBalancer.ingress[].ip|hostname` + service port.
  - `NodePort` → a node IP + the allocated `nodePort`.
  - `Ingress` → `spec.rules[].host` (+ path) → `http`/`https`.
  - port 80 / 8080 → `http`, 443 / 8443 → `https`, anything else → `tcp`.

Add `checkTypeKubernetes = "kubernetes"` to the `checkType*` constants (`suggest.go:6-9`).
`normalizeCheckType` (`service.go:659`) only remaps `ping → icmp`; `kubernetes → kubernetes`
passes through unchanged, and `PromoteHost` (`service.go:561`) builds the check config from
the suggested check's config merged with any `extraConfig` — no promote-path changes beyond
the suggestion existing.

### 4. API + frontend

**Backend** (`server/internal/handlers/discovery/`):

- New route `POST /api/v1/orgs/:org/discovery/kubernetes-scans` (admin-only via `isAdmin`,
  `handler.go:33-40`), mirroring the Freebox `POST /freebox-scans` registration
  (`handler.go:60`, handler at `:76`). Body: `{ "clusterUid": "...", "namespaces": [],
  "timeout": "30s" }`.
- `Service.StartKubernetesScan(ctx, orgUID, cfg)` mirrors `StartFreeboxScan`
  (`service.go:146`): validate the `clusterUid` resolves to a registered connection
  (fail-fast, like Freebox validates the channel), guard with the existing per-type
  `checkAlreadyRunning(orgUID, JobTypeKubernetesDiscovery)` (`service.go:192`), then
  `jobSvc.CreateJob`. Reuse `DISCOVERY_ALREADY_RUNNING` (`handler.go:24`); add
  `KUBERNETES_CLUSTER_NOT_FOUND` for an unknown `clusterUid` (parallel to
  `FREEBOX_NOT_GRANTED`, `handler.go:27`).
- The existing `GET /scans`, `GET /scans/:jobUid`, `GET /hosts`, `POST /hosts/:uid/promote`,
  `DELETE /hosts/:uid` all work unchanged — they key on job type / `source`, and the source
  filter already accepts any enum value (`service.go:490-536`).

**Frontend** (`web/dash0/`) — *all new UI reuses the design-reference primitives per
`web/dash0/CLAUDE.md`; start from `design-reference.tsx`*. (The "Kubernetes clusters"
management surface is built in `02`; this slice only consumes existing connections.)

- `discovery.new.tsx` — extend the `ScanMethod` type (`:38`, currently `"lan" | "freebox"`)
  and the scan-method `Select` (`:165-178`) with **Kubernetes** (shown only when ≥1 cluster
  connection exists, exactly as Freebox is gated on granted channels at `:50-58`). When
  selected: a cluster `Select` + an optional "namespaces" input + the shared confirmation
  checkbox; submit dispatches a new `useStartKubernetesScan` hook and navigates to scan
  detail.
- `discovery.index.tsx` — extend `scanSource` (`:51-53`) and the source-filter `Select`
  (`:136-148`) with `kubernetes`.
- Scan detail / host list (`discovery.$jobUid.index.tsx`, `HostRow` at `:57-125`, headers
  `:261-270`) — render workload rows: `namespace/name` (from `hostname`), a kind +
  `ready/desired` replica badge from `metadata`, endpoints from `metadata`, and the
  suggested checks. The list already renders hostname/openPorts/suggestedChecks, so
  workloads slot in; only the kind+replica badge is new.
- Promote page (`discovery.$jobUid.$hostUid.promote.tsx`) — unchanged; `kubernetes` appears
  as a selectable suggested type, prefilled with `clusterUid`+`namespace`+`kind`+`name`.
- `web/dash0/src/api/hooks.ts` — extend `DiscoverySource` (`:3229`, currently
  `"lan" | "freebox"`) with `kubernetes`; add `useStartKubernetesScan` (mirror
  `useStartFreeboxScan`, `:3278-3293`); extend `canSource`/`CAPABILITIES` (`:2742-2764`) so
  `kubernetes` is sourceable.
- i18n — add `methodKubernetes`, `selectCluster`, `clusterNamespaces`,
  `kubernetesScanStarted`, `sourceKubernetes`, replica/kind labels to
  `web/dash0/src/locales/{en,fr,de,es}/discovery.json` (existing source/method keys:
  `sourceLan`/`sourceFreebox` ~`:67-68`, `methodLan`/`methodFreebox` ~`:72-73`).

## Decisions (applied 2026-06-21)

1. **Workload set → Deployments + bare ReplicaSets in v1.** Deployment-owned ReplicaSets
   are folded into their Deployment (not surfaced twice); StatefulSet / DaemonSet / etc. are
   a follow-up on the same code path (and the `02` checker supports only these two kinds).
2. **Source name → `kubernetes`.** Distinct from the `docker`-flavoured `container` source;
   the promoted check type is `kubernetes`.
3. **Identity column → reuse the shared generic `resource_uid` + `metadata`** from
   container-discovery (`01`'s decision 5), so the two sibling specs do not each bolt a
   bespoke identity column onto `discovered_hosts`. For Kubernetes, `resource_uid` =
   `metadata.uid`.
4. **Reachability → only `NodePort`/`LoadBalancer`/`Ingress` endpoints** get HTTP/TCP
   suggestions; ClusterIP services and pod IPs are not worker-reachable, so the `kubernetes`
   check covers those (the "published ports only" analog).

## Files to create / modify

### New (backend)
- `server/internal/discovery/kubernetes.go` + `kubernetes_test.go` — `ListWorkloads`
  against a `client-go` `fake.Clientset`.
- `server/internal/jobs/jobtypes/job_kubernetes_discovery.go` + test.

### Modified (backend)
- `server/internal/db/models/discovered_host.go` — `DiscoverySourceKubernetes` (reuses the
  `ResourceUID`/`Metadata` fields added by `01`; **no migration here** — `003_discovery_resources`
  already exists).
- `server/internal/discovery/suggest.go` — `SuggestKubernetesChecks`, `checkTypeKubernetes`.
- `server/internal/jobs/jobdef/types.go` + `jobtypes/registry.go` — register the job.
- `server/internal/handlers/discovery/{handler,service}.go` + tests — `kubernetes-scans`
  route, `StartKubernetesScan`, cluster validation, `KUBERNETES_CLUSTER_NOT_FOUND`.

### New / modified (frontend)
- `discovery.new.tsx`, `discovery.index.tsx`, host-list / scan-detail components.
- `web/dash0/src/api/hooks.ts` — `useStartKubernetesScan`, `DiscoverySource`, `canSource`.
- `web/dash0/src/locales/{en,fr,de,es}/discovery.json`.
- `web/dash0/e2e/discovery.spec.ts` — Kubernetes method coverage.

## Verification

- **Unit (table-driven, `testify/require`, `t.Parallel()` — `server/CLAUDE.md`):**
  - `ListWorkloads` against a `fake.Clientset`: Deployments + bare ReplicaSets surfaced,
    Deployment-owned ReplicaSets skipped; LoadBalancer/NodePort/Ingress matched to endpoints
    by selector; ClusterIP → no endpoint; per-namespace error skip.
  - `SuggestKubernetesChecks`: `kubernetes` check always present; LB/NodePort/Ingress →
    http(s)/tcp per `defaultPorts`; ClusterIP-only → no endpoint suggestion.
- **Re-scan upsert** (Postgres + SQLite): a second scan of the same cluster upserts on
  `resource_uid` (`metadata.uid`), not IP — no duplicate rows when the API-server IP is shared.
- **End-to-end** (`make dev-test`, against a `kind`/minikube cluster or a stored kubeconfig,
  with `02` already merged): register a cluster (via `02`'s UI), start a Kubernetes scan,
  confirm Deployments appear with a `kubernetes` suggestion (+ endpoint suggestions for any
  LB/NodePort), promote one, confirm the resulting check carries `auto-discovery: true` and
  reports the workload's replica health.
- **Guards:** unknown `clusterUid` → 404 `KUBERNETES_CLUSTER_NOT_FOUND`; second concurrent
  Kubernetes scan → 409 `DISCOVERY_ALREADY_RUNNING`; non-admin → 403.
- `make build && make lint && make test && make test-dash`.

## Risk log

| Risk | Mitigation |
|---|---|
| Depends on the `02` checker + cluster connection existing | `02` is the explicit prerequisite (ships first); this spec builds no checker/connection and fails fast if `clusterUid` does not resolve |
| Pod IPs / ClusterIP services look monitorable but aren't worker-reachable | Only `NodePort`/`LoadBalancer`/`Ingress` endpoints get HTTP/TCP suggestions; the `kubernetes` replica check covers the rest |
| Workload `metadata.uid` churns when an object is deleted+recreated | Upsert on the stable `uid` (`resource_uid`); promotion snapshots `{clusterUid,namespace,kind,name}` into the check, which is then independent (no sync) — the Freebox/container stance |
| Two sibling specs both rework the IP unique index and add an identity column | Converged on one generic `resource_uid` + `metadata` and an `IN ('lan','freebox')`-scoped IP index, introduced once by `01`; this spec only adds the `kubernetes` enum value (decision 3) |
| Mapping Services/Ingresses to workloads by label selector is best-effort | Endpoint suggestions are secondary; the always-present `kubernetes` check never depends on the match, so a missed match only drops an optional HTTP/TCP suggestion |

**Status**: Todo | **Created**: 2026-06-21 | **Clarified**: 2026-06-26 — split from the original kubernetes-discovery spec (checker+connection moved to `02`); renumbered `02`→`03`; converged on container-discovery's shared `resource_uid`/`metadata` migration (no migration added here); references verified against current code.
