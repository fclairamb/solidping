---
model: opus
effort: medium
pr: batch
automerge: false
---

# Authenticated bug reports skip the screenshot budget for any org, member or not

## Problem

Found by the security scan of 2026-10-30 (finding Z1).

**Severity: low.** Any signed-in user can trigger it. On SaaS that means anyone, since sign-up is open. It is a residual of Y1 (`specs/done/2026/10/2026-10-29-01-bug-report-upload-instance-budget.md`, fixed in #708).

The Y1 fix bounds what `POST /api/mgmt/report` stores for **anonymous** callers. It added:
- an instance-wide hourly screenshot budget;
- the requirement of a free GitHub issue dispatch slot.

Both run only when `req.UserUID == ""` (`server/internal/handlers/feedback/service.go:238-242`). Step 5 of that spec kept "authenticated members" under the per-org quota only. The code does not check membership, though:

- `attachUserIfAuthenticated` sets `UserUID` from any valid bearer token (`server/internal/handlers/feedback/handler.go:124-146`). It does not look at the org.
- The org still comes from the caller's `org` form field (`handler.go:69`). `resolveOrg` accepts any slug on the instance (`service.go:161`, `:203-231`).

So one account can do all of the following:
- write screenshots into every org on the instance, up to the 100 MB per-org quota each (`server/internal/config/config.go:1262`, `service.go:331`);
- go past the 20-per-hour instance budget;
- store files past the dispatch cap that are never filed. They stay until the 90-day retention.

The only rate limit is the per-IP route limiter: 5 requests per minute, burst 10 (`server/internal/app/server.go:655,663-669`). That is about 3 GB per hour per IP.

**Impact:** the impact Y1 described, now open to any free account instead of anonymous callers:
- the data disk fills (with the default `local` file backend, it also holds the database), or S3 cost grows;
- each target org's report quota fills up, and its members' own reports with a screenshot get 413.

The requests are attributed to the account, which makes them traceable but does not stop them.

## Proposal

1. In `admitAndStoreScreenshot` (`server/internal/handlers/feedback/service.go:236-246`), an authenticated report skips the anonymous admission only when the user is a member of the resolved org (`db.GetMemberByUserAndOrg`, `server/internal/db/service.go:187`) or is a super admin.
2. Any other authenticated report goes through `admitAnonymousScreenshot`, like an anonymous one: same instance budget, same dispatch-slot requirement. Its attribution (`UserUID`, `UserEmail`) is kept.
3. Pass what the decision needs from the handler, the user's super-admin flag included, rather than re-parsing the token in the service.
4. Update the comment at `server/internal/app/server.go:2510-2520` to say "members of the named org".

## Tests

`server/internal/handlers/feedback` (SQLite):
- **Regression (fails before the fix):**
  - setup: anonymous screenshot budget 1, already spent; user U is a member of org A only;
  - U sends a report with a screenshot naming org B;
  - expected: 429 `RATE_LIMITED`, and no `reports` file in org B.
- **Regression:** with the dispatch cap spent, U's screenshot to org B is not stored.
- **Positive controls:**
  - with the budget spent, U's report with a screenshot naming org A is stored (201);
  - a super admin's report naming any org is stored;
  - U's report without a screenshot to org B is still accepted.

## Open questions

- Should a non-member's report be refused outright (403) instead of being treated as anonymous? **Recommended:** treat it as anonymous. The status0 report dialog can be used by a signed-in user on another org's status page, and that report should still go through.

## Resolved open questions

- Treat a non-member's report as anonymous (no 403). (autopilot: recommended answer)
