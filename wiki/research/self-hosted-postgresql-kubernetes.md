# Self-hosted multi-PostgreSQL hosting on Kubernetes

How to run **many PostgreSQL instances** on your own servers, with **automatic
failover across machines**, **point-in-time recovery**, and **no hyperscaler**
(no RDS, no Cloud SQL). Everything below uses standard Kubernetes mechanisms
plus one operator, so the whole platform is declarative and GitOps-friendly.

**The one-paragraph summary:** rent or rack 3+ machines in separate failure
domains, run a small HA Kubernetes cluster (k3s or Talos) on them, give
PostgreSQL **local NVMe storage** (not replicated storage), and let
**CloudNativePG** handle replication, failover, backups, and connection
routing. Reliability comes from three independent layers: streaming
replication across nodes (availability), continuous WAL archiving to object
storage (durability/PITR), and an off-site copy of that object storage
(disaster recovery). Each tenant/app gets its own small `Cluster` resource —
one YAML file per database instance.

---

## 1. What "reliable" means here — pick your targets first

Before choosing anything, fix the two numbers everything else derives from:

| Target | Meaning | What this design achieves |
|---|---|---|
| **RTO** (recovery time) | How long a DB may be down after a node dies | ~10–60 s (automatic failover) |
| **RPO** (data loss) | How much committed data you may lose | 0 with synchronous replication; seconds→minutes with async + WAL archiving |

Also decide the failure scenarios you actually defend against, in order of
likelihood:

1. **A process/pod crashes** → Kubernetes restarts it. Trivial.
2. **A server dies** → automatic failover to a replica on another server.
3. **A disk dies** → same as server death; the replica is rebuilt elsewhere.
4. **The whole site burns down** → restore from off-site backups (this is
   where RPO is really decided).
5. **You `DROP TABLE` in prod** → point-in-time recovery from WAL archive.
   Replication does *not* protect against this — replicas replay your mistake
   in milliseconds. Backups are not optional.

## 2. Hardware and provider layout

You don't need a big cloud, but you do need **independent failure domains**.

- **Minimum: 3 nodes.** Quorum systems (etcd, synchronous replication,
  failover consensus) need an odd count to avoid split-brain. Two servers
  cannot give you safe automatic failover.
- Good options without a hyperscaler: Hetzner (bare metal or cloud), OVH,
  Scaleway dedibox, or colocated hardware. Ideally spread nodes across
  different physical hosts / racks / rooms; different datacenters of the same
  provider work if latency stays low (< ~2 ms for synchronous replication to
  stay pleasant).
- **Local NVMe on every node.** PostgreSQL is extremely sensitive to fsync
  latency. Local NVMe beats any network-attached volume you can self-host.
- **Private network between nodes**: provider vSwitch/private network, or
  WireGuard mesh (e.g. what k3s ships via `--flannel-backend=wireguard-native`,
  or Netbird/Tailscale). Replication and etcd traffic should never cross the
  public internet unencrypted.
- **One machine that is *not* part of the cluster** (can be tiny, can be at a
  different provider) for off-site backup storage — or an S3-compatible
  storage service (Backblaze B2, Wasabi, Scaleway Object Storage). Using a
  small storage vendor is not "using a big provider"; refusing off-site
  backups is just choosing data loss.

A concrete, budget-friendly reference layout:

```
┌────────────── provider A ─────────────┐   ┌── provider B ──┐
│  node1          node2          node3  │   │  backup box    │
│  cp+worker      cp+worker      cp+worker   │  MinIO/Garage  │
│  NVMe 1TB       NVMe 1TB       NVMe 1TB│   │  big HDD       │
└───────────────────────────────────────┘   └────────────────┘
        k3s HA (embedded etcd)                  S3 API, versioned buckets
```

## 3. Kubernetes base layer

Keep it boring:

- **Distribution**: k3s (simple, embedded etcd HA with 3 servers) or Talos
  (immutable, API-driven, arguably the best ops story for bare metal).
  Both are proven for exactly this use case.
- **3 control-plane nodes** that are also workers (fine at this scale). etcd
  gets quorum across the 3 machines.
