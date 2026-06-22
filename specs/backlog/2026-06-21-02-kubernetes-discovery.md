# Kubernetes discovery — enumerate Deployments/ReplicaSets on a cluster and suggest checks

> Sibling of the container-discovery spec (`2026-06-21-01-container-discovery.md`),
> which it deliberately mirrors. Both add a new `DiscoverySource` alongside `lan`
> and `freebox` and reuse the shared `discovered_hosts` table + promote/dismiss UX.
> **Critical difference, stated up front:** container-discovery ships *on top of* an
> already-existing `docker` check type — "no new checker is needed". A Kubernetes
> source has **no such luck**: there is no `kubernetes` check type today, and no
> `k8s.io/client-go` in `server/go.mod`. So this spec's load-bearing prerequisite —
> a `kubernetes` checker that monitors a Deployment/ReplicaSet's replica health —
> must be **built first** (slices 1–2). It could be carved into its own spec; this
> document keeps it as the two foundational slices so the feature is coherent
> end-to-end.

## Context

solidping already has two discovery sources, both modelled as background jobs that
write into the shared `discovered_hosts` table and feed one promote/dismiss UX:

- **`lan`** — a CIDR scan (`server/internal/discovery/scanner.go`) that ICMP-pings
  and TCP-probes every address in a range, then maps open ports to suggested checks
  via the authoritative `defaultPorts` table (`ports.go:20-37`) and `SuggestChecks`
  (`suggest.go:18`).
- **`freebox`** — pulls a paired Freebox router's LAN host list and runs it through
  the *same* probe engine (`job_freebox_lan_discovery.go`, `disc.ScanHosts`). It is
  driven by a **stored, granted integration**: the job config carries only a
  `channelUid` (`FreeboxLanDiscoveryConfig`, `job_freebox_lan_discovery.go:25-27`),
  and the credential behind it lives in the integration/credentials layer.

The sources are unified by `models.DiscoverySource`
(`server/internal/db/models/discovered_host.go:13-21`), a closed string enum, and by
the unified "Start new scan" selector on the frontend (LAN / Freebox today,
`discovery.new.tsx:38,165-178`). Promotion (`handlers/discovery/service.go:561`,
`PromoteHost`) turns any discovered row's `suggested_checks` into real `checks`,
tagging them `auto-discovery: true` + `discovery-job: <jobUid>`. The per-source
start endpoint, validation, and concurrency guard are all per-job-type, so a new
source slots in cleanly (`checkAlreadyRunning`, `service.go:192-209`; source filter,
`service.go:490-536`).

What is missing is the **Kubernetes story**. A user running 40 Deployments across a
cluster must today add 40 checks by hand — and there is no check type that even
understands a Deployment. "Point me at the cluster and tell me what is running, and
tell me when a workload's pods stop being ready" — the discovery story applied to
**workloads** instead of IPs — does not exist at all.

## Honest opinion — the two key open questions

Unlike container-discovery, where the only real decision was *how to find
containers* (the checker already existed), Kubernetes forces **two** decisions, and
both shape the build.

### Q1 — There is no `kubernetes` check, so what does a promoted check *do*?

A discovery source is only worth building if promotion yields a check that monitors
something. The `docker` checker monitors a container's `State.Health` (the
HEALTHCHECK) (`checkdocker/checker.go:140-198`). The Kubernetes analog of "is this
thing healthy?" is **a workload's replica readiness**: a Deployment exposes
`status.replicas`, `status.readyReplicas`, `status.availableReplicas`,
`status.updatedReplicas`, and `status.conditions[]` (`Available`, `Progressing`); a
ReplicaSet exposes the same minus conditions. That is a clean, universal,
poll-friendly health signal — the direct structural mirror of Docker's HEALTHCHECK.

So the only defensible answer is: **build a new `kubernetes` checker** that, given a
workload `(namespace, kind, name)`, reports

- `up` — `readyReplicas == desiredReplicas` and `desiredReplicas > 0`;
- `warning` — `0 < readyReplicas < desiredReplicas` (mid-rollout or partially
  degraded), or `desiredReplicas == 0` (intentionally scaled to zero — surfaced, not
  paged);
