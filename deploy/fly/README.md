# SolidPing platform agents on fly.io

Running SolidPing's **own** cloud check workers on fly.io, over the deported-agent
WebSocket transport instead of a direct PostgreSQL connection.

> Internal / platform-operator material. System agents are invisible to
> customers: they serve the *existing* cloud region slugs, so nothing in the
> dashboard, the docs site or a customer's check configuration changes. See
> [`wiki/features/deported-agents.md`](../../wiki/features/deported-agents.md).

## Why

The in-cluster cloud worker (`SP_NODE_ROLE=checks`) claims jobs with
`SELECT … FOR UPDATE SKIP LOCKED` straight against PostgreSQL. Exposing that
database to fly machines would be both a security regression and a performance
trap — every claim transaction would carry a cross-continent round trip.

Deported agent mode already solves exactly this: outbound-only WebSocket,
Ed25519-signed reconnects, pull-based claiming, results written server-side, and
**zero** database access. Spec `2026-07-27-01` generalized it from
tenant-private regions to platform-operated ones, so the same binary in the same
mode now serves a *shared cloud region across every organization*.

## Region mapping

System agents serve the **existing** SolidPing region slugs — there are no
`fly-*` slugs and no migration of any check's region set. A fly app is simply
another way to staff a region you already have.

| fly region code | Location | SolidPing region slug |
|---|---|---|
| `cdg` | Paris, France | the EU slug in your `SP_REGIONS` (e.g. `default` / `eu-west-1`) |
| `iad` | Ashburn, Virginia | your US-East slug (e.g. `us-east-1`) |
| `sjc` | San Jose, California | your US-West slug |
| `lhr` | London, UK | your UK slug, if you define one |
| `fra` | Frankfurt, Germany | your EU-Central slug, if you define one |
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
cp fly.toml fly.cdg.toml     # set app = "solidping-agent-cdg", primary_region = "cdg"

fly apps create solidping-agent-cdg
fly volumes create agent_data --region cdg --size 1   # per machine

fly secrets set -a solidping-agent-cdg \
  SP_AGENT_SERVER_URL="https://app.solidping.io" \
  SP_AGENT_ENROLLMENT_TOKEN="spe_…"                  # the region's system token

fly deploy -c fly.cdg.toml
```

### The secret set

| Variable | Where | Value |
|---|---|---|
| `SP_NODE_ROLE` | `fly.toml` `[env]` | `agent` |
| `SP_AGENT_SERVER_URL` | fly secret | the API base URL the agent dials |
| `SP_AGENT_ENROLLMENT_TOKEN` | fly secret | the region's **multi-use system** token |
| `SP_AGENT_NAME` | runtime | `$FLY_MACHINE_ID` — see below |
| `SP_AGENT_KEYS_FILE` | `fly.toml` `[env]` | `/data/agent-keys.json` (per-machine volume) |

`SP_AGENT_NAME` must be per-machine, and fly secrets are **app-wide**, so it
cannot be a secret. Set it from fly's own runtime variable, e.g. with a wrapper
entrypoint:

```dockerfile
ENTRYPOINT ["/bin/sh", "-c", "SP_AGENT_NAME=\"${FLY_MACHINE_ID}\" exec /solidping serve"]
```

or by templating it into the machine config at deploy time. The name is
cosmetic — identity is the keypair — but it is what makes the agents list line
up with `fly machine list`.

## The multi-machine story

```bash
fly scale count 3 -a solidping-agent-cdg --region cdg
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
fly logs -a solidping-agent-cdg           # expect "enrolled" then claim/result traffic
```

Server side, the region's jobs should show `worker_uid` values belonging to
`ag-…`-slugged workers, and results should keep arriving with the normal
cadence. Because system agents serve the existing slugs, switching a region from
in-cluster workers to fly is a drop-in: scale the fly app up, then scale the
in-cluster workers for that region down.