- **Label your topology** so anti-affinity can work:

  ```bash
  kubectl label node node1 topology.kubernetes.io/zone=z1
  kubectl label node node2 topology.kubernetes.io/zone=z2
  kubectl label node node3 topology.kubernetes.io/zone=z3
  ```

- **Load balancing without a cloud LB**: MetalLB (L2 mode is enough) or
  kube-vip to give `Service type=LoadBalancer` a floating IP on your network.
  You only need this if databases must be reachable from outside the cluster.

## 4. Storage: the decision that makes or breaks it

This is the most common self-hosting mistake, so it gets its own section.

**Use local disks and let PostgreSQL do the replication. Do not put
PostgreSQL on replicated network storage (Longhorn, Ceph/Rook, DRBD).**

Why:

- PostgreSQL **already replicates itself** (streaming replication, managed by
  the operator). Putting it on replicated storage replicates every write
  twice — once at the storage layer, once at the database layer — doubling
  write amplification and adding network round-trips to every fsync.
- Failover on shared storage is *slower and riskier* than failover to a hot
  streaming replica that is already running and already has the data in RAM.
- When a node dies, the operator simply promotes a replica and re-clones a
  new one from the primary. You don't need the storage layer to survive node
  loss — the *database layer* survives it.

Practical setup:

- **StorageClass**: OpenEBS LocalPV (hostpath or device), or the
  `local-path` provisioner that ships with k3s. `WaitForFirstConsumer`
  binding mode so the PVC lands where the pod lands.
- Local PVs pin a pod to its node — that's expected and fine: if the node
  dies, CloudNativePG doesn't try to move the pod; it promotes a replica on
  another node and rebuilds the lost instance there.
- Keep **WAL on the same fast disk** unless you have a measured reason not
  to; CNPG supports a separate WAL volume (`walStorage`) if you have two disk
  classes.
