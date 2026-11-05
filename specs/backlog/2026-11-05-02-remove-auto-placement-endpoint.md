---
model: sonnet
effort: low
---

# Remove the deprecated `POST /checks/auto-placement`

**Not before 2026-12-15.** Move this spec to `specs/todos/` on or after that date,
not earlier: the date is announced to API clients in the `Sunset` header.

## Source

Spec 2026-11-05-01 replaced the per-purpose bulk endpoints with one generic
bulk update, `PATCH /api/v1/orgs/:org/checks` with
`{"checkUids": [...], "changes": {...}, "dryRun": bool}`. The dashboard's
"Automatic placement" dialog now sends `{"placement": "auto"}` through it.
`POST /api/v1/orgs/:org/checks/auto-placement` was deprecated on 2026-11-05:
OpenAPI `deprecated: true`, and every response carries
`Deprecation: @1793836800` and `Sunset: Tue, 15 Dec 2026 00:00:00 GMT`.

## Proposal

1. Delete the route in `server/internal/app/server.go`, the handler
   `SwitchToAutoPlacement` and `autoPlacementSunset` in
   `server/internal/handlers/checks/handler.go`, and
   `server/internal/handlers/checks/placement_bulk.go` with its tests
   (`TestSwitchToAutoPlacementBulk` in `placement_test.go`,
   `placement_postgres_test.go`, the cases in `handler_cover_test.go`).
   Keep any helper still used elsewhere (`grep` before deleting).
2. Remove the path from `server/internal/app/openapi/openapi.yaml` and
   regenerate `server/pkg/client` (`go generate ./pkg/client/...`).
3. Drop the deprecation bullet from
   `web/docs/docs/features/region-placement.md` § Switching.
4. Changelog entry: breaking for API clients still calling the old endpoint,
   with the replacement body.

## Tests

- `make test` and `make lint` green; `grep -rn 'auto-placement' server web/dash0/src`
  finds only the dashboard dialog's names, no endpoint.
- `e2e/check-placement.spec.ts` (bulk switch through the dialog) still passes.
