# Schema notes (behavioural detail per table)

Moved from the backend agent file. Read when changing organizations, slugs, parameters, users, workers, results or credential encryption. Column-level tables live in the sibling pages (see [README.md](README.md)).

### Core Tables

**organizations** - Multi-tenant structure for isolating monitoring resources
- `uid` (uuid) - Primary key
- `slug` (text) - URL-friendly unique identifier (3-20 chars, alphanumeric + hyphens)
- `logo_url` / `logo_file_uid` - Optional org logo: an external http(s) URL, or `/pub/assets/<file uid>` for an upload (owner-only `PATCH /api/v1/orgs/:org` and `POST /api/v1/orgs/:org/logo`). The uploaded blob is public because its file row carries the topic `organizations/<org uid>/logo` — see `internal/handlers/files/publictopics.go`
- Soft delete support via `deleted_at`

**organization_previous_slugs** - Rename aliases. A renamed org keeps answering on its old slug: lookups fall back to this table and permanently redirect (301 GET/HEAD, 308 otherwise) to the current slug, across the API, status pages, badges, the embed widget and the dash0/status0 SPA URLs. A live `organizations.slug` always wins over an alias, and an alias is released the moment another org claims the slug. A soft-deleted org is never reachable through an alias — its slug 404s immediately (spec 2026-08-08-11). See `wiki/api-specification/orgs.md`.

**parameters** - Key-value configuration per organization
- `uid` (uuid) - Primary key
- `organization_uid` - Foreign key to organizations
- `key` (text) - Configuration key. CHECK-enforced on **both** engines as of
  `021_v0_28_0`: lowercase letters, digits, `_`, `.` and `-`. The hyphen was
  added by spec 2026-09-11-03 (org-managed parameters use it, and Postgres had
  refused it since the 001 baseline while SQLite had no CHECK at all — the
  divergence shipped a 500). Org-managed rows live under the `usr.` prefix
  (`internal/paramkeys`); never write that prefix from platform code
- `value` (jsonb) - Configuration value
- `secret` (boolean) - Whether value is sensitive

**Parameter key convention.** Param keys mirror the config struct path: dots for hierarchy (`email.host`, `auth.google.client_id`), snake_case within a segment for word breaks (`email.from_name`, `aggregation.retention_raw`). New keys must follow this — never add a top-level snake_case key.

**users** - Organization members with authentication and role-based access
- `uid` (uuid) - Primary key
- `organization_uid` - Foreign key to organizations
- `user_id` (text) - User identifier
- `password_hash` - Hashed password for local auth
- `auth_provider_uid` - Optional link to OAuth provider
- `role` - User role: owner, admin, user, or viewer (hierarchical — an owner passes every admin gate; only an owner may delete the org or grant ownership)

**auth_providers** - Authentication methods per organization
- `uid` (uuid) - Primary key
- `organization_uid` - Foreign key to organizations
- `slug` - URL-friendly provider identifier within organization
- `type` - Authentication type: email, password, google, github, gitlab, microsoft, twitter, oauth2
- `config` (jsonb) - Provider-specific configuration

**workers** - Distributed service workers that execute monitoring checks
- `uid` (uuid) - Primary key
- `identifier` - Unique system identifier (e.g., hostname, container ID)
- `name` - Human-readable name
- `context` (jsonb) - Worker metadata (e.g., {"region": "eu"})
- `last_active_at` - Last heartbeat timestamp

**checks** - Monitoring configurations and target definitions
- `uid` (uuid) - Primary key
- `organization_uid` - Foreign key to organizations
- `name` - Check name
- `slug` - URL-friendly unique identifier (unique per organization)
- `type` - Check type (ping, http, tcp, dns, ssl, etc.)
- `config` (jsonb) - Check-specific configuration (URLs, ports, timeouts, etc.)
- `enabled` - Whether check is active
- `period` - Check frequency (default: 1 minute)

**check_jobs** - Scheduler state for distributed check execution
- `uid` (uuid) - Primary key
- `organization_uid` - Foreign key to organizations
- `check_uid` - One-to-one relationship with checks (unique)
- `context_conditions` (jsonb) - Criteria to match on workers.context
- `period` - Execution interval
- `scheduled_at` - Next execution time
- `lease_worker_uid` - Worker assigned to execute
- `lease_expires_at` - Lease timeout
- `lease_starts` - Execution attempt counter (0-1 normal, 10 indicates crash)

