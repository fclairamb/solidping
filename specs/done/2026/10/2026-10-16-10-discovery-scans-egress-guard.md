---
model: opus
effort: high
---

# Put discovery scans (LAN, container, Kubernetes) under the egress policy

## Problem
Found by the security scan of 2026-10-13 (finding T1), plus the unguarded-dial half of finding M2 from the scan of 2026-10-11.

Severity: high on SaaS. Any org can map the platform's internal network, Docker daemons and private Kubernetes API servers from the shared API/jobs process. On self-hosted it is informational: scanning the LAN is what the feature is for.

Who can trigger it:
- **Discovery scans** need an org admin (`StartScan` checks `isAdmin`, `server/internal/handlers/discovery/handler.go:94-97`).
- **The Kubernetes connection test** needs role ≥ `user` (`POST /orgs/:org/integrations/:uid/test`, `app/server.go:1878`, `orgGroup`).
- **On SaaS that means anyone.** Open signup lets anyone create their own org and be its admin.

**`lan` scans.**
- **Validation.** `LANDefinition.BuildJob` (`server/internal/discovery/scantypes/lan.go:26-60`) only checks CIDR syntax and size (`discovery/safety.go:40-66,72-86`).
  - RFC 1918, loopback, link-local and the cloud metadata address are all accepted.
  - A scan can cover up to a /8, split across child jobs.
- **The engine dials with no policy:**
  - TCP connects use a bare `(&net.Dialer{}).DialContext` (`discovery/scanner.go:321-334`, dial at `:326`);
  - raw ICMP echo goes to any IP (`:274-318`);
  - reverse DNS runs on every live IP (`:337-353`).
- **No egress reference.** Nothing under `server/internal/discovery/` uses `egress`.
- **Where it runs.** The job runs in the server's job worker (`jobs/jobtypes/job_network_discovery.go:72`). Live hosts, open ports and PTR names come back to the admin as discovered checks.
- **The Freebox path (not in the report, same root cause).**
  - `ScanHosts` (`scanner.go:133-152`) shares the same engine. The Freebox job feeds it the IPs the Freebox API reports (`job_freebox_lan_discovery.go:77-95`).
  - On SaaS, those are private addresses, probed from the platform's own network.
  - A fake Freebox API on a public https host could report any internal IP. `freebox.ValidateBaseURL` allows a public https host (`integrations/freebox/validate.go:101-140`).

**`container` scans.**
- **Validation.** `ContainerDefinition.BuildJob` (`scantypes/container.go:37-85`) accepts any `unix://` or `tcp://` endpoint.
- **Dial.** `ListContainers` (`discovery/container.go:43-76`) calls `client.New(client.WithHost(endpoint))` (`:54`), with the moby client's own dialer:
  - `unix:///var/run/docker.sock` reads the local daemon if the socket is mounted;
  - `tcp://10.x:2375` reaches internal daemons.

  Container names, images, states and ports come back as discovered checks.
- **No SaaS gate.** Docker *checks* are refused on SaaS for exactly this reason, both at write time (`handlers/checks/validate.go:465-493`) and on the worker (`checkworker/worker.go:1676-1678`), per spec 2026-09-25-22. Discovery has no such gate: `StartScan` (`handlers/discovery/service.go:102-133`) never checks the deployment mode.

**Kubernetes (M2, unguarded dial).**
- **The guard only applies when the context carries one.** `applyEgressGuard` (`integrations/kubernetes/client.go:235-255`) reads the guard with `egress.FromContext`. Three callers never put one there:
  - the connection test (`handlers/integrations/service.go:1763-1795`, reached from `TestIntegration` `:1677-1690`);
  - `probeCluster` at scan start (`scantypes/kubernetes.go:77-92`);
  - the scan job (`jobtypes/job_kubernetes_discovery.go:114`).

  So an API-server URL pointing at a private address is dialed from the API/jobs process.
- **Error oracle.** The connection test and `probeCluster` return the dial error verbatim (`service.go:1771-1786`, `scantypes/kubernetes.go:84,88`). Connection refused versus timeout tells the caller whether an internal port is open.
- **Checks are already covered.** Kubernetes *checks* are guarded: the check worker puts its guard on the context (`checkworker/egress.go:47-51`).
- **Known gap in the docs.** The egress-policy page lists Kubernetes discovery and connection validation as unguarded (`web/docs/docs/configuration/egress-policy.md:191-201`).

**How the deployment mode and the private-target policy are exposed.**
- **Effective policy.** `config.Config.EgressAllowsPrivateTargets()` (`config/config.go:707-717`) is tri-state, set by `SP_EGRESS_ALLOW_PRIVATE` / `egress.allow_private_targets`. By default it denies on SaaS and allows on self-hosted and on deported agents.
- **Process guard.** `services.Registry.EgressGuard` (`app/services/services.go:115-124`) is built once from that policy by `installEgressGuard` (`app/server.go:4324-4336`), before routes and workers start. `guard.Enforcing()` is the switch. Jobs reach it as `jctx.Services.EgressGuard`.
- **Docker gate.** The docker-check rule is `cfg.Deployment.Mode == config.DeploymentModeSaaS` (`config/config.go:73`) on a node that is not an agent (`cfg.IsAgentMode()`, `config/node_role.go:145`).

