# Kubernetes discovery — enumerate Deployments/ReplicaSets on a cluster and suggest checks

> **Lands after BOTH foundations.**
> - `2026-06-21-00-discovery-check-model-and-generic-scan-api.md` — the check-centric,
>   grouped `discovered_checks` model + the generic `{type, parameters}` scan API + the
>   `scantypes` registry. This spec adds a new `DiscoverySource` alongside `lan`/`freebox`
>   by **registering a discovery type** against that foundation — **no schema migration,
>   no new endpoint**, only its own Go-level `source` constant.
> - `2026-06-21-02-kubernetes-checker.md` — the `kubernetes` check type (replica-health)
>   and the encrypted **cluster connection** (`clusterUid`) plus its
>   `kubernetesClient(clusterUid)` resolver. This spec promotes to that `kubernetes`
>   check and authenticates its scan job via that same `clusterUid`. It builds **no
>   checker and no connection** — only the discovery story on top.
>
> **Order:** sibling of container-discovery (`2026-06-21-01`); both register a
> `scantypes.Definition` + a `JobDefinition` against `…-00`. This one additionally
> requires the `kubernetes` checker + cluster connection from `…-02`.

## Context

After the foundation spec (`2026-06-21-00`), discovery is **check-centric and grouped**:
a scan writes one `discovered_checks` row per *suggested check*
(`{group_key, group_label, name, slug, type, config, metadata}` + `source`, `job_uid`,
`promoted_to_check_uid`), and the frontend renders them grouped by `group_key`. Two
reference sources already register against it:

- **`lan`** — a CIDR scan (`server/internal/discovery/scanner.go`, `Scan`/`ScanHosts`)
  that ICMP-pings and TCP-probes every address, then maps open ports to grouped suggested
  checks via the authoritative `defaultPorts` table (`ports.go:20-37`) and `SuggestChecks`
  (`suggest.go`); one group per host (`group_key = ip`).
- **`freebox`** — pulls a paired Freebox router's LAN host list through the *same* probe
  engine (`job_freebox_lan_discovery.go`). It is driven by a **stored, granted
  integration**: the job config carries only a `channelUid` (`FreeboxLanDiscoveryConfig`,
  `job_freebox_lan_discovery.go:25-27`), and the credential behind it is resolved at run
  time (`server/internal/integrations/freebox/lanlookup.go:34-124`).

Sources are unified by `models.DiscoverySource` (a Go-level string enum in
`server/internal/db/models/discovered_check.go`), by the generic `POST /discovery/scans`
body `{type, parameters}` routing through the `scantypes` registry
(`server/internal/discovery/scantypes/`), and by the grouped promote/dismiss UX.
Promotion (`POST /discovery/checks/promote {uids:[…]}`) turns selected `discovered_checks`
rows into real `checks`, tagging them `auto-discovery: true` + `discovery-job: <jobUid>`.

**Prerequisite — built in `…-02` (kubernetes-checker):** the `kubernetes` check type
(replica-health) and the encrypted **cluster connection** (`clusterUid`) plus its
`kubernetesClient(clusterUid)` helper. Discovery's promoted checks are `kubernetes`
checks; this scan job authenticates via the same `clusterUid`. This spec builds neither —
it consumes both.

What is missing is the **enumeration**: a user running 40 Deployments must add 40 checks
by hand. "Point me at the cluster and tell me what is running" — the discovery story
applied to **workloads** — does not exist.

## Non-goals

- **Building the `kubernetes` checker or the cluster connection** — those are `…-02`;
  this spec is discovery-only (engine + suggester + job + type registration + frontend).
- **Changing the `discovered_checks` model or the generic scan API** — those are `…-00`;
  this spec consumes both unchanged and adds no migration.
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

Discovery-only now: five vertical slices, each independently committable — the
`discovered_checks` model + generic scan API come from `…-00`, the `kubernetes` checker +
cluster connection from `…-02`.

> **Prerequisite (built in `…-02`):** the `kubernetes` checker and the encrypted cluster
> connection + `kubernetesClient(clusterUid)` helper. This spec does not build them.

### 1. Model: the `kubernetes` source constant

