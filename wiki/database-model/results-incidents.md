# Monitoring Results, Incidents & Events

Time-series check results, the incident lifecycle, notification delivery
records, and the audit log. See [README.md](README.md) for the full index.

### results
Time-series check execution results (raw and aggregated). This page covers the
schema; the rollup behavior (job, boundaries, transactional compaction,
retention) is in
[features/results-aggregation.md](../features/results-aggregation.md).

| Column | Type | Description |
|--------|------|-------------|
| uid | uuid PK | Primary key |
| organization_uid | uuid | FK to organizations |
| check_uid | uuid | FK to checks |
| period_type | text | Granularity: raw, hour, day, month (no code path produces anything finer than `month`) |
| period_start | timestamptz | Period start |
| period_end | timestamptz | Period end |
| region | text | Execution region |
| worker_uid | uuid | FK to workers (raw only) |
| status | smallint | Lifecycle order: 1=created, 2=running, 3=up, 4=down, 5=timeout, 6=error, 7=degraded, 8=warning |
| duration | real | Execution duration in milliseconds |
| metrics | jsonb | Numerical metrics (NULL for HTTP raw rows — response time lives in `duration`) |
| output | jsonb | Diagnostic output (raw rows only; rollups leave it NULL) |
| total_checks | integer | Total count (aggregated) |
| successful_checks | integer | Success count (aggregated) |
| duration_min/max/avg/p95 | real | Duration stats (aggregated) |

Availability % is **not stored**: it is derived at read time as
`successful_checks / total_checks × 100` (null when `total_checks = 0`). The
`last_for_status` and `availability_pct` columns were dropped in migration
`008_v0_7_0` (spec 2026-07-24-02) — the former was write-only and cost an extra
UPDATE per insert, the latter was redundant with the two counts.

**Foreign Keys**:
- `organization_uid` → organizations(uid)
- `check_uid` → checks(uid)
- `worker_uid` → workers(uid)

**Indexes**: unique on (organization_uid, check_uid, coalesce(region, ''), period_type, period_start) where period_type != 'raw' — the `coalesce` closes a NULL-region duplication hole fixed in migration `006_v0_5_0`.

---

### incidents
Tracks periods when a check is down.

| Column | Type | Description |
|--------|------|-------------|
| uid | uuid PK | Primary key |
| organization_uid | uuid | FK to organizations |
| check_uid | uuid | FK to checks |
| check_group_uid | uuid | FK to check_groups (NULL = traditional per-check incident) |
| region | text | Region where incident occurred |
| state | smallint | 1=active, 2=resolved |
| started_at | timestamptz | When failures started |
| resolved_at | timestamptz | When recovered (NULL = ongoing) |
| escalated_at | timestamptz | When escalation triggered |
| acknowledged_at | timestamptz | When acknowledged |
| acknowledged_by | uuid | FK to users |
| failure_count | integer | Total failures during incident |
| relapse_count | integer | Number of times reopened after brief recoveries |
| last_reopened_at | timestamptz | When last reopened (NULL if never reopened) |
| snoozed_until | timestamptz | NULL = not snoozed; the sweeper unsnoozes when now() passes it |
| snoozed_by | text | Who snoozed the incident |
| snooze_reason | text | Why it was snoozed |
| resolved_by | text | Who resolved the incident |
| resolution_type | text | auto, manual, or expired. NULL until resolved_at is set |
| caused_by_incident_uid | uuid | FK to incidents — root-cause incident this one was rolled up under at open time |
| paging_suppressed | boolean | TRUE when notifications/escalation must skip; flips back to FALSE on parent resolve if still down |
| title | text | Auto-generated title |
| description | text | Human-readable description |
| details | jsonb | Additional data |

**Foreign Keys**:
- `organization_uid` → organizations(uid)
- `check_uid` → checks(uid)
- `check_group_uid` → check_groups(uid)
- `acknowledged_by` → users(uid)
- `caused_by_incident_uid` → incidents(uid) (self-reference)

