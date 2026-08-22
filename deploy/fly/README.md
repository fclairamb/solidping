# Running SolidPing check agents on fly.io

An **example** of staffing a SolidPing cloud check region with machines outside
your cluster, using the deported-agent WebSocket transport instead of a direct
PostgreSQL connection.

This directory is a template, not a deployment. [`fly.toml`](fly.toml) is
generic on purpose: copy it, fill in your own app name and region, and keep your
real values (hosts, tokens, app names) in whatever private repo holds your
infrastructure. See [`wiki/features/deported-agents.md`](../../wiki/features/deported-agents.md)
for the protocol and `specs/done/2026/07/2026-07-27-01-fly-io-system-agents.md`
for the design.

## Why not just point a worker at the database

The in-cluster cloud worker (`SP_NODE_ROLE=checks`) claims jobs with
`SELECT … FOR UPDATE SKIP LOCKED` straight against PostgreSQL. Exposing that
database to a machine on the public internet would be both a security regression
and a performance trap — every claim transaction would carry a cross-continent
round trip.

Deported agent mode already solves exactly this: outbound-only WebSocket,
Ed25519-signed reconnects, pull-based claiming, results written server-side, and
**zero** database access. Spec `2026-07-27-01` generalized it from tenant-private
regions to platform-operated ones, so the same binary in the same mode can serve
a *shared cloud region across every organization*.

## Region mapping

System agents serve the **existing** SolidPing region slugs — there are no
`fly-*` slugs and no migration of any check's region set. A fly app is simply
another way to staff a region you already have.

| fly region code | Location | Maps to |
|---|---|---|
| `cdg` | Paris, France | the EU slug in your `SP_REGIONS` (e.g. `default` / `eu-west-1`) |
| `iad` | Ashburn, Virginia | your US-East slug (e.g. `us-east-1`) |
| `sjc` | San Jose, California | your US-West slug |
| `lhr` | London, UK | your UK slug, if you define one |
| `fra` | Frankfurt, Germany | your EU-Central slug, if you define one |
| `nrt` | Tokyo, Japan | your Japan slug |
| `sin` | Singapore | your APAC slug |
| `syd` | Sydney, Australia | your AU slug |
| `gru` | São Paulo, Brazil | your South-America slug |

The right-hand column is deliberately not hard-coded: the slug set is per
deployment, seeded from `SP_REGIONS` (see `server/internal/app/regions_seed.go`).
Enrollment **validates** the token's region against that catalog with
`regions.ValidateWorkerRegion`, so a mistyped or stale slug is refused at
enrollment rather than producing an agent bound to nothing. A `@org/region`
private slug is refused outright — a platform agent may never serve somebody's
private location.

## Server side: mint the enrollment token

System enrollment tokens are **platform-operator material**. They are not
mintable through the org-admin `/orgs/:org/agent-enrollment-tokens` routes and
there is no `mgmt` endpoint: they are seeded from the API deployment's
environment.

```bash
# One token per region. Generate with any CSPRNG; the spe_ prefix is required.
python3 -c 'import secrets; print("spe_" + secrets.token_hex(32))'
```

Then, on the **API** deployment (not the fly app):

```
SP_SYSTEM_AGENT_ENROLLMENT_TOKENS="eu-west-1=spe_…,us-east-1=spe_…"
```

- Comma-separated `region=spe_…` pairs.
- Only the SHA-256 hash is stored; the plaintext lives in your deployment secret.
- Tokens are **multi-use** (unlimited by default): every fly machine generates
  its own keypair and enrolls on boot, so no private key is ever shared between
  machines and the fleet scales without an operator minting one token per
  machine.
- **Removing an entry revokes it** on the next API boot. That is the revocation
  path — deleting the secret is what turns a token off. Revoking a *token* does
  not revoke already-enrolled *agents*; use the agent GC (below) or revoke them
  explicitly.
- An entry with an unknown or `@private` region is logged and skipped; it never
  fails the boot.

## Fly side: deploy a region

```bash
cd deploy/fly
cp fly.toml fly.cdg.toml     # set app and primary_region to your own values

fly apps create <your-app>
fly volumes create agent_data --region cdg --size 1   # per machine

fly secrets set -a <your-app> \
  SP_AGENT_SERVER_URL="https://<your-api-host>" \
  SP_AGENT_ENROLLMENT_TOKEN="spe_…"                  # the region's system token

fly deploy -c fly.cdg.toml
```

> ⚠️ **The auto-stop trap.** `fly.toml` here deliberately declares **no service
> block**, because the agent has no listener — and that is precisely what keeps
> fly's auto-stop away from these machines (`auto_stop_machines` /
> `min_machines_running` are only valid *inside* a service block; fly.toml has
> no app-level equivalent, which is why they cannot appear as active top-level
> keys). If anyone ever adds an `[http_service]` — a health endpoint, metrics,
> anything — they **must** set `auto_stop_machines = "off"`,
> `auto_start_machines = false` and `min_machines_running = 1` in it. Fly's
> default is to stop idle machines, and a stopped platform agent is a region
> that silently stops probing: no error surfaces, the region's checks simply
> stop being claimed. The `[[restart]] policy = "always"` block is the active
> always-on counterpart.