- `down` — `readyReplicas == 0` with `desiredReplicas > 0`, or
  `Progressing=False / reason=ProgressDeadlineExceeded` (a stuck rollout), or the
  workload no longer exists.

The user explicitly asked for **Deployment and ReplicaSet**. Both are first-class:
the checker targets a workload by `kind ∈ {Deployment, ReplicaSet}` (StatefulSet /
DaemonSet are an easy follow-up on the same code path). Discovery enumerates
**Deployments** (what a human thinks of as "my app") plus **bare ReplicaSets** (those
without a Deployment owner) so nothing is missed; the Deployment-owned ReplicaSets
are not surfaced separately (the Deployment already covers them).

**Cost to be honest about:** this pulls `k8s.io/client-go`, `k8s.io/api`, and
`k8s.io/apimachinery` into `server/go.mod` (none present today). That is a large
dependency tree and a real binary-size/maintenance cost — but it is unavoidable for
*any* Kubernetes integration, and it is the same shape as the Docker SDK the project
already vendors (`github.com/docker/docker v28.5.2+incompatible`).

### Q2 — How do we connect to (and authenticate against) a cluster?

Container endpoints (`unix://`, `tcp://`) are non-secret, so the `docker` checker
embeds the host string directly in check config (`checkdocker/config.go`). **A
Kubernetes API server requires a bearer token / client cert — a secret — so it must
NOT be embedded in check config** (which is stored as plain jsonb). Three approaches:

**A. Embed an API URL + token in each check / job config.** Simplest to wire.
- *Con:* secrets land unencrypted in `checks.config` and job config; one token per
  check duplicates the secret across dozens of rows. Wrong on security grounds.

**B. A stored "cluster connection" credential, referenced by UID.** The user
registers a cluster once (API server URL + token + CA cert, or a pasted kubeconfig);
it is encrypted at rest via `internal/crypto/credentials/`; both the discovery job
and every promoted `kubernetes` check reference it by a `clusterUid`.
- *Pro:* **exactly the Freebox model** — Freebox discovery already carries only a
  `channelUid` and resolves the secret behind it. One secret, encrypted once,
  rotated in one place. The promoted check stores `{clusterUid, namespace, kind,
  name}` — no secret in check config.
- *Con:* needs a small new credential type + a "test connection" probe. Modest.

**C. In-cluster service account only.** When solidping itself runs as a pod,
`rest.InClusterConfig()` reads the mounted service-account token — zero config.
- *Pro:* zero-config for the self-hosted-in-k8s case.
- *Con:* only works when solidping runs *inside the target cluster*; useless for
  monitoring a remote cluster, and useless when solidping runs outside k8s.

**Decision: B is the mechanism, with C as a zero-config convenience.** Register a
cluster connection (kubeconfig or API-URL+token+CA), store it encrypted, reference it
by `clusterUid` everywhere — the Freebox `channelUid` pattern. When solidping detects
it is running in-cluster, offer an "in-cluster (this cluster)" connection that needs
no secret (C). A (embedded secrets) is rejected.

For v1, auth is **bearer token + CA cert (+ `insecureSkipTLSVerify`)** or a pasted
kubeconfig that resolves to those. Client-certificate and exec/OIDC auth plugins are
a follow-up.

## Goal

An admin registers a **Kubernetes cluster** connection (or picks the auto-detected
in-cluster one), then selects **Kubernetes** as the scan method and confirms. A
`kubernetes_discovery` job connects to the cluster, lists Deployments and bare
ReplicaSets across the visible namespaces, and writes one `discovered_hosts` row per
workload (`source = kubernetes`) carrying the workload's `namespace/name`, kind,
image(s), desired/ready replica counts, and a set of suggested checks. The user
reviews the list and **promotes** workloads into real checks — primarily a
`kubernetes` check (the replica-health monitor), optionally HTTP/TCP checks on any
externally-reachable Service/Ingress — through the existing promote flow, with the
existing `auto-discovery` / `discovery-job` labels.