This spec **consumes the `discovered_checks` model from `2026-06-21-00` unchanged — NO
schema migration.** Add only the Go source constant in
`server/internal/db/models/discovered_check.go` (enum alongside `lan`/`freebox`):

```go
// DiscoverySourceKubernetes marks a Deployment/ReplicaSet found on a configured
// Kubernetes cluster connection.
DiscoverySourceKubernetes DiscoverySource = "kubernetes"
```

A workload's stable identity is its Kubernetes **`metadata.uid`**, which maps cleanly onto
the foundation's render-time grouping — there is no IP to collide on. For a `kubernetes`
workload group:

- `group_key   = workload metadata.uid` (stable across re-scans of the same object).
- `group_label = "namespace/name"` (the human label, e.g. `prod/api-server`).
- `metadata    = {clusterUid, kind, namespace, name, images, desiredReplicas,
  readyReplicas, availableReplicas, conditions, endpoints}` —
  `clusterUid`+`kind`+`namespace`+`name` are what the promoted `kubernetes` check needs;
  the rest are group-display hints (denormalized across the group's rows, written by the
  suggester, per the foundation's `metadata` contract).

### 2. Discovery engine + job

No CIDR fan-out and no active network probe — the cluster API returns everything needed as
metadata, so this mirrors container-discovery's metadata-derived approach (not Freebox's
`ScanHosts` probe), wrapped in the **single-job** Freebox shape
(`job_freebox_lan_discovery.go`), not the plan/child LAN model.

- Job type in `server/internal/jobs/jobdef/types.go` (Freebox at `:39`):
  ```go
  // JobTypeKubernetesDiscovery connects to a configured Kubernetes cluster, lists
  // Deployments and bare ReplicaSets, and records them in discovered_checks
  // (source='kubernetes') for operator review and promotion.
  JobTypeKubernetesDiscovery JobType = "kubernetes_discovery"
  ```
  Wire into the registration switch in `jobtypes/registry.go:8-39` (Freebox at `:32-33`).
  The job is registered **both** as a `JobDefinition` (job-layer, as today) **and** as a
  `scantypes.Definition` (slice 4) — a source lives in both registries per the foundation
  (`…-00` §2).
- Engine `server/internal/discovery/kubernetes.go` exposing
  ```go
  func ListWorkloads(ctx context.Context, client kubernetes.Interface,
      namespaces []string, timeout time.Duration) ([]DiscoveredWorkload, error)
  ```
  so it is unit-testable against a `client-go` `fake.Clientset` independently of the job
  plumbing (mirrors how `Scan`/`ScanHosts` are testable in isolation). It lists Deployments
  + ReplicaSets (skipping ReplicaSets with a Deployment `ownerReference`), and lists
  `NodePort`/`LoadBalancer` Services + Ingresses, best-effort matching them to workloads by
  label selector to populate `endpoints`. The `client` is built by `…-02`'s
  `kubernetesClient(clusterUid)` resolver — this engine takes the interface, not a
  `clusterUid`, keeping it pure.
- Implementation `server/internal/jobs/jobtypes/job_kubernetes_discovery.go`, mirroring
  `FreeboxLanDiscovery*`:
  ```go
  type KubernetesDiscoveryConfig struct {
      ClusterUID string   `json:"clusterUid"`           // required (the 02 connection)
      Namespaces []string `json:"namespaces,omitempty"` // empty = all visible
      Timeout    string   `json:"timeout,omitempty"`    // default "30s"
  }
  ```
  `Run` builds the clientset via `…-02`'s `kubernetesClient`, calls `ListWorkloads`, and
  for each workload calls `SuggestKubernetesChecks` (slice 3) to get the grouped
  `SuggestedCheck` rows, then persists them through the foundation's shared
  `UpsertDiscoveredChecks(ctx, db, orgUID, jobUID, DiscoverySourceKubernetes, rows)`
  (`…-00` §3) — which upserts on `(organization_uid, source, group_key, slug)`. Per-row
  build/upsert errors are logged `Warn` and `continue`d — never abort the run — the
  per-item log-and-continue resilience contract the foundation preserves from
  `FreeboxLanDiscoveryJobRun.persistHosts` (`job_freebox_lan_discovery.go:120-162`).

### 3. Suggested checks — the workload health monitor + reachable endpoints

