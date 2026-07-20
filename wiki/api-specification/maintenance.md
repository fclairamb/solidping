# Maintenance Windows

Planned windows during which a set of checks (or check groups) is suppressed.

### GET /api/v1/orgs/:org/maintenance-windows
List maintenance windows. Auth: required

### POST /api/v1/orgs/:org/maintenance-windows
Create a maintenance window. Auth: required

### GET /api/v1/orgs/:org/maintenance-windows/:uid
Get a maintenance window. Auth: required

### PATCH /api/v1/orgs/:org/maintenance-windows/:uid
Update a maintenance window. Auth: required

### DELETE /api/v1/orgs/:org/maintenance-windows/:uid
Delete a maintenance window. Auth: required

### GET /api/v1/orgs/:org/maintenance-windows/:uid/checks
List checks associated with a maintenance window. Auth: required

### PUT /api/v1/orgs/:org/maintenance-windows/:uid/checks
Set (replace) the checks associated with a maintenance window. Auth: required