## Non-goals

- **Discovering ClusterIP-only workloads as HTTP/TCP targets.** Pod IPs and ClusterIP
  Services are not reachable from an out-of-cluster worker, so there is nothing to
  HTTP/TCP-probe — the `kubernetes` replica-health check covers those. Only
  `NodePort` / `LoadBalancer` Services and `Ingress` hosts yield endpoint
  suggestions (the direct analog of container-discovery's "published ports only").
- **Workload kinds beyond Deployment / bare ReplicaSet** (StatefulSet, DaemonSet,
  CronJob, Job, raw Pod) — a follow-up on the same checker code path.
- **Client-cert / exec / OIDC auth plugins** — v1 is bearer token + CA (or kubeconfig
  resolving to those) + in-cluster. Follow-up via the credentials system.
- **Scanning the network for exposed kube-apiservers (`:6443`).** As with
  container-discovery Approach A, basing discovery on a port scan rewards a
  misconfiguration and finds nothing on a correctly-secured cluster. Configured
  connection only.
- **Continuous / scheduled re-discovery or keeping checks in sync** as Deployments
  scale and roll. On-demand only, same as LAN/Freebox.
- **Writing to the cluster** (scaling, applying). Strictly read-only `list`/`get`.
- **Auto-creating checks.** The `auto-discovery` label is applied at promote time, as
  today.

## Design

Six vertical slices, each independently committable. Slices 1–2 are the prerequisite
the container-discovery spec got for free (the checker); slices 3–6 are the
discovery story and mirror container-discovery slice-for-slice. The work reuses the
`discovered_hosts` table and promote/dismiss UX rather than building a parallel
surface — keeping the unified-discovery investment intact.

### 1. Cluster connection (encrypted credential, the `clusterUid` everything references)

Model a **Kubernetes cluster connection** as a stored, encrypted credential —
structurally the Freebox integration (a granted, per-org connection referenced by
UID), persisted through `internal/crypto/credentials/`.

- Stored fields: `name`, `apiServer` (URL), `token` (secret, encrypted), `caCert`
  (PEM, optional), `insecureSkipTLSVerify` (bool), or alternatively a pasted
  `kubeconfig` (secret) that resolves to those at connect time. Plus a synthetic
  **in-cluster** connection when `rest.InClusterConfig()` succeeds (no stored
  secret).
- A `kubernetesClient(clusterUid)` helper builds a `*kubernetes.Clientset` from the
  resolved credential — used by *both* the checker (slice 2) and the discovery engine
  (slice 4). This is the single chokepoint where the secret is decrypted.
- A "test connection" probe (`/version` or `list namespaces`) validates the
  credential at registration time and surfaces RBAC gaps early.
- Required cluster RBAC (documented, shipped as an example read-only `ClusterRole`):
  `list`/`get` on `deployments`, `replicasets` (apps), `services`, `ingresses`
  (networking), `nodes`, `endpoints`, across the namespaces to be monitored.

### 2. The `kubernetes` checker — replica-health (the HEALTHCHECK analog)

A greenfield checker, modelled on `checkdocker` (the closest existing template — an
infra-category checker that talks to an external API and reports up/down/warning).

- New package `server/internal/checkers/checkkubernetes/` with `KubernetesConfig` +
  `KubernetesChecker`, following `checkers/CLAUDE.md`'s six registration steps:
  1. `CheckTypeKubernetes CheckType = "kubernetes"` in `checkerdef/types.go` (the
     const block at `:102-173`).
  2. A `CheckTypeMeta` entry in `ListCheckTypeMetas` (`types.go:217+`) — required, or
     `activation_test.go:19` (asserts every meta enabled by default) fails.
  3. Import + `case` in both registry switches:
     `GetChecker` (`registry/registry.go:65-140`, docker at `:127-128`) and
     `ParseConfig` (`:148-223`, docker at `:210-211`).
- `KubernetesConfig`:
  ```go
  type KubernetesConfig struct {
      ClusterUID string `json:"clusterUid"`           // the stored connection (slice 1)
      Namespace  string `json:"namespace"`            // required
      Kind       string `json:"kind"`                 // "Deployment" | "ReplicaSet"
      Name       string `json:"name"`                 // required
      Timeout    string `json:"timeout,omitempty"`    // default "10s", max 60s
  }
  ```
  `Validate`: `clusterUid`, `namespace`, `name` non-empty; `kind ∈ {Deployment,
  ReplicaSet}`; timeout `>0 && ≤60s` — mirrors `checkdocker/config.go:137-166`.
- `Check`: build the clientset via slice-1's `kubernetesClient(ClusterUID)`; on
  client/auth failure → `StatusError` (mirrors `checkdocker/checker.go:91-96`). Fetch
  the workload; `NotFound` → `StatusDown` ("workload not found"); other API error →
  `StatusTimeout` on deadline else `StatusDown` (mirrors
  `handleInspectError`, `checker.go:117-138`). Then apply the up/warning/down rule
  from Q1 off `spec.replicas` vs `status.readyReplicas` (+ Deployment conditions).
  Outputs: `namespace`, `kind`, `name`, `images`, `conditions`; metrics:
  `desiredReplicas`, `readyReplicas`, `availableReplicas`, `updatedReplicas`,
  `unavailableReplicas`, `query_time_ms`.

### 3. Model + DB: the `kubernetes` source and per-workload identity

The `discovered_hosts` table is IP-centric: `ip INET NOT NULL` plus a partial unique
index `idx_discovered_hosts_org_ip_source_active` on `(organization_uid, ip, source)`
(Postgres `migrations/001_v0_1_0.up.sql:1001-1002`, SQLite `:775-776`). Many
workloads share one API-server IP, so that index would collide. A workload's stable
identity is its Kubernetes **`metadata.uid`**.

This is the *same* model pressure container-discovery hit (many containers, one host
IP). **Converge on one generic identity column** rather than adding `container_id`
*and* a `kubernetes_uid`:

`server/internal/db/models/discovered_host.go` (enum at `:16-21`, struct at `:24-37`,
neither field exists today):

- Add the source constant:
  ```go
  // DiscoverySourceKubernetes marks a Deployment/ReplicaSet found on a configured
  // Kubernetes cluster connection.
  DiscoverySourceKubernetes DiscoverySource = "kubernetes"
  ```
- Add two nullable fields (no-ops for `lan`/`freebox`; **shared with
  container-discovery** — that spec's proposed `container_id` becomes this generic
  `resource_uid`):
  ```go
  ResourceUID *string         `bun:"resource_uid"        json:"resourceUid,omitempty"`
  Metadata    json.RawMessage `bun:"metadata,type:jsonb" json:"metadata,omitempty"`
  ```
  `hostname` reuses the existing column for the workload's `namespace/name` (the
  human label, e.g. `prod/api-server`). `metadata` holds `{clusterUid, kind,
  namespace, name, images, desiredReplicas, readyReplicas, availableReplicas,
  conditions, endpoints}` — `clusterUid`+`kind`+`namespace`+`name` are what the
  promoted `kubernetes` check needs.