New `SuggestKubernetesChecks` in `server/internal/discovery/suggest.go`. Per workload it
returns a **group** of grouped `SuggestedCheck` rows (the foundation's struct with
`{GroupKey, GroupLabel, Name, Slug, Type, Config, Metadata}`, `…-00` §3), all sharing
`group_key = workload.uid`, `group_label = "namespace/name"`, and the slice-1 `metadata`.
Names/slugs are generated via the foundation's `checkName`/`checkSlug` helpers (deduped
within the group); the `defaultPorts` port→scheme mapping (`ports.go:20-37`) decides the
endpoint schemes.

- **Primary, always emitted — a `kubernetes` check.** This *is* the replica-health monitor
  from `…-02`:
  ```json
  { "type": "kubernetes",
    "config": { "clusterUid": "<uid>", "namespace": "<ns>",
                "kind": "Deployment", "name": "<name>" } }
  ```
  Universal — it works even for a workload that exposes nothing externally (the common
  "internal service behind a ClusterIP" case).
- **Secondary — one http/tcp check per externally-reachable endpoint** the engine matched
  in slice 2, using the worker-reachable address and the scheme decided by the
  service/target port via `defaultPorts`:
  - `LoadBalancer` → `status.loadBalancer.ingress[].ip|hostname` + service port.
  - `NodePort` → a node IP + the allocated `nodePort`.
  - `Ingress` → `spec.rules[].host` (+ path) → `http`/`https`.
  - port 80 / 8080 → `http`, 443 / 8443 → `https`, anything else → `tcp`.

Add `checkTypeKubernetes = "kubernetes"` to the `checkType*` constants (`suggest.go:6-9`).
`normalizeCheckType` (`service.go:659`) only remaps `ping → icmp`; `kubernetes → kubernetes`
passes through unchanged. The check `config`/`name`/`slug` are already on the
`discovered_checks` row, so the foundation's grouped promote
(`POST /discovery/checks/promote {uids:[…]}`) creates the checks directly — no
kubernetes-specific promote-path change beyond the suggestion existing.

### 4. Discovery-type registration + API

Register a `kubernetes` discovery type rather than adding a dedicated endpoint — the
generic `POST /discovery/scans` from `…-00` routes to it.

- New `server/internal/discovery/scantypes/kubernetes.go` implementing
  `scantypes.Definition` (mirrors the `lan`/`freebox` definitions, `…-00` §2):
  - `Type() string` → `"kubernetes"`.
  - `Source() models.DiscoverySource` → `DiscoverySourceKubernetes`.
  - `BuildJob(ctx, deps, orgUID, parameters)` validates
    `{ clusterUid, namespaces?, timeout? }` — the `clusterUid` must resolve to a registered
    cluster connection from `…-02` (fail-fast, like `freebox` validates its channel) — and
    returns `("kubernetes_discovery", cfg)` (the `KubernetesDiscoveryConfig` from slice 2).
    An unknown `clusterUid` returns a coded error `KUBERNETES_CLUSTER_NOT_FOUND` (parallel
    to `FREEBOX_NOT_GRANTED`).
  - Reachable via the generic route: `POST /api/v1/orgs/:org/discovery/scans` body
    `{ "type": "kubernetes", "parameters": { "clusterUid": "...", "namespaces": [],
    "timeout": "30s" } }`. `Service.StartScan(type, parameters)` (`…-00` §2) does the
    `Get("kubernetes")` → `BuildJob` → `checkAlreadyRunning(orgUID, JobTypeKubernetesDiscovery)`
    (→ `DISCOVERY_ALREADY_RUNNING`) → `jobSvc.CreateJob` routing — **no new handler or
    service method here.**
- The generic `GET /scans`, `GET /scans/:jobUid`, `POST /scans/:jobUid/cancel`, and the
  `/discovery/checks` list/promote/dismiss endpoints all work unchanged — they are
  type-agnostic and drive their user-facing-type set from `scantypes.List()`, so the
  `kubernetes` type appears automatically.

### 5. Frontend (`web/dash0/`)

*All new UI reuses the design-reference primitives per `web/dash0/CLAUDE.md`; start from
`design-reference.tsx`.* (The "Kubernetes clusters" management surface is built in `…-02`;
this slice only consumes existing connections.)