## Proposal
1. **Pass the policy into discovery.**
   - `discovery.NewService` (`handlers/discovery/service.go:75-93`, wired at `app/server.go:1232-1234`) takes two new inputs:
     - the process guard (`s.services.EgressGuard`);
     - whether docker is blocked on this deployment (SaaS and not an agent node, the same rule as `validateDockerDeploymentConfig`).
   - `scantypes.Deps` (`scantypes.go:58-61`) gains `EgressGuard *egress.Guard` and `DockerBlocked bool`, and `StartScan` fills them in (`service.go:111`).
2. **`lan`: refuse the scan type when the guard is enforcing** (open question 1).
   - In `LANDefinition.BuildJob`, when `deps.EgressGuard.Enforcing()`, return a new coded error `DISCOVERY_NOT_AVAILABLE`. Its message names `SP_EGRESS_ALLOW_PRIVATE`.
   - Map that code to 403 in `statusForDiscoveryCode` (`handler.go:147-160`).
   - Re-check in `NetworkDiscoveryPlanJobRun.Run` (`job_network_discovery_plan.go:50`), so a plan queued before the fix does not fan out.
3. **Engine: check every probed IP against the guard.**
   - `runProbes` (`scanner.go:157-201`) reads `egress.FromContext(ctx)`. A target that `guard.CheckAddr(ctx, ip, ip)` refuses is skipped before `probeHost`: no ICMP, no TCP, no PTR lookup.
   - `dialTCP` (`:321-334`) dials through `guard.DialContextWith(dialCtx, &net.Dialer{}, "tcp", addr)`. A nil or permissive guard dials exactly as today.
   - For `lan`, this is defense in depth.
   - For the Freebox path, it is the fix. Private hosts fall back to the router's reachability flag, which `buildFreeboxRows` already handles (`job_freebox_lan_discovery.go:55-58`), so Freebox discovery keeps producing rows on SaaS.
4. **Put the process guard on every discovery context** (same pattern as `aichecks/runner.go:67-71`).
   - In the jobs:
     - add a nil-safe `egressGuardFrom(jctx)` beside `credsFrom` (`job_kubernetes_discovery.go:157-163`), or reuse it if spec 2026-10-16-07 already added it;
     - at the top of each discovery job's `Run`, set `ctx = egress.WithGuard(ctx, egressGuardFrom(jctx))`: `job_network_discovery.go:46`, `job_freebox_lan_discovery.go:59`, `job_container_discovery.go:60` and `job_kubernetes_discovery.go:95`.
   - Outside the jobs:
     - `probeCluster` (`scantypes/kubernetes.go:77`) wraps its context with `deps.EgressGuard`;
     - `testKubernetesConnection` (`handlers/integrations/service.go:1763`) wraps its context with `s.egressGuard()` (`:744-750`).
   - `applyEgressGuard` then pins the API-server dial to the checked address and turns the environment proxy off. A refusal reads as the policy's own message, which names the host but not the IP. That replaces the raw dial error.
   - Side effect: an `inCluster` connection is refused on SaaS, because the in-cluster API server is a private service IP (see C1).
5. **`container`: refuse it where docker checks are refused** (open question 2).
   - `ContainerDefinition.BuildJob` returns `DISCOVERY_NOT_AVAILABLE` when `deps.DockerBlocked` is set.
   - `ContainerDiscoveryJobRun.Run` refuses the same way, reading the mode from `jctx.AppConfig`.
   - Defense in depth in `ListContainers` (`discovery/container.go:43-76`), when the guard on the context is enforcing:
     - refuse `unix://`, since that is not an IP dial the guard can judge;
     - dial `tcp://` through `client.WithDialContext(guard.DialContext)` (moby `client@v0.6.2`, `client_options.go:117`).
6. **Types list.** `ListTypes` (`handler.go:73-83`) omits the types this deployment refuses (open question 3). The dash0 picker and source filter are built from that list (`useDiscoveryTypes`).
7. **Docs.**
   - `web/docs/docs/configuration/egress-policy.md:191-201`: drop the Kubernetes line from the unguarded list, and describe how discovery behaves under the policy.
   - `web/docs/docs/features/discovery.md` ("Sources", `:22`): say which sources are available under an enforcing policy, and on SaaS.

