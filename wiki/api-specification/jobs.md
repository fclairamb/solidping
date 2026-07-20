# Jobs

Background-task management plus the observability surfaces over the job and
check-job queues.

## Org jobs

Job management for background tasks. Routes are registered without
authentication middleware at the router level (auth may be checked in
handlers).

### GET /api/v1/orgs/:org/jobs
List jobs. Auth: required

### POST /api/v1/orgs/:org/jobs
Create a job. Auth: required

### GET /api/v1/orgs/:org/jobs/:uid
Get a job. Auth: required

### DELETE /api/v1/orgs/:org/jobs/:uid
Cancel a job. Auth: required

### GET /api/v1/orgs/:org/jobs/stats
Aggregate job counters for the org (per status, per type). Auth: admin

## Org job observability (admin)

Deeper read-only views used by the org admin troubleshooting screens. All
require org **admin**.

### GET /api/v1/orgs/:org/admin/jobs
List the org's jobs with the full internal shape (config, attempts, errors).

### GET /api/v1/orgs/:org/admin/jobs/:uid
Get one job with its full internal shape.

### GET /api/v1/orgs/:org/admin/jobs/:uid/chain
Get the job's chain — its parent plan and child jobs — so a fan-out (e.g. a
discovery scan) can be inspected as a whole.

### GET /api/v1/orgs/:org/check-jobs
List the org's check jobs (the per-check, per-region execution records).

### GET /api/v1/orgs/:org/check-jobs/:uid
Get one check job.

## System job observability (super-admin)

The same views, unscoped, across every organization. All require
**super-admin**.

### GET /api/v1/system/jobs/stats
Aggregate job counters across the whole server.

### GET /api/v1/system/jobs
List jobs across all orgs.

### GET /api/v1/system/jobs/:uid
Get one job.

### GET /api/v1/system/jobs/:uid/chain
Get a job's parent/child chain.

### GET /api/v1/system/check-jobs
List check jobs across all orgs.

### GET /api/v1/system/check-jobs/:uid
Get one check job.
