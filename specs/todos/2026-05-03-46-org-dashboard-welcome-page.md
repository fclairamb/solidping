# Org dashboard welcome page

## Context

Today, when an authenticated user lands on `/orgs/$org` they hit a redirect stub (`web/dash0/src/routes/orgs/$org/index.tsx`, 16 lines) that bounces them straight to `/orgs/$org/checks`. The sidebar's first nav item is "Dashboard" (`web/dash0/src/components/AppSidebar.tsx:45`) and points to the same redirect — so the most prominent navigation entry sets an expectation ("dashboard") and immediately breaks it ("…you mean the checks list"). That is the UX problem this spec fixes.

This spec replaces the redirect with a real operator-facing dashboard: the page that welcomes an admin back into their operational reality and answers, at a glance, *what's broken right now, what changed recently, and where do I jump.*

Related observations gathered while scoping:

- An orphan `StatusDashboard` component lives at `web/dash0/src/components/shared/status-dashboard.tsx` (287 lines, currently imported nowhere). It was built when dash0 was a simpler public status page. See "Honest opinion" #5 — do not reuse it.
- `web/dash0/CLAUDE.md` is stale — it still describes dash0 as a "public read-only status page" but the routes show a full multi-tenant authenticated admin app. Refresh in the same PR.
- No backend aggregate / overview / stats endpoint exists. v1 composes from existing list endpoints client-side.

## Honest opinion

1. **Yes, do this.** A "Dashboard" sidebar item that redirects to `/checks` is worse than no item at all — it wastes the most valuable real estate in the nav. Build a real page or remove the item; building it is the right call.
2. **Operator view, not subscriber view.** "Welcome" means "the page that welcomes you back into your operational reality" — what's broken, what changed, where to jump. It must NOT become a marketing splash or duplicate the public status page (`/status-pages/...` already covers subscribers).
3. **Keep v1 deliberately small.** Five sections max: header, overall status banner, 4 KPI tiles, two-column body (needs-attention + active-incidents), recent activity. No charts, no time-series, no customisable widgets. Anything more is feature creep before validation.
4. **No new backend endpoint for v1.** Compose from existing `/checks`, `/incidents`, `/results`, `/events` list endpoints with `?size=1000` and a `TODO(perf)` comment. A `/orgs/$org/dashboard` aggregate is 1–2 days of premature optimisation for an audience size we don't yet know. Add it when an org actually crosses ~1000 checks.
5. **Don't salvage the orphan `status-dashboard.tsx`.** It uses raw `fetch()` (bypassing the auth wrapper), defines its own divergent `Check` type, and renders a subscriber-style timeline grid. Reusing it would create coupling and type drift for marginal LOC savings. Leave it for one PR cycle, then delete in a follow-up cleanup PR.

## Scope

**In scope**
- Replace the `/orgs/$org` redirect with a real `OrgDashboardPage` component.
- Compose data from existing list endpoints; no backend changes.
- Empty-state ("welcome to a fresh org") and populated-state behaviour.
- Per-card error boundaries so one failed query doesn't blank the page.
- New i18n namespace for dashboard strings (en/fr/de/es).
- Refresh the stale `web/dash0/CLAUDE.md` framing (note in PR; not blocker).

**Out of scope** (see "Out of scope for v1" below for the full list)

## Page structure (top → bottom)

Wrap the page in `<div className="space-y-6">` inside the existing `OrgLayout > SidebarInset > p-4` container — same shell as `checks.index.tsx` and `incidents.index.tsx`.

### 1. Header row
`<div className="flex items-center justify-between">`:
- Left: `<h1 className="text-3xl font-bold tracking-tight">{orgName}</h1>` (org name from `useAuth().organizations`, mirroring `AppSidebar.tsx:132`) plus a muted `<p>` subtitle from i18n (e.g. `t("subtitle")`).
- Right: a small `RefreshCw` icon-only button that invalidates the dashboard queries (copy the affordance pattern from `incidents.index.tsx:136-145`), with a "Updated 12s ago" relative-time label next to it driven by the freshest `dataUpdatedAt` of the five queries.

### 2. Overall status banner
A single `<Card>` with `border-2` whose tint reflects the worst aggregate state. Three mutually exclusive states:

- **Empty org** (`checks.length === 0`): blue/muted card, `Plus` icon, `"Welcome to SolidPing"` headline, muted subline ("Create your first check to start monitoring"), `<Button asChild>` linking to `/orgs/$org/checks/new`. **Sections 3–5 hide entirely.**
- **All green** (no checks down, no active incidents): `bg-green-50 dark:bg-green-950 border-green-200 dark:border-green-800`, `CheckCircle`, copy "All systems operational — N checks monitored, M% uptime over 24h".
- **Issues**: `bg-red-50 dark:bg-red-950 border-red-200 dark:border-red-800` if any check status ∈ {`down`,`error`}; otherwise `bg-yellow-50 dark:bg-yellow-950 border-yellow-200 dark:border-yellow-800` if only `timeout`. Copy: "3 checks down, 2 active incidents".

