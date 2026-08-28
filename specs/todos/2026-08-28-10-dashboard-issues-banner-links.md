---
model: sonnet
effort: medium
---

# The dashboard "Issues detected" banner is inert and its subtitle lies when only an incident is active

## Problem

Two defects in the org dashboard's red status banner (`OverallStatusBanner` in
[web/dash0/src/components/dashboard/dashboard-page.tsx:754-786](web/dash0/src/components/dashboard/dashboard-page.tsx)):

**1. The banner does nothing on click.** "Issues detected — 1 check down,
1 active incidents" announces a problem, but an operator who sees it has to
hunt through the sidebar for the incidents page or the checks list — exactly at
the moment they want to move fastest. The banner is the most prominent element
on the page when something is wrong; it should be the fastest path to acting on
the problem.

**2. The subtitle contradicts the banner when the check has recovered but the
incident is still open.** Observed live: banner reads "Issues detected — No
active incidents" while the Active Incidents KPI tile right below shows **1**.
Root cause: the subtitle at
[dashboard-page.tsx:771](web/dash0/src/components/dashboard/dashboard-page.tsx)
passes `count: hardDownCount` as the i18next plural selector —

```tsx
t("banner.issuesSub", { count: hardDownCount, down: hardDownCount, incidents: incidentsCount })
```

— so when `hardDownCount === 0` and `incidentsCount > 0` (check back up,
incident not yet resolved; exactly the state that keeps the red branch alive
via `hardDownCount > 0 || incidentsCount > 0`), i18next selects
`issuesSub_zero`, whose English text is "No active incidents"
([locales/en/dashboard.json:46](web/dash0/src/locales/en/dashboard.json)). The
banner fires *because* of the incident, then denies it exists. A secondary
symptom of the same single-`count` design: the incidents fragment can never
pluralize independently, producing "1 check down, 1 active incident**s**".

## Proposal

### Fix the subtitle (bug 2)

Compose the subtitle from two independently pluralized fragments instead of one
key driven by a single `count` — e.g. `issuesSubDown` ("{{count}} check down" /
"{{count}} checks down") and `issuesSubIncidents` ("{{count}} active incident" /
"{{count}} active incidents") — and join only the non-zero parts:

- down > 0, incidents > 0 → "2 checks down, 1 active incident"
- down > 0, incidents = 0 → "2 checks down"
- down = 0, incidents > 0 → "1 active incident"

(down = 0 **and** incidents = 0 never reaches this branch.) Update all locale
files under `web/dash0/src/locales/*/dashboard.json`, removing the now-unused
`issuesSub_*` keys, and run `bun run test:unit` — the locale-key parity tests
must stay green.

### Make the banner a link (feature 1)

Make the red banner a link to the most actionable destination, with **incidents
taking priority** (an active incident carries the ack/snooze/resolve workflow;
a down check without an incident is just a state):

- `incidentsCount > 0` → `/orgs/$org/incidents?state=active`
  (the incidents list already validates `state` in
  [incidents.index.tsx:53](web/dash0/src/routes/orgs/$org/incidents.index.tsx)).
- otherwise (`hardDownCount > 0`) → `/orgs/$org/checks?status=down`
  (the checks list already validates `status`; `"down"` is a
  `STATUS_FILTER_VALUES` member, [checks.index.tsx:139](web/dash0/src/routes/orgs/$org/checks.index.tsx)).

Implementation notes:

- Render the banner card as (or wrapped in) a TanStack Router `<Link>` — not a
  `div` with `onClick` — so keyboard focus, middle-click and copy-link work.
  Add a hover affordance (e.g. `cursor-pointer` plus a slightly stronger
  border/background on hover) so it reads as clickable.
- `OverallStatusBanner` doesn't currently know the org — pass the org slug down
  from `DashboardPage` (call site at
  [dashboard-page.tsx:439](web/dash0/src/components/dashboard/dashboard-page.tsx))
  or read it via router params.
- Keep `data-testid="overall-status-banner"` intact — existing E2E asserts on it.
- **Consistency (in scope, cheap):** give the amber "Degraded Performance"
  banner (same component, timeout-only branch) the same treatment, linking to
  `/orgs/$org/checks?status=warning`. The green all-clear banner stays inert.
- Check the design reference
  ([design-reference.tsx](web/dash0/src/routes/orgs/$org/design-reference.tsx))
  first; if a "clickable banner/alert" pattern is missing there, add it as part
  of this change.

## Testing

Extend [web/dash0/e2e/dashboard.spec.ts](web/dash0/e2e/dashboard.spec.ts):

- Click-through: with an active incident the banner lands on the incidents list
  filtered to active; with only a down check (no incident) it lands on the
  checks list filtered to down.
- Subtitle: in the recovered-check-but-open-incident state (0 hard-down,
  ≥1 active incident) the banner subtitle names the active incident count and
  never reads "No active incidents"; with both counts non-zero, each fragment
  pluralizes on its own count.
