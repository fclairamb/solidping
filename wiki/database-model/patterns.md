# Schema Patterns & File Locations

Schema-wide conventions shared by every table. Per-table documentation lives in
the sibling domain pages — see [README.md](README.md).

## Design Patterns

1. **Soft Deletes**: Most tables have `deleted_at` for recovery. Uniqueness is
   enforced with partial unique indexes (`where deleted_at is null`) so a
   deleted row does not block re-creating the same slug/key.
2. **Timestamps**: All tables track `created_at` and `updated_at`
   (append-only tables such as `events` and `results` keep only `created_at`).
3. **Organization Scoping**: All operational data is scoped to organizations via
   `organization_uid`, usually with `on delete cascade`.
4. **UUID Primary Keys**: All tables use `uid uuid` for distributed system
   compatibility. `app_settings` is the one exception (text key PK).
5. **JSONB for Flexibility**: `config`, `settings`, `metadata`, `payload` and
   `output` are stored as JSONB.
6. **Credential Encryption**: Secret-bearing JSONB fields are split into a
   public column and an AES-256-GCM `*_private` TEXT envelope, with
   `*_private_keys` listing the encrypted key names for placeholder rendering
   (`checks`, `check_jobs`, `integrations`, `organization_providers`). A
   `*_sealed` column carries the age-X25519 envelope readable only by a private
   region's deported agents.
7. **Lease-based Job Distribution**: `check_jobs` uses worker leases
   (`lease_worker_uid` / `lease_expires_at`) for distributed execution, with
   fast/slow lanes and a cost-aware `effective_scheduled_at` ordering key.
8. **Time-series Results**: the `results` table supports both raw and
   pre-aggregated data in the same shape, distinguished by `period_type`.
9. **NULL-proof partial uniqueness**: where a nullable column participates in a
   unique key, the schema either splits into two partial indexes (see
   `email_suppressions`, `maintenance_window_checks`) or wraps the column in
   `coalesce(...)` (see `results_aggregated_unique_idx`), because Postgres
   treats each NULL as distinct.

## Migration Layout

One consolidated migration per release, named `NNN_vX_Y_Z.up.sql` — the file
holds the *net* final DDL for that release cycle, not the incremental steps that
produced it.

| Migration | Release | Highlights |
|-----------|---------|------------|
| `001_v0_1_0` | v0.1.0 | Consolidated baseline (replaces the old incremental 001–036) |
| `002_v0_2_0` | v0.2.0 | `oauth_clients`, discovery rework (`discovered_hosts` → `discovered_checks`), status-page `history_period`, adaptive recovery / flapping columns, cost-aware scheduling columns |
| `003_v0_2_1` | v0.2.1 | `email_suppressions` (per-recipient email unsubscribe) |
| `004_v0_3_0` | v0.3.0 | LDAP/AD provider type on `user_providers` |
| `005_v0_4_0` | v0.4.0 | Drops the unused `slug` from `escalation_policies` and `on_call_schedules` |
| `006_v0_5_0` | v0.5.0 | Aggregation hardening, deported agents (`agents`, `agent_enrollment_tokens`, drops `workers.token`, adds `config_sealed`), org default escalation policy |

**Note:** `discovered_hosts` is created by `001_v0_1_0` and dropped again by
`002_v0_2_0`. It is not part of the current schema and is not documented here;
see [discovery.md](discovery.md) for its replacement.

## File Locations

- **PostgreSQL migrations**: `server/internal/db/postgres/migrations/`
- **SQLite migrations**: `server/internal/db/sqlite/migrations/`
- **Go models**: `server/internal/db/models/`
- **Migration conventions**: `wiki/conventions/database.md`
