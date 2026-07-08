# Status badges are uncolored in "Checks at a glance" and the check-details result box

## Problem

The dash0 UI has a single source of truth for status colors —
`web/dash0/src/lib/status-style.ts` — and a shared `StatusBadge` component
(`web/dash0/src/components/shared/status-badge.tsx`) that renders every
status with its proper color (green Up, amber Warning/Validating/Degraded,
red Down) and localized label. But two surfaces still render plain,
uncolored badges:

1. **Org dashboard "Checks at a glance" card**
   (e.g. `https://solidping.k8xp.com/dash0/orgs/webingenia`). The glance list
   in `web/dash0/src/components/dashboard/dashboard-page.tsx` uses a local
   `statusBadgeVariant()` helper (`dashboard-page.tsx:650-656`) that maps:
   - `down`/`error` → `destructive` (red — the only colored one)
   - `validating`/`timeout` → `secondary` (gray)
   - everything else, including `up` and `warning` → `outline` (gray)

   So "UP", "VALIDATING", and "WARNING" all read as neutral gray chips, and
   `timeout` (a hard-down status per `status-style.ts`) is gray instead of
   red. The badge is rendered at `dashboard-page.tsx:725-732`.

2. **Check-details chart pinned result box** (the "Duration / Region /
   Status … More details →" popup on the response-time chart). The Status
   row in `web/dash0/src/components/checks/pinned-result-box.tsx:143-148`
   renders `<Badge variant="outline">` with the raw status token —
   uncolored and unlocalized.

Both diverge from every other status surface (check list, check detail
header, timelines, status pages), which route through `StatusBadge` /
`statusStyle`.

## Solution

Replace the ad-hoc badges with the shared `StatusBadge` component:

- **`dashboard-page.tsx`**: in the glance list row, render
  `<StatusBadge status={status} />` instead of
  `<Badge variant={statusBadgeVariant(status)}>…</Badge>`, and delete the
  now-unused local `statusBadgeVariant()` helper. `StatusBadge` already
  handles the `validating` label localization the inline code duplicates
  (`dashboard-page.tsx:729-731`) and gives `timeout` the correct red.

- **`pinned-result-box.tsx`**: replace the Status-row
  `<Badge variant="outline" className="capitalize text-xs">` with
  `<StatusBadge status={data.status} className="text-xs" />` (keep the
  small text size to fit the compact box; drop `capitalize` since
  `StatusBadge` provides proper labels).

Also sweep the check-details page for any other uncolored status render —
e.g. the inline hover tooltip in
`web/dash0/src/components/checks/response-time-chart.tsx:875-902` prints
`{data.status}` as plain text when the status isn't `up`; if that turns out
to read as unstyled in practice, route it through `statusStyle` too.

Notes:
- No new colors or variants are needed — everything exists in
  `status-style.ts` / `badge.tsx`.
- Verify the glance card in both light and dark mode and on mobile.
- Check the design reference
  (`web/dash0/src/routes/orgs/$org/design-reference.tsx`) still reflects
  the canonical status-badge usage; no change expected there since
  `StatusBadge` is already the documented primitive.

## Implementation Plan

1. `web/dash0/src/components/dashboard/dashboard-page.tsx`:
   - Replace the glance-list row's
     `<Badge variant={statusBadgeVariant(status)} className="text-xs uppercase shrink-0">…</Badge>`
     with `<StatusBadge status={status} className="text-xs uppercase shrink-0" />`.
   - Delete the now-unused `statusBadgeVariant()` helper.
   - Drop the now-unused `Badge` import (no other usage in the file) and add
     the `StatusBadge` import.
2. `web/dash0/src/components/checks/pinned-result-box.tsx`:
   - Replace the Status row's `<Badge variant="outline" className="capitalize text-xs">{data.status}</Badge>`
     with `<StatusBadge status={data.status} className="text-xs" />`.
   - Keep the existing `Badge` import (still used for the `periodType` row).
3. `web/dash0/src/components/checks/response-time-chart.tsx`:
   - The single-series and multi-series tooltip renderers both print
     `{data.status}` as a hardcoded `text-red-500` `<p>` whenever status
     isn't `up` — wrong for warning/degraded/validating (amber) statuses,
     and unlocalized. Replace both occurrences with
     `<StatusBadge status={data.status} className="mt-1 text-xs" />`.
4. `web/dash0/src/routes/orgs/$org/design-reference.tsx`: confirm (no edit
   expected) that it already documents `StatusBadge` as the canonical
   primitive — it does (see the `StatusBadge` code sample and the check-list
   preview rows), so no update needed.
5. QA: `make fmt`, `make build-dash0`, `bun run lint` (no new errors vs.
   base), manual visual check in light/dark + mobile width via a dev
   server, and Playwright coverage if a natural extension point exists in
   `web/dash0/e2e/`.
