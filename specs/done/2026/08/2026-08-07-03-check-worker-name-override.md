---
model: opus
effort: medium
---

# Check-worker identity is hard-wired to `os.Hostname()`, which blocks IPv6 egress on Kubernetes

## Problem

A check worker's `workers` row is derived entirely from the OS hostname, with no
way to override it:

```go
// server/internal/checkworker/worker.go:325-337
hostname, err := os.Hostname()
if err != nil {
	hostname = "unknown"
}
// Limit hostname to ensure slug doesn't exceed 20 characters (slug = hostname-cr-X)
// Reserve 5 chars for "-cr-X", leaving 15 for hostname
if len(hostname) > 15 {
	hostname = hostname[:15]
}
slug := strings.ToLower(hostname)
name := hostname
```

`NodeConfig` ([`config.go:413-417`](../../server/internal/config/config.go)) exposes
only `Role` and `Region`, so there is no `SP_NODE_NAME` equivalent to the existing
`SP_NODE_ROLE` / `SP_NODE_REGION`. The slug must satisfy a Postgres CHECK
constraint ([`001_v0_1_0.up.sql:170`](../../server/internal/db/postgres/migrations/001_v0_1_0.up.sql)):

```sql
slug text not null check (slug ~ '^[a-z][a-z0-9-]{2,20}$'),
```

Two concrete problems follow.

### 1. It blocks IPv6 egress for check workers on Kubernetes