- Replicated storage (Longhorn/Ceph) is still useful **for everything else**
  (small app volumes, MinIO's backing store on the backup box, etc.) — just
  not under Postgres data directories.

## 5. The operator: CloudNativePG

The operator is what turns "a pile of StatefulSets and scripts" into a
reliable platform. Use **[CloudNativePG](https://cloudnative-pg.io)** (CNPG) —
CNCF project, originally by EDB:

- No Patroni/etcd sidecar cluster to babysit: it uses the Kubernetes API
  itself for consensus and an instance manager as PID 1 in each pod.
- Native primitives: automated failover, switchover, rolling minor upgrades,
  replica re-cloning, backup/PITR, TLS, pooling, metrics — all declarative.
- One `Cluster` CR = one independent PostgreSQL instance-group. Perfect for
  multi-tenant hosting: **cheap to stamp out dozens of them**.

Alternatives, for the record:

| Operator | Notes |
|---|---|
| **CloudNativePG** | Recommended. Kubernetes-native failover, best docs, very active. |
| Zalando postgres-operator | Mature, Patroni-based, huge install base; older design. |
| Crunchy PGO | Solid, commercial backing; container images have licensing gotchas. |
| Percona PG Operator | Patroni-based, good if you're already in Percona-land. |
| StackGres | Feature-rich with a web console; heavier. |

Install:

```bash
helm repo add cnpg https://cloudnative-pg.github.io/charts
helm upgrade --install cnpg cnpg/cloudnative-pg \
  --namespace cnpg-system --create-namespace
```

### A production-shaped tenant cluster

One file like this per hosted database (templated later — see §8):

```yaml
apiVersion: postgresql.cnpg.io/v1
kind: Cluster
metadata:
  name: pg-acme            # tenant "acme"
  namespace: tenant-acme
spec:
  instances: 3             # 1 primary + 2 replicas, one per node
  imageName: ghcr.io/cloudnative-pg/postgresql:17

  primaryUpdateStrategy: unsupervised   # rolling minor upgrades, auto switchover

  storage:
    storageClass: openebs-hostpath      # LOCAL storage (see §4)
    size: 20Gi

  # Spread instances across nodes/zones — this is the redundancy
  affinity:
    topologyKey: topology.kubernetes.io/zone
    podAntiAffinityType: required

  # RPO=0 for this tenant: quorum-based synchronous replication.
  # Drop this block for cheaper async tenants (RPO = replication lag).
  postgresql:
    synchronous:
      method: any
      number: 1            # any 1 of the 2 replicas must confirm
    parameters:
      max_connections: "100"
      shared_buffers: 256MB
      wal_compression: zstd

  resources:               # requests == limits -> Guaranteed QoS, no OOM roulette
    requests: { cpu: "1", memory: 1Gi }
    limits:   { cpu: "1", memory: 1Gi }

  bootstrap:
    initdb:
      database: acme
      owner: acme

  # Continuous WAL archiving + base backups to object storage (see §6)
  plugins:
    - name: barman-cloud.cloudnative-pg.io
      isWALArchiver: true
      parameters:
        barmanObjectName: backup-store
```

What you get automatically:

- Services `pg-acme-rw` (primary), `pg-acme-ro` (replicas), `pg-acme-r`
  (any instance) — apps point at `-rw` and never chase failovers.
- App credentials in Secret `pg-acme-app`.
- A PodDisruptionBudget, so node drains never take out primary + replicas
  together.
- **Automatic failover**: primary node dies → a replica is promoted in
  seconds → `-rw` service repoints → the lost instance is re-cloned onto a
  healthy node when capacity returns.

> **Sync vs async trade-off:** synchronous (quorum) replication gives RPO=0
> but every commit waits for a replica's confirmation — that's why nodes need
> a fast private network. For low-value tenants, async (just omit the
> `synchronous` block) is usually fine: you risk the last few transactions if
> the primary's node is vaporized at the wrong instant.

## 6. Backups and PITR: where reliability actually lives

Replication keeps you *up*; **backups keep you *alive***. Non-negotiables:
continuous WAL archiving, scheduled base backups, off-site copy, and
**tested restores**.

### Object storage without a big provider

- **Self-hosted**: MinIO or Garage on the separate backup box (Garage is
  lighter and geo-replicates nicely if you later add a second backup site).
- **Or a small S3 vendor**: Backblaze B2 / Wasabi / Scaleway — pennies per GB
  and your backups survive your provider account, your cluster, and your
  building. Recommended even if you also run MinIO: two independent copies.

### Wiring it into CNPG (Barman Cloud plugin)

CNPG's current backup architecture is the **Barman Cloud plugin** (the older
in-tree `backup.barmanObjectStore` still works but is deprecated). Install the
plugin, then declare a store and reference it from clusters (as done in §5):

```yaml
apiVersion: barmancloud.cnpg.io/v1
kind: ObjectStore
metadata:
  name: backup-store
  namespace: tenant-acme
spec:
  retentionPolicy: "30d"
  configuration:
    destinationPath: s3://pg-backups/acme
    endpointURL: https://minio.backup.example.net
    s3Credentials:
      accessKeyId:     { name: backup-creds, key: ACCESS_KEY_ID }
      secretAccessKey: { name: backup-creds, key: SECRET_ACCESS_KEY }
    wal:
      compression: zstd
    data:
      compression: zstd
---
apiVersion: postgresql.cnpg.io/v1
kind: ScheduledBackup
metadata:
  name: pg-acme-nightly
  namespace: tenant-acme
spec:
  cluster: { name: pg-acme }
  schedule: "0 0 2 * * *"       # 02:00 nightly (6-field cron, seconds first)
  backupOwnerReference: self
  method: plugin
  pluginConfiguration:
    name: barman-cloud.cloudnative-pg.io
```

With WAL archiving on, your real RPO for full-site disaster is "seconds to a
few minutes" (WAL segments ship as they fill or on `archive_timeout`), and you
can restore to **any point in time** within retention.

### Restore = a new Cluster (this is the DR story too)

```yaml
apiVersion: postgresql.cnpg.io/v1
kind: Cluster
metadata:
  name: pg-acme-restored
spec:
  instances: 3
  storage: { storageClass: openebs-hostpath, size: 20Gi }
  bootstrap:
    recovery:
      source: origin
      recoveryTarget:
        targetTime: "2026-08-08 09:30:00+00"   # optional: PITR to before the oops
  externalClusters:
    - name: origin
      plugin:
        name: barman-cloud.cnpg.io
        parameters:
          barmanObjectName: backup-store
          serverName: pg-acme
```

Because restore is *just another manifest*, you can — and must — **drill it**:
a monthly job (or manual ritual) that restores the latest backup of one tenant
into a scratch namespace, runs a sanity query, and tears it down. An untested
backup is a hypothesis. Alert if the drill fails or if the last successful
backup / archived WAL is older than your RPO (CNPG exposes both as metrics).

## 7. Getting connections in, and pooling

- **Inside the cluster**: apps just use `pg-acme-rw.tenant-acme.svc:5432`.
  Failover is invisible — the Service repoints.
- **Outside the cluster**: expose per-tenant `LoadBalancer` Services through
  MetalLB/kube-vip, or one TCP port per tenant on an L4 proxy. Don't try to
  route PostgreSQL through an HTTP ingress; it's not HTTP. (SNI-based routing
  on a single port is possible with newer libpq + Traefik/HAProxy SNI
  passthrough, but per-tenant ports/IPs are simpler and debuggable.)