Migration `003_discovery_resources.up.sql` / `.down.sql` (Postgres + SQLite mirror,
next free number is `003` — current dirs hold only `001_v0_1_0` and `002_mcp_oauth`):

```sql
ALTER TABLE discovered_hosts ADD COLUMN resource_uid TEXT;
ALTER TABLE discovered_hosts ADD COLUMN metadata     JSONB;

-- Scope the existing IP-uniqueness to the IP-based sources only…
DROP INDEX idx_discovered_hosts_org_ip_source_active;
CREATE UNIQUE INDEX idx_discovered_hosts_org_ip_source_active
  ON discovered_hosts (organization_uid, ip, source)
  WHERE deleted_at IS NULL AND promoted_to_check_uid IS NULL
        AND source IN ('lan', 'freebox');

-- …and give API-resource sources (kubernetes, container) their own identity index.
CREATE UNIQUE INDEX idx_discovered_hosts_org_resource_active
  ON discovered_hosts (organization_uid, source, resource_uid)
  WHERE deleted_at IS NULL AND promoted_to_check_uid IS NULL
        AND resource_uid IS NOT NULL;
```

`ip` for a Kubernetes row is the API server's resolved IP (or `127.0.0.1` for an
in-cluster connection) — it stays populated so the `NOT NULL` constraint and the
host-list UI keep working. *(If container-discovery lands first with its own
`container_id` column, this slice instead renames/reuses it; the two specs must agree
on one generic column — flagged in both.)*

