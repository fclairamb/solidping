# Integration, Escalation & Notification Tables

Where alerts go: integration connections, per-check bindings, escalation
policies, on-call rotations, per-user routing, and email opt-outs. See
[README.md](README.md) for the full index, and
`wiki/features/notifications-and-escalation.md` for the feature narrative.

### integrations
Generic table for all integrations (Slack, webhook, email, Freebox, etc.).
Renamed from `integration_connections` in migration 035 (pre-consolidation).

| Column | Type | Description |
|--------|------|-------------|
| uid | uuid PK | Primary key |
| organization_uid | uuid | FK to organizations |
| type | varchar | Integration type: slack, discord, webhook, email, googlechat, mattermost, ntfy, pagerduty, pushover, freebox |
| name | varchar | Human-readable name |
| enabled | boolean | Whether active |
| is_default | boolean | Auto-attach to new checks |
| settings | jsonb | Type-specific configuration |
| settings_private | text | AES-256-GCM envelope of the secret settings fields |
| settings_private_keys | text | Names of the keys held in `settings_private` |

**Foreign Keys**: `organization_uid` → organizations(uid)

---

### check_channels
Junction table linking checks to the integrations they notify through. Renamed
from `check_connections` in migration 035 (the binding keeps the "channel" name
to match the notify-role taxonomy); FK column `connection_uid` → `integration_uid`.

| Column | Type | Description |
|--------|------|-------------|
| uid | uuid PK | Primary key |
| check_uid | uuid | FK to checks |
| integration_uid | uuid | FK to integrations |
| organization_uid | uuid | FK to organizations |
| settings | jsonb | Per-check override settings (e.g., Slack channel override) |

**Foreign Keys**:
- `check_uid` → checks(uid)
- `integration_uid` → integrations(uid)
- `organization_uid` → organizations(uid)

**Indexes**: unique on (check_uid, integration_uid)

---

### escalation_policies
Reusable orchestration of paging steps. Distinct from `check_channels`
(per-check broadcast): a check, its group, or the organization references one
policy via `escalation_policy_uid`.

| Column | Type | Description |
|--------|------|-------------|
| uid | uuid PK | Primary key |
| organization_uid | uuid | FK to organizations |
| name | text | Display name |
| description | text | Optional description |
| repeat_max | integer | How many times the whole policy repeats (0 = no repeat) |
| repeat_after_seconds | integer | Delay between repeat cycles (NULL = immediate) |

**Foreign Keys**: `organization_uid` → organizations(uid)

**Note**: the `slug` column was dropped in migration `005_v0_4_0`; policies are
addressed by uid only.

---

### escalation_policy_steps
Ordered rungs of an escalation policy. Each step waits `delay_seconds`, then
pages its targets through the channel set of its severity.

| Column | Type | Description |
|--------|------|-------------|
| uid | uuid PK | Primary key |
| policy_uid | uuid | FK to escalation_policies |
| position | integer | Step order within the policy |
| severity_uid | uuid | FK to severities (channel set to page through) |
| delay_seconds | integer | Wait before this step fires (default 0) |

**Foreign Keys**:
- `policy_uid` → escalation_policies(uid)
- `severity_uid` → severities(uid) (on delete set null)

**Constraints**: unique on (policy_uid, position)

---

### escalation_policy_targets
Recipients of one escalation step.

| Column | Type | Description |
|--------|------|-------------|
| uid | uuid PK | Primary key |
| step_uid | uuid | FK to escalation_policy_steps |
| target_type | text | user, schedule, connection, or all_admins |
| target_uid | uuid | The referenced user / schedule / integration. NULL for all_admins. Polymorphic — no FK |
| position | integer | Order within the step (default 0) |

**Foreign Keys**: `step_uid` → escalation_policy_steps(uid)

---

### on_call_schedules
A rotation: a list of users plus a cadence and timezone that, together with a
moment in time, resolves to one on-call user. The schedule does not page anyone
itself — escalation policies consume it at fan-out time.

| Column | Type | Description |
|--------|------|-------------|
| uid | uuid PK | Primary key |
| organization_uid | uuid | FK to organizations |
| name | text | Display name |
| description | text | Optional description |
| timezone | text | IANA timezone the handoff time is interpreted in |
| rotation_type | text | daily or weekly |
| handoff_time | text | HH:MM in the schedule timezone |
| handoff_weekday | integer | 0–6 (Mon=0); required for weekly rotations |
| start_at | timestamptz | First handoff in the rotation cycle |
| ical_secret | text | Secret for the read-only iCal feed (NULL = feed disabled) |

**Foreign Keys**: `organization_uid` → organizations(uid)

**Note**: the `slug` column was dropped in migration `005_v0_4_0`.

---

### on_call_schedule_users
Ordered rotation membership for a schedule.

| Column | Type | Description |
|--------|------|-------------|
| uid | uuid PK | Primary key |
| schedule_uid | uuid | FK to on_call_schedules |
| user_uid | uuid | FK to users |
| position | integer | Position in the rotation order |

**Foreign Keys**:
- `schedule_uid` → on_call_schedules(uid)
- `user_uid` → users(uid)

**Constraints**: unique on (schedule_uid, position); unique on (schedule_uid, user_uid)

---

### on_call_schedule_overrides
Time-bounded substitutions that win over the computed rotation.

| Column | Type | Description |
|--------|------|-------------|
| uid | uuid PK | Primary key |
| schedule_uid | uuid | FK to on_call_schedules |
| user_uid | uuid | FK to users (who covers instead) |
| start_at | timestamptz | Override start |
| end_at | timestamptz | Override end |
| reason | text | Optional note (holiday, sick, swap) |
| created_by_uid | uuid | FK to users (who created the override) |

**Foreign Keys**:
- `schedule_uid` → on_call_schedules(uid)
- `user_uid` → users(uid)
- `created_by_uid` → users(uid) (on delete set null)

**Indexes**: (schedule_uid, start_at, end_at) for the resolver lookup

---

### user_notification_routes
Per-org ordering and enablement of a user's contact endpoints. One route per
contact.

| Column | Type | Description |
|--------|------|-------------|
| uid | uuid PK | Primary key |
| user_uid | uuid | FK to users |
| org_uid | uuid | FK to organizations |
| contact_uid | uuid | FK to user_contacts |
| enabled | boolean | Whether this route is used (default true) |
| position | integer | Delivery order (default 0) |

**Foreign Keys**:
- `user_uid` → users(uid)
- `org_uid` → organizations(uid)
- `contact_uid` → user_contacts(uid)

**Indexes**: unique on (contact_uid); index on (user_uid, org_uid)

---

### email_suppressions
Per-recipient email unsubscribe: (org, email, check_uid) has opted out of
incident/alert emails. Transactional emails (registration, password reset,
invitation, password-changed) never consult this table.

| Column | Type | Description |
|--------|------|-------------|
| uid | uuid PK | Primary key |
| organization_uid | uuid | FK to organizations |
| email | text | Suppressed recipient address |
| check_uid | uuid | FK to checks. NULL means the suppression applies to every check in the org |
| source | text | link (unsubscribe page / one-click), header (List-Unsubscribe-Post), or dashboard |

**Foreign Keys**:
- `organization_uid` → organizations(uid)
- `check_uid` → checks(uid)

**Indexes**: two partial unique indexes — (organization_uid, email, check_uid)
where check_uid is not null, and (organization_uid, email) where check_uid is
null — because a plain unique index would let duplicate org-wide rows through
(Postgres treats each NULL as distinct).
