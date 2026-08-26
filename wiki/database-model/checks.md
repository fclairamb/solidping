# Monitoring Configuration Tables

What to monitor, how it is grouped and labelled, how it is scheduled, and who
executes it. See [README.md](README.md) for the full index.

### workers
Distributed workers that execute monitoring checks. Global, not org-scoped.

| Column | Type | Description |
|--------|------|-------------|
| uid | uuid PK | Primary key |
| slug | text | Unique system identifier |
| name | text | Human-readable name |
| region | text | Region identifier (e.g., eu-west-1) |
| last_active_at | timestamptz | Last heartbeat |
| capabilities | text[] (SQLite: JSON text) | Self-reported capability set — `NULL` = never reported (unknown), `{}` = reported and has none, a populated set = reported this exact set. See [../features/deported-agents.md](../features/deported-agents.md#ipv6-egress) |
| version | text, nullable | Self-reported build version (`internal/version.Get().Version`), refreshed alongside `capabilities`. `NULL` = never reported (unknown) — must never be rendered as "drifted". Two-state, not three: a real build version is never the empty string, so a `CHECK` constraint refuses one outright. See [../features/deported-agents.md](../features/deported-agents.md#build-version) |

**Foreign Keys**: None

**Indexes**: unique on (slug) where not deleted

**Note**: the legacy plaintext `token` column was dropped in migration
`006_v0_5_0` together with the HTTP edge-worker API. Deported executors are now
`agents`, which authenticate by Ed25519 signature — see [agents.md](agents.md).

---

### check_groups
Flat organizational grouping for checks. A check belongs to zero or one group.

| Column | Type | Description |
|--------|------|-------------|
| uid | uuid PK | Primary key |
| organization_uid | uuid | FK to organizations |
| name | text | Display name for the group |
| slug | text | URL-friendly identifier, unique per organization |
| description | text | Optional description of what this group contains |
| sort_order | smallint | Display order (lower = higher). Default 0 |
| escalation_policy_uid | uuid | FK to escalation_policies (group-level policy) |

**Foreign Keys**:
- `organization_uid` → organizations(uid)
- `escalation_policy_uid` → escalation_policies(uid) (on delete set null)

**Indexes**: unique on (organization_uid, slug) where not deleted

---

### checks
Monitoring configurations that define what to monitor.

| Column | Type | Description |
|--------|------|-------------|
| uid | uuid PK | Primary key |
| organization_uid | uuid | FK to organizations |
| check_group_uid | uuid | FK to check_groups (NULL means ungrouped) |
| name | text | Check name |
| slug | text | URL-friendly identifier (per org) |
| description | text | Documentation |
| type | text | Check type: http, tcp, ping, dns, ssl, etc. |
| config | jsonb | Check-specific configuration |
| config_private | text | AES-256-GCM envelope of the secret config fields |
| config_private_keys | text | Names of the keys held in `config_private` |
| config_sealed | text | age-X25519 envelope sealed to a private region's agents. NULL when the check targets no private region |
| regions | text[] | Regions where check runs |
| enabled | boolean | Whether active |
| internal | boolean | Internal checks are hidden from public status pages |
| period | interval | Check frequency (default: 1 minute) |
| escalation_threshold | integer | Failures before escalation (default 10) |
| confirmation_period_seconds | integer | Sustained failure time before an incident opens (default 0) |
| recovery_period_seconds | integer | Sustained success time before an incident auto-resolves (default 0) |
| reopen_cooldown_multiplier | integer | Multiplier for adaptive cooldown before reopening (NULL = system default) |
| flapping_window_seconds | integer | Rolling window over which outages accumulate the recovery backoff. 0 = adaptive recovery off (default 21600) |
| flap_backoff_factor | integer | Multiplies required recovery time per flap inside the window. 1 = off (default 2) |
| max_recovery_multiplier | integer | Cap on the recovery multiple (default 8; a 30m hard ceiling also applies) |
| flap_count | integer | Outages accumulated inside the rolling flapping window |
| last_outage_at | timestamptz | Wall-clock of the most recent outage onset (NULL until the first outage) |
| escalation_policy_uid | uuid | FK to escalation_policies (check-level policy) |
| status | smallint | Current check status |
| status_streak | integer | Current status streak count |
| status_changed_at | timestamptz | Last status change |
| first_failure_at | timestamp | Onset of the current failure run (confirmation clock) |
| first_success_since_failure_at | timestamp | Onset of the current success run (recovery clock) |

**Foreign Keys**:
- `organization_uid` → organizations(uid)
- `check_group_uid` → check_groups(uid)
- `escalation_policy_uid` → escalation_policies(uid)

**Indexes**: unique on (organization_uid, slug) where not deleted and slug is not null

---

### check_dependencies
Parent/child dependency edges between checks, used to roll a dependent
incident up under its root cause.

| Column | Type | Description |
|--------|------|-------------|
| uid | uuid PK | Primary key |
| organization_uid | uuid | FK to organizations |
| parent_check_uid | uuid | FK to checks (the dependency) |
| child_check_uid | uuid | FK to checks (the dependent) |
| kind | text | hard (suppress the child's paging) or soft (annotate only) |
| description | text | Optional note explaining the edge |

**Foreign Keys**:
- `organization_uid` → organizations(uid)
- `parent_check_uid` → checks(uid)
- `child_check_uid` → checks(uid)

**Constraints**: unique on (parent_check_uid, child_check_uid); check `parent_check_uid <> child_check_uid`

---

### labels
Key-value pairs for organizing and filtering checks.

| Column | Type | Description |
|--------|------|-------------|
| uid | uuid PK | Primary key |
| organization_uid | uuid | FK to organizations |
| key | text | Label key (1-50 chars) |
| value | text | Label value (max 200 chars) |

**Foreign Keys**: `organization_uid` → organizations(uid)

**Indexes**: unique on (organization_uid, key, value) where not deleted

---

### check_labels
Many-to-many relationship between checks and labels.

| Column | Type | Description |
|--------|------|-------------|
| uid | uuid PK | Primary key |
| check_uid | uuid | FK to checks |
| label_uid | uuid | FK to labels |

**Foreign Keys**:
- `check_uid` → checks(uid)
- `label_uid` → labels(uid)

**Indexes**: unique on (check_uid, label_uid)

---

### check_jobs
Scheduled execution jobs for checks with worker lease tracking. One row per
check per region.

| Column | Type | Description |
|--------|------|-------------|
| uid | uuid PK | Primary key |
| organization_uid | uuid | FK to organizations |
| check_uid | uuid | FK to checks (1:n, one job per region) |
| region | text | Specific region for execution (NULL = any region) |
| type | text | Check type (copied for performance) |
| config | jsonb | Check configuration |
| config_private | text | AES-256-GCM envelope of the secret config fields |
| config_private_keys | text | Names of the keys held in `config_private` |
| config_sealed | text | Copy of `checks.config_sealed`, shipped verbatim to deported agents; never decrypted server-side |
| encrypted | boolean | Whether config is encrypted |
| period | interval | Execution interval |
| scheduled_at | timestamptz | Next scheduled execution (the claim gate) |
| effective_scheduled_at | timestamptz | Cost-adjusted claim ORDER BY key: scheduled_at + cost_ewma_ms×2 (clamped to 30s) − tier credit |
| cost_ewma_ms | double | EWMA of execution duration in ms; timeouts pinned to the ceiling |
| delay_ewma_ms | double | EWMA of (probe start − scheduled_at) in ms. Pure telemetry; never steers claim ordering |
| plan_weight | smallint | Denormalized plan tier from org_entitlements (0 = free) |
| lane | smallint | Scheduling lane: 0 = fast, 1 = slow (classified from cost_ewma_ms with hysteresis) |
| lease_worker_uid | uuid | FK to workers (assigned worker) |
| lease_expires_at | timestamptz | Lease expiration |
| lease_starts | smallint | Execution attempt counter |

**Foreign Keys**:
- `organization_uid` → organizations(uid)
- `check_uid` → checks(uid)
- `lease_worker_uid` → workers(uid)

**Indexes**:
- Unique on (check_uid) where region is null; unique on (check_uid, region) where region is not null
- Index on scheduled_at
- Partial claim indexes on effective_scheduled_at, one per lane

**Rate-limited deferrals keep their ordering key** (spec 2026-08-26-02). When
the per-org `MaxChecksPerMinute` bucket turns a job away before its probe runs,
the release advances `scheduled_at` to the next aligned tick but leaves
`effective_scheduled_at` at the tick that was missed
(`checkjobsvc.DeferLeaseRateLimited`, used by both the in-process worker gate
and the deported-agent dispatch gate). Since the claim orders by
`effective_scheduled_at ASC`, the deferred job grows more overdue with every
window it loses and wins the next contended slot, so an over-cap org rotates
its deficit across all its checks. Re-anchoring here — which is what the
generic `ReleaseLease` does, correctly, for a job whose attempt is actually
over — made phases (a stable hash of the check UID) into a permanent ranking
and starved the same checks indefinitely.