- `discovery.new.tsx` — the registry-driven scan-start form from `…-00` lists types from
  `scantypes.List()`; add a **kubernetes** parameter sub-form (the `DISCOVERY_TYPES`
  descriptor: label + capability gate + a parameter component): a cluster `Select` + an
  optional "namespaces" input, shown only when ≥1 cluster connection exists. Submit
  dispatches the single `useStartDiscoveryScan({ type: "kubernetes", parameters })`.
- `discovery.index.tsx` — the source filter is registry-driven; `kubernetes` appears
  automatically once the type is registered (gate via `canSource`).
- `discovery.$jobUid.index.tsx` — the grouped render from `…-00` groups
  `discovered_checks` by `groupLabel`. For the `kubernetes` source, the group header shows
  `namespace/name` (the `group_label`) with a kind + `ready/desired` replica badge and
  endpoints drawn from the group's `metadata`; the suggested `kubernetes` + endpoint checks
  render beneath with the shared per-check checkbox, "select all in group", **Promote
  selected**, and per-check/per-group dismiss. Only the kind+replica badge is k8s-specific.
- Promotion is inline on the grouped list (`POST /discovery/checks/promote {uids:[…]}`,
  `…-00` §4) — there is no standalone promote page; the `kubernetes` suggested check is
  prefilled with `clusterUid`+`namespace`+`kind`+`name` on its row.
- `web/dash0/src/api/hooks.ts` — extend the `DiscoverySource` union and
  `canSource`/`CAPABILITIES` with `kubernetes`; **no new start hook** — the generic
  `useStartDiscoveryScan({ type, parameters })` from `…-00` carries the kubernetes
  parameters.
- i18n — add `methodKubernetes`, `selectCluster`, `clusterNamespaces`, `sourceKubernetes`,
  and replica/kind labels to `web/dash0/src/locales/{en,fr,de,es}/discovery.json`,
  alongside the foundation's group vocabulary.

## Decisions (applied 2026-06-21)

1. **Workload set → Deployments + bare ReplicaSets in v1.** Deployment-owned ReplicaSets
   are folded into their Deployment (not surfaced twice); StatefulSet / DaemonSet / etc. are
   a follow-up on the same code path (and the `…-02` checker supports only these two kinds).
2. **Source name → `kubernetes`.** Distinct from the `docker`-flavoured `container` source;
   the promoted check type is `kubernetes`.
3. **Identity → the check-centric grouping from `…-00`, no identity column.** A workload is
   a *group* (`group_key = metadata.uid`, `group_label = "namespace/name"`) of
   `discovered_checks` rows; there is no `ip` to collide on and therefore no bespoke
   identity column or migration — the check-centric model dissolves the per-workload
   identity problem the old host-centric table had.
4. **Reachability → only `NodePort`/`LoadBalancer`/`Ingress` endpoints** get HTTP/TCP
   suggestions; ClusterIP services and pod IPs are not worker-reachable, so the `kubernetes`
   check covers those (the "published ports only" analog).

## Files to create / modify

### New (backend)
- `server/internal/discovery/kubernetes.go` + `kubernetes_test.go` — `ListWorkloads`
  against a `client-go` `fake.Clientset`.
- `server/internal/jobs/jobtypes/job_kubernetes_discovery.go` + test.
- `server/internal/discovery/scantypes/kubernetes.go` + test — the `scantypes.Definition`
  (`Type`/`Source`/`BuildJob`, `clusterUid` validation).

### Modified (backend)
- `server/internal/db/models/discovered_check.go` — add the `DiscoverySourceKubernetes`
  source constant. **No migration** — the `discovered_checks` model from `…-00` is consumed
  unchanged.
- `server/internal/discovery/suggest.go` — `SuggestKubernetesChecks` (grouped rows),
  `checkTypeKubernetes`.
- `server/internal/jobs/jobdef/types.go` + `jobtypes/registry.go` — register the job
  (`JobDefinition`).

### New / modified (frontend)
- `discovery.new.tsx` (kubernetes parameter sub-form for the registry-driven scan form),
  grouped scan-detail render for the `kubernetes` source.