Investigated on the k8xp cluster (2026-08-07). SolidPing's check pods have **no
IPv6 at all** — no global address, no v6 default route, `ping6` returns
`Network unreachable`. The cluster is single-stack (`cluster-cidr 10.42.0.0/16`,
flannel `"EnableIPv6": false`), and k3s
[cannot enable dual-stack on an already-created cluster](https://docs.k3s.io/networking/basic-network-options).
The nodes themselves have perfectly good IPv6 (eu2 `2a02:4780:28:5d93::1`, us1
`2607:f1c0:f0a3:3b00::1`, both with working default routes and sub-8ms internet
pings) — it just never reaches the pod.

The one mechanism that fixes this without rebuilding cluster networking is
`hostNetwork: true`, which puts the worker directly in the node's netns. It was
tried and **everything worked except registration**: v6 address, v6 default route,
internet ping6, AAAA resolution, and full cluster connectivity (the pods' existing
`dnsPolicy: None` config is honoured as-is, `kube-dns-local` still resolves,
ClusterIP egress to Postgres still connects). There is no port conflict either,
since `role=checks` opens no listening socket.

It fails because `hostNetwork` shares the **host UTS namespace**, so `spec.hostname`
is silently ignored and `os.Hostname()` returns the node name — which contains dots:

```
level=WARN  msg="SQL query failed" operation=INSERT
  error="ERROR: new row for relation \"workers\" violates check constraint \"workers_slug_check\" (SQLSTATE=23514)"
level=ERROR msg="Check worker error" error="failed to register worker: ..."
```

`eu2.k8xp.com` fails the `^[a-z][a-z0-9-]{2,20}$` regex on the dots, the worker
never registers, and the deployment runs no checks. The change had to be rolled
back.

### 2. Silent slug collisions from the 15-char truncation

Registration is upsert-by-slug
([`postgres.go:1081`](../../server/internal/db/postgres/postgres.go)), so two
workers whose hostnames share the first 15 characters silently **collapse onto one
`workers` row** and fight over it. This is not hypothetical: Kubernetes pod names
are `<deployment>-<hash>-<rand>`, so `solidping-checks-eu2-…` and
`solidping-checks-us1-…` both truncate to `solidping-check`. The k8xp manifests work
around it by pinning `spec.hostname: solidping-eu2` / `solidping-us1` — and that pin
is exactly what `hostNetwork` discards.

There is no diagnostic for this today; the workers just interfere with each other.

Note also that the comment at
[`worker.go:330-331`](../../server/internal/checkworker/worker.go) is stale: it
justifies the 15-char cut by reserving 5 characters for a `-cr-X` suffix, but
nothing appends such a suffix — the slug is literally the truncated hostname.

## Proposal

Add an explicit worker-name override, so worker identity is a deployment decision
rather than an accident of the container's UTS namespace.

### 1. Config

Add `Name` to `NodeConfig` ([`config.go:413-417`](../../server/internal/config/config.go)):

```go
type NodeConfig struct {
	Role   string `koanf:"role"`   // Node role: all, api, jobs, checks
	Region string `koanf:"region"` // Node region (required for checks role)
	Name   string `koanf:"name"`   // Worker slug/name override (default: os.Hostname())
}
```

The koanf env loader ([`config.go:1197-1201`](../../server/internal/config/config.go))
lowercases and converts `_` to `.` under the `SP_` prefix, so this is reachable as
`SP_NODE_NAME` with no extra wiring — consistent with `SP_NODE_ROLE` and
`SP_NODE_REGION`.

### 2. Use it in `registerWorker`

In [`worker.go:323-346`](../../server/internal/checkworker/worker.go): when
`config.Node.Name` is set, use it verbatim for both `slug` and `name` and skip the
hostname read and truncation entirely. When unset, keep today's behaviour exactly
(no change for existing deployments).

### 3. Validate at startup, not at INSERT time

Today an invalid slug surfaces as an opaque `SQLSTATE=23514` from Postgres, after
the worker has already started. Validate the effective slug — override *or* derived
hostname — against `^[a-z][a-z0-9-]{2,20}$` during config validation and fail fast
with a message naming the offending value and `SP_NODE_NAME` as the fix.

This also converts problem 1 from a confusing runtime crash into a clear startup
error, and gives problem 2 a home: when the hostname is truncated, log a WARN
naming the resulting slug and pointing at `SP_NODE_NAME`, so a silent collision
becomes visible.

### 4. Same treatment for the job worker

[`jobworker/worker.go:451-458`](../../server/internal/jobs/jobworker/worker.go)
repeats the identical `hostname[:15]` pattern for its self-stats slug and has the
same failure mode. Reuse whatever helper comes out of steps 2-3.

### 5. Docs

- [`web/docs/docs/configuration/index.md`](../../web/docs/docs/configuration/index.md) —
  add `SP_NODE_NAME` to the node-role table alongside `SP_NODE_ROLE` / `SP_NODE_REGION`.
- [`wiki/conventions/runners.md`](../../wiki/conventions/runners.md) — document that
  worker identity defaults to the hostname and when to override it (any orchestrator
  where the hostname is not stable, unique within 15 chars, and slug-legal).

Call out the Kubernetes `hostNetwork` case explicitly: it is the reason the knob
exists, and someone will hit it again.

### 6. Tests

- Override set → slug/name come from it, hostname never consulted.
- Override unset → current behaviour preserved (truncation, lowercasing).
- Invalid override (dots, too long, leading digit) → startup validation error, not
  a DB constraint violation.
- Hostname requiring truncation → WARN emitted.

## Deployment follow-up (k8xp, separate repo)

Once released, `k8s/solidping/overlays/dev-checks-{us1,eu2}/deployment-patch.yaml`
in the k8xp repo swap `hostname: solidping-<region>` for:

```yaml
      hostNetwork: true
      # ... env:
        - name: SP_NODE_NAME
          value: "solidping-eu2"   # us1: solidping-us1
```

which preserves the existing worker slugs (no stale rows) while giving the checkers
the node's real IPv6 stack. Both overlays currently carry a comment recording why
`hostNetwork` is blocked; that comment should be replaced at the same time.

## Out of scope — needs its own spec

Egress alone does **not** make dual-stack targets get checked over IPv6. Most
checkers explicitly prefer IPv4 at address-selection time and only fall back to
AAAA when no A record exists — TCP
([`checktcp/checker.go:130-152`](../../server/internal/checkers/checktcp/checker.go)),
ICMP, UDP, SSL, SSH, IMAP, POP3, SMTP. Only HTTP is fully dual-stack (bare
`http.Client`, so Go's Happy Eyeballs applies). There is no `ip_version` *input*
option anywhere; it exists only as an output field.

So this spec's effect is: **IPv6-only targets become checkable** (they have no A
record, so the existing fallback picks AAAA). Choosing the family for a dual-stack
target is a separate feature — a per-check `ip_version: auto|v4|v6` option threaded
through the shared resolver
([`checkerdef/resolve.go:31-32`](../../server/internal/checkers/checkerdef/resolve.go)).

## Acceptance criteria

- [ ] `SP_NODE_NAME` sets the check worker's `workers.slug` and `name`.
- [ ] Unset `SP_NODE_NAME` reproduces today's behaviour byte-for-byte.
- [ ] An invalid effective slug fails at startup with an actionable message naming
      `SP_NODE_NAME`, never as a Postgres constraint violation.
- [ ] Hostname truncation emits a WARN identifying the resulting slug.
- [ ] Job worker uses the same identity logic.
- [ ] A check worker runs with `hostNetwork: true` on Kubernetes, registers under
      its configured slug, and successfully checks an IPv6-only target.
- [ ] Docs list `SP_NODE_NAME` and explain the `hostNetwork` case.
