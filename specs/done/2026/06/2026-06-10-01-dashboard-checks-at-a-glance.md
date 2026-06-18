# Dashboard: show real monitoring data in the healthy state (checks at a glance)

## Context
The org dashboard (`/orgs/$org`, rendered by
`web/dash0/src/components/dashboard/dashboard-page.tsx`) currently stacks: a
page header, a large overall-status banner, four KPI tiles, a two-column row
with "Needs attention" + "Active incidents", a "Recent activity" feed, and the
on-call widget.

In the healthy state — which is the state the dashboard is in almost all the
time — this page says "everything is fine" three times (green banner, the
"Currently down: 0" tile, the "Active incidents: 0" tile) and then spends its
two most prominent cards on empty placeholders ("All checks are passing", "No
active incidents"). There is **zero actual monitoring data on screen**: no
check names, no uptime history, no response times. To see anything real the
operator has to leave the dashboard and open `/checks`.

A monitoring dashboard should *show the fleet*, not just summarize its absence
of problems.

## Goal
Make the dashboard informative in the healthy state by replacing the two
mostly-empty cards with a **"Checks at a glance"** card that always has
content: every check (up to a cap) with its status, a 24-hour uptime strip,
and its latest response time. Checks that need attention sort first, so the
card also fully subsumes today's "Needs attention" list. Trim the redundancy
around it.

## Behaviour

### 1. New full-width "Checks at a glance" card
Replaces the `NeedsAttentionList` card. Always rendered (the empty-org case is
already handled by `EmptyStateOnboarding` and is unchanged).

- **Rows**: up to 10 checks. Ordering: checks needing attention first (status
  not `up`/`created`, most recent `lastStatusChange` first — same ordering as
  today's `pickTopAttention`), then healthy enabled checks sorted by name.
  Disabled checks come last and render muted.
- **Each row** (links to `/orgs/$org/checks/$checkUid`, same hover treatment
  as today's list rows):
  - status badge (reuse the existing `statusBadgeVariant` mapping),
  - check name (truncating, `name || slug || uid`),
  - a **24h uptime strip** (see §2) filling the flexible middle space —
    hidden on narrow screens (`hidden sm:block`) so mobile rows stay one line,
  - latest response time (`durationMs` of the most recent hour bucket,
    `—` when absent),
  - for non-up checks: the "since 12m" relative timestamp shown today.
- **Footer**: "View all N checks →" linking to `/orgs/$org/checks` (N = total
  check count, so a capped list is never mistaken for the full fleet).
- **Data**: one aggregated query for the whole card —
  `useResults(org, { periodType: "hour", periodStartAfter: since24h, size: 1000, refetchInterval: RESULT_POLL_MS })`
  grouped client-side by `checkUid` (`OrgResult` already carries `checkUid`,
  `availabilityPct`, `durationMs`, `periodStart`). **No per-check requests** —
  the card costs one HTTP call regardless of fleet size. The existing
  `periodType: "day"` query keeps feeding the availability KPI unchanged.

### 2. `UptimeStrip` presentational component
A new small component (suggested: `web/dash0/src/components/ui/uptime-strip.tsx`)
rendering 24 hourly cells, oldest → newest, modeled on the `StatusBar` cells in
`web/dash0/src/components/shared/status-timeline.tsx`:

- Cell color from the bucket's `availabilityPct`: green at 100, yellow in
  (0, 100), red at 0, gray (`bg-gray-300`-equivalent token) for hours with no
  data. Reuse the green/yellow/red classes already used by `StatusTimeline` so
  the two visuals stay consistent.
- Tooltip per cell: hour label, availability %, avg latency (same tooltip
  styling as `StatusTimeline`).
- Pure props-in component (`buckets: {periodStart, availabilityPct?, durationMs?}[]`),
  no fetching — the dashboard groups and passes data down.
- **Add it to the design reference page**
  (`web/dash0/src/routes/orgs/$org/design-reference.tsx`) with its import
  line, per the design-reference convention.

### 3. Active incidents card only when there are incidents
- When `incidentsCount === 0` the `ActiveIncidentsList` card is **not
  rendered** — the green banner and the incidents KPI tile already state it,
  and the slot was pure placeholder.
- When `incidentsCount > 0` the card renders full-width **above** "Checks at a
  glance" (incidents are the most urgent thing on the page), content unchanged
  from today.

### 4. Compact status banner
The `OverallStatusBanner` keeps its three states and colors but shrinks to a
single-line strip (icon + title + the existing sub-line inline, reduced
vertical padding — roughly half the current height). With real data now below
it, the banner is a headline, not the main content.

### Resulting layout (healthy state)
Header → compact green strip → 4 KPI tiles → **Checks at a glance** (10 rows
of status + uptime strips + latencies) → Recent activity → My on-call. Every
section now shows data.

## Out of scope
- No backend changes; the hourly aggregate endpoint already exists. (The
  `GET /orgs/{org}/dashboard` aggregate endpoint noted in the file-top TODO
  stays a separate, future spec.)
- KPI tiles, Recent activity, `MyOnCallWidget`, `FirstResultCelebration`,
  empty-org onboarding, and polling cadences are unchanged.
- No filtering/grouping controls on the glance card (labels, groups, search
  live on `/checks`).
- `web/dash` (legacy) untouched.

## Testing
dash0 has Playwright E2E only (`web/dash0/e2e/`); dashboard coverage lives in
`e2e/dashboard.spec.ts`.

- **Update existing assertions** that target the "Needs attention" /
  always-present "Active incidents" cards.
- **New assertions**:
  - Healthy org: glance card lists the seeded checks with uptime strips
    (`data-testid="checks-glance"`, rows `data-testid="glance-row"`), no
    incidents card present, footer links to `/checks`.
  - With a down check (reuse the existing down-check fixture pattern): the
    down check sorts first with a destructive badge and a "since" timestamp.
  - With an active incident: incidents card renders above the glance card.
- **Manual / visual**: `make dev-test`, check `/dash0/orgs/test`, desktop +
  mobile viewport (strips hidden on mobile, rows stay tappable), light + dark
  mode, and the new `UptimeStrip` entry on the design-reference page.

## Implementation Plan
1. **`ui/uptime-strip.tsx`**: build the presentational strip + tooltips;
   register it on the design-reference page.
2. **`dashboard-page.tsx`**: add the hourly `useResults` query and a
   `groupByCheck` memo; replace `NeedsAttentionList` with `ChecksGlanceList`
   (ordering, rows, footer as in §1); gate `ActiveIncidentsList` on
   `incidentsCount > 0` and move it above the glance card; compact the banner.
3. **i18n**: add `dashboard.glance.*` strings (title, footer with count) to
   all locales; drop the now-unused `needsAttention.*` keys.
4. **E2E**: update `e2e/dashboard.spec.ts` per the Testing section.
5. Verify: `bun run lint`, `make test-dash`, manual pass on mobile + dark mode.
