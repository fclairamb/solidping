# Jobs

Background-task management plus the observability surfaces over the job and
check-job queues.

## Org jobs

Job management for background tasks. Every route below is registered behind
`RequireAuth` + `RequireOrgAccess` (org membership) at the router level; the
create route adds `RequireOrgAdmin` on top. The route table and its guards live
in one place — `jobs.Handler.RegisterRoutes`, called from `app/server.go`.

### GET /api/v1/orgs/:org/jobs
List jobs. Auth: required

### POST /api/v1/orgs/:org/jobs
Create a job. Auth: **admin**

Only **allowlisted job types** can be created through this endpoint, and the
allowlist is `sleep` only (`jobdef.IsPubliclyCreatable`). Anything else — and
that includes `email`, `webhook`, `notification`, `aggregation` and the
discovery family — is refused with **403 / `FORBIDDEN`**, naming the type.

Why: the job registry is generic, so an open create endpoint means any caller
can send **arbitrary email** through the deployment's own SMTP sender (the
`email` type accepts raw HTML, which leaves with the deployment's From: address
and its SPF/DKIM alignment, to arbitrary recipients) and can make the server
issue **arbitrary outbound HTTP requests** (`webhook` takes any URL, method,
headers and body — SSRF against cloud metadata endpoints and cluster-internal
services). Neither type has a first-party caller on this endpoint: SolidPing
enqueues them internally through the job service, which is deliberately not
subject to the allowlist.

Status codes: 403 for a non-admin caller or a blocked type; 400 /
`VALIDATION_ERROR` for an unknown type or a config that is invalid for the
requested type; 500 only for genuine infrastructure failures.

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