- **TLS**: CNPG generates and rotates server certs by default; set
  `enableSuperuserAccess: false` (default) and SCRAM auth stays on. For
  public exposure, require TLS in `pg_hba` custom rules.
- **Pooling**: PostgreSQL connections are expensive (~a backend process
  each). For any tenant with bursty or many-connection apps, put CNPG's
  `Pooler` (managed PgBouncer) in front:

```yaml
apiVersion: postgresql.cnpg.io/v1
kind: Pooler
metadata:
  name: pg-acme-pooler
  namespace: tenant-acme
spec:
  cluster: { name: pg-acme }
  instances: 2
  type: rw
  pgbouncer:
    poolMode: transaction
    parameters:
      max_client_conn: "500"
      default_pool_size: "20"
```

## 8. Multi-tenancy: stamping out databases as code

The whole point of "multi-PostgreSQL": each tenant/app gets its **own
`Cluster`**, not a database inside a shared instance.

- **Isolation**: noisy neighbors are contained by per-cluster
  CPU/memory/storage limits; a tenant filling its disk doesn't hurt others.
- **Independent lifecycle**: per-tenant PostgreSQL version, PITR window,
  restore, deletion. You can restore tenant A to 09:30 without touching B.
- **Blast radius**: a corrupted catalog or runaway autovacuum stays inside
  one small cluster.
- Overhead per cluster is modest (3 pods you were going to size anyway).
  Only fold multiple *databases of one owner* into one cluster.

Mechanics:

- **Namespace per tenant** (`tenant-<name>`), holding the `Cluster`,
  `ObjectStore`, `ScheduledBackup`, `Pooler`, Secrets, and a
  `ResourceQuota`. Deleting the namespace deletes the tenant (backups
  survive in object storage — deliberate!).
- **Template it**: a small Helm chart or Kustomize base where a tenant is
  ~10 lines of values (name, size, resources, sync-or-async, backup
  retention). This mirrors the config-as-code approach used elsewhere in
  SolidPing.
- **GitOps**: commit tenant files to a repo, let ArgoCD/Flux apply. Creating
  a hosted PostgreSQL becomes a PR; disaster recovery of *the whole
  platform's definition* becomes `git clone` + `kubectl apply` + restores
  from object storage.
- Capacity rule of thumb: keep every node under ~60–70% memory committed by
  Postgres pods, so that any single node's instances can fail over onto the
  survivors without evictions.

## 9. Monitoring and alerting

CNPG exports Prometheus metrics from every instance out of the box
(`podMonitorEnabled: true` with Prometheus Operator, or scrape port 9187).

Minimum alert set:

| Alert | Why |
|---|---|
| `cnpg_pg_replication_lag` beyond threshold | Your failover candidate is stale → real RPO is growing |
| Last archived WAL age > RPO target | WAL archiving silently broken = your DR is broken |
| Last successful base backup > 1.5× schedule | Backups failing |
| Cluster has fewer ready instances than spec | Running without redundancy |
| Disk usage > 80% on PG PVCs | Postgres on a full disk goes read-only, then down |
| Failover/switchover events | Not always a page, but always a look |
| Restore-drill job failed | The only proof backups work |

