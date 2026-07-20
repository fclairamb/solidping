# Discovery Tables

Output of network/container discovery scans: suggested checks a user can promote
into real ones. See [README.md](README.md) for the full index.

### discovered_checks
Suggested checks produced by discovery scans, grouped for display by
`group_key`. Ephemeral scratch data — regenerable by re-scanning.

| Column | Type | Description |
|--------|------|-------------|
| uid | uuid PK | Primary key |
| organization_uid | uuid | FK to organizations |
| job_uid | uuid | Scan job that produced this row |
| source | text | Discovery source (e.g. lan, docker, kubernetes) |
| group_key | text | Stable grouping identity (IP, container ID, workload uid). A render-time GROUP BY, not a second table |
| group_label | text | Human-readable label for the group |
| name | text | Suggested check name |
| slug | text | Suggested check slug |
| type | text | Suggested check type (http, tcp, ping, …) |
| config | jsonb | Suggested check configuration |
| metadata | jsonb | Denormalized group-display hints (identical across a group's rows), written by the suggester |
| promoted_to_check_uid | uuid | FK to checks, set once the suggestion is accepted |
| discovered_at | timestamptz | When the scan observed this candidate |

**Foreign Keys**:
- `organization_uid` → organizations(uid)
- `promoted_to_check_uid` → checks(uid)

**Indexes**:
- Unique on (organization_uid, source, group_key, slug) where not deleted and not promoted — the upsert key across re-scans
- Index on (job_uid) where not deleted
- Index on (organization_uid, source) where not deleted

**History**: the v0.1.0 baseline shipped a host-centric `discovered_hosts` table.
Migration `002_v0_2_0` dropped it outright (results are regenerable by re-scan)
and replaced it with this check-centric model. `discovered_hosts` is not part of
the current schema.
