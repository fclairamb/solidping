---
model: sonnet
effort: medium
pr: batch
automerge: false
---

# The bug-report retention sweep also deletes website crawl reports

## Problem

Found by the security scan of 2026-10-30 (finding Z2).

**Severity: low.** Nobody needs to trigger it: the state-cleanup job does it on every instance where retention is on, which is the default (90 days). It is a regression of the Y1 fix (#708, `specs/done/2026/10/2026-10-29-01-bug-report-upload-instance-budget.md`).

Two features store files in the same `reports` storage group (`server/internal/handlers/filestorage/filestorage.go:27`):
- bug-report screenshots, created without a topic (`server/internal/handlers/feedback/service.go:303-307`);
- website crawl reports, attached under the `checks/<uid>/crawl-report` topic. `groupForTopic` maps `KindCrawlReport` to `GroupTypeReports` (`server/internal/handlers/attachments/service.go:472-486`).

The sweep `sweepExpiredReportFiles` (`server/internal/jobs/jobtypes/job_state_cleanup.go:209-248`) lists files through `ListFilesByGroup`. That query matches only `file_uri LIKE '%/reports/%'` and `created_at` (`server/internal/db/postgres/postgres.go:8034`, `server/internal/db/sqlite/sqlite.go:7998`). It then purges every crawl report older than the retention, row and blob.

**Impact:**
- A check that crawls less often than the retention, or is paused, loses all its crawl reports. Crawl reports are already capped at 5 per check (`attachments/steps.go:107-113`).
- Its next run has no baseline to diff against (`server/internal/crawltask/server.go:468`).
- `GET .../crawl-reports` returns nothing for it.

The shared group has a second effect, older than #708. The per-org bug-report quota sums the whole group (`SumFileSizeByGroup`, `feedback/service.go:337`). An org's own crawl reports therefore use up its 100 MB screenshot quota. Its members' reports with a screenshot then get 413.

## Proposal

1. Restrict both feedback queries to bug-report screenshots with `topic IS NULL`. Feedback files never carry a topic. Every attachment, crawl reports included, does (`attachments.WithTopic`).
   - `ListFilesByGroup`: in both dialects, or through a dedicated `ListFeedbackFiles` method if the generic name is kept for other callers.
   - The quota sum used by `checkStorageQuota` (`feedback/service.go:331-354`).
2. Alternative, if a dedicated group is preferred: write new screenshots under a `feedback` group. Existing rows stay matched by `topic IS NULL` until they expire. Do not move blobs.
3. Update the comment of `sweepExpiredReportFiles` to say it covers screenshots only.

## Tests

- `server/internal/jobs/jobtypes/job_state_cleanup_reports_test.go`:
  - **Regression (fails before the fix):** a crawl report attached to a check, older than the retention, survives the sweep with its blob.
  - **Positive control:** a topic-less `reports` file older than the retention is still purged; a younger one is kept.
- `server/internal/handlers/feedback`:
  - **Regression:** an org whose crawl reports total more than the quota still accepts a screenshot report.
  - **Positive control:** screenshots alone over the quota still get 413.
- Run the Postgres variant of the quota test (`quota_postgres_test.go`) for the dialect change.
