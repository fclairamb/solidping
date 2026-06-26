# Kubernetes checker — replica-health check type + encrypted cluster connection

> **Prerequisite spec**, carved out of the original Kubernetes-discovery design so the
> from-scratch foundations ship and are testable on their own. It builds the two things
> Kubernetes discovery (`2026-06-21-03-kubernetes-discovery.md`) needs but cannot
> assume exist — unlike container-discovery, which shipped *on top of* an already-present
> `docker` check type:
>
> 1. a **`kubernetes` check type** that monitors a Deployment/ReplicaSet's replica
>    health (the structural analog of how `docker` mirrors Docker's HEALTHCHECK), and
> 2. an **encrypted "cluster connection"** credential (`clusterUid`) that both the
>    checker and, later, the discovery job resolve to a `*kubernetes.Clientset`.
>
> Neither exists today: there is no `kubernetes` check type and no `k8s.io/client-go` in
> `server/go.mod`. This spec is independently valuable — once it lands, an operator can
> add a `kubernetes` check by hand against a registered cluster, with no discovery at all.
>
> **Order:** second of three (container-discovery → **kubernetes-checker** →
> kubernetes-discovery). It does **not** touch `discovered_hosts` or the discovery
> subsystem — that is entirely `03`'s job.

## Context

solidping ships a `docker` check type (`server/internal/checkers/checkdocker/`) that
connects to a Docker-compatible endpoint and reports `up/down/warning` from a container's
running state and `State.Health.Status` (`checker.go:140-198`) — the HEALTHCHECK. The
checker registry is a closed, switch-based set (`server/internal/checkers/registry/registry.go`)
and every check type carries a `CheckTypeMeta` (`checkerdef/types.go`).

What is missing is **any Kubernetes awareness**. A user running 40 Deployments has no
check type that even understands a workload — "tell me when a workload's pods stop being
ready" does not exist. Building that is the load-bearing prerequisite for Kubernetes
discovery, so it is specified here first.

## The two key questions

### Q1 — There is no `kubernetes` check, so what does a promoted/manual check *do*?

A check type is only worth building if it monitors something. The `docker` checker
monitors a container's `State.Health` (`checkdocker/checker.go:140-198`). The Kubernetes
analog of "is this thing healthy?" is **a workload's replica readiness**: a Deployment
exposes `status.replicas`, `status.readyReplicas`, `status.availableReplicas`,
`status.updatedReplicas`, and `status.conditions[]` (`Available`, `Progressing`); a
ReplicaSet exposes the same minus conditions. That is a clean, universal, poll-friendly
health signal — the direct structural mirror of Docker's HEALTHCHECK.

So the only defensible answer is: **build a new `kubernetes` checker** that, given a
workload `(namespace, kind, name)`, reports

- `up` — `readyReplicas == desiredReplicas` and `desiredReplicas > 0`;
- `warning` — `0 < readyReplicas < desiredReplicas` (mid-rollout or partially degraded),
  or `desiredReplicas == 0` (intentionally scaled to zero — surfaced, not paged);
- `down` — `readyReplicas == 0` with `desiredReplicas > 0`, or
  `Progressing=False / reason=ProgressDeadlineExceeded` (a stuck rollout), or the
  workload no longer exists.

The user explicitly asked for **Deployment and ReplicaSet**; both are first-class via
`kind ∈ {Deployment, ReplicaSet}` (StatefulSet / DaemonSet are an easy follow-up on the
same code path).

**Cost to be honest about:** this pulls `k8s.io/client-go`, `k8s.io/api`, and
`k8s.io/apimachinery` into `server/go.mod` (none present today). A large dependency tree
and a real binary-size/maintenance cost — but unavoidable for *any* Kubernetes
integration, and the same shape as the already-vendored Docker SDK
(`github.com/docker/docker v28.5.2+incompatible`).

### Q2 — How do we connect to (and authenticate against) a cluster?

Container endpoints (`unix://`, `tcp://`) are non-secret, so the `docker` checker embeds
the host string directly in check config (`checkdocker/config.go`). **A Kubernetes API
server requires a bearer token / client cert — a secret — so it must NOT be embedded in
check config** (which is stored as plain jsonb). Three approaches:

**A. Embed an API URL + token in each check / job config.** Simplest to wire.
- *Con:* secrets land unencrypted in `checks.config`; one token per check duplicates the
  secret across dozens of rows. Wrong on security grounds.

**B. A stored "cluster connection" credential, referenced by UID.** The user registers a
cluster once (API server URL + token + CA cert, or a pasted kubeconfig); it is encrypted
at rest via `internal/crypto/credentials/`; both every promoted/manual `kubernetes` check
and (later) the discovery job reference it by a `clusterUid`.
- *Pro:* **exactly the Freebox model** — the Freebox integration is a stored, per-org
  connection whose secret (`appToken`) lives encrypted in the `integrations` row and is
  resolved at run time by UID (`server/internal/integrations/freebox/lanlookup.go:34-124`).
  One secret, encrypted once, rotated in one place. The check stores only
  `{clusterUid, namespace, kind, name}`.
- *Con:* needs a small new integration type + a "test connection" probe. Modest, and the
  CRUD/test plumbing already exists (`handlers/integrations/`).

**C. In-cluster service account only.** When solidping itself runs as a pod,
`rest.InClusterConfig()` reads the mounted service-account token — zero config.
- *Pro:* zero-config for the self-hosted-in-k8s case.
- *Con:* only works when solidping runs *inside the target cluster*; useless for a remote
  cluster or when solidping runs outside k8s.

**Decision: B is the mechanism, with C as a zero-config convenience.** Register a cluster
connection (kubeconfig or API-URL+token+CA), store it encrypted, reference it by
`clusterUid` everywhere — the Freebox `channelUid` pattern. When solidping detects it is
running in-cluster, offer an "in-cluster (this cluster)" connection that needs no stored
secret (C). A (embedded secrets) is rejected. For v1, auth is **bearer token + CA cert
(+ `insecureSkipTLSVerify`)** or a pasted kubeconfig that resolves to those.
Client-certificate and exec/OIDC auth plugins are a follow-up.

## Goal

An admin registers a **Kubernetes cluster** connection (or picks the auto-detected
in-cluster one) and can immediately add a `kubernetes` check against a workload
`(namespace, kind, name)` — by hand for now; discovery-driven promotion arrives in `03`.
The check reports the workload's replica health (`up`/`warning`/`down` per Q1) and
exposes replica-count metrics. No secret ever lands in `checks.config`.

## Non-goals

- **Discovery / enumeration of workloads.** That is `2026-06-21-03-kubernetes-discovery.md`.
  This spec adds no `discovered_hosts` column, no discovery source, no scan job.
- **Workload kinds beyond Deployment / ReplicaSet** (StatefulSet, DaemonSet, CronJob,
  Job, raw Pod) — a follow-up on the same checker code path.
- **Client-cert / exec / OIDC auth plugins** — v1 is bearer token + CA (or kubeconfig
  resolving to those) + in-cluster.
- **Writing to the cluster** (scaling, applying). Strictly read-only `list`/`get`.
- **Scanning the network for exposed kube-apiservers (`:6443`).** Configured connection
  only.

## Design

Two vertical slices, each independently committable. Slice 1 is the credential and the
single decryption chokepoint; slice 2 is the checker that consumes it.

### 1. Cluster connection (encrypted credential, the `clusterUid` everything references)

Model a **Kubernetes cluster connection** as a stored, encrypted **Integration** — the
existing umbrella entity (`server/internal/db/models/integration.go:64-85`, table
`integrations`) that already backs both notification channels and the Freebox data
source. (The legacy `IntegrationConnection`/`integration_connections` names were renamed
to `Integration`/`integrations`.) Add a new `ConnectionType` value (enum at
`integration.go:12-27`), e.g. `kubernetes`.

- **Stored fields.** Public side in `settings` (queryable JSONB): `apiServer` (URL),
  `caCert` (PEM, optional), `insecureSkipTLSVerify` (bool), `inCluster` (bool). Secret
  side in `settings_private` (the AES-256-GCM envelope written via
  `credentials.EncryptForOrg`): `token` **or** a pasted `kubeconfig`. `settings_private_keys`
  lists which keys are secret so the dashboard renders placeholders (the same
  `configPrivateKeys` pattern checks use). An **in-cluster** connection (C) stores no
  secret — `inCluster=true`, resolved via `rest.InClusterConfig()` at connect time.
- **Capability.** Extend `CapabilitiesFor(connType)` (`integration.go:51-58`) so the
  `kubernetes` type reports `CanSource: true` — this is what `03` and the frontend gate
  the Kubernetes scan method on (mirrors how Freebox's `CanSource` gates its scan option).
- **The decryption chokepoint.** New package `server/internal/integrations/kubernetes/`,
  mirroring `server/internal/integrations/freebox/lanlookup.go`. A
  `kubernetesClient(ctx, db, creds, orgUID, clusterUid) (*kubernetes.Clientset, error)`
  helper: load the `integrations` row by UID, verify org + type, parse public settings,
  decrypt the token/kubeconfig via `creds.DecryptForOrg(ctx, orgUID, *settingsPrivate)`
  (exactly as `resolveAppToken`, `lanlookup.go:96-124`), build a `*rest.Config`
  (`rest.InClusterConfig()` when `inCluster`, else from apiServer+token+CA), and return a
  clientset. This is the single place the secret is decrypted; both the checker (slice 2)
  and the discovery engine (`03`) call it.
- **Test connection.** Reuse the existing integration test path: `TestIntegration`
  (`server/internal/handlers/integrations/service.go:972-1038`, route
  `POST /orgs/:org/integrations/:uid/test`, `handler.go:193`) dispatches per-type. Add a
  `ValidateConnection` for `kubernetes` (analog of
  `server/internal/integrations/freebox/service.go:89-115`) that builds the clientset and
  calls `/version` (or `list namespaces`) — surfacing RBAC/credential failures at
  registration time.
- **CRUD + management UI.** Register / test / delete a cluster connection via the existing
  integrations CRUD (`/orgs/:org/integrations`, `server/internal/app/server.go:868-882`;
  hooks `useCreateIntegration`/`useUpdateIntegration`/`useDeleteIntegration`,
  `web/dash0/src/api/hooks.ts:2815-2854`). Add a small **"Kubernetes clusters"** surface
  in the dashboard (mirrors the Freebox integration UI; reuse design-reference primitives
  per `web/dash0/CLAUDE.md`).
- **Required cluster RBAC** (documented; ship an example read-only `ClusterRole`):
  `list`/`get` on `deployments`, `replicasets` (apps), and — for `03` — `services`,
  `ingresses` (networking), `nodes`, `endpoints`, across the monitored namespaces.

### 2. The `kubernetes` checker — replica-health (the HEALTHCHECK analog)

A greenfield checker modelled on `checkdocker` (the closest existing template — an
infra-category checker that talks to an external API and reports up/down/warning),
following the six registration steps in `server/internal/checkers/CLAUDE.md`:

1. `CheckTypeKubernetes CheckType = "kubernetes"` in `checkerdef/types.go` (the `CheckType`
   const block, `:102-173`, where `CheckTypeDocker` lives).
2. A `CheckTypeMeta` entry in `ListCheckTypeMetas` (`types.go:~279-286`) — **required**, or
   `activation_test.go:19` (`r.Len(enabled, len(checkerdef.ListCheckTypeMetas()))`, asserts
   every meta enabled by default) fails.
3. Import + `case` in **both** registry switches: `GetChecker`
   (`registry/registry.go:66-140`, docker at `:127-128`) and `ParseConfig` (`:148-223`,
   docker at `:210-211`).
4–6. New package + config validation + lint/tests, as below.

- New package `server/internal/checkers/checkkubernetes/` with `config.go` + `checker.go`.
- `KubernetesConfig`:
  ```go
  type KubernetesConfig struct {
      ClusterUID string `json:"clusterUid"`        // the stored connection (slice 1)
      Namespace  string `json:"namespace"`         // required
      Kind       string `json:"kind"`              // "Deployment" | "ReplicaSet"
      Name       string `json:"name"`              // required
      Timeout    string `json:"timeout,omitempty"` // default "10s", max 60s
  }
  ```
  `Validate`: `clusterUid`, `namespace`, `name` non-empty; `kind ∈ {Deployment,
  ReplicaSet}`; timeout `>0 && ≤60s` — mirrors `checkdocker/config.go:137-166`. No secret
  fields (the token lives in the connection, never here).
- `Check`: build the clientset via slice-1's `kubernetesClient(ClusterUID)`; on
  client/auth failure → `StatusError` (mirrors `checkdocker/checker.go:89-96`). Fetch the
  workload; **`NotFound` → `StatusDown`** ("workload not found") — note `checkdocker`'s
  `handleInspectError` (`checker.go:117-138`) folds all non-deadline errors into
  `StatusDown` without a NotFound branch, so the k8s checker adds the explicit
  `apierrors.IsNotFound` check; deadline-exceeded → `StatusTimeout`, other API error →
  `StatusDown`. Then apply the up/warning/down rule from Q1 off `spec.replicas` vs
  `status.readyReplicas` (+ Deployment conditions). Outputs: `namespace`, `kind`, `name`,
  `images`, `conditions`; metrics: `desiredReplicas`, `readyReplicas`,
  `availableReplicas`, `updatedReplicas`, `unavailableReplicas`, `query_time_ms`.

## Decisions (applied 2026-06-21)

1. **Promoted/manual check → a new `kubernetes` checker (Q1).** Monitors a Deployment's /
   ReplicaSet's `readyReplicas` vs desired (+ Deployment conditions); up / warning / down
   as in Q1. The direct structural mirror of how `docker` mirrors HEALTHCHECK. Accepts the
   `k8s.io/client-go` dependency cost as unavoidable.
2. **Connection → stored encrypted Integration referenced by `clusterUid` (Q2, B).** The
   Freebox model — a new `kubernetes` `ConnectionType` on the existing `integrations`
   table, secret in the `settings_private` envelope; never embed the token in check config.
   In-cluster service account (C) is offered as a zero-config convenience; embedded secrets
   (A) rejected.
3. **Workload kinds → Deployment + ReplicaSet in v1.** StatefulSet / DaemonSet / etc. are a
   follow-up on the same `Check` code path.
4. **Carved out of the discovery spec.** This checker + connection are the prerequisite the
   container-discovery spec got for free; `03` depends on it and adds nothing to the
   checker.

## Files to create / modify

### New (backend)
- `server/internal/checkers/checkkubernetes/{config.go,checker.go}` + tests — the
  replica-health checker (slice 2).
- `server/internal/integrations/kubernetes/{client.go,service.go}` + tests — the
  `kubernetesClient` resolver/decryption chokepoint, in-cluster detection, and
  `ValidateConnection` test probe (slice 1).
- An example read-only `ClusterRole` manifest (docs) for the required RBAC.

### Modified (backend)
- `server/go.mod` / `go.sum` — add `k8s.io/client-go`, `k8s.io/api`,
  `k8s.io/apimachinery` (pinned; `go mod tidy` reviewed).
- `server/internal/checkers/checkerdef/types.go` — `CheckTypeKubernetes` + `CheckTypeMeta`.
- `server/internal/checkers/registry/registry.go` — import + `case` in both switches.
- `server/internal/db/models/integration.go` — new `kubernetes` `ConnectionType` +
  `CapabilitiesFor` entry (`CanSource: true`); a `KubernetesSettings` struct for the public
  settings (parallel to `FreeboxSettings`).
- `server/internal/handlers/integrations/service.go` — `kubernetes` branch in
  `TestIntegration` → `ValidateConnection`.

### New / modified (frontend)
- A "Kubernetes clusters" management surface (register / test / delete) — reuse
  design-reference primitives.
- `web/dash0/src/api/hooks.ts` — cluster-connection CRUD/test hooks (reuse the
  `useCreateIntegration` family); a `canSource('kubernetes')` capability.
- i18n — cluster-management strings in `web/dash0/src/locales/{en,fr,de,es}/` (integration
  namespace).

## Verification

- **Unit (table-driven, `testify/require`, `t.Parallel()` — `server/CLAUDE.md`):**
  - `KubernetesChecker` against a `client-go` `fake.Clientset`: ready==desired → up;
    partial → warning; zero ready → down; scaled-to-zero → warning;
    `ProgressDeadlineExceeded` → down; missing workload (`NotFound`) → down; bad cluster →
    error; deadline → timeout.
  - `KubernetesConfig.Validate`: empty clusterUid/namespace/name → error; bad kind →
    error; timeout bounds.
  - `kubernetesClient`: resolves a stored connection → decrypts token → builds a config;
    in-cluster path; unknown `clusterUid` → error; wrong org/type → error.
- **Integration:** register a `kubernetes` connection via the integrations API, hit
  `POST /integrations/:uid/test`, confirm a good credential passes and a bad token/RBAC
  gap surfaces a clear error.
- **End-to-end** (`make dev-test`, against a `kind`/minikube cluster or a stored
  kubeconfig): register a cluster, add a `kubernetes` check by hand for a Deployment,
  confirm it reports replica health; scale the Deployment down → check goes warning/down;
  scale to zero → warning.
- **Guards:** unknown `clusterUid` on a check → check reports error, not a panic;
  non-admin cannot register/delete a connection (403).
- `make build && make lint && make test`.

## Risk log

| Risk | Mitigation |
|---|---|
| No `kubernetes` checker exists today — this is a from-scratch checker, not a thin layer | Modelled step-for-step on `checkdocker`; the six registration steps in `checkers/CLAUDE.md` are followed and pinned by `activation_test.go` |
| `k8s.io/client-go` is a large dependency tree (binary size, maintenance) | Unavoidable for any k8s integration; same shape as the already-vendored Docker SDK; pinned + `go mod tidy` reviewed |
| A bearer token embedded in check config would leak the cluster secret | Token stored once, encrypted, in the `integrations` `settings_private` envelope via `internal/crypto/credentials/`; checks carry only `clusterUid` (the Freebox `channelUid` model) |
| The connection's RBAC may lack `list`/`get` on a resource → opaque failures | "Test connection" probe at registration surfaces RBAC gaps; ship an example read-only `ClusterRole` |
| In-cluster (C) only works when solidping runs inside the target cluster | Offered as a convenience default; the primary path is a configured remote connection (B) that works from anywhere |
| `checkdocker`'s error helper has no explicit NotFound branch | The k8s checker adds an explicit `apierrors.IsNotFound → down` check rather than reusing docker's fold-everything-to-down behaviour |

**Status**: Todo | **Created**: 2026-06-21 | **Clarified**: 2026-06-26 — carved out of the original kubernetes-discovery spec as the checker+connection prerequisite; integration references corrected to the renamed `Integration`/`integrations` model and the real `TestIntegration` path; `ListCheckTypeMetas` line refreshed (~279).
