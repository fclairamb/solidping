# Worker Tasks

Multi-slice units of work (a website crawl, a LAN discovery chunk) that a worker
claims under a lease, runs in slices and reports back. Check scheduling does not
use this table: see `check_jobs` in [checks.md](checks.md). Mechanism:
`server/internal/workertasks`; agent routes: [agents API](../api-specification/agents.md).

### worker_tasks

| Column | Type | Description |
|--------|------|-------------|
| uid | uuid PK | Primary key |
| organization_uid | uuid | FK to organizations (cascade). The claim predicate for org agents |
| kind | text | `crawl` or `discovery` |
| region | text | Exact region the task is bound to (`@slug` for a private region) |
| check_uid | uuid | FK to checks (cascade). Exactly one of `check_uid` / `job_uid` is set |
| job_uid | uuid | FK to jobs (cascade). A discovery chunk points at its scan plan |
| payload | jsonb | What to do (for discovery: `region`, CIDRs, ports) |
| progress | jsonb | Latest progress reported by `step` |
| status | text | `pending`, `claimed`, `done`, `failed`, `cancelled` |
| worker_uid | text | Holder of the lease |
| lease_until | timestamptz | Lease end; a claimed task past it can be claimed again |
| attempts | integer | Claim count |
| slice_count / slice_failures | integer | Slices run and failed |
| state_file_uid | text | Server-held state of the previous slice |
| units_granted | integer | Units of the kind's quota already granted (crawl) |
| not_before | timestamptz | Earliest claim time |
| run_started_at | timestamptz | First claim |
| error_code | text | Failure code (`DISCOVERY_NOT_ALLOWED`, `expired`...) |
| created_at / updated_at | timestamptz | Timestamps |

There is no sweeper: a non-terminal task untouched for 24 hours is reported as
failed with `expired` by readers. Discovery progress (`failedChunks`,
`lastErrorCode`) is derived from these rows: see
[discovery API](../api-specification/discovery.md).