**Indexes**: unique on (organization_uid, check_group_uid) where state = 1 and check_group_uid is not null and not deleted — at most one active group incident per group.

---

### incident_member_checks
Per-member state inside a group incident: which checks joined the rollup and
whether each is still failing.

| Column | Type | Description |
|--------|------|-------------|
| incident_uid | uuid | FK to incidents (composite PK) |
| check_uid | uuid | FK to checks (composite PK) |
| joined_at | timestamptz | When this check joined the group incident |
| first_failure_at | timestamptz | First failure of this member within the incident |
| last_failure_at | timestamptz | Most recent failure |
| last_recovery_at | timestamptz | Most recent recovery (NULL if never recovered) |
| failure_count | integer | Failures contributed by this member |
| currently_failing | boolean | Whether this member is failing right now |

**Primary Key**: (incident_uid, check_uid)

**Foreign Keys**:
- `incident_uid` → incidents(uid)
- `check_uid` → checks(uid)

---

### incident_notifications
One row per notification attempt made for an incident — the delivery ledger
behind the incident timeline and the escalation audit.

| Column | Type | Description |
|--------|------|-------------|
| uid | uuid PK | Primary key |
| organization_uid | uuid | FK to organizations |
| incident_uid | uuid | FK to incidents |
| event_type | text | Which incident event triggered the notification |
| step_uid | uuid | FK to escalation_policy_steps (NULL for non-escalation sends) |
| repeat_index | integer | Which policy repeat cycle produced this send |
| source | text | What produced the notification (e.g. check channel, escalation) |
| user_uid | uuid | FK to users (target user, if any) |
| connection_uid | uuid | FK to integrations (target integration, if any) |
| channel_type | text | Delivery channel (email, slack, webhook, …) |
| status | text | Delivery status |
| skip_reason | text | Why the send was skipped, if it was |
| error | text | Failure message |
| job_uid | uuid | Background job that performed the delivery (unconstrained column) |
| message_id | text | Provider-side message identifier |
| created_at, sent_at, cancelled_at, failed_at | timestamptz | Lifecycle timestamps |
| delivery_details | jsonb | Structured per-attempt delivery artifacts (status, url, capped bodies, duration); secrets never stored |

**Foreign Keys**:
- `organization_uid` → organizations(uid)
- `incident_uid` → incidents(uid)
- `step_uid` → escalation_policy_steps(uid) (on delete set null)
- `user_uid` → users(uid) (on delete set null)
- `connection_uid` → integrations(uid) (on delete set null)

---

### severities
Per-org channel-set primitive consumed by escalation steps: one severity says
"page critically" and fans out to several channel types in a single tick.

| Column | Type | Description |
|--------|------|-------------|
| uid | uuid PK | Primary key |
| organization_uid | uuid | FK to organizations |
| slug | text | URL-friendly identifier, unique per organization |
| name | text | Display name |
| description | text | Optional description |
| channels | jsonb | JSON array of channel-type strings (email, slack, discord, sms, voice, push, critical_push, …) |
| is_default | boolean | At most one default severity per org |

**Foreign Keys**: `organization_uid` → organizations(uid)

**Indexes**: unique on (organization_uid, slug) where not deleted; unique on (organization_uid) where is_default and not deleted

---

### events
Audit log for incident lifecycle and system events.

| Column | Type | Description |
|--------|------|-------------|
| uid | uuid PK | Primary key |
| organization_uid | uuid | FK to organizations |
| incident_uid | uuid | FK to incidents (optional) |
| check_uid | uuid | FK to checks (optional) |
| job_uid | uuid | Related background job (unconstrained column) |
| event_type | varchar | Event type: check.created, incident.created, etc. |
| actor_type | varchar | system or user |
| actor_uid | uuid | FK to users (optional) |
| payload | jsonb | Event-specific data |

**Foreign Keys**:
- `organization_uid` → organizations(uid)
- `incident_uid` → incidents(uid)
- `check_uid` → checks(uid)
- `actor_uid` → users(uid)

Append-only: `created_at` only, no `updated_at` or soft delete.
