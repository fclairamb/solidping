---
model: sonnet
effort: medium
---

# `PUT /maintenance-windows/:uid/checks` accepts check and group uids it never validates

## Problem

Found by the Terraform audit (`wiki/terraform-provider-api-audit.md`, maintenance-window
section, gap 2). `SetChecks` verifies the window but inserts `checkUids` / `checkGroupUids`
as given (`server/internal/handlers/maintenancewindows/service.go:362-395`,
`server/internal/db/postgres/postgres.go:7152-7195`). A uid that belongs to another organization
is linked (a window then suppresses another tenant's check), and an unknown uid surfaces as a
database error (500) rather than a `VALIDATION_ERROR` the provider can show at plan/apply time.

## Proposal

- Before the transaction, check every uid exists, is not deleted and belongs to the org; reject
  with a `422 VALIDATION_ERROR` naming the offending `checkUids[i]` / `checkGroupUids[i]`.
- Dedupe repeated uids (a Terraform set cannot contain them, other clients can).

## Tests

- Foreign-org check uid -> validation error, associations unchanged.
- Unknown uid -> validation error, not 500.
- Valid set still replaces the previous associations.