The colour/copy logic is lifted in spirit from the orphan `OverallStatus` (`status-dashboard.tsx:165-198`) but rewritten to consume already-derived counts from sibling queries rather than refetching.

### 3. KPI tiles
`<div className="grid gap-4 md:grid-cols-2 lg:grid-cols-4">` — four small `<Card>`s, each with `CardHeader` (label + Lucide icon) and `CardContent` (large number + sub-line). Pattern reference: `web/dash0/src/components/checks/check-summary-cards.tsx`.

1. **Monitored checks** — count where `enabled === true`. Sub-line: "X disabled" if any disabled exist, else nothing.
2. **Currently down** — count where `lastResult.status` ∈ {`down`,`error`}. Tile turns red (`text-red-600`) when > 0.
3. **Active incidents** — total from `useIncidents({state:"active"})`. Tile turns yellow when > 0.
4. **24h availability** — weighted mean of `availabilityPct` across checks for the last 24 h, from `useResults({periodType:"day", periodStartAfter:<24h ago>})`. Render `—` if no aggregated data is available yet.

### 4. Two-column body
`<div className="grid gap-6 lg:grid-cols-2">`:

**4a. Needs attention** (left) — `<Card>` titled "Needs attention". Top 5 checks where `lastResult.status !== "up"`, sorted by `lastStatusChange.time` desc. Each row: `StatusIcon` + check name + status badge + relative duration "since 14m ago". Each row is a `<Link>` to `/orgs/$org/checks/$checkUid`. Card footer: "View all checks →" link to `/orgs/$org/checks`. Empty state inside the card: a small green `CheckCircle` + "Everything is up".

**4b. Active incidents** (right) — `<Card>` titled "Active incidents". Top 5 from `useIncidents({state:"active", size:5, with:"check"})`. Reuse the row shape from `incidents.index.tsx` (title, check link, started time, live duration counter). Footer link: "View all incidents →" → `/orgs/$org/incidents?state=active`. Empty state: green `CheckCircle` + "No active incidents".

