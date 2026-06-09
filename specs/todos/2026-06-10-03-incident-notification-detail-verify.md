# Incident notification detail: verify click-through opens the detail page

## Context
On the incident detail page
(`web/dash0/src/routes/orgs/$org/incidents.$incidentUid.tsx`), the
`NotificationsCard` renders each notification as a clickable table row
(`data-testid="notification-row"`, `role="link"`, keyboard support). The click
handler (`openNotification`, lines 1057-1061) already navigates to
`/orgs/$org/incidents/$incidentUid/notifications/$notificationUid`, and that
route exists and is fully built
(`incidents.$incidentUid.notifications.$notificationUid.tsx`): a complete detail
page with a delivery timeline (created/sent/failed/cancelled), target section,
escalation context, HTTP delivery details, identifiers, and an error card.

In other words, **the feature already works** — clicking a notification already
opens its details in a dedicated page. This spec exists to **lock that behaviour
with an end-to-end assertion** so a future refactor can't silently break it, not
to build anything new.

## Goal
Add (or confirm) e2e coverage that proves: from the incident detail page,
clicking a notification row navigates to the per-notification detail page and
that page renders its detail content.

## Behaviour
No production-code change is expected. If writing the test surfaces a genuine
gap (broken navigation, empty detail page for some notification status), capture
it as a separate follow-up spec rather than expanding this one.

## Testing
dash0 Playwright E2E (`web/dash0/e2e/`); incident coverage lives in
`e2e/incidents.spec.ts` (add a notifications-detail case there, or a focused
`e2e/incident-notifications.spec.ts` if cleaner).

Seed an incident that has at least one notification (reuse the existing incident
+ notification fixture pattern), then:
- Open the incident detail page; assert at least one `notification-row` is
  present.
- Click the first `notification-row`; assert the URL matches
  `…/incidents/<uid>/notifications/<uid>` and that the detail page renders
  (e.g. the delivery timeline / a known heading or `data-testid` on the
  notification detail route).
- Assert the detail page's back affordance returns to the incident.

Manual: `make dev-test`, open an incident with notifications, click a row,
confirm the detail page; desktop + mobile, light + dark.

## Implementation Plan

The click-through feature is already fully built (incident detail
`NotificationsCard` → per-notification detail route). This spec locks it with a
**deterministic** E2E assertion. The existing
`e2e/notification-detail.spec.ts` test ("clicking a notification row opens the
deep-linkable detail") covers the happy path but *skips* whenever the test org
has no incident carrying notification rows — which is the default state, since
nothing seeds an incident notification. To make the assertion run reliably we
seed one in test mode.

1. **Seed an incident with a notification (backend, test mode only).** Extend
   `server/test/testdata/CreateTestData` to create, on deterministic UIDs in the
   `test` org: a check, an active incident on that check (with a title), and one
   `IncidentNotification` row (a failed webhook delivery, so the detail page also
   exercises the error + delivery surfaces). Use the existing
   `db.Service.CreateCheck` / `CreateIncident` / `CreateIncidentNotification`
   methods. This only runs under `SP_RUNMODE=test`, the mode the E2E suite boots.
2. **Add the deterministic E2E case in `e2e/incidents.spec.ts`** per Testing:
   open the seeded incident detail page, assert at least one `notification-row`
   is present, click the first row, assert the URL matches
   `…/incidents/<uid>/notifications/<uid>` and the detail page renders (the
   "Notification" heading and "Delivery timeline"), then assert the back
   affordance (`aria-label="Back to incident"`) returns to the incident detail
   page. No `test.skip` fallback — the seed guarantees data.
3. No production UI tweak is needed: the notification detail page already exposes
   a stable `role="heading" name="Notification"` and a `Back to incident`
   aria-label; the incident detail `NotificationsCard` already tags rows with
   `data-testid="notification-row"`.
4. Verify: `make build`, `make test`, `make test-dash`. If the test reveals a
   real defect, file a follow-up spec and link it here.