**results** - Time-series monitoring data — both raw check executions and rollups
- `uid` (UUIDv7, PK) - Time-ordered identifier; the embedded millisecond timestamp is used for fallback lookups when a row has been rolled up and deleted
- `organization_uid`, `check_uid` - Foreign keys
- `period_type` - Aggregation level: `raw` | `hour` | `day` | `month`. Aggregation job rolls `raw → hour → day → month` and deletes the source rows; retention thresholds are configurable
- `period_start` (notnull) - Start of the period (raw: execution time; aggregated: bucket start)
- `period_end` (nullable) - Bucket end, exclusive. Set for aggregated rows; nil for raw
- `region` (nullable) - Region the check ran in. Aggregations are per-region (one row per period × region)

Raw-only fields (period_type = 'raw'):
- `worker_uid` - Worker that executed the check
- `status` - 1=created, 2=running, 3=up, 4=down, 5=timeout, 6=error, 8=warning, 9=abandoned (7=degraded is aggregated-only). 9 is server-minted by the abandoned-result reaper and, like the created/running lifecycle markers, is excluded from every availability calculation — see `models.ResultStatus.ExcludedFromAvailability`
- `duration` (float32) - Response time
- `metrics` (jsonb) - Per-execution metrics (the HTTP checker leaves this NULL — response time lives in `duration`)
- `output` (jsonb) - Detailed results and error messages

Aggregated-only fields (period_type ∈ 'hour', 'day', 'month'):
- `total_checks`, `successful_checks` - Uptime stats over the bucket; availability % is derived at read time (`successful_checks / total_checks × 100`, null when `total_checks = 0`), never stored
- `duration_min`, `duration_max`, `duration_p95` - Response-time stats
- `metrics` - Aggregated by suffix convention (`_min`, `_max`, `_avg`, `_pct`, `_rte`, `_sum`, `_cnt`, `_val`); see `server/internal/jobs/jobtypes/job_aggregation.go`

- `created_at` - Insertion timestamp (set by DB default)

### Credential Encryption
Secret-bearing fields in `checks.config`, `integration_connections.settings`, and `check_jobs.config` are split into a public column (queryable JSONB) and an AES-256-GCM-encrypted private column (`*_private` TEXT envelope) when `SP_ENCRYPTION_MASTER_KEY` (or `SP_ENCRYPTION_MASTER_KEY_FILE`) is set. Per-org DEKs wrapped by the master KEK live in `parameters` (`secret=true`). When unset, secrets fall back to plaintext (intentional V1 fallback for self-hosted, logged at startup). PATCH semantics: secret keys absent from the request preserve the encrypted value; explicit empty/null clears. **Decrypt-and-merge always happens at the claim/dispatch boundary**, never inside `CheckWorker` or a checker: the in-process path merges in `checkworker/backend.DirectBackend.ClaimJobs`/`ClaimJobsForCheck` and deported agents unseal their region-sealed envelope in `backend.WSBackend`, so every job reaching the worker loop carries one merged plaintext `Config` and no envelope. The merge rule itself lives once in `checkjobsvc.MergeJobSecrets`. A job whose envelope cannot be opened (no master key on this process, or a decrypt failure) is **never dispatched without its secrets and never silently skipped** — it is dropped from the claim batch and reported as an explicit `StatusError` result naming the fix, which also releases the lease. With no master key configured at all there is no envelope and the plaintext public config passes through untouched (the documented V1 fallback). The dashboard never sees secrets — `GET` returns the public side plus `configPrivateKeys: [...]` for placeholder rendering. See `internal/crypto/credentials/` and `internal/credmigrate/`. Threat model: protects against DB theft only — not against process compromise, malicious admins, or worker log leakage.

### Monitoring system properties
- **Multi-tenancy**: All resources scoped to organizations via `organization_uid`
- **Soft deletes**: Most tables support `deleted_at` for recovery
- **Flexible authentication**: Email/password, OAuth2, and social providers
- **Distributed workers**: Multiple workers can execute checks with lease-based distribution
- **Results aggregation**: Results table holds both raw rows and rolled-up aggregations (hour/day/month) in the same shape, distinguished by `period_type`
- **Configuration management**: Flexible key-value config per organization via `parameters` table
- **Real-time monitoring**: Sub-minute check frequencies with immediate alerting
- **Domain Expiration Monitoring**: RDAP-based domain expiration tracking (WHOIS fallback) with configurable alert thresholds (days remaining)
