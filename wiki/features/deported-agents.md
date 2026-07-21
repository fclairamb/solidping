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
> deported agent serves is a **private region**, written `@<org>/<region>`.

---

## At a glance

| | |
|---|---|
| **Artifact** | The same SolidPing binary/container, run with `SP_NODE_ROLE=agent` |
| **Transport** | Outbound-only WebSocket to `GET /api/v1/agent/ws` |
| **Enrollment** | One-shot token `spe_<64 hex>`, bound to (org, region), SHA-256 stored |
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

1. An org admin creates a private region (`@acme/dc1`) and mints an enrollment
   token. The token is displayed once; only its SHA-256 is persisted
   (`server/internal/agents/crypto.go`).
2. The operator starts the agent with `SP_AGENT_ENROLLMENT_TOKEN=spe_…`.
3. The agent generates **two keypairs locally** — Ed25519 (identity) and
   X25519/age (credential decryption) — and persists them to
   `SP_AGENT_KEYS_FILE`.
4. It connects with `Authorization: Bearer spe_…`. The token is validated
   **before** the WebSocket upgrade
   ([`handlers/agentws/handler.go`](../../server/internal/handlers/agentws/handler.go)),
   then the agent sends an `enroll` frame carrying both **public** keys. The
   server atomically creates the `agents` row and marks the token used.

The private keys never leave the agent. The server stores public keys only.

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
| `SP_AGENT_ENROLLMENT_TOKEN` | — | One-shot `spe_…`, first run only |
| `SP_AGENT_KEYS_FILE` | `/data/agent-keys.json` (falls back to `./agent-keys.json`) | Identity persistence |
| `SP_AGENT_KEYS` | — | Base64 identity JSON; wins over the file (Kubernetes secret) |
| `SP_AGENT_NAME` | hostname | Display name |

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

1. **Nonce replay protection is per-instance** (in-memory). On multi-replica
   deployments a captured handshake is replayable against another replica inside
   the ±5 min window. Needs a shared nonce store.
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

1. Per-instance nonce cache (above).
2. `jobs-available` hints are per-instance — latency only, poll covers
   correctness.
3. Revocation is not retroactive.
4. Agent-path jobs do not update cost/delay EWMAs or scheduler lanes;
   `ReleaseLease` re-anchors on ack.
5. Out of scope so far, no spec yet: client-side (browser) sealing, **peer
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
| Region rules | `server/internal/regions/regions.go` |
| Sealing | `server/internal/crypto/credentials/sealing.go` |
| Models | `server/internal/db/models/agent.go` |
| Migrations | `server/internal/db/{postgres,sqlite}/migrations/006_v0_5_0.up.sql` |
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