- `web/dash0/src/api/hooks.ts` — extend `DiscoverySource` / `canSource` with `kubernetes`
  (no new start hook; the generic `useStartDiscoveryScan` carries the parameters).
- `web/dash0/src/locales/{en,fr,de,es}/discovery.json`.
- `web/dash0/e2e/discovery.spec.ts` — Kubernetes coverage via the generic scan endpoint.

## Verification

- **Unit (table-driven, `testify/require`, `t.Parallel()` — `server/CLAUDE.md`):**
  - `ListWorkloads` against a `fake.Clientset`: Deployments + bare ReplicaSets surfaced,
    Deployment-owned ReplicaSets skipped; LoadBalancer/NodePort/Ingress matched to endpoints
    by selector; ClusterIP → no endpoint; per-namespace error skip.
  - `SuggestKubernetesChecks` → grouped rows: every workload yields a `kubernetes` row plus
    one http/tcp row per LB/NodePort/Ingress endpoint (per `defaultPorts`), all sharing
    `group_key = metadata.uid`, `group_label = "namespace/name"`, distinct slugs, and the
    expected `metadata`; ClusterIP-only → only the `kubernetes` row.
  - `scantypes` `kubernetes` `BuildJob`: valid `{clusterUid,…}` → `("kubernetes_discovery",
    cfg)`; unknown `clusterUid` → `KUBERNETES_CLUSTER_NOT_FOUND`.
