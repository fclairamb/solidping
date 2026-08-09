---
model: opus
effort: high
---

# Node role can't express "api + jobs, but not checks", blocking a clean IPv6 fix for the EU1/default region

## Problem

Check `c0437dd8-65bd-4767-9956-6c899eef8b5b` (org `stonaltech`, named "IPv6",
target `https://v6.test-ipv6.vu.ayepv6.com/...`, an IPv6-only host) validates that
check workers can reach IPv6-only targets. After the `hostNetwork: true` +
`SP_NODE_NAME` fix (spec `2026-08-07-03-check-worker-name-override.md`, applied to
the k8xp cluster 2026-08-08), the `eu-2`, `us-1`, and `jp-1` regions all pass
consistently. The `default` region — displayed on the dashboard as **"EU1"**
(`SP_REGIONS` slug→name mapping) — still fails on every cycle, e.g. result
`019fe5c3-5007-7001-b808-362cc8e58e73` (2026-08-09):

```
dial tcp [2001:19f0:5001:c44:5400:1ff:fe39:a00a]:443: connect: network is unreachable
```

### Why `default` can't get the same fix

`default`-region checks are executed by the main `solidping` Deployment in the
k8xp repo (`k8s/solidping/overlays/dev/environment-patch.yaml`), which sets no
`SP_NODE_ROLE` and so defaults to `NodeRoleAll` ("all") — it runs the HTTP
API/dashboard, the job processor, *and* the check executor all in one process
(`ShouldRunAPI` / `ShouldRunJobs` / `ShouldRunChecks`,
[`config.go:461-471`](../../server/internal/config/config.go)). This pod is
scheduled with no `nodeSelector` and currently lands on `master.k8xp.com` — the
cluster's **k8s control-plane node**.

