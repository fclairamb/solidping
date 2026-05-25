# Org Activation UI Fixes

## Context

The org activation/onboarding page at `/dash0/orgs/:org` has two small issues.

## Issues

### 1. Double arrow on "View all events" button

The button renders as `View all events → →` (two right-arrow icons) instead of a single `→`. Remove the duplicate arrow so it reads `View all events →`.

### 2. Missing event descriptions

The following activation events lack a human-readable description:

| Event key | Suggested description |
|---|---|
| `org.activation.first_check_created` | Your first uptime check is live and monitoring. |
| `org.activation.first_notification_configured` | You have set up your first notification channel. |

Add descriptions so they appear in the activation checklist UI alongside the event name.

## Implementation Plan

The page at `/dash0/orgs/:org` renders `OrgDashboardPage`
(`web/dash0/src/components/dashboard/dashboard-page.tsx`). The two issues both
live in the "Recent activity" card (`RecentActivityList`).

### Issue 1 — double arrow

`RecentActivityList`'s footer renders `{t("recentActivity.footer")}` (whose
locale string already ends in ` →`) followed by an extra `<ArrowRight>` icon —
producing `View all events → →`. The two sibling footers (`needsAttention`,
`activeIncidents`) rely on the ` →` in their locale string and render no icon.

- Remove the `<ArrowRight className="h-3 w-3" />` from the `recentActivity`
  footer in `dashboard-page.tsx` so it reads `View all events →`, matching the
  sibling footers. Keep the `ArrowRight` import (still used by
  `FirstResultCelebration`).

### Issue 2 — missing event descriptions

`org.activation.first_check_created` and
`org.activation.first_notification_configured` have no label or description in
the events locale, so the recent-activity feed shows the raw event key and no
description.

- Add a `getEventDescription(eventType, t)` helper alongside `getEventLabel`
  in `web/dash0/src/components/dashboard/event-display.tsx`. It looks up
  `descriptions.<eventType>` and returns `""` when absent (no description row
  rendered for events that have none).
- In `RecentActivityList`, render the description (when present) as a muted
  second line under the event name.
- Add to all four `events.json` locales (`en`, `fr`, `de`, `es`):
  - `types.org.activation.first_check_created` (a human label)
  - `types.org.activation.first_notification_configured`
  - `descriptions.org.activation.first_check_created` =
    "Your first uptime check is live and monitoring." (translated per locale)
  - `descriptions.org.activation.first_notification_configured` =
    "You have set up your first notification channel." (translated per locale)

### Tests

- Extend `web/dash0/e2e/dashboard.spec.ts`: assert the "Recent activity" footer
  contains exactly one `→`, and that an activation event with a description
  renders its description text.

### QA

- `make build-backend build-client lint-back test`.
