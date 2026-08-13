# Deported Agents (Private Locations)

A **deported agent** is a SolidPing check worker that a customer runs inside
their own network. It monitors targets the cloud workers cannot reach — RFC1918
addresses, VPN-only hosts, air-gapped segments, databases behind a bastion —
without the customer opening a single inbound firewall port.

Shipped in **v0.5.0** (spec
[`2026-07-16-02-per-org-deported-agent-websocket`](../../specs/done/2026/07/2026-07-16-02-per-org-deported-agent-websocket.md)).
User-facing docs: [`web/docs/docs/features/private-locations.md`](../../web/docs/docs/features/private-locations.md).

> **Naming.** "Deported agent" is the internal/engineering term; the dashboard and
> public docs say **private location**. They are the same thing. The region a
> deported agent serves is a **private region**, written `@<region>` —
> org-relative, so an org rename touches nothing (spec 2026-08-13-01; the
> matching audit lives in [`../conventions/regions.md`](../conventions/regions.md)).

---

## At a glance

| | |
|---|---|
| **Artifact** | The same SolidPing binary/container, run with `SP_NODE_ROLE=agent` |
| **Transport** | Outbound-only WebSocket to `GET /api/v1/agent/ws` |
| **Enrollment** | One-shot token `spe_<64 hex>`, bound to (org, region), SHA-256 stored — *system agents use a multi-use token bound to a cloud region, see below* |
| **Steady-state auth** | Ed25519-signed request headers — no bearer credential exists after enrollment |
| **Secrets** | age/X25519-sealed to the agents of the region; server cannot decrypt private-only checks |
| **HA** | N agents per region, all recipients of the sealed envelope |
| **State** | No database, no HTTP server, no migrations on the agent side |

---

## Why it exists

The cloud worker pool lives in SolidPing's infrastructure. It can only reach
what is routable from there. Every serious monitoring buyer eventually has an
internal target — an intranet app, a Postgres primary, a Kubernetes control
plane — and the alternatives are all bad: publish it, poke a firewall hole, or
run a proxy the vendor can see through.

Deported agents invert the direction of trust: the agent dials out, and the
sensitive material never leaves the customer's boundary in a form SolidPing can
read.

---

## Execution model

### 1. Enrollment (once per agent)

1. An org admin creates a private region (`@dc1`) and mints an enrollment
   token. The token is displayed once; only its SHA-256 is persisted
   (`server/internal/agents/crypto.go`).
2. The operator starts the agent with `SP_AGENT_ENROLLMENT_TOKEN=spe_…`.
3. The agent generates **two keypairs locally** — Ed25519 (identity) and
   X25519/age (credential decryption) — and persists them to
   `SP_AGENT_KEYS_FILE` (mode `0600`). It logs only the *path*, never the keys.
4. It connects with `Authorization: Bearer spe_…`. The token is validated
   **before** the WebSocket upgrade
   ([`handlers/agentws/handler.go`](../../server/internal/handlers/agentws/handler.go)),
   then the agent sends an `enroll` frame carrying both **public** keys. The
   server atomically creates the `agents` row and marks the token used.