## Tests
- `server/internal/discovery/scanner_egress_test.go` (new), table-driven over `Scan` (`CIDRs: ["127.0.0.1/32"]`) and `ScanHosts` (`127.0.0.1`). Both run against a TCP listener on 127.0.0.1 that counts accepted connections.
  - **Regression.** With `egress.New(false)` on the context, the scan returns zero hosts and the listener accepts zero connections. Today the host comes back with the port open.
  - **Positive control.** With `egress.New(true)`, and with no guard at all, the host is found with that port open.
- `server/internal/discovery/scantypes/scantypes_test.go`:
  - **`lan`.** With `Deps{EgressGuard: egress.New(false)}`, `10.0.0.0/24` and `169.254.169.254/32` are refused with `DISCOVERY_NOT_AVAILABLE`, and so is a public CIDR if open question 1 is answered as recommended. With `egress.New(true)` or a nil guard, the plan job is returned.
  - **`container`.** With `DockerBlocked: true`, `unix:///var/run/docker.sock` and `tcp://10.0.0.5:2375` are refused. With `false`, the job is returned.
  - **`kubernetes`.** Use a real SQLite cluster connection, in token mode, whose `apiServer` is an httptest server on 127.0.0.1.
    - Under an enforcing guard, `probeCluster` fails with the egress denial and the server receives no request.
    - Under a permissive guard (control), it reaches `/version`.
- `server/internal/handlers/discovery/service_test.go`:
  - On a service built with an enforcing guard and docker blocked, `StartScan` for `lan` and for `container` returns the coded error and creates no job.
  - On the self-hosted service, it still creates the plan job.
- The four discovery job tests, each with `jctx.Services.EgressGuard = egress.New(false)` and a permissive-guard control:
  - `job_network_discovery_test.go`: a scan of the loopback listener persists no row and opens no connection.
  - `job_container_discovery_test.go`: a scan of a fake Docker API on `tcp://127.0.0.1:<port>` makes no request.
  - `job_kubernetes_discovery_test.go`: the resolver seam (`SetKubernetesClientsetResolver`) sees a context where `egress.FromContext(ctx).Enforcing()` is true.
  - `job_freebox_lan_discovery_test.go`: the reported private hosts are not probed, and rows are still built from the router's list.
- `server/internal/handlers/integrations/` (the Kubernetes connection-test tests), with a Kubernetes connection pointing at a loopback httptest API server:
  - registry guard enforcing: `TestIntegration` returns `Success=false`, the error mentions the egress policy, and the server receives zero requests;
  - permissive control: `Success=true`.

## To verify
- Check that discovery jobs always run in the API/jobs process on SaaS. A deported agent carries its own permissive guard, so a discovery job routed there would not be protected.
- The new-scan button in dash0 hard-codes `method: "lan"` (`web/dash0/src/routes/orgs/$org/organization.discovery.index.tsx:175`). Check that the form shows the 403 message cleanly when the type is refused, or that it follows the types list.

## Open questions
1. **`lan` under an enforcing policy: refuse the type, or allow public-only CIDRs?**
   - The report suggests refusing non-public CIDRs and checking each IP. But a public-only "LAN" scan has no use on SaaS. It would turn the shared platform into an internet port scanner, running from the platform's egress IPs and drawing abuse reports: a /8 per scan, with the default port list.
   - Recommended: refuse `lan` whenever the process guard is enforcing, and keep the per-IP check in the engine as defense in depth. The guard is enforcing on SaaS by default, and on self-hosted when `SP_EGRESS_ALLOW_PRIVATE=false`. The self-hosted default is unchanged.
2. **Which switch gates `container`?**
   - The egress policy cannot judge a `unix://` socket.
   - Recommended: use the docker-check rule (SaaS, and not an agent node), so docker checks and container scans are allowed in exactly the same places. Also dial `tcp://` through the guard when it is enforcing.
3. **Types list on SaaS.**
   - Hide refused types from `GET /discovery/types`, or list them with `available: false` and a reason the UI can show?
   - Recommended: hide them. The dash0 picker and source filter are already built from that list.

## Resolved open questions
Decided 2026-10-16 (all recommendations accepted):
1. Refuse `lan` whenever the process egress guard is enforcing; keep the per-IP check in the engine as defense in depth. Self-hosted default unchanged.
2. Gate `container` with the docker-check rule (SaaS and not an agent node), and dial `tcp://` through the guard when it is enforcing.
3. Hide refused types from `GET /discovery/types`.

## Related
- **M2, cross-org half (2026-10-11).** `KubernetesDefinition.BuildJob` ignores the org (`scantypes/kubernetes.go:41`), and `ResolveClientsetByUID` takes the org from the connection row (`integrations/kubernetes/client.go:86-109`). So anyone who knows another org's cluster UID can scan that cluster.
- **D6 (2026-10-14).** The `freebox_line` check resolves a foreign Freebox connection by its bare UID (`app/server.go:4627-4647`).
- **C1 (2026-10-11).** Kubeconfig `exec` plugins and `inCluster` connections.