The zero-cost IPv6 fix validated for eu2/us1 (`hostNetwork: true`, sharing the
node's network namespace) can't simply be copied onto this pod: unlike the
checks-only workers (no listening socket, no user-facing traffic), this pod
serves the public dashboard/API. Putting it on the host network namespace of the
control-plane node is a materially different, more sensitive change, and
shouldn't be done as a side effect of an IPv6 test fix.

The clean alternative — keep api+jobs pod-networked, move `default`-region checks
duty onto a *separate* checks-only `hostNetwork` worker, exactly like eu2/us1 —
is blocked by the role model itself.
[`NodeConfig.Role`](../../server/internal/config/config.go) (`config.go:413-423`)
is a single string, and each of `ShouldRunAPI` / `ShouldRunJobs` /
`ShouldRunChecks` (`config.go:461-471`) only returns true for `NodeRoleAll` or
its one matching role
([role constants at `config.go:26-34`](../../server/internal/config/config.go)):
`all`, `api`, `jobs`, `checks`, `agent`. There is no value meaning "api + jobs,
not checks":

- Setting the main pod's role to `api` would silently stop job processing —
  there is no other deployment running the job processor today.
- Setting it to `jobs` would stop serving the dashboard/API entirely.

So today, splitting checks off the main pod is not expressible without either
losing a duty the pod currently provides, or standing up a role value that
doesn't exist yet.

## Proposal

### 1. Multi-value `SP_NODE_ROLE`

Extend `NodeConfig.Role` to accept a comma-separated list (e.g.
`SP_NODE_ROLE=api,jobs`), while every value accepted today (`all`, `api`, `jobs`,
`checks`, `agent`) keeps working byte-for-byte unchanged — no existing deployment
sets a multi-value role, so this is purely additive.

- Parse the configured value into a role set. `ShouldRunAPI` / `ShouldRunJobs` /
  `ShouldRunChecks` become "the set contains `all`, or contains the specific
  role" instead of an exact string match.
- `checks` in the set still requires `SP_NODE_REGION`, unchanged from today's
  rule ([`config.go:1897`](../../server/internal/config/config.go)).
- `agent` is a wholly different binary mode (no DB, no migrations, deported
  worker over WebSocket) and must stay mutually exclusive with everything else.
  A flat multi-value list makes a typo like `agent,api` newly representable —
  validate that explicitly and fail fast with a message naming the conflict.
- Find and extend whatever backs `ValidNodeRoles()` (referenced from
  `validateNodeConfig`, [`config.go:~1888`](../../server/internal/config/config.go))
  so it accepts either one of the historic exact values, or a well-formed
  comma-separated subset of `{api, jobs, checks}`.

### 2. Docs

- `web/docs/docs/configuration/index.md` — document the multi-role form and when
  to use it: separating checks-with-`hostNetwork` from an api/jobs pod that
  shouldn't be on the host network.
- `wiki/conventions/runners.md` — same, alongside the existing `SP_NODE_NAME` /
  `hostNetwork` writeup from spec `2026-08-07-03`.

### 3. Tests

- Single-role values (including `all`) behave identically to current behavior.
- Multi-role combinations resolve `ShouldRunAPI`/`ShouldRunJobs`/`ShouldRunChecks`
  correctly (e.g. `api,jobs` → API and jobs run, checks does not).
- Invalid combinations (`agent` combined with anything, an unknown role in the
  list, empty/duplicate segments) are rejected at startup with an actionable
  message — not a silent misconfiguration.

## Deployment follow-up (k8xp, separate repo)

Once released, split the k8xp `k8s/solidping/overlays/dev` main deployment:

- Change the main `solidping` Deployment's env to `SP_NODE_ROLE=api,jobs`
  (dropping the implicit `all`) — it keeps its current placement and stays off
  `hostNetwork`; public exposure is unchanged.
- Add a new checks-only overlay (e.g. `dev-checks-default`), mirroring
  `dev-checks-eu2`/`dev-checks-us1`
  (`k8s/solidping/overlays/dev-checks-{eu2,us1}/deployment-patch.yaml`):
  `SP_NODE_ROLE=checks`, `SP_NODE_REGION=default`, `hostNetwork: true`,
  a fixed `SP_NODE_NAME` (e.g. `solidping-default`), and
  `livenessProbe`/`readinessProbe`/`startupProbe: null` (same reasoning as
  eu2/us1 — role=checks opens no HTTP listener).
- Pin it via `nodeSelector`/toleration to a node with real IPv6.
  `master.k8xp.com` has `2001:41d0:305:2100::3afa` and is fine to use here even
  though it's the control-plane node — this pod is checks-only with no public
  socket, the same low-risk profile already validated for eu2/us1's
  `hostNetwork` pods (unlike putting the *api* pod there, which this spec
  specifically avoids).
- Verify against check `c0437dd8-65bd-4767-9956-6c899eef8b5b`: `default`/EU1
  should flip from `dial tcp [...]: network is unreachable` to `status=up`,
  matching eu-2/us-1/jp-1.

## Out of scope

- `@stonaltech/aws-paris` (custom region, currently unstaffed, falls back to
  `default`'s worker pool): may start passing as a side effect once `default`
  has a dedicated checks-only worker, but isn't this spec's target and
  shouldn't block it.
- Any change to prod's role/deployment topology — this covers the dev overlays
  only, matching the scope of the original IPv6 spec (`2026-08-07-03`).

## Acceptance criteria

- [ ] `SP_NODE_ROLE` accepts a comma-separated role list; every single-value
      role behaves identically to today.
- [ ] Invalid role combinations fail fast at startup with a message naming the
      offending value.
- [ ] Docs updated for the multi-role form.
- [ ] k8xp: main deployment runs `api,jobs` only (no `hostNetwork`); a new
      checks-only `default`-region deployment runs with `hostNetwork: true` and
      passes the IPv6 check consistently, without destabilizing the public
      dashboard/API pod.

## Implementation Plan

1. **`server/internal/config/node_role.go` (new)** — the whole role model in one
   place:
   - `NodeRoleSet` (a `map[string]bool`) with `Has` (raw membership) and `Runs`
     (membership honoring `all`).
   - `ParseNodeRoles(raw string) (NodeRoleSet, error)`: split on `,`, trim each
     segment, reject an empty raw value / empty segment / unknown token /
     duplicate token, then reject `all` or `agent` appearing alongside anything
     else (both are whole-node modes). `MultiValueNodeRoles()` documents the
     `{api, jobs, checks}` subset usable in a list.
   - `Config.NodeRoles()` parses on every call — the `node.role` system
     parameter can overwrite `Config.Node.Role` *after* `Validate()`, so a
     cached set would go stale.
   - `ShouldRunAPI` / `ShouldRunJobs` / `ShouldRunChecks` move here and become
     `NodeRoles().Runs(...)`; add `IsAgentMode()` for the `agent` branch.
2. **`config.go`** — drop the three exact-string `ShouldRun*`, extend the
   `ErrInvalidNodeRole` text, add `ErrExclusiveNodeRole`; make
   `applyNodeRolePoolDefaults` set-aware ("runs checks or jobs and does *not*
   serve the API" → smaller pool, which is byte-identical for every single-value
   role); rewrite `validateNodeConfig` on top of `ParseNodeRoles` (region still
   required only when `checks` is an explicit member, never for `all`).
3. **`server/main.go`** — `cfg.Node.Role == config.NodeRoleAgent` →
   `cfg.IsAgentMode()`.
4. **Docs** — `web/docs/docs/configuration/index.md`, `wiki/conventions/runners.md`,
   `README.md`.
5. **Tests** — `server/internal/config/node_role_test.go`: a backward-compat
   table pinning the `ShouldRun*` triple for every legacy single value plus the
   unset default; the new `api,jobs` combination; every rejection path; and an
   end-to-end `Load()` test proving a comma-separated value survives the koanf
   env path unsplit.

Out of scope here: the k8xp deployment split (separate repo). Nothing in this
change assumes it has happened — `all` stays the default.