### The secret set

| Variable | Where | Value |
|---|---|---|
| `SP_NODE_ROLE` | `fly.toml` `[env]` | `agent` |
| `SP_AGENT_SERVER_URL` | fly secret | the API base URL the agent dials |
| `SP_AGENT_ENROLLMENT_TOKEN` | fly secret | the region's **multi-use system** token |
| `SP_AGENT_NAME` | **unset — do not set it** | falls back to the machine ID, see below |
| `SP_AGENT_KEYS_FILE` | `fly.toml` `[env]` | `/data/agent-keys.json` (per-machine volume) |
| `SP_AGENT_KEYS` | **unset — do not set it** | fly secrets are app-wide; one identity shared by every machine is not usable for a multi-machine agent app |
| `SP_AGENT_PRINT_KEYS` | **unset** | opt-in printing of **private key material** to stdout — and fly aggregates stdout, so anything printed lands in `fly logs` |

**Never set `SP_AGENT_KEYS` on a multi-machine fly agent app.** fly secrets are
app-wide, so every machine would boot with the same identity; each machine
enrolls itself and keeps its own keys on its own volume instead. (Pinning
`SP_AGENT_KEYS` *is* a reasonable trade at `count=1`, where it keeps the agent
row stable across `fly deploy` machine recreations — but going above one machine
then requires switching back to the volume model first.)

There is no working way to extract that volume's `agent-keys.json` from
outside the machine: the image ships no shell and no `base64` (`FROM
gcr.io/distroless/base-debian13:nonroot`), and `fly ssh console` additionally
needs `hallpass` baked into the image, which this one doesn't carry — so a
`-C "base64 -w0 …"` one-liner fails before it even gets to the missing binary.
If you ever genuinely need the base64 for a single-machine app, start it once
with `SP_AGENT_PRINT_KEYS=true` instead (prints the private key material to
stdout, i.e. into `fly logs`; unset it and restart afterwards, and treat the
machine as compromised if that log line was retained by a log drain).

**Leave `SP_AGENT_NAME` unset.** The agent already defaults its name to
`os.Hostname()` ([`agentmode.go`](../../server/internal/agentmode/agentmode.go)),
which on a fly machine is the machine ID — so the agents list lines up with
`fly machine list` with no configuration at all.

Setting it in `fly.toml` `[env]` (or as a secret) would actively make things
worse: both are **app-wide**, so every machine in the region would report the
same name. And a wrapper-entrypoint trick cannot work against the published
image — it is built `FROM gcr.io/distroless/base-debian13:nonroot`, which ships
no `/bin/sh`.

The name is cosmetic in any case: identity is the keypair, not the name.

## The multi-machine story

```bash
fly scale count 3 -a <your-app> --region cdg
```

Each machine:

1. boots, finds no `/data/agent-keys.json`, and generates **its own** Ed25519 +
   X25519 keypair;
2. enrolls with the shared multi-use token — the server creates a distinct
   `kind='system'`, `organization_uid = NULL` agent row bound to the token's
   region;
3. persists its identity on its own volume, so a restart reuses the same agent
   row and reconnects with signed headers (no token needed);
4. claims that region's jobs **across every organization**, exactly like an
   in-cluster worker, and submits results over the same socket.

Two consequences worth knowing:

- **App-wide secrets are fine.** The enrollment token is the only shared
  material, and it is a bootstrap credential, not a steady-state one. No private
  key is ever shared.
- **Machine replacement leaves rows behind.** `fly deploy` destroys and recreates
  machines (and their volumes), so each deploy enrolls new agents. The
  `agent_gc` job retires `kind='system'` agents unheard-from for 7 days and
  deletes their `workers` rows, so the fleet list stays meaningful without
  manual cleanup. Org (customer) agents are never touched by it.

## Credentials

Cloud checks store their secrets in the server-side envelope (`config_private`,
AES-GCM under the per-org DEK) — an envelope a fly machine holds no key for. So
for a system agent the server **opens that envelope at claim time and re-seals
it to the claiming agent's X25519 recipient**, shipping it in the same
`configSealed` wire field the agent already knows how to open. One wire format,
no plaintext-config branch, and defense-in-depth beyond TLS.

An envelope that cannot be opened drops the job from the batch and records an
explicit error result naming the fix — a check is never dispatched without its
credentials, and never silently skipped.

## Verifying a region

```bash
fly status -a <your-app>    # what is ACTUALLY running — editing the pin is not the deploy
fly logs   -a <your-app>    # expect "enrolled" then claim/result traffic
```

The logs must show `Agent identity persisted path=/data/agent-keys.json` and
**no base64 blob**: an agent never prints its own private keys unless
`SP_AGENT_PRINT_KEYS` is set. If you do see one, that identity is exposed in
fly's log stream — revoke the agent, destroy the machine *and its volume*,
re-enroll with a fresh token, and rotate every check credential that was sealed
to the old X25519 identity.

Server side, the region's jobs should show `worker_uid` values belonging to
`ag-…`-slugged workers, and results should keep arriving with the normal
cadence. Because system agents serve the existing slugs, switching a region from
in-cluster workers to fly is a drop-in: scale the fly app up, then scale the
in-cluster workers for that region down.