Grafana: the official CNPG dashboard covers per-cluster health, lag, WAL,
backup age. And of course, black-box `pg` checks from outside the network
(SolidPing's `postgresql` check type does exactly this) tell you what your
*users* see, which no amount of in-cluster monitoring replaces.

## 10. Operations: failures and upgrades in practice

- **Node dies**: failover fires automatically (typically well under a
  minute). When the node comes back, its old primary is demoted and
  re-synced via `pg_rewind`, or re-cloned if diverged. Your job: replace the
  hardware; the data layer heals itself.
- **Planned node maintenance**: `kubectl cordon` + `kubectl drain` — CNPG's
  PDBs and switchover make this a zero-downtime, ordered evacuation
  (switchover first, replicas after).
- **Minor version upgrades** (17.4 → 17.5): bump `imageName`, rolling
  restart, replicas first, supervised or not — your choice per cluster.
- **Major version upgrades** (16 → 17): CNPG supports declarative offline
  in-place upgrades (bump the image major; short downtime while `pg_upgrade`
  runs), or blue/green via a new cluster bootstrapped with `import` from the
  old one for near-zero downtime.
- **Scaling a tenant**: bump `resources`/`storage.size` (local PVs make
  storage growth the one awkward spot: growth happens via replica re-clone
  onto bigger allocation — plan sizes generously).
- **Second site / serious DR**: CNPG "replica clusters" let a second
  Kubernetes cluster (another DC, another provider) continuously replay from
  the same object store or stream from the primary cluster, promotable by
  flipping one field. That's the step *after* everything above works.

## 11. Security checklist

- `enableSuperuserAccess: false` (default) — nobody speaks as `postgres`.
- SCRAM-SHA-256 auth (default), TLS on all client connections; CNPG manages
  and rotates certs, or plug in cert-manager.
- NetworkPolicies: only the tenant's app namespace (and the pooler) may reach
  the tenant's pods on 5432; only the operator namespace may reach the
  instance-manager port.
- Backup credentials: per-tenant bucket paths, and object-storage
  **versioning/immutability** on the backup bucket so ransomware or a fat
  finger can't destroy history.
- Keep the operator and PostgreSQL images updated (Renovate on the GitOps
  repo does this for free).
- Encrypt disks at rest (LUKS on the nodes) if the hardware isn't physically
  yours.

## 12. Build order (checklist)

1. ☐ 3 nodes, private network between them, NVMe local disks.
2. ☐ k3s/Talos HA (3 control planes), zone labels, MetalLB if external
   access is needed.
3. ☐ Local-PV StorageClass (`WaitForFirstConsumer`).
4. ☐ Backup target: MinIO/Garage on a 4th machine at another location, or
   B2/Wasabi bucket — versioned.
5. ☐ Install CNPG operator + Barman Cloud plugin + Prometheus/Grafana.
6. ☐ First tenant `Cluster` (3 instances, required anti-affinity, WAL
   archiving, nightly `ScheduledBackup`).
7. ☐ **Kill a node on purpose.** Watch failover. Measure RTO.
8. ☐ **Restore the backup into a scratch namespace.** Prove PITR. Automate
   this as a recurring drill.
9. ☐ Wire alerts (lag, WAL age, backup age, ready instances, disk).
10. ☐ Template tenants (Helm/Kustomize) + GitOps; onboard the rest.
11. ☐ Only then, if needed: poolers, replica cluster on a second site.

Steps 7 and 8 are the difference between "we have HA and backups" and "we
*believe* we have HA and backups". Do them before any real data arrives, and
keep doing them.

---

## References

- CloudNativePG docs: https://cloudnative-pg.io/documentation/
- Barman Cloud plugin: https://cloudnative-pg.io/plugin-barman-cloud/
- CNPG replica clusters (multi-site DR): https://cloudnative-pg.io/documentation/current/replica_cluster/
- OpenEBS LocalPV: https://openebs.io/docs
- MetalLB: https://metallb.universe.tf / kube-vip: https://kube-vip.io
- Garage (self-hosted S3): https://garagehq.deuxfleurs.fr
- Talos Linux: https://www.talos.dev — k3s: https://k3s.io
