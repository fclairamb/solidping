---
model: opus
effort: medium
---

# Anonymous bug-report screenshots are capped per org, not per instance

## Problem

Found by the security scan of 2026-10-29 (finding Y1).

**Severity: low.** Anyone can trigger it, with no account, on an instance where bug reports are enabled (`app.github.issues_token` and `app.github.repo` set, `server/internal/config/config.go:3610-3612`). Since #701 the login page links to the same dialog, so the endpoint is now advertised to anonymous visitors.

`POST /api/mgmt/report` is anonymous (`server/internal/app/server.go:2510-2518`). The only limits on what it stores are:

- the 10 MB body cap (`server/internal/handlers/feedback/handler.go:19,53`);
- a per-IP limiter of 5 requests per minute, burst 10 (`server/internal/app/server.go:655,663-669`);
- a per-org quota of 100 MB of `reports` files (`server/internal/config/config.go:1249-1251`, checked in `server/internal/handlers/feedback/service.go:208-231`).

The quota does not bound what the instance stores:

1. **The caller picks the org.** The `org` form field names any org on the instance (`feedback/service.go:135-153`), so the 100 MB cap applies once per org. On SaaS, the total an anonymous caller can write is 100 MB times the number of orgs. Anyone can also add orgs by signing up.
2. **The screenshot is stored before, and whether or not, the GitHub issue is filed.** `CreateFile` runs in the request (`feedback/service.go:163-186`). The issue dispatch, which is the only consumer of the file, is capped at 20 per hour per process (`feedback/service.go:49`, `:271`). Every screenshot past that cap is written and never referenced.
3. **The quota check is not atomic.** `checkStorageQuota` sums the group and compares, then `CreateFile` writes (`feedback/service.go:167-176` and `:208-231`). Parallel requests from several IPs all pass the check and overshoot the cap by about 10 MB each.
4. **Nothing deletes report files.** No job touches `filestorage.GroupTypeReports` (`server/internal/handlers/filestorage/filestorage.go:27`).

**Impact:**

- One IP writes about 50 MB per minute (3 GB per hour) of arbitrary bytes into the file store. Several IPs multiply that.
- With the default `local` file backend (`config.go:1209`), the files share the server's data disk. Filling that disk can stop the database, and with it monitoring for every org. With S3 it is a storage cost.
- Each target org's report quota stays full for good, so its members' own bug reports with screenshots get 413 (`feedback/handler.go:104-106`).

Spec 2026-09-25-26 added the per-org quota and the limiter as the storage protection. It assumed one org; this finding is the many-org case.

## Proposal

1. **Budget anonymous screenshots for the whole instance.** In `Service.SubmitReport` (`feedback/service.go:163`), when the report has no authenticated user (`req.UserUID == ""`), spend a global counter with `db.BumpGlobalCounter` before `CreateFile`. Use a key such as `feedback_report_anon` and a one-hour window, the primitive the status-page subscribe budget uses (`server/internal/handlers/statussubscribers/service.go`, `spendConfirmBudget`). Over budget, answer 429 `RATE_LIMITED` and store nothing.
2. **Do not store a screenshot that will never be filed.** Reserve the issue dispatch slot (`allowIssueDispatch`, `feedback/service.go:237`, today called from the goroutine started at `:196`) before `CreateFile`, not inside the goroutine. If no slot is left, refuse the screenshot as in step 1, before anything is written.
3. **Make the per-org quota check atomic** with the file insert, or spend the bytes through a counter, so parallel requests cannot overshoot it.
4. **Expire report files.** Add a retention to the state-cleanup job (`server/internal/jobs/jobtypes/job_state_cleanup.go`) that deletes `reports` files older than the retention, both the row and the stored blob.
5. Authenticated members keep today's behaviour, under the per-org quota only.
6. Update the comment at `server.go:2510-2515` to describe the real storage controls.

## Tests

- `server/internal/handlers/feedback` (SQLite, plus Postgres for the counter):
  - **Regression (fails before the fix):** with the anonymous budget set to 3 and two orgs, send 6 anonymous reports with a screenshot, alternating the `org` field. The first 3 store their file, the next 3 get 429, and the `reports` group holds exactly 3 files across both orgs.
  - **Regression:** with the dispatch budget spent, an anonymous report with a screenshot stores no file.
  - **Regression:** N parallel anonymous reports against an org whose quota has room for one file store at most one file.
  - **Positive controls:**
    - a first anonymous report with a screenshot is stored, answers 201 and dispatches one issue;
    - an authenticated member's report with a screenshot is still stored after the anonymous budget is spent;
    - a report without a screenshot is accepted under the budget.
- `server/internal/jobs/jobtypes`: a `reports` file older than the retention is deleted with its blob, and a younger one is kept.

## Open questions

- Anonymous screenshot budget: **recommended** 20 per hour for the instance, the same as the issue dispatch cap, as a config value with that default.
- Report file retention: **recommended** 90 days, as a config value.
