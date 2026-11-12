---
model: sonnet
effort: low
---

# The sealing re-wrap endpoints run one query per listed check, uncapped

## Problem

Found by the security scan of 2026-11-12 (finding AK5).

**Severity: Low (resource use).** Who can trigger it:
- an org admin, for `POST /orgs/:org/sealing/rewrap`. On SaaS, any user can create an org and be its admin;
- the same role for `GET /orgs/:org/sealing/rewrap?check=…`.

The endpoints:
- `POST` takes `checkUids` from the JSON body (`server/internal/handlers/checks/rewrap.go:34`).
  - `checksByRef` runs one `GetCheckByUidOrSlug` per entry, with no cap and no dedup (`rewrap.go:118-135`).
  - All of them then go to `rewrap.Start`, which builds and validates a candidate for each one.
- `GET` splits `check` on commas (`browser_sealing_handler.go:180-190`).
  - `RewrapStatus` passes the list to `rewrap.Status`, an uncapped `IN` list (`server/internal/rewraptask/server.go:547`).
  - It then runs one `GetCheck` per outcome (`rewrap.go:85-90`).

One request repeating the same identifier tens of thousands of times holds a database connection for as long as the list lasts. This is the class fixed for results and incidents in AG1/AE1 (`server/internal/checkfilter`). The new endpoints did not reuse that filter. The `GET` parameter is also named `check`, where the API convention is the singular `checkUid` (AGENTS.md).

## Proposal

1. In `rewrapCandidates` (`rewrap.go:97`) and `RewrapStatus` (`rewrap.go:70`):
   - run the identifiers through `checkfilter.CheckCount` (refuse more than the cap with `VALIDATION_ERROR`), then `checkfilter.Normalize` (dedup), in that order (`server/internal/checkfilter/checkfilter.go:39,51`);
   - do this before any query.
2. In the `GET` handler, accept `checkUid` (keep `check` as an alias), split with `checkfilter.Split` (`checkfilter.go:24`), and update `openapi.yaml` and dash0's caller.
3. Optionally load the checks of `checksByRef` in one query (UIDs and slugs), not one per entry.

## Tests

- `server/internal/handlers/checks/rewrap_test.go`:
  - **Regression:** a `POST` with more identifiers than the cap answers 400 `VALIDATION_ERROR` and makes no check query (fails on main today);
  - a `POST` listing the same UID 50 times starts a single re-wrap;
  - the same two cases for `GET ?checkUid=`.
- Positive control: a `POST` with two distinct flagged checks still starts two re-wraps, and `GET ?check=` (alias) still answers.
