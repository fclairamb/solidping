# System

Server-wide configuration and operations. Everything here is **super-admin**
unless stated otherwise. Job observability under `/api/v1/system` is documented
in [jobs.md](jobs.md).

## Regions

### GET /api/v1/regions
List all available global regions. Auth: public

### GET /api/v1/orgs/:org/regions
List regions relevant to the organization. Auth: required

Private regions are created and managed separately — see [agents.md](agents.md).

## System parameters

### GET /api/v1/system/parameters
List all system parameters. Auth: super-admin

### GET /api/v1/system/parameters/:key
Get a system parameter by key. Auth: super-admin

### PUT /api/v1/system/parameters/:key
Set a system parameter. Auth: super-admin

### DELETE /api/v1/system/parameters/:key
Delete a system parameter. Auth: super-admin

### GET /api/v1/system/parameters/email_inbox/public
Public projection of the `email_inbox` parameter. Auth: **required only** — any
authenticated user, deliberately *not* super-admin. It exposes only
`addressDomain`, so the dashboard can render per-check email addresses without
surfacing the rest of the JMAP credentials. Registered on its own group ahead
of the super-admin group (`server/internal/app/server.go`).

## Email

### POST /api/v1/system/test-email
Send a test email to verify email configuration. Auth: super-admin

### GET /api/v1/system/email-inbox/config
Get the inbound email (JMAP) configuration. Auth: super-admin

### GET /api/v1/system/email-inbox/status
Get the inbound email connection/sync status. Auth: super-admin

### POST /api/v1/system/email-inbox/test
Test the inbound email connection. Auth: super-admin

### POST /api/v1/system/email-inbox/sync
Force an inbound email sync now. Auth: super-admin

## Operations

### GET /api/v1/system/activation
Activation funnel counters across the server. Auth: super-admin

### GET /api/v1/system/scheduling/lane-load
Current load per scheduling lane — used to diagnose an overloaded or unbalanced
scheduler. Auth: super-admin
