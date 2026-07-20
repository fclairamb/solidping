# Background Jobs & State Tables

The generic async task queue and the generic key-value state store. Note that
*check* scheduling does **not** go through `jobs` — it has its own lease-based
`check_jobs` table, documented in [checks.md](checks.md). See
[README.md](README.md) for the full index.

### jobs
Background tasks scheduled for asynchronous execution.

| Column | Type | Description |
|--------|------|-------------|
| uid | uuid PK | Primary key |
| organization_uid | uuid | FK to organizations (optional; NULL = system-wide) |
| type | text | Job type: email, webhook, check-run |
| config | jsonb | Job configuration |
| retry_count | integer | Retry attempts |
| scheduled_at | timestamptz | Execution time |
| status | text | pending, running, success, retried, failed |
| output | jsonb | Execution output |
| previous_job_uid | uuid | FK to jobs (retry chain) |

**Foreign Keys**:
- `organization_uid` → organizations(uid)
- `previous_job_uid` → jobs(uid) (self-reference)

**Indexes**: (scheduled_at, status) where pending and not deleted — the queue
scan; plus an expression index on `config->>'incidentUid'` for in-flight
incident jobs.

---

### state_entries
Key-value state storage for notifications, tokens, and distributed locking.

| Column | Type | Description |
|--------|------|-------------|
| uid | uuid PK | Primary key |
| organization_uid | uuid | FK to organizations (optional) |
| user_uid | uuid | FK to users (optional) |
| key | text | Namespaced key, max 255 chars (e.g., `email_confirm/{token}`) |
| value | jsonb | JSON value |
| expires_at | timestamptz | Optional TTL |

**Foreign Keys**:
- `organization_uid` → organizations(uid)
- `user_uid` → users(uid)

**Constraints**: unique on (organization_uid, key)

**Indexes**: (expires_at) for the TTL sweeper

See `wiki/conventions/state-entries.md` for the key namespaces in use.
