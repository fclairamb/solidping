# Entitlement Tables

Per-organization plan limits and the audit trail of who changed them. Written by
the external billing service through `PUT /api/v1/orgs/:org/entitlements`. See
[README.md](README.md) for the full index, and the SaaS section of the root
`CLAUDE.md` for the deployment story.

### org_entitlements
One row per organization. Limits, features, and source live inside `payload`;
absence of a key inside the payload means "use the in-code default" — resolution
happens in the entitlements service and is never stored.

| Column | Type | Description |
|--------|------|-------------|
| uid | uuid PK | Primary key |
| organization_uid | uuid | FK to organizations, UNIQUE (one row per org) |
| payload | jsonb | Limits (`maxChecks`, `maxUsers`, `maxChecksPerMinute`, `maxDeportedAgents`), display identity (`displayName`, `displayEmoji`), and schema version |
| external_ref | text | Billing-side subscription reference |
| expires_at | timestamptz | When the entitlement lapses (NULL = no expiry) |
| last_synced_at | timestamptz | Last successful sync from the billing service |
| metadata | jsonb | Free-form metadata |

**Foreign Keys**: `organization_uid` → organizations(uid), unique

**Indexes**: (external_ref) where external_ref is not null

The three scalar columns are kept out of the payload because they are queried or
indexed at the SQL level.

---

### org_entitlement_audits
Append-only record of every entitlement write, with before/after snapshots.

| Column | Type | Description |
|--------|------|-------------|
| uid | uuid PK | Primary key |
| organization_uid | uuid | FK to organizations |
| source | text | Who wrote the row: default, self-hosted, admin, billing-service |
| actor | text | Identifier of the acting principal |
| before_snapshot | jsonb | Entitlement payload before the write (NULL on creation) |
| after_snapshot | jsonb | Entitlement payload after the write |
| reason | text | Optional explanation recorded with the change |

**Foreign Keys**: `organization_uid` → organizations(uid)

**Indexes**: (organization_uid, created_at desc)
