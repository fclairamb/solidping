# Escalation Policies & On-Call Schedules

## Escalation Policies

Manage escalation policies — ordered steps of notification targets that fire
when an incident is not acknowledged. Policies are addressed by their `uid`
only; a slug-shaped identifier returns `404 NOT_FOUND`.

### GET /api/v1/orgs/:org/escalation-policies
List escalation policies (headers only, steps not expanded). Auth: required

### POST /api/v1/orgs/:org/escalation-policies
Create an escalation policy with its steps and targets. Auth: required

### GET /api/v1/orgs/:org/escalation-policies/:uid
Get a single escalation policy (with expanded steps and targets) by **uid**.
Returns `404 NOT_FOUND` for an unknown uid. Auth: required

### PATCH /api/v1/orgs/:org/escalation-policies/:uid
Update an escalation policy by **uid**. When `steps` is present the entire step
list is replaced. Auth: required

### DELETE /api/v1/orgs/:org/escalation-policies/:uid
Delete an escalation policy by **uid** (soft delete). Returns
`409 ESCALATION_POLICY_IN_USE` when an open incident still references it.
Auth: required

## On-Call Schedules

Manage on-call rotation schedules, their rosters, overrides, and iCal feeds.
Schedules are addressed by their `uid` only; a slug-shaped identifier returns
`404 NOT_FOUND`.

### GET /api/v1/orgs/:org/on-call-schedules
List on-call schedules. Auth: required

### POST /api/v1/orgs/:org/on-call-schedules
Create an on-call schedule with its initial roster. Auth: required

### GET /api/v1/orgs/:org/on-call-schedules/:uid
Get a single schedule by **uid**, including the current on-call user.
Returns `404 NOT_FOUND` for an unknown uid. Auth: required

### PATCH /api/v1/orgs/:org/on-call-schedules/:uid
Update a schedule by **uid**. When `userUids` is present the roster
is rewritten. Auth: required

### DELETE /api/v1/orgs/:org/on-call-schedules/:uid
Delete a schedule by **uid** (soft delete). Auth: required

### GET /api/v1/orgs/:org/on-call-schedules/:uid/preview
Preview the rotation over a window. Query: `from` (RFC3339, default now),
`days` (1–365, default 14). Auth: required

### GET /api/v1/orgs/:org/on-call-schedules/:uid/overrides
List overrides on the schedule. Query: `from`, `until` (RFC3339). Auth: required

### POST /api/v1/orgs/:org/on-call-schedules/:uid/overrides
Create an override. Auth: required

### DELETE /api/v1/orgs/:org/on-call-schedules/:uid/overrides/:overrideUid
Delete an override. Auth: required

### POST /api/v1/orgs/:org/on-call-schedules/:uid/ical-feed/enable
Enable the public iCal feed and return its secret + URL. Auth: required

### POST /api/v1/orgs/:org/on-call-schedules/:uid/ical-feed/rotate
Rotate the iCal feed secret. Old URLs stop working. Auth: required

### POST /api/v1/orgs/:org/on-call-schedules/:uid/ical-feed/disable
Disable the iCal feed. Subscribers begin receiving 410. Auth: required

### GET /api/v1/on-call-schedules/:secret/feed.ics
Public iCal feed. The secret in the URL authorizes access. Auth: public