### 4. Discovery engine + job

No CIDR fan-out and no active network probe — the cluster API returns everything
needed as metadata, so this mirrors container-discovery's metadata-derived approach
(not Freebox's `ScanHosts` probe), wrapped in the **single-job** Freebox shape
(`job_freebox_lan_discovery.go`), not the plan/child LAN model.

- Job type in `server/internal/jobs/jobdef/types.go` (Freebox at `:39`):
  ```go
  // JobTypeKubernetesDiscovery connects to a configured Kubernetes cluster, lists
  // Deployments and bare ReplicaSets, and records them in discovered_hosts
  // (source='kubernetes') for operator review and promotion.
  JobTypeKubernetesDiscovery JobType = "kubernetes_discovery"
  ```
  Wire into the `switch` in `jobtypes/registry.go:8-39` (Freebox at `:32-33`).
- Engine `server/internal/discovery/kubernetes.go` exposing
  ```go
  func ListWorkloads(ctx, client kubernetes.Interface, namespaces []string,
      timeout time.Duration) ([]DiscoveredWorkload, error)
  ```
  so it is unit-testable against a `client-go` `fake.Clientset` independently of the
  job plumbing (mirrors how `Scan`/`ScanHosts` and the proposed `ListContainers` are
  testable in isolation). It lists Deployments + ReplicaSets (skipping ReplicaSets
  with a Deployment `ownerReference`), and lists `NodePort`/`LoadBalancer` Services +
  Ingresses, best-effort matching them to workloads by label selector to populate
  `endpoints`.
- Implementation `server/internal/jobs/jobtypes/job_kubernetes_discovery.go`,
  mirroring `FreeboxLanDiscovery*`:
  ```go
  type KubernetesDiscoveryConfig struct {
      ClusterUID string   `json:"clusterUid"`            // required (slice 1)
      Namespaces []string `json:"namespaces,omitempty"`  // empty = all visible
      Timeout    string   `json:"timeout,omitempty"`     // default "30s"
  }
  ```
  `Run` builds the clientset via slice-1's helper, calls `ListWorkloads`, and for each
  workload builds a `models.DiscoveredHost` (`NewDiscoveredHost(..., DiscoverySourceKubernetes)`,
  `discovered_host.go:40-51`). Per-workload build/upsert errors are logged `Warn` and
  `continue`d — never abort the run — the exact resilience contract of
  `FreeboxLanDiscoveryJobRun.persistHosts` (`job_freebox_lan_discovery.go:120-162`).
  Persist via upsert keyed on `(organization_uid, source, resource_uid)` (the new
  index), parallel to the Freebox `ON CONFLICT (organization_uid, ip, source) …`
  upsert.

### 5. Suggested checks — the workload health monitor + reachable endpoints

New `SuggestKubernetesChecks` in `server/internal/discovery/suggest.go`, reusing the
`SuggestedCheck` struct (`:12-15`) and the `defaultPorts` port→scheme mapping
(`ports.go:20-37`):

- **Primary, always emitted — a `kubernetes` check.** This *is* the replica-health
  monitor from slice 2:
  ```json
  { "type": "kubernetes",
    "config": { "clusterUid": "<uid>", "namespace": "<ns>",
                "kind": "Deployment", "name": "<name>" } }
  ```
  Universal — it works even for a workload that exposes nothing externally (the common
  "internal service behind a ClusterIP" case).
- **Secondary — one check per externally-reachable endpoint** the engine matched in
  slice 4, using the worker-reachable address and the scheme decided by the
  service/target port via `defaultPorts`:
  - `LoadBalancer` → `status.loadBalancer.ingress[].ip|hostname` + service port.
  - `NodePort` → a node IP + the allocated `nodePort`.
  - `Ingress` → `spec.rules[].host` (+ path) → `http`/`https`.
  - port 80 / 8080 → `http`, 443 / 8443 → `https`, anything else → `tcp`.

Add `checkTypeKubernetes = "kubernetes"` to the `checkType*` constants
(`suggest.go:5-9`). `normalizeCheckType` (`service.go:659-665`) only remaps
`ping → icmp`; `kubernetes → kubernetes` passes through unchanged, and `PromoteHost`
(`service.go:561-627`) builds the check config from the suggested check's config
merged with any `extraConfig` — no promote-path changes beyond the suggestion
existing.

### 6. API + frontend

**Backend** (`server/internal/handlers/discovery/`):

- New route `POST /api/v1/orgs/:org/discovery/kubernetes-scans` (admin-only via
  `isAdmin`, `handler.go:33-40`), mirroring the Freebox `POST /freebox-scans`
  registration (`handler.go:60`). Body: `{ "clusterUid": "...", "namespaces": [],
  "timeout": "30s" }`.
- `Service.StartKubernetesScan(ctx, orgUID, cfg)` mirrors `StartFreeboxScan`
  (`service.go:146-181`): validate the `clusterUid` resolves to a registered
  connection (fail-fast, like Freebox validates the channel), guard with the existing
  per-type `checkAlreadyRunning(orgUID, JobTypeKubernetesDiscovery)`
  (`service.go:192-209`), then `jobSvc.CreateJob`. Reuse `DISCOVERY_ALREADY_RUNNING`
  (`handler.go:24`); add `KUBERNETES_CLUSTER_NOT_FOUND` for an unknown `clusterUid`
  (parallel to `FREEBOX_NOT_GRANTED`, `handler.go:27`).
- Add `JobTypeKubernetesDiscovery` to the `ListScans` type filter so K8s scans appear
  in the scan list. The existing `GET /scans`, `GET /scans/:jobUid`, `GET /hosts`,
  `POST /hosts/:uid/promote`, `DELETE /hosts/:uid` all work unchanged — they key on
  job type / `source`, and the source filter already accepts any enum value
  (`service.go:490-536`; handler parses comma-separated `?source=` at the list route).

**Frontend** (`web/dash0/`) — *all new UI reuses the design-reference primitives per
`CLAUDE.md`; start from `design-reference.tsx`*:

- A small **"Kubernetes clusters"** management surface (register / test / delete a
  cluster connection) — mirrors the Freebox integration UI; gated by `canSource`
  (`hooks.ts:2762-2764`, `CAPABILITIES` at `:2742-2754`).
- `discovery.new.tsx` — extend the `ScanMethod` type (`:38`) and the scan-method
  `Select` (`:165-178`) with **Kubernetes** (shown only when ≥1 cluster connection
  exists, exactly as Freebox is gated on granted channels). When selected: a cluster
  `Select` + an optional "namespaces" input + the shared confirmation checkbox;
  submit dispatches a new `useStartKubernetesScan` hook and navigates to scan detail.
- `discovery.index.tsx` — extend `scanSource` (`:51-53`) and the source-filter
  `Select` (`:136-148`) with `kubernetes`.
- Scan detail / host list (`discovery.$jobUid.index.tsx`, `HostRow` at `:57-125`,
  headers `:261-270`) — render workload rows: `namespace/name` (from `hostname`), a
  kind + `ready/desired` replica badge from `metadata`, endpoints from `metadata`, and
  the suggested checks. The list already renders hostname/openPorts/suggestedChecks,
  so workloads slot in; only the kind+replica badge is new.
- Promote page (`discovery.$jobUid.$hostUid.promote.tsx`) — unchanged; `kubernetes`
  appears as a selectable suggested type, prefilled with
  `clusterUid`+`namespace`+`kind`+`name`.
- `web/dash0/src/api/hooks.ts` — extend `DiscoverySource` (`:3229`) with
  `kubernetes`; add `useStartKubernetesScan` (mirror `useStartFreeboxScan`,
  `:3278-3293`) and the cluster-connection CRUD hooks; extend `canSource`.
- i18n — add `methodKubernetes`, `selectCluster`, `clusterNamespaces`,
  `kubernetesScanStarted`, `sourceKubernetes`, replica/kind labels to
  `web/dash0/src/locales/{en,fr,de,es}/discovery.json` (existing source/method keys
  at `en/discovery.json:66-77`).

## Decisions (applied 2026-06-21)

1. **Promoted check → a new `kubernetes` checker (Q1).** Monitors a Deployment's /
   ReplicaSet's `readyReplicas` vs desired (+ Deployment conditions); up / warning /
   down as in Q1. The direct structural mirror of how `docker` mirrors HEALTHCHECK.
   Accepts the `k8s.io/client-go` dependency cost as unavoidable.
2. **Connection → stored encrypted credential referenced by `clusterUid` (Q2, B).**
   The Freebox `channelUid` pattern; never embed the token in check/job config. The
   in-cluster service account (C) is offered as a zero-config convenience; embedded
   secrets (A) rejected.
3. **Workload set → Deployments + bare ReplicaSets in v1.** Deployment-owned
   ReplicaSets are folded into their Deployment (not surfaced twice); StatefulSet /
   DaemonSet / etc. are a follow-up on the same code path.
4. **Source name → `kubernetes`.** Distinct from the `docker`-flavoured `container`
   source; the promoted check type is `kubernetes`.
5. **Identity column → a shared generic `resource_uid` + `metadata`**, converging with
   container-discovery's proposed `container_id` so the two sibling specs do not each
   bolt a bespoke identity column onto `discovered_hosts`.
6. **Reachability → only `NodePort`/`LoadBalancer`/`Ingress` endpoints** get HTTP/TCP
   suggestions; ClusterIP services and pod IPs are not worker-reachable, so the
   `kubernetes` check covers those (the "published ports only" analog).

## Files to create / modify

### New (backend)
- `server/internal/checkers/checkkubernetes/{config.go,checker.go}` + tests — the
  replica-health checker (slice 2).
- Cluster-connection credential type + `kubernetesClient` helper + "test connection"
  probe (slice 1), under the integrations/credentials layer + `internal/crypto/credentials/`.
- `server/internal/discovery/kubernetes.go` + `kubernetes_test.go` — `ListWorkloads`
  against a `client-go` `fake.Clientset`.
- `server/internal/jobs/jobtypes/job_kubernetes_discovery.go` + test.
- Migration `003_discovery_resources.{up,down}.sql` (Postgres + SQLite) adding
  `resource_uid` / `metadata` and the reworked unique indexes.
- An example read-only `ClusterRole` manifest (docs) for the required RBAC.

### Modified (backend)
- `server/go.mod` / `go.sum` — add `k8s.io/client-go`, `k8s.io/api`,
  `k8s.io/apimachinery`.
- `server/internal/checkers/checkerdef/types.go` — `CheckTypeKubernetes` + meta;
  `registry/registry.go` — import + both switch cases.
- `server/internal/db/models/discovered_host.go` — `DiscoverySourceKubernetes`,
  `ResourceUID`, `Metadata`.
- `server/internal/discovery/suggest.go` — `SuggestKubernetesChecks`,
  `checkTypeKubernetes`.
- `server/internal/jobs/jobdef/types.go` + `jobtypes/registry.go` — register the job.
- `server/internal/handlers/discovery/{handler,service}.go` + tests —
  `kubernetes-scans` route, `StartKubernetesScan`, cluster validation, `ListScans`
  type filter.

### New / modified (frontend)
- Kubernetes-cluster management UI; `discovery.new.tsx`, `discovery.index.tsx`,
  host-list / scan-detail components.
- `web/dash0/src/api/hooks.ts` — `useStartKubernetesScan`, cluster CRUD, `DiscoverySource`,
  `canSource`.
- `web/dash0/src/locales/{en,fr,de,es}/discovery.json`.
- `web/dash0/e2e/discovery.spec.ts` — Kubernetes method coverage.

## Verification

- **Unit (table-driven, `testify/require`, `t.Parallel()` — `server/CLAUDE.md`):**
  - `KubernetesChecker` against a `fake.Clientset`: ready==desired → up; partial →
    warning; zero ready → down; scaled-to-zero → warning; `ProgressDeadlineExceeded`
    → down; missing workload → down; bad cluster → error/timeout.
  - `ListWorkloads` against a `fake.Clientset`: Deployments + bare ReplicaSets
    surfaced, Deployment-owned ReplicaSets skipped; LoadBalancer/NodePort/Ingress
    matched to endpoints by selector; ClusterIP → no endpoint; per-namespace error
    skip.
  - `SuggestKubernetesChecks`: `kubernetes` check always present; LB/NodePort/Ingress
    → http(s)/tcp per `defaultPorts`; ClusterIP-only → no endpoint suggestion.
- **Migration round-trip** (Postgres + SQLite): apply + `migrate down 1` + up; confirm
  both unique indexes and that a re-scan upserts on `resource_uid` (not IP).
- **End-to-end** (`make dev-test`, against a `kind`/minikube cluster or a stored
  kubeconfig): register a cluster, start a Kubernetes scan, confirm Deployments appear
  with a `kubernetes` suggestion (+ endpoint suggestions for any LB/NodePort), promote
  one, confirm the resulting check carries `auto-discovery: true` and reports the
  workload's replica health (scale the Deployment down → check goes warning/down).
- **Guards:** unknown `clusterUid` → 404 `KUBERNETES_CLUSTER_NOT_FOUND`; second
  concurrent Kubernetes scan → 409 `DISCOVERY_ALREADY_RUNNING`; non-admin → 403.
- `make lint && make test && make test-dash`.

## Risk log

| Risk | Mitigation |
|---|---|
| No `kubernetes` checker exists today — this is a from-scratch checker, not a thin discovery layer | Slices 1–2 build it first, modelled on `checkdocker`; the rest of the spec mirrors container-discovery unchanged |
| `k8s.io/client-go` is a large dependency tree (binary size, maintenance) | Unavoidable for any k8s integration; same shape as the already-vendored Docker SDK; pinned + `go mod tidy` reviewed |
| A bearer token embedded in check config would leak the cluster secret | Token stored once, encrypted, via `internal/crypto/credentials/`; checks/jobs carry only `clusterUid` (the Freebox `channelUid` model) |
| The connection's RBAC may lack `list` on some resource → silent empty scan | "Test connection" probe at registration surfaces RBAC gaps; ship an example read-only `ClusterRole`; per-namespace error skip logs what was unreachable |
| Pod IPs / ClusterIP services look monitorable but aren't worker-reachable | Only `NodePort`/`LoadBalancer`/`Ingress` endpoints get HTTP/TCP suggestions; the `kubernetes` replica check covers the rest |
| In-cluster (C) only works when solidping runs inside the target cluster | Offered as a convenience default; the primary path is a configured remote connection (B) that works from anywhere |
| Workload `metadata.uid` churns when an object is deleted+recreated | Upsert on the stable `uid`; promotion snapshots `{clusterUid,namespace,kind,name}` into the check, which is then independent (no sync) — matching the Freebox/container stance |
| Two sibling specs (`container`, `kubernetes`) both rework the IP unique index and add an identity column | Converge on one generic `resource_uid` + `metadata` and an `IN ('lan','freebox')`-scoped IP index; whichever spec lands first introduces them, the other reuses (decision 5) |
| Mapping Services/Ingresses to workloads by label selector is best-effort | Endpoint suggestions are secondary; the always-present `kubernetes` check never depends on the match, so a missed match only drops an optional HTTP/TCP suggestion |
