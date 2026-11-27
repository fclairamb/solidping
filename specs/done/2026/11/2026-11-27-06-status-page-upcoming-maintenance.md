---
model: sonnet
effort: medium
---

# Public status pages don't announce scheduled maintenance before it starts

GitHub Issue: https://github.com/fclairamb/solidping/issues/1084

## Source

Florent filed this from competitor research (Uptime Kuma issues
[#2389](https://github.com/louislam/uptime-kuma/issues/2389), "show upcoming scheduled
maintenance on status pages before the window starts", and
[#862](https://github.com/louislam/uptime-kuma/issues/862), "Incident Timeline"). He asks for
two status-page presentation features built on data SolidPing already holds:

1. an "upcoming maintenance" section listing the scheduled windows before they start;
2. a public, chronological incident timeline with its updates.

The first one is a real gap. The second one already exists (see below), so this spec only
covers item 1 and records item 2 as covered.

## Problem

**Upcoming maintenance is invisible until the window opens.** The public payload only carries
a per-resource boolean: `InMaintenance` (`server/internal/handlers/statuspages/service.go:878`),
computed by `anyWindowActive` (`service.go:4691`) from
`ListMaintenanceWindowsForCheck` / `ListMaintenanceWindowsForCheckGroup`
(`service.go:4634`, `service.go:4675`). status0 turns it into the "Scheduled maintenance" badge
(`web/status0/src/components/shared/status-page-view.tsx:166-249`). A window that starts in
two hours produces nothing at all on the page: visitors see no warning, then a badge appears
out of nowhere. The data needed is there: `models.MaintenanceWindow` (title, description,
start/end, recurrence, `server/internal/db/models/maintenance_window.go:10`), its targets in
`MaintenanceWindowCheck` (check or check group, `maintenance_window.go:70`), and
`models.NextOccurrences(window, from, n)` (`maintenance_window.go:165`), which already expands
recurring windows into their next occurrences, active one first.

**The incident timeline is already there.** `GET /status-pages/:org/:slug/incidents`
(`server/internal/app/server.go:2320`, `incidentpublications.Handler.ViewPublicIncidents`)
returns every public publication with its updates, and status0 renders the resolved ones,
newest first, with their update threads, in the collapsed "Past incidents" section
(`web/status0/src/components/shared/incident-history.tsx`, mounted at
`status-page-view.tsx:752`). A dedicated timeline page would duplicate it; it is out of scope
unless the open question below says otherwise.

## Why a spec

- A new or changed API field: the public status-page payload gains an `upcomingMaintenance`
  list (OpenAPI change).
- A product decision the reporter didn't settle: which windows a page shows, how far ahead,
  and whether the operator-written description is public.

## Proposal

### Backend

1. In `statuspages.Service`, on the public view paths only (`ViewStatusPage`,
   `ViewDefaultStatusPage`, same population rule as `ActiveIncidents`), compute
   `UpcomingMaintenance []PublicMaintenance` for the page:
   - Collect the page's resources (checks, and check groups with their members), then the
     maintenance windows targeting any of them (reuse the per-check / per-group lookups, or
     add one batched `ListMaintenanceWindowsForChecks(ctx, checkUIDs, groupUIDs)` to avoid an
     N+1 on large pages; dedupe windows by UID).
   - Expand each window with `models.NextOccurrences(window, now, 3)` and keep the
     occurrences that are active now or start within the horizon (see open question 2,
     default 7 days). Sort by start, cap at 10 entries.
   - Each entry: `uid` (window UID), `title`, `startsAt`, `endsAt`, `active` (bool),
     `affectedResources` (the page's own public resource names that the window targets,
     never a check the page doesn't show), and `description` only per open question 3.
2. Add the field to the status-page response struct next to `ActiveIncidents`
   (`service.go:729`), `omitempty`, documented like its neighbours. Never on admin listings.
3. Memo invalidation: the public page is memoized (`memo.go`). Creating, updating or deleting
   a maintenance window (and changing its targets) must invalidate the org's page memos,
   the same way `incidentPublicationsService.SetPageMemoInvalidator(statusPagesService)`
   does (`server.go:2108`). Wire `maintenancewindows.Service` to the invalidator. A window
   that *starts* without any write is covered by the memo TTL; check that the TTL is short
   enough that the badge and the section flip within a minute, otherwise cap the memo expiry
   at the next occurrence boundary.
4. OpenAPI: document `upcomingMaintenance` on the public status-page schema.

### status0

5. A new `UpcomingMaintenance` component in `web/status0/src/components/shared/`, rendered
   above the resource sections (below active incidents) when the list is non-empty: one card
   per entry with the title, the time range in the visitor's locale and timezone, a
   relative hint ("starts in 2 h", "in progress"), and the affected resources.
   `data-testid`: `upcoming-maintenance`, `upcoming-maintenance-item`,
   `upcoming-maintenance-active-badge`.
6. i18n keys in `web/status0/src/locales/{en,fr}/status.json`.
7. TV/kiosk board (`web/status0/src/components/tv/`): out of scope, keep it on live state.

### Tests

8. Go (`statuspages` package): a page with a check targeted by a one-off window starting in
   2 h lists it; a window starting beyond the horizon is absent; a recurring daily window
   yields its next occurrence; an active window has `active: true`; a window targeting only
   a check the page doesn't show is absent; a window targeting a group on the page is
   listed with the group's public name; the description follows open question 3; the admin
   payload has no `upcomingMaintenance`.
9. Go: updating a window invalidates the page memo (the next public read sees the change).
10. Vitest or Playwright for status0: the section renders the cards and hides when empty.

### Docs

11. Mention the section in the status-page docs (`web/docs/` page on status pages) and in the
    maintenance-windows docs: "windows targeting a page's resources are announced on it
    up to 7 days ahead".

## Open questions

1. **Opt-in per page or always on?** Recommended: always on, with no new column. The
   maintenance-window title is already documented as public ("shown in notifications and
   status pages", `001_v0_1_0.up.sql:815`), and a page only lists windows on resources it
   already shows. Trade-off: an operator who names windows with internal jargon gets it
   published on upgrade; a `show_upcoming_maintenance` flag would avoid that at the cost of
   a migration and a settings toggle.
2. **Horizon.** Recommended: 7 days, fixed. Trade-off: a per-page setting is more flexible
   but is one more knob nobody tunes; 7 days covers the usual "announce a week ahead" habit.
3. **Is the description public?** Recommended: yes, rendered as plain text (no markdown),
   since it is the natural place for "what will be affected and why". Trade-off: some
   operators may have written internal notes there; dropping it is safer but leaves the
   card with a bare title.
4. **A dedicated incident-timeline page?** Recommended: no. The "Past incidents" section
   already gives the chronological history with updates; revisit only if users ask for a
   shareable `/history` URL or month grouping.

## Resolved open questions

1. Always on, no new column, no migration: every public status page lists the upcoming windows of the resources it shows. (autopilot: recommended answer)
2. Fixed 7-day horizon. (autopilot: recommended answer)
3. Publish the window's description, rendered as plain text (no markdown). (autopilot: recommended answer)
4. No dedicated incident-timeline page: the existing "Past incidents" section covers it. (autopilot: recommended answer)

## Closing the issue

This spec closes #1084. The implementing PR body **must** carry one `Closes #1084` line so
the squash-merge closes it. When the spec is archived to `specs/done/`, verify the issue is
closed and, if the merge did not close it, close it by hand:

    gh issue close 1084 --comment "Implemented by <PR or commit>; spec: <archived spec path>"
