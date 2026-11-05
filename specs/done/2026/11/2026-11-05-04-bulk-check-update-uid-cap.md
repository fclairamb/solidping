---
model: opus
effort: medium
pr: batch
automerge: false
---

# The bulk check update dedups `checkUids` in quadratic time before applying its 1000-UID cap

## Problem

Found by the security scan of 2026-11-05 (finding AE1).

**Severity: medium.** Any member with write access to an org (the `user` role, `RequireOrgWrite`) can trigger it. On SaaS that is anyone: sign-up is open and the org creator is its owner.

`PATCH /api/v1/orgs/:org/checks` (#755, `server/internal/handlers/checks/bulk_update.go`) decodes the whole `checkUids` array, then dedups it with `uniqueCheckUIDs` (`bulk_update.go:240-250`). That function calls `slices.Contains(out, uid)` for each input, so it costs O(n²) string comparisons. The `MaxBulkUpdateChecks` cap (1000, `bulk_update.go:22`, `:121-126`) is only checked on the *deduplicated* list, after that loop has run.

- A single request whose `checkUids` holds many distinct short strings keeps one CPU core busy for a long time. Measured on 2026-11-05 with a copy of `uniqueCheckUIDs`: 100,000 UIDs (a body of about 770 KiB) take 9 s, and 200,000 UIDs (about 1.6 MB) take 26 s. Growth is quadratic, so a 10 MB body runs for about a quarter of an hour.
- The UIDs do not have to exist. The loop runs before any database access, and no org check is needed beyond write access to the attacker's own org.
- The loop never checks the request context. A client that disconnects, or a server write timeout, does not stop it.
- A few such requests in parallel fill every core of the API process, and every tenant served by that process stalls.
- Nothing caps the request body (the known D5), so the body can be as big as the attacker wants. A body cap alone does not fix this: at 1 MB, one request still costs about 10 s.

The deprecated `POST /checks/auto-placement` (`placement_bulk.go:83-86`, `:120-124`) also takes an uncapped `checkUids` list. It walks the list linearly, with a map lookup, but passes the whole list into `uid IN (?)`. That is the D5 class, but the same fix should close it until the endpoint is removed (`specs/backlog/2026-11-05-02-remove-auto-placement-endpoint.md`).

## Proposal

1. `server/internal/handlers/checks/bulk_update.go`, `BulkUpdateChecks`: refuse the request with `errBulkTooManyChecks` when `len(req.CheckUIDs) > MaxBulkUpdateChecks`, *before* calling `uniqueCheckUIDs`. Duplicates count toward the cap: a client never needs to send the same UID twice. Keep the check on the deduplicated list too.
2. `uniqueCheckUIDs` (`bulk_update.go:240-250`): dedup through a `map[string]struct{}` (or `seen` set) and keep the request order, so the cost is linear even below the cap.
3. `server/internal/handlers/checks/placement_bulk.go`, `SwitchToAutoPlacement`: apply the same cap (`len(req.CheckUIDs) > MaxBulkUpdateChecks` → 400 with a validation error) before the query. Map it in `handler.go` `SwitchToAutoPlacement` the way `BulkUpdateChecks` maps `IsBulkUpdateRequestError`.
4. Optional, if it stays small: a `http.MaxBytesReader` on these two handlers' bodies (about 1 MiB is far above 1000 UIDs plus the changes). The repo-wide body cap stays D5's job.
5. Update `server/internal/app/openapi/openapi.yaml` if it documents the 400 of the bulk route, so the cap on the raw list is stated.

## Tests

- `server/internal/handlers/checks/bulk_update_test.go`:
  - Regression: a request with `MaxBulkUpdateChecks + 1` *distinct* unknown UIDs is a `errBulkTooManyChecks` 400. This already passes today, keep it as the baseline.
  - Regression that fails before the fix: a request with `MaxBulkUpdateChecks + 1` entries that are all the same UID is refused with `errBulkTooManyChecks`. Today it dedups to one UID and is accepted.
  - Regression that fails before the fix: `uniqueCheckUIDs` on 200,000 distinct UIDs finishes within a small time budget (for example 200 ms, generous for a linear pass on a CI runner). Today it takes tens of seconds.
  - Positive control: exactly `MaxBulkUpdateChecks` distinct UIDs, some of them real checks of the org, are accepted. The real checks are updated and the others are reported `not_found`. A list with a repeated UID below the cap is still deduplicated and keeps the request order.
- `server/internal/handlers/checks/placement_bulk_test.go` (or the existing auto-placement test file): `MaxBulkUpdateChecks + 1` UIDs is a 400; a short list still works.
