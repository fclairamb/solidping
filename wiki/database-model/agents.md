# Deported Agent Tables

Org-scoped check agents that connect outbound over WebSocket and execute checks
inside a customer's private region. Added in migration `006_v0_5_0`; they replace
the HTTP edge-worker API and its plaintext `spw_` bearer tokens. The database
stores only public keys — no usable agent credential is ever persisted. See
[README.md](README.md) for the full index.

### agents
One row per enrolled agent, hard-scoped to exactly one private region
(`@<region>`, org-relative — the org is `organization_uid`, never part of the
string; see [../conventions/regions.md](../conventions/regions.md)).

| Column | Type | Description |
|--------|------|-------------|
| uid | uuid PK | Primary key |
| organization_uid | uuid | FK to organizations |
| region | text | Org-relative private-region slug (`@<region>`) the agent is bound to |
| name | text | Human-readable agent name |
| ed25519_public_key | text | Base64 Ed25519 identity public key; verifies reconnect signatures |
| x25519_public_key | text | age X25519 recipient (`age1...`) credentials are sealed to |
| fingerprint | text | Short display fingerprint of the identity key |
| status | text | Lifecycle status (default `active`) |
| last_seen_at | timestamptz | Last WebSocket activity |
| enrolled_at | timestamptz | When the agent completed enrollment |
| revoked_at | timestamptz | When the agent was revoked (NULL if live) |

**Foreign Keys**: `organization_uid` → organizations(uid)

**Indexes**:
- Unique on (ed25519_public_key) where not deleted
- Index on (organization_uid, region) where not deleted
- Index on (status) where not deleted

---

### agent_enrollment_tokens
One-shot enrollment tokens (`spe_` prefix) that bind a future agent to an
(org, region) pair. Only the SHA-256 hash of the token is stored; the row is
consumed atomically at enrollment, so a token is single-use under concurrency.

| Column | Type | Description |
|--------|------|-------------|
| uid | uuid PK | Primary key |
| organization_uid | uuid | FK to organizations |
| region | text | Private region the enrolled agent will be bound to |
| token_hash | text | SHA-256 hash of the `spe_` token; the token itself is never stored |
| expires_at | timestamptz | Expiry of the enrollment window |
| used_at | timestamptz | When the token was redeemed (NULL if unused) |
| used_by_agent_uid | uuid | Agent that redeemed the token (unconstrained column) |
| created_by_user_uid | uuid | User who minted the token (unconstrained column) |

**Foreign Keys**: `organization_uid` → organizations(uid)

**Indexes**:
- Unique on (token_hash) where not deleted
- Index on (organization_uid) where not deleted