- **End-to-end** (`make dev-test`, against a `kind`/minikube cluster or a stored kubeconfig,
  with `…-02` already merged): register a cluster (via `…-02`'s UI), start a scan through
  the generic `POST /discovery/scans {type:"kubernetes",…}`, confirm Deployments render
  **grouped by `namespace/name`** with a `kubernetes` suggestion (+ endpoint suggestions for
  any LB/NodePort), select a group and promote, confirm the resulting check carries
  `auto-discovery: true` and reports the workload's replica health. A re-scan upserts on
  `(org, source, group_key, slug)` (the foundation's identity index) — no duplicate rows
  when the API-server IP is shared.
- **Guards:** unknown `clusterUid` → 404 `KUBERNETES_CLUSTER_NOT_FOUND`; second concurrent
  Kubernetes scan → 409 `DISCOVERY_ALREADY_RUNNING`; non-admin → 403.
- `make build && make lint && make test && make test-dash`.

## Risk log

| Risk | Mitigation |
|---|---|
| Depends on the `…-02` checker + cluster connection existing | `…-02` is the explicit prerequisite (ships first); this spec builds no checker/connection and the `kubernetes` `scantypes` definition fails fast if `clusterUid` does not resolve |
| `client-go` is a heavy dependency to add to the server module | Bounded: the discovery engine and the `…-02` checker share the one `client-go` import; pinned and vendored like any other module dependency |
| RBAC gaps — the service account may lack `list`/`get` on some namespaces | Strictly read-only `list`/`get`; a per-namespace authorization error is logged and skipped (never aborts the run), so a partial result still surfaces what is visible |
| Pod IPs / ClusterIP services look monitorable but aren't worker-reachable | Only `NodePort`/`LoadBalancer`/`Ingress` endpoints get HTTP/TCP suggestions; the `kubernetes` replica check covers the rest |
| An in-cluster connection only works for a worker running inside that cluster | Same in-process-worker locality caveat as a `unix://` Docker socket; documented, not blocked — out-of-cluster connections use the stored kubeconfig from `…-02` |
| Workload `metadata.uid` churns when an object is deleted+recreated | `group_key = metadata.uid` upserts on the stable uid; promotion snapshots `{clusterUid,namespace,kind,name}` into the check, which is then independent (no sync) — the Freebox/container stance |
| Mapping Services/Ingresses to workloads by label selector is best-effort | Endpoint suggestions are secondary; the always-present `kubernetes` check never depends on the match, so a missed match only drops an optional HTTP/TCP suggestion |

**Status**: Todo | **Created**: 2026-06-21 | **Reworked**: 2026-06-27 — rebased onto the check-centric `discovered_checks` model + generic `{type,parameters}` scan API (`2026-06-21-00`): dropped the old host-centric model/migration slice and the dedicated per-type scan endpoint, now registers a `kubernetes` `scantypes` definition and emits grouped `discovered_checks` rows; checker + cluster connection remain the prerequisite from `2026-06-21-02`.

## Implementation Plan

Each step is independently committable. Foundation (`…-00`) + checker/connection (`…-02`)
are already on `batch/2026-06-23`; this only adds the kubernetes discovery story.

1. **Model source constant.** Add `DiscoverySourceKubernetes = "kubernetes"` to
   `server/internal/db/models/discovered_check.go` (no migration). _Commit: `feat: add kubernetes discovery source constant`._

2. **Discovery engine.** New `server/internal/discovery/kubernetes.go` exporting
   `ListWorkloads(ctx, client kubernetes.Interface, namespaces []string, timeout) ([]DiscoveredWorkload, error)`:
   lists Deployments + bare ReplicaSets (skip Deployment-owned RS via ownerReference),
   lists `NodePort`/`LoadBalancer` Services + Ingresses, best-effort label-selector matches
   them to workloads → `WorkloadEndpoint`s, populates `metadata.uid`/kind/namespace/name/
   images/replicas/conditions. Per-namespace list error logged + skipped. Pure (takes the
   interface). `kubernetes_test.go` against a `fake.Clientset`. _Commit: `feat: kubernetes discovery engine (ListWorkloads)`._

3. **Suggester.** `SuggestKubernetesChecks` + `checkTypeKubernetes` in
   `server/internal/discovery/suggest.go`: per workload a group (`group_key=uid`,
   `group_label="ns/name"`) with a primary `kubernetes` row (config `{clusterUid,namespace,
   kind,name}`) + one http/tcp row per endpoint (scheme via `defaultPorts`). Table-driven
   test. _Commit: `feat: SuggestKubernetesChecks grouped suggestions`._

4. **Job type + engine job.** Register `JobTypeKubernetesDiscovery="kubernetes_discovery"`
   in `jobdef/types.go` + `jobtypes/registry.go`. New
   `jobtypes/job_kubernetes_discovery.go` (`KubernetesDiscoveryConfig{ClusterUID,Namespaces,
   Timeout}`): `Run` resolves a clientset via a swappable resolver seam (default
   `integrationk8s.ResolveClientsetByUID(ctx, jctx.DBService, jctx.Services.Credentials, …)`),
   calls `ListWorkloads`, `SuggestKubernetesChecks`, persists via `UpsertDiscoveredChecks`
   (source kubernetes), per-row log-and-continue. Test via fake clientset factory +
   in-memory DB. _Commit: `feat: kubernetes_discovery job type + runner`._

5. **scantypes registration.** New `scantypes/kubernetes.go` implementing `Definition`
   (`Type"kubernetes"`, `Source`, `BuildJob` validating `{clusterUid,namespaces?,timeout?}`
   — probe via `integrationk8s.ValidateConnection(ResolveClientsetByUID)`; unknown/wrong-org
   cluster → `KUBERNETES_CLUSTER_NOT_FOUND`). Add the code constant; register in
   `RegisterDefaults`; add to service `scanJobTypes()`; map the new code → 404 in the
   handler's `statusForDiscoveryCode`. Tests (build-job + activation guard). _Commit:
   `feat: register kubernetes discovery scantype + 404 mapping`._

6. **Frontend.** `discovery.new.tsx` kubernetes sub-form (cluster `Select` from
   kubernetes connections + optional namespaces input, gated on ≥1 cluster);
   `discovery.index.tsx` `scanSource` maps `kubernetes_discovery`→`kubernetes`;
   `discovery.$jobUid.index.tsx` `MetadataBadges` kubernetes branch (kind + ready/desired +
   endpoints). hooks.ts already carries the union; add i18n keys (`methodKubernetes` area:
   `selectCluster`, `clusterNamespaces`, replica/kind labels) in en/fr/de/es. _Commit:
   `feat: dash0 kubernetes discovery scan form + grouped render`._

7. **E2E + docs.** Add kubernetes coverage to `web/dash0/e2e/discovery.spec.ts`; document
   the kubernetes parameters + `KUBERNETES_CLUSTER_NOT_FOUND` 404 in `openapi.yaml`.
   _Commit: `test: e2e + docs for kubernetes discovery`._

8. **QA + audit + archive.**
