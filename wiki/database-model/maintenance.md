# Maintenance Tables

Scheduled maintenance periods and what they cover. See [README.md](README.md)
for the full index.

### maintenance_windows
Scheduled maintenance periods that suppress incident alerts for affected checks.

| Column | Type | Description |
|--------|------|-------------|
| uid | uuid PK | Primary key |
| organization_uid | uuid | FK to organizations |
| title | text | Maintenance window title shown in notifications and status pages |
| description | text | Detailed description of the planned maintenance |
| start_at | timestamptz | When the maintenance window begins |
| end_at | timestamptz | When the maintenance window ends (must be after start_at) |
| recurrence | text | Recurrence pattern: none, daily, weekly, monthly |
| recurrence_end | timestamptz | When the recurring schedule stops (NULL = indefinite) |
| created_by | text | Identifier of the user or system that created this window |

**Foreign Keys**: `organization_uid` → organizations(uid)

**Constraints**: check `end_at > start_at`

---

### maintenance_window_checks
Links maintenance windows to individual checks or check groups. Exactly one of check_uid or check_group_uid must be set.

| Column | Type | Description |
|--------|------|-------------|
| uid | uuid PK | Primary key |
| maintenance_window_uid | uuid | FK to maintenance_windows |
| check_uid | uuid | FK to checks (NULL if targeting a group) |
| check_group_uid | uuid | FK to check_groups (NULL if targeting an individual check) |

**Foreign Keys**:
- `maintenance_window_uid` → maintenance_windows(uid)
- `check_uid` → checks(uid)
- `check_group_uid` → check_groups(uid)

**Constraints**: check that exactly one of `check_uid` / `check_group_uid` is set

**Indexes**: two partial unique indexes on (maintenance_window_uid, check_uid)
and (maintenance_window_uid, check_group_uid), each restricted to rows where the
respective column is not null.
