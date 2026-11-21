---
model: sonnet
effort: medium
---

# `PATCH /maintenance-windows/:uid` cannot clear `description` or `recurrenceEnd`

## Problem

Found by the Terraform audit (`wiki/terraform-provider-api-audit.md`, maintenance-window
section, gap 1). `UpdateRequest.Description` and `RecurrenceEnd` are `omitempty` pointers
(`server/internal/handlers/maintenancewindows/service.go:76-85`): absent and `null` both mean
"unchanged", and the DB update only sets the column when non-nil
(`server/internal/db/postgres/postgres.go:7092-7135`, `db/sqlite/sqlite.go:7041`). A declarative
client that removes `recurrence_end` or `description` from its config can never clear it: the
next plan shows a permanent diff.

## Proposal

- Distinguish "absent" from explicit `null` in `UpdateRequest` for `description` and
  `recurrenceEnd` (explicit `null` clears, as `channels` already does for secret keys).
- Carry the clear through `models.MaintenanceWindowUpdate` and both DB backends.
- Keep `sp apply` and the dash0 edit form working unchanged.

## Tests

- Service/handler test: PATCH `{"recurrenceEnd": null}` clears it; PATCH without the key keeps it;
  same for `description`. Both SQLite and, non-short, Postgres.