### 5. Recent activity
`<Card>` titled "Recent activity". Last 8 from `useEvents({size: 8})`. Reuse `getEventIcon` and `getEventLabel` extracted from `events.tsx:50-68` into a new shared `event-display.tsx` (so the dashboard doesn't depend on the events route file). Card footer: "View all events →" → `/orgs/$org/events`.

## Data fetching strategy

Five concurrent `useQuery` calls, all keyed by `org`. Each section has its own loading skeleton (mirror `incidents.index.tsx:151-155`) and its own inline error state. Render the page shell immediately so layout doesn't pop.

| Hook | Options | Polling |
|------|---------|---------|
| `useChecks(org, ...)` | `{ with: "last_result,last_status_change", limit: 1000 }` | 30 s |
| `useIncidents(org, ...)` | `{ state: "active", size: 5, with: "check" }` | 30 s |
| `useResults(org, ...)` | `{ periodType: "day", periodStartAfter: <ISO 24h ago>, size: 1000 }` | 60 s |
| `useEvents(org, ...)` | `{ size: 8 }` | 60 s (requires adding `refetchInterval?` to `useEvents` in `hooks.ts:663`) |

**Per-card error boundaries.** Wrap each section independently — if incidents fail but checks succeed, the dashboard still shows checks. Use `<QueryErrorView />` only if all five queries fail simultaneously; otherwise show inline "Failed to load (retry)" inside the affected card. Reasoning: this dashboard composes 5 unrelated queries; one failing endpoint shouldn't blank the whole page.

**Counts from paginated checks.** `?limit=1000` is the cheapest correct path for v1. Add a top-of-file comment in `dashboard-page.tsx`:

```
// TODO(perf): switch to GET /api/v1/orgs/{org}/dashboard once a backend
// aggregate endpoint exists. For orgs > 1000 checks this fetches the full
// list just to count — fine for now (typical org has <100 checks).
```

**Refresh button** invalidates the four `react-query` keys; relative-time label uses the freshest `dataUpdatedAt` across them.

## Files to add / change

**MODIFY**
- `web/dash0/src/routes/orgs/$org/index.tsx` — swap `beforeLoad: redirect(...)` for `component: OrgDashboardPage`. Keep the `?access_token=` early-return guard exactly as-is (as a `beforeLoad` returning nothing) so OAuth still flows to `OrgLayout`.
- `web/dash0/src/api/hooks.ts` — add `refetchInterval?: number` option to `useEvents` (one line, matches the `useIncidents` pattern).
- `web/dash0/src/routes/orgs/$org/events.tsx` — import `getEventIcon` / `getEventLabel` from the new shared file instead of defining inline.
- `web/dash0/src/locales/{en,fr,de,es}/dashboard.json` — new i18n namespace (registered in i18n init).
- `web/dash0/CLAUDE.md` — refresh the stale "public read-only status page" framing to reflect the multi-tenant operator app reality. Note in PR description; not a release blocker.

**NEW**
- `web/dash0/src/components/dashboard/dashboard-page.tsx` (~200 LOC) — `OrgDashboardPage` with all 5 sections inlined as small internal components: `OverallStatusBanner`, `KpiTile`, `NeedsAttentionList`, `ActiveIncidentsList`, `RecentActivityList`.
- `web/dash0/src/components/dashboard/event-display.tsx` (~30 LOC) — extracted `getEventIcon` and `getEventLabel`, consumed by both the dashboard and the events route.

## Out of scope for v1

- Time-series charts (response time, availability over 7 d / 30 d).
- Worker / region health (no endpoint exposed to users).
- Customisable widgets / drag-to-reorder.
- Date-range selector — the dashboard is fixed to "now" + "24 h".
- Cross-org overview for super-admins (they pick an org via the switcher).
- Activity feed pagination — last 8 only, link to `/events` for more.
- A new `/orgs/$org/dashboard` aggregate endpoint (TODO comment only; build it once an org actually outgrows the `?limit=1000` ceiling).
- Deleting `status-dashboard.tsx` (do it in a follow-up cleanup PR).
- `make build-dash0` / embed pipeline changes (the route addition is automatic).

## Verification plan (`make dev-test`)

The `tools/test-data` generator covers most states; reuse it.

- **Empty state**: create a fresh org via the UI ("New organization") — should land on the dashboard and render only the welcome card + "Create your first check" CTA. Sections 3–5 must not render at all (not even skeletons).
- **All green**: in an existing test org with all checks up, verify the green banner, populated KPIs (down=0, incidents=0), "Everything is up" + "No active incidents" affirmations, and a populated recent-activity feed.
- **Mixed state**: seed via `tools/test-data` (or `/orgs/$org/test/generate`) — checks with `down` results plus an active incident. Verify red KPI tile for "down", "Needs attention" ordered by most-recent `lastStatusChange.time`, the active-incidents card shows the live duration counter ticking each second, and the overall banner is red.
- **Backend down**: `pkill solidping`. Each card should show its own inline "Failed to load (retry)" independently. Restart, click retry on one card — only that card repopulates.
- **Multi-org isolation**: switch orgs via the user menu. Dashboard must fully refetch (queries are keyed by `org`).
- **i18n**: toggle to French via `LanguageSwitcher`. All dashboard strings must translate (header, KPI labels, empty-state copy, footer links).
- **OAuth callback regression**: log out → "Sign in with Google" → callback URL `/orgs/{org}?access_token=...` must still hit `OrgLayout`'s OAuth handler (token consumed, user landed). The `beforeLoad` early-return preserves this. Also test the same flow for the legacy `/orgs/{org}/?access_token=...` (trailing slash) path if applicable.

## Implementation order

Ship incrementally — each step leaves the app in a working state.

1. Extract `event-display.tsx`; refactor `events.tsx` to consume it; verify the events page is visually unchanged.
2. Add the `refetchInterval?` option to `useEvents` in `hooks.ts`.
3. Add the `dashboard.json` i18n namespace + register it in i18n init; provide all 4 locales.
4. Build `dashboard-page.tsx` skeleton with mock/in-memory data; verify the layout in light + dark mode and at mobile + desktop widths before wiring queries.
5. Wire in real React Query hooks; verify each section's loading / error / data states independently.
6. Swap `index.tsx` redirect for `component: OrgDashboardPage`; verify OAuth callback still works via the early-return guard.
7. Run all `make dev-test` verification scenarios above.
8. Refresh `web/dash0/CLAUDE.md` to drop the stale "public read-only" framing.

## Critical files (read these first)

- `web/dash0/src/routes/orgs/$org/index.tsx` — current redirect stub being replaced.
- `web/dash0/src/components/AppSidebar.tsx:45` — sidebar "Dashboard" item that points here.
- `web/dash0/src/components/shared/status-dashboard.tsx` — orphan reference; **do NOT reuse** (see Honest opinion #5).
- `web/dash0/src/routes/orgs/$org/incidents.index.tsx` — layout pattern + refresh affordance to mirror.
- `web/dash0/src/routes/orgs/$org/events.tsx` — `getEventIcon` / `getEventLabel` to extract.
- `web/dash0/src/api/hooks.ts` — React Query hook conventions and the `useEvents` signature to extend.
- `web/dash0/src/components/checks/check-summary-cards.tsx` — KPI tile pattern reference.
