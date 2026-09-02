---
model: sonnet
effort: medium
---

# The "Create a weekly uptime report for me" wand creates a report with an empty check list — it should carry the 10 most recently created checks

## Problem

The magic wand on the uptime-reports page (`/orgs/$org/organization/report-schedules`)
creates a schedule whose scope is empty. `buildWeeklyReportWandPayload`
(`web/dash0/src/lib/onboarding-wand.ts:42-56`) hard-codes `checkUids: []` and
`checkGroupUids: []`, and the index page's click handler
(`web/dash0/src/routes/orgs/$org/organization.report-schedules.index.tsx:59-75`) sends
that as-is. The result is a report that, from the operator's side, contains nothing:

- Opening the created schedule shows both pickers blank
  (`web/dash0/src/components/slos/report-schedule-form.tsx:141-160`). The only hint that
  this means "everything" is the small help line under the *groups* picker
  (`web/dash0/src/locales/en/slos.json:180`); the checks picker has none.
- The list page shows "All checks" in the Scope column, but nothing in the created report
  names a single check.

Spec 2026-08-29-03 chose the empty scope deliberately ("empty scopes = org-wide, matching
the seeder") — the backend does honour it: `IsOrgWide()`
(`server/internal/db/models/report_schedule.go:57-58`) makes `scopeChecks`
(`server/internal/uptimereport/report.go:543-556`) list every non-internal check in the
org. On the local dev database the wand-created schedule's 30 Aug run rendered
`CheckCount: 35` / 35 rows, so the *email body* is not blank on an org that has checks. The
report the wand hands the user is nonetheless empty as a thing they can look at, and a
schedule with no checks of its own is not what "create a report for me" should produce.

The ask: the wand should create a report scoped to the **10 most recently created checks**.

## Proposal

Frontend-only. No API or backend change: `POST /orgs/:org/report-schedules` already
accepts `checkUids`, and the mail's scope label already handles a scoped schedule
(`server/internal/uptimereport/report.go:848-854` → "Weekly uptime report (10 checks)").

1. **Fetch the checks to attach.** `GET /orgs/:org/checks?limit=10` — the default list
   order is `created_at DESC, uid DESC` and the default `internal` filter hides internal
   checks (`server/internal/db/models/check.go:518-547`,
   `server/internal/db/postgres/postgres.go:1849-1867`), so the first page *is* "the last
   10 created checks". Use the existing `useChecks(org, { limit: 10 })`
   (`web/dash0/src/api/hooks.ts:633`) on the index page; keep the wand disabled (spinner)
   while that query is loading, the same way it is while the create mutation is pending.
2. **Extend the payload builder.** `buildWeeklyReportWandPayload(t, email, timezone,
   checks)` takes the fetched list and emits `checkUids` = up to the first 10 UIDs in the
   order received (newest first). `checkGroupUids` stays `[]`. Fewer than 10 checks →
   all of them. Update the doc comment that says the shape mirrors `seedOrgDefaults` —
   the scope no longer does.
3. **Zero-check org → unchanged behaviour.** With nothing to pick, keep sending the
   empty (org-wide) scope rather than hiding the wand or creating a schedule that can
   never report on anything. The Getting Started checklist only shows once the org has
   ≥1 check (`web/dash0/src/components/dashboard/dashboard-page.tsx`), so this branch is
   reachable only from the page itself.
4. **Seeder stays org-wide.** `seedOrgDefaults`
   (`server/internal/handlers/auth/service.go:3129-3131`) runs at org creation, when
   the org has zero checks — there is nothing to scope to. Out of scope here.
5. **Tests.**
   - `web/dash0/src/lib/onboarding-wand.test.ts`: caps at 10, keeps order, fewer-than-10
     passes all, zero → both scopes empty, `checkGroupUids` always empty; the locale-parity
     assertions stay as they are.
   - `web/dash0/e2e/magic-wand.spec.ts:220-266` ("report wand: creates a weekly org-wide
     report…"): seed at least 12 checks, click the wand, then assert the created
     schedule's `checkUids` equals the UIDs returned by `GET /checks?limit=10` (same
     order), the Scope column reads "10 checks, 0 groups", the edit page's checks picker
     lists those 10, and the `onboarding-step-report` flip still holds. Rename the test —
     it is no longer org-wide.
6. **Docs.** The changelog line for #288 (`web/docs/docs/changelog.md:40`) describes the
   report wand only as "the sensible default"; no docs page documents the empty scope, so
   nothing to rewrite. Add a changelog entry per `wiki/conventions/changelog.md`.

### Open question

If the report that prompted this was an actual blank *email* (no check rows) on an org
that has checks, that is a separate backend defect this spec does not touch. It can be
told apart by reading the enqueued job: `jobs.config → templateData.CheckCount` /
`templateData.Checks` for `type = 'email'` with the uptime-report template. Locally that
row shows 35 rows for the wand-created schedule.