The private keys never leave the agent. The server stores public keys only, and
the agent never logs its own keys — not at INFO, not at DEBUG. The single
exception is opt-in and deliberate: `SP_AGENT_PRINT_KEYS=true` prints the base64
identity to stdout inside a `!!! PRIVATE KEY MATERIAL … !!!` banner (see
[Bootstrapping `SP_AGENT_KEYS`](#bootstrapping-sp_agent_keys)).

#### Bootstrapping `SP_AGENT_KEYS`

**The distroless runtime stage is deliberate** (smaller attack surface, no
shell to pivot from if the agent is ever compromised), and it has a direct
consequence here: there is no `base64`, no `tar`, no `sh` in the image, so
none of the exec-based recipes that look obvious actually work.

- `kubectl exec … base64 …` fails outright: `exec: "base64": executable file
  not found in $PATH`. Verified against the real image — a filesystem export
  of `ghcr.io/fclairamb/solidping:latest` has no `base64`/`tar`/`sh`/`bash`/
  `busybox` binary anywhere in it, and `docker exec <container> /bin/sh` fails
  with `stat /bin/sh: no such file or directory`.
- `kubectl cp` fails too, and for a different reason: it doesn't ask the API
  server to read the file directly, it execs `tar` **inside the pod** to
  stream the copy — same missing binary, same failure.
- `fly ssh console -C "…"` needs `hallpass` compiled into the image; it isn't.

What all three have in common is that they try to run something *inside the
agent container*. Nothing has to: **enroll in a throwaway Pod where an ordinary
`alpine` sidecar shares an `emptyDir` with the agent, and read the keys file
from the sidecar.** Verified in production on 2026-08-13 while standing up the
`@stonal/s3ns-paris` agent on a GKE Autopilot cluster:

```bash
kubectl exec solidping-agent-enroll -c extract \
  -- sh -c "base64 /data/agent-keys.json | tr -d '\n'" \
  | kubectl create secret generic solidping-agent-keys --from-file=keys=/dev/stdin
```

Piping it means the key material never lands in a terminal, a shell history or
a log. (An earlier revision of this page claimed Kubernetes had no in-cluster
extraction path and recommended a PVC on that basis — that was wrong.)

The steady-state deployment then runs from `SP_AGENT_KEYS` alone
(it wins over the keys file — see
[`config.go:183`](../../server/internal/config/config.go)), so the pod is
**stateless**: no volume to pin it to a node, nothing to migrate, nothing to
lose on recreate. That is the recommendation, and what our own production
deployment runs. The full sequence is in the public doc's
[Kubernetes section](../../web/docs/docs/features/private-locations.md#kubernetes).
Load-bearing details that are easy to miss:

- `securityContext.fsGroup: 65532` on the **enrollment** Pod — distroless
  `:nonroot` runs as uid 65532, and without the `fsGroup` the shared volume
  stays root-owned and the agent can't write the `0600` keys file at all.
- `SP_NODE_NAME` on the Deployment — without it the worker slug comes from the
  truncated pod hostname, so every restart lands on a new `workers` row.
- The Secret (plus whatever backup you take of it) is the **only** copy of the
  identity; losing it means revoking the agent and enrolling a new one.

A PVC at `/data` with `SP_AGENT_KEYS` unset remains a valid alternative when
you would rather the identity never leave the cluster — at the cost of pinning
the pod to a `ReadWriteOnce` volume (`strategy: Recreate`) whose loss is
unrecoverable.

**Docker** has its own way out, and a simpler one: `docker cp` is implemented
daemon-side against the container's filesystem — it never execs anything inside
the container, so the missing shell doesn't matter. Also verified against the
real image:

```bash
docker cp <container>:/data/agent-keys.json - | tar -xO | base64 -w0
# or, against a named volume with no running container:
docker run --rm -v agent-data:/data alpine base64 -w0 /data/agent-keys.json
```

**`SP_AGENT_PRINT_KEYS=true` is a last resort, not a bootstrap procedure.**
It prints the banner-wrapped base64 to stdout, i.e. into whatever aggregates
container logs — Kubernetes' own log pipeline for a pod running in-cluster.
Because the exec routes are broken, this flag was the *de-facto only*
Kubernetes path before the PVC pattern was documented; it must not become the
standard way to bootstrap an agent. Use it only when no volume is available:
copy the banner-wrapped value into your secret store, then **unset the
variable and restart**, and treat that agent as compromised (revoke +
re-enroll) if the output was retained by a log drain. The flag is honoured on
every start, not just at enrollment, so an already-enrolled agent's value can
still be recovered this way without shell access.

### 2. Reconnection (every time after)

There is no long-lived bearer credential to steal. The agent signs each
connection with Ed25519 over the canonical challenge
`method + "|" + path + "|" + timestamp + "|" + nonce`
(`agents.SignatureChallenge`) and sends four headers:

```
X-Sp-Agent-Uid   X-Sp-Timestamp   X-Sp-Nonce   X-Sp-Signature
```

The timestamp is RFC3339. The server enforces a ±5 minute clock-skew window
(`agents.DefaultClockSkew`) plus an in-memory nonce replay cache
(`agents/nonce.go`) keyed by agent UID and retained for twice the skew.
Presence of `X-Sp-Agent-Uid` is what selects the signed path over the
`Authorization: Bearer spe_…` enrollment path.

### 3. Job flow

Versioned JSON frames over one WebSocket (`agents/protocol.go`):

| Direction | Frames |
|---|---|
| Agent → server | `enroll`, `claim {maxJobs}`, `result` |
| Server → agent | `hello`, `enrolled`, `jobs`, `ack`, `error`, `jobs-available` |

Claiming is a **pull** — the agent asks, the server answers. `jobs-available` is
a server-pushed *hint* that shortens latency on idle agents; it is an
optimization, and the poll remains the correctness guarantee. (This is what
[`2026-07-20-03-sub-minute-checks-idle-worker-poll`](../../specs/done/2026/07/2026-07-20-03-sub-minute-checks-idle-worker-poll.md)
tightened.)

A 25s ping ticker on the server refreshes `last_seen_at`; the agent runs a
matching keepalive with drop detection and exponential-backoff reconnect
([`checkworker/backend/ws.go`](../../server/internal/checkworker/backend/ws.go)).

---

## The security boundary

This is the part worth understanding before changing any of it.

**Private regions are a structural boundary, not a routing preference.**

- `regions.ValidateWorkerRegion` rejects any region containing `@`, so a cloud
  worker can never bind a private region. The DB check constraint on
  `workers.region` forbids it independently — two locks, different mechanisms.
- `regions.MatchesRegion` does prefix matching for cloud regions but
  **exact-equality only** when either side is private. A cloud worker's prefix
  match can never widen into an `@…` job.
- Agent claims are scoped **server-side** to the agent's `org_uid` and its exact
  bound region. The agent's own claim parameters are not trusted.
- The **`org_uid` half is now the load-bearing one.** Since regions are stored
  org-relatively, `@dc1` is unique only inside an org and two tenants can hold
  the identical string, so no path may rely on the region string alone. Every
  claim/dispatch/reseal path and its org predicate is enumerated in
  [`../conventions/regions.md`](../conventions/regions.md) — read that before
  adding a new one. The cloud claim lane, which deliberately has no org
  predicate, excludes `@…` jobs outright in SQL instead.

**Secrets are sealed to the agents, not to the server.**

Credentials for private-region checks are age-encrypted (X25519) to *all active
agents of the region* — multi-recipient, so HA doesn't cost you the guarantee.

- A check that runs **only** in a private region is **sealed-only**: SolidPing
  structurally cannot decrypt its credentials.
- A check spanning both private and cloud regions **dual-stores**:
  `checks.config_private` (v1 symmetric, server-readable, for the cloud leg) and
  `checks.config_sealed` (v2 age envelope, for the agent leg).
- On the agent dispatch path the server skips its own decrypt and ships
  `config_sealed` verbatim, stripping `config_private` structurally.

Consequence, deliberately: a private-region-only check whose secrets cannot be
sealed — because no agent has enrolled yet — is **rejected at write time** with
`VALIDATION_ERROR`. An earlier iteration silently fell back to a
server-decryptable envelope; that was reverted as wrong. Failing closed is the
feature.

---

## Operating an agent

No separate binary. Same container, different role:

| Env var | Default | Purpose |
|---|---|---|
| `SP_NODE_ROLE=agent` | — | Enables agent mode |
| `SP_AGENT_SERVER_URL` | — | **Required** — fails fast without it |
| `SP_AGENT_ENROLLMENT_TOKEN` | — | `spe_…`; one-shot for an org agent (first run only), multi-use for a platform one |
| `SP_AGENT_KEYS_FILE` | `/data/agent-keys.json` (falls back to `./agent-keys.json`) | Identity persistence |
| `SP_AGENT_KEYS` | — | Base64 identity JSON; wins over the file (Kubernetes secret) |
| `SP_AGENT_NAME` | hostname | Display name |
| `SP_AGENT_PRINT_KEYS` | `false` | **Prints private key material to stdout** — opt-in bootstrap only, honoured on every start; unset it again afterwards |

> These are read by a hand-rolled reader rather than koanf, because koanf
> collapses underscores to dots and `SP_AGENT_KEYS_FILE` would collide with
> `SP_AGENT_KEYS` (`server/internal/config/config.go`).

**`agent-keys.json` holds live private key material.** It is gitignored, but
treat it as a secret: back it up as one, mount it as one, and rotate by
re-enrolling if it leaks.

### Surfaces

- **Admin API** (under `/api/v1/orgs/:org`): `GET|POST /private-regions`,
  `DELETE /private-regions/:slug`, `GET|POST /agent-enrollment-tokens`,
  `DELETE /agent-enrollment-tokens/:uid`, `GET /agents`, `DELETE /agents/:uid`.
- **Dashboard**: `/orgs/$org/organization/private-locations`, with a guided
  `/register` wizard.
- **CLI**: none yet — the `sp` CLI has no agent or private-location commands.
- **Platform (system) agents have none of the above.** Their tokens come from
  the API deployment's `SP_SYSTEM_AGENT_ENROLLMENT_TOKENS`, and their rows never
  surface on the org-admin API. See *System agents* below.

---

## System agents (platform regions)

Shipped by spec
[`2026-07-27-01`](../../specs/todos/2026-07-27-01-fly-io-system-agents.md).

An agent now carries a **kind**:

| | `org` | `system` |
|---|---|---|
| Who runs it | the customer, in their network | SolidPing, e.g. a fly.io machine |
| Owning org | exactly one (`organization_uid` NOT NULL) | **none** (`organization_uid` NULL) |
| Region | private, `@<region>` | a **shared cloud region slug** |
| Claim scope | that org **and** that exact region | that exact region, **across every org** |
| Enrollment token | org admin mints it, strictly **one-shot** | env-seeded, **multi-use** and revocable |
| Credentials | sealed at *save* to the region's agents; server cannot read them | sealed at *claim* to the claiming agent |

The point: the deported-agent transport (outbound WebSocket, Ed25519 reconnects,
pull-based claiming, zero DB access) is exactly what SolidPing needs to run its
**own** cloud workers outside the cluster, instead of exposing PostgreSQL to a
fly machine across a continent. The wire protocol
(`server/internal/agents/protocol.go`) is unchanged apart from one added,
optional field (`execStart`, below) — a system agent speaks byte-identical
frames.

**Regions do not change.** System agents serve the *existing* cloud region
slugs, so fly is a drop-in replacement per region: no `fly-*` slugs, no
migration of any check's region set, nothing customer-visible. System agents do
not appear on the org-admin API at all (`ListAgents` / `ListAgentEnrollmentTokens`
stay org-filtered).

**Minting is env-seeded only.** `SP_SYSTEM_AGENT_ENROLLMENT_TOKENS` holds
comma-separated `region=spe_…` pairs, reconciled at boot by
`server/internal/app/systemagents.go` (each region validated with
`regions.ValidateWorkerRegion`). Removing an entry soft-deletes its token —
deleting the deployment secret is the revocation path. There is no `/api/mgmt`
endpoint and no org-admin route.

**Multi-use tokens, per-machine keys.** Fly secrets are app-wide, so a fleet
cannot carry one private key per machine in the environment. Instead every
machine generates its own keypair on boot and enrolls with the shared token; the
token's `use_count` is bounded only by an optional `max_uses`. Org tokens are
untouched — still atomically single-use.

**Seal at claim.** A cloud check's secrets live in the server-side envelope
(`config_private`), which the agent holds no key for. For a system agent the
server opens it and re-seals it to the agent's X25519 recipient at claim time,
shipping it in the same `configSealed` field — one wire format, no
plaintext-config branch, defense-in-depth beyond TLS. The same re-seal applies
to a tunneled job's SSH block. An envelope that cannot be opened drops the job
and records an explicit error result (the established decision-6 contract).

**Accounting parity.** The post-exec math (cost/delay EWMAs, effective deadline,
hysteresis lane) lives once in `scheduling.Params.PostExec`. The in-process
worker computes and persists it through `DirectBackend`; the shared server-side
`workers.Service.SubmitResult` now computes the same thing for results arriving
over the agent transport, so a region staffed by platform agents keeps fair
scheduling instead of degrading to unweighted FIFO. The agent transmits
`execStart` in the `result` frame for the delay sample; a result without it
folds cost and lane but leaves the delay EWMA alone. Agent clock skew is
harmless — the delay EWMA is telemetry and never steers claim order or lanes,
and the sample is floored at 0.

**Fleet hygiene.** Enroll-on-boot churns rows, so the global `agent_gc` job
(6 h, self-rescheduling) retires `kind='system'` agents unheard-from past a
configurable window (default 7 days), soft-deletes their `workers` rows, and
prunes consumed reconnect nonces. Org agents are user-managed and never touched.

**Shared nonce store.** The reconnect-replay guard moved from per-instance
memory to the `agent_nonces` table (`agents.NonceGuard` is the seam;
`agents.NonceCache` remains the in-memory, single-replica/no-DB implementation).
A multi-machine fleet reconnecting through a load balancer would otherwise let a
captured signature be replayed against a different replica. Fails closed.

Deploy reference: [`deploy/fly/`](../../deploy/fly/README.md) — `fly.toml`
template, the fly-region-code → SolidPing-slug mapping, the secret set, and the
multi-machine story.

---

## Competitive position

Private locations are table stakes at the top of the market and absent at the
bottom. The differentiator is **not** having them — it's who can read your
secrets. Surveyed 2026-07-20; see [sources](#sources).

| Vendor | Product | Transport | Enrollment auth | **Vendor can read check secrets?** | Plan gate |
|---|---|---|---|---|---|
| **SolidPing** | Private locations | Outbound WebSocket | One-shot token → **agent-generated** Ed25519 keypair | **No**, for private-only checks (age-sealed to the agent) | SaaS: 1/3/6/9 agents by plan · Self-hosted: unlimited |
| Checkly | Private Locations | Outbound, protocol undocumented | Shared API key | Yes | Team ($64/mo)+ |
| Datadog | Private Locations | Outbound HTTPS poll | SigV4 keys + RSA keypair (**vendor-generated**) | Yes | Usage-billed |
| New Relic | Synthetics job manager | Outbound HTTPS poll | Shared key, **non-rotatable** | Yes (vendor-held KMS) | Counts vs check budget |
| Grafana Cloud | Private probes | Outbound gRPC (streaming) | Per-probe bearer token | Yes (secrets proxy) | Per-execution |
| Site24x7 | On-Premise Poller | Outbound 443 | Shared Device Key | Yes | Starter+ (not Free) |
| SolarWinds Obs. | Synthetic private probe | Outbound gRPC | Ingestion API token | N/A — **secret-bearing checks disallowed** | — |
| Gatus | `external-endpoints` (DIY) | Outbound push | Static per-endpoint token | No — never sees the check at all | Free/OSS |
| BetterStack, Pingdom, StatusCake, UptimeRobot, Uptime Kuma, Hyperping | — | No capability | — | — | — |

**Where SolidPing wins**

1. **No commercial vendor in this set offers a control plane that cannot read
   check secrets.** Datadog comes closest architecturally, but generates the
   keypair server-side and stores test configs itself. New Relic's and Grafana's
   guarantees are RBAC and policy, not cryptography. SolarWinds sidesteps it by
   forbidding secrets on private probes entirely. The only vendor-blind design is
   Gatus's — and it achieves that by not distributing check config at all,
   surrendering server-side configuration and result detail.
   SolidPing's agent generates its own keypair locally and receives secrets
   sealed to it: cryptographic, not contractual.
2. **No self-hosted upcharge, and the SaaS free tier still includes one.**
   Checkly gates the feature entirely behind its $64/mo Team plan, and
   Site24x7 excludes its Free tier outright. SolidPing's SaaS Free plan ships
   1 agent (ladder: Free 1, Starter 3, Pro 6, Scale 9 — `maxDeportedAgents`,
   see [entitlements.md](entitlements.md)); self-hosted has no cap at all.
3. **Enrollment is one-shot and rotatable.** Everyone else ships a long-lived
   shared bearer secret; New Relic's explicitly cannot be rotated.
4. **Real multi-agent HA** with the sealed envelope addressed to every active
   agent. Grafana's model makes each probe its own billable location, which is
   not HA at all.
5. **Same binary, no second product.** Checkly, Datadog, New Relic and Site24x7
   all ship a distinct agent artifact; Site24x7's wants 16 GB RAM and 8 cores.

**Where we're behind**

1. ~~**Nonce replay protection is per-instance**~~ — **closed** by spec
   `2026-07-27-01`: the guard is the shared `agent_nonces` table, so a captured
   handshake is refused by every replica.
2. **No standby/failover story** comparable to Site24x7's designated standby
   poller and poller groups, and no documented sizing formula like Datadog's.
3. **No CLI**, and no agent auto-update — Checkly and Datadog both ship Helm
   charts with upgrade paths.
4. **Revocation is not retroactive.** A revoked agent already saw past
   credentials; rotation is manual.
5. **Windows support** — Site24x7 and Datadog ship Windows service installers.

**Messaging hook.** Every competitor's private location answers "can your
monitoring reach my internal network?" Only SolidPing also answers "…without
your vendor being able to read the database password it uses to get there."

---

## Known gaps

Tracked honestly rather than in `TODO` comments — there are none in the agent
packages.

1. ~~Per-instance nonce cache~~ — **closed** (spec `2026-07-27-01`): shared
   `agent_nonces` table behind `agents.NonceGuard`, fail-closed.
2. `jobs-available` hints are per-instance — latency only, poll covers
   correctness.
3. Revocation is not retroactive.
4. ~~Agent-path jobs do not update cost/delay EWMAs or scheduler lanes~~ —
   **closed** (spec `2026-07-27-01`): `workers.Service.SubmitResult` runs the
   shared `scheduling.Params.PostExec` and releases the lease with the recomputed
   state, using the `execStart` the agent now sends.
5. ~~System agents have no management surface~~ — **closed** (spec
   `2026-08-05-01`): `GET /api/v1/system/agents` (superadmin-only) plus the
   dash0 server-page **Agents** tab give a fleet-wide, read-only view across
   every org agent and every platform-operated system agent — the latter
   otherwise invisible to any org-scoped endpoint. It flags an agent whose
   `last_seen_at` is stale (> 5 min while `active`) since the `agent_gc` job
   only reaps a silent system agent after 7 days. Revoking a system agent from
   this view is still out of scope — that stays the
   `SP_SYSTEM_AGENT_ENROLLMENT_TOKENS` removal path.
6. Out of scope so far, no spec yet: client-side (browser) sealing, **peer
   re-wrap** (asking an online agent to add a recipient to a sealed-only blob),
   agent auto-update.

Open bug: [`2026-07-20-02-private-locations-token-dialog-dirty-rendering`](../../specs/todos/2026-07-20-02-private-locations-token-dialog-dirty-rendering.md)
— enrollment-token dialog renders fragments outside the dialog on the dev deploy.

---

## Code map

| Area | Path |
|---|---|
| Agent-mode entrypoint | `server/internal/agentmode/agentmode.go` |
| Token mint/hash, signing, nonces, frames | `server/internal/agents/` |
| WebSocket server | `server/internal/handlers/agentws/handler.go` |
| Admin API | `server/internal/handlers/agents/` |
| Agent-side client | `server/internal/checkworker/backend/ws.go` |
| Claim scope (`AgentScope`) | `server/internal/checkworker/checkjobsvc/service.go` |
| Shared submit path / accounting | `server/internal/handlers/workers/service.go` |
| Post-exec math | `server/internal/checkworker/scheduling/scheduling.go` (`PostExec`) |
| System-token seeding | `server/internal/app/systemagents.go` |
| Fleet GC | `server/internal/jobs/jobtypes/job_agent_gc.go` |
| Region rules | `server/internal/regions/regions.go` |
| Sealing | `server/internal/crypto/credentials/sealing.go` |
| Models | `server/internal/db/models/agent.go` |
| Migrations | `server/internal/db/{postgres,sqlite}/migrations/006_v0_5_0.up.sql`, `008_v0_7_0.up.sql` |
| Fly deploy reference | `deploy/fly/` |
| Dashboard | `web/dash0/src/routes/orgs/$org/organization.private-locations*.tsx` |
| E2E | `web/dash0/e2e/private-locations.spec.ts`, `deported-agent-wizard.spec.ts` |

## Related

- [conventions/regions.md](../conventions/regions.md) — region naming and matching
- [conventions/runners.md](../conventions/runners.md) — worker pools and fetching
- [features/config-as-code.md](config-as-code.md) — `${env:}`/`${param:}` secret references
- SSH tunnels ride the agent path too — see
  [`2026-07-18-07-ssh-tunnel-on-deported-agents`](../../specs/done/2026/07/2026-07-18-07-ssh-tunnel-on-deported-agents.md)

## Sources

Competitor claims surveyed 2026-07-20 from vendor documentation: Checkly
(`checklyhq.com/docs/private-locations/`), Datadog
(`docs.datadoghq.com/synthetics/platform/private_locations/`,
`docs.datadoghq.com/data_security/synthetics/`), New Relic
(`docs.newrelic.com/docs/synthetics/synthetic-monitoring/private-locations/`),
Grafana (`grafana.com/docs/grafana-cloud/testing/synthetic-monitoring/set-up/set-up-private-probes/`),
Site24x7 (`site24x7.com/help/getting-started/on-premise-poller.html`),
SolarWinds Observability, Gatus (`github.com/TwiN/gatus`), BetterStack
(`betterstack.com/docs/uptime/monitoring-private-networks/`), Pingdom,
StatusCake, UptimeRobot, Hyperping, Uptime Kuma.

Pricing observed 2026-07-20 and will drift — re-verify before using in
customer-facing material.
