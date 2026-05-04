# Response-time chart — first click previews, second click opens

## Context

On the check detail page
`/dash0/orgs/$org/checks/$checkUid` (e.g.
`https://solidping.k8xp.com/dash0/orgs/test/checks/d37c271a-069c-…`),
the response-time chart renders a small green dot per result. Today
the interaction is:

- **Hover** a dot → a Recharts tooltip appears next to the cursor with
  `MMM d, HH:mm:ss`, the duration in `ms`, the down-status (if any),
  and the hint **"Click point for details"**.
- **Click** a dot → immediate navigation to
  `/orgs/$org/checks/$checkUid/results/$resultUid` (full-page result
  detail with cards for Period / Availability / Metrics / Output).

The single click → full-page jump is too eager. Most of the time the
user just wants to read the headline numbers for that point — period,
region, status, min/max if aggregate, maybe one or two metrics — and
then keep scanning the chart. Bouncing to a separate route for that
breaks the flow and forces a back-button round-trip.

The user also has no way to **compare two points** without two
navigations + back presses, which is precisely what a chart is for.

## Desired behavior

Two-step interaction on the same dot:

1. **First click** on a dot → the point becomes **selected** (visually
   highlighted, slightly larger). A persistent info box appears,
   anchored to the dot, that loads the detail payload from
   `GET /api/v1/orgs/$org/checks/$checkUid/results/$resultUid` and
   renders a compact preview (period, region, status, duration plus
   min/max for aggregates, top metrics — see "Box content" below).
   The bottom hint changes from "Click point for details" to **"Click
   again to open full page"**.
2. **Second click on the same selected dot** → navigate to the full
   result detail page (current behavior).
3. **Click a different dot** → that becomes the new selected dot
   (replace the box content; do not navigate).
4. **Click outside any dot** (chart background) or the box's ✕ button
   → clear the selection.
5. **Hover** behavior is unchanged for non-selected dots: the
   ephemeral Recharts tooltip still appears next to the cursor. While
   a dot is selected, suppress the hover tooltip for that specific
   dot (the pinned box already shows its data and the duplicate
   would jitter).

## Honest opinion

1. **Anchor the box to the dot, don't put it in a strip below the
   chart.** The user's request shows the existing hover tooltip and
   asks for *that* box to load info — pinning the same visual
   element matches their mental model. A separate slot below the
   chart would also work but loses the spatial connection between
   the box and the dot, which matters when comparing points
   visually.
2. **Don't reuse the Recharts `<Tooltip>` for the pinned state.**
   Recharts tooltips are cursor-driven; trying to pin one fights the
   library. Render a separate absolutely-positioned `<div>` inside a
   `position: relative` wrapper around `<ResponsiveContainer>`, and
   compute its anchor from the dot's `cx`/`cy` (already passed to
   the dot render callback). Keep the hover `<Tooltip>` as-is for
   the ephemeral case.
3. **Reuse `useResult` — do not invent a new endpoint.** The hook
   already exists (`web/dash0/src/api/hooks.ts` L489–499), is
   `staleTime: Infinity`, and returns `OrgResultDetail`. The full
   detail page (`checks.$checkUid.results.$resultUid.tsx`) renders
   the same shape — borrow its formatters (`formatPercent`,
   `formatMetric`, `formatDate`, `statusBadgeClass`) but render a
   condensed subset. No backend work.
4. **The hardcoded "Click point for details" string at
   `response-time-chart.tsx:512` is not in i18n** even though every
   other user-visible string in dash0 is. Fix this drive-by while
   touching the line — add the new states (`Click again to open`,
   `Loading details…`, close-button label) to the same `checks.json`
   namespace at the same time.
5. **Don't try to "merge" the pinned box into the existing Recharts
   tooltip render path.** The Recharts tooltip is positioned by the
   library against the cursor; the pinned box is positioned by us
   against the dot. They are different beasts — keep them separate
   and let the pinned box win when both would point at the same
   `uid`.

## Scope

**In**

- Two-step click on response-time chart dots (preview → navigate),
  scoped to `web/dash0`'s `ResponseTimeChart`.
- New `PinnedResultBox` component (sibling of `<ResponsiveContainer>`),
  positioned via `cx`/`cy` captured at click time and refreshed
  when the same `uid` re-renders.
- Hover-tooltip suppression for the currently selected dot.
- Visual highlight on the selected dot (radius `5`, distinct stroke).
- ✕ close button + click-outside-to-dismiss.
- i18n-ify the existing hint string and the four new strings.
- Playwright e2e covering: click → box appears with details, click
  again → navigates, click another dot → box swaps, ✕ closes.

**Out**

- The full-page result detail route — untouched.
- Backend changes — none. `useResult` hits an existing endpoint.
- Status0 / public status pages — not affected.
- Mobile-touch ergonomics beyond what comes for free (tap-to-pin
  works the same as click-to-pin; no double-tap special handling).
  If two-finger gestures or long-press are wanted later, that's a
  follow-up spec.
- Keyboard navigation across dots (Recharts doesn't expose this and
  it's a separate accessibility concern).

## Per-element changes

### 1. `web/dash0/src/components/checks/response-time-chart.tsx`

#### 1a. New state and refs (top of `ResponseTimeChart`)

```tsx
const [selectedUid, setSelectedUid] = useState<string | null>(null);
const dotPositions = useRef<Record<string, { cx: number; cy: number }>>({});
const [, forcePositionTick] = useReducer((n: number) => n + 1, 0);
```

`dotPositions` is written from the dot render callback (no setState
during render); `forcePositionTick` is bumped from a layout effect
when the chart resizes so the pinned box recomputes its anchor.

#### 1b. Dot render callback (L546–586)

- Replace the inline `onClick={() => navigate(...)}` with:
  ```tsx
  onClick={() => {
    if (!payload.uid) return;
    if (selectedUid === payload.uid) {
      navigate({
        to: "/orgs/$org/checks/$checkUid/results/$resultUid",
        params: { org, checkUid, resultUid: payload.uid },
      });
      return;
    }
    setSelectedUid(payload.uid);
  }}
  ```
- After the dot is rendered, write its `(cx, cy)` into
  `dotPositions.current[payload.uid] = { cx, cy }` (do this in a
  `useLayoutEffect` keyed on data — *not* inside the render
  callback's render path).
- When `payload.uid === selectedUid`, render the dot with `r={5}`
  and an additional `stroke="var(--primary)" strokeWidth={2}` so
  the user can see what's pinned.

#### 1c. Hover tooltip (L492–517)

- Suppress the tooltip when the hovered point's `uid` equals
  `selectedUid` (return `null`):
  ```tsx
  if (data.uid && data.uid === selectedUid) return null;
  ```
- Move the **`Click point for details`** string to i18n
  (`t("checks:chart.tooltipHint")`, see §3 below). Same string, same
  position — only the source changes.

#### 1d. New `PinnedResultBox` (rendered as a sibling of
`<ResponsiveContainer>` inside a `position: relative` wrapper)

```tsx
{selectedUid && (
  <PinnedResultBox
    org={org}
    checkUid={checkUid}
    resultUid={selectedUid}
    anchor={dotPositions.current[selectedUid]}
    onClose={() => setSelectedUid(null)}
    onOpen={() =>
      navigate({
        to: "/orgs/$org/checks/$checkUid/results/$resultUid",
        params: { org, checkUid, resultUid: selectedUid },
      })
    }
  />
)}
```

- Position: `position: absolute`, `left: anchor.cx`, `top: anchor.cy`,
  `transform: translate(-50%, calc(-100% - 12px))` so it floats
  above the dot like the hover tooltip does today. Clamp to the
  chart container's width (left/right edge cases) with a small CSS
  guard or a simple `Math.min(Math.max(cx, MARGIN), width - MARGIN)`.
- If `anchor` is missing (dot not rendered yet, e.g. data refresh
  in flight), render the box pinned to the chart's top-right corner
  rather than disappearing — losing the box on every poll would be
  worse than a small position jump.

#### 1e. Click-outside dismiss

- Wrap the chart in a `<div>` with `onClick` that, when the click
  target is the chart area but not a dot, calls
  `setSelectedUid(null)`. The dot's `onClick` already calls
  `e.stopPropagation()` (add it if not).

### 2. New file
`web/dash0/src/components/checks/pinned-result-box.tsx`

A small component that:

- Calls `useResult(org, checkUid, resultUid)`.
- While loading, renders a 3-line `Skeleton` block inside the same
  outer box shell.
- On error, renders a single line "Could not load details" + the
  ✕ button.
- On success, renders the **box content** below.
- Outer shell: same look as the current hover tooltip — `rounded-md
  border bg-popover p-2 text-sm shadow-md` plus a header row with
  the timestamp on the left and a small ✕ icon button on the right.
- A footer row with the hint **"Click again to open full page"** in
  `text-xs text-muted-foreground` (mirrors today's hint position).

#### Box content (success state)

| Field                | Source                                  | Render |
|----------------------|-----------------------------------------|--------|
| Date / time          | `data.periodStart` (or `data.ts`)       | `MMM d, HH:mm:ss` |
| Duration             | `data.durationMs`                        | `formatMs` |
| Min / Max (aggregate)| `data.durationMinMs`/`data.durationMaxMs` | `min … / max …` line, only if aggregate |
| Status               | `data.status`                            | `Badge` reusing `statusBadgeClass` (extract to shared util) |
| Region               | `data.region`                            | small `region: us-east-1` line if present |
| Period type          | `data.periodType` if not `raw`           | small `Badge variant="outline"` |
| Availability %       | `data.availabilityPct` if aggregate      | `formatPercent` |
| Top 2 metrics        | first 2 entries of `data.metrics`        | `key: value` lines, with `formatMetric` |

Output JSON is **not** rendered here — it can be very large; the
full page is for that.

Extract `formatPercent`, `formatMetric`, `formatDate`,
`statusBadgeClass` from `checks.$checkUid.results.$resultUid.tsx`
(L22–60) into a small shared module
`web/dash0/src/lib/result-format.ts` and import them from both
the full page and the pinned box. Avoids duplication.

### 3. i18n — `web/dash0/src/locales/{en,fr,de,es}/checks.json`

Add a `chart` block (sibling of the existing `resultDetail`):

```json
"chart": {
  "tooltipHint": "Click point for details",
  "pinnedHint": "Click again to open full page",
  "loadingDetails": "Loading details…",
  "loadError": "Could not load details",
  "close": "Close"
}
```

Translate to fr / de / es using the same tone as the existing
`checks` namespace strings.

The existing `resultDetail.*` keys (period, region, duration,
durationMin, durationMax, totalChecks, successfulChecks,
availability, metrics) are reused by `PinnedResultBox` for its
field labels — no new keys needed for those.

### 4. Tooltip text source — line `response-time-chart.tsx:512`

Replace the literal `"Click point for details"` with
`{t("checks:chart.tooltipHint")}`. Add `useTranslation(["checks"])`
to `ResponseTimeChart` (it does not currently call it).

## Files to modify / create

- `web/dash0/src/components/checks/response-time-chart.tsx` — modify
  (state, click handler, tooltip suppression, dot position capture,
  selected-dot styling, render `PinnedResultBox`).
- `web/dash0/src/components/checks/pinned-result-box.tsx` — **new**.
- `web/dash0/src/lib/result-format.ts` — **new** (extract shared
  formatters).
- `web/dash0/src/routes/orgs/$org/checks.$checkUid.results.$resultUid.tsx`
  — replace the local `formatPercent`/`formatMetric`/`formatDate`/
  `statusBadgeClass` with imports from `result-format.ts` (no
  behavior change).
- `web/dash0/src/locales/en/checks.json` (+ `fr` / `de` / `es`) —
  add the `chart.*` keys.

## Verification

### Manual smoke

1. `make dev-test`. Login as test user.
2. Navigate to a check with at least 20 results
   (e.g. `/dash0/orgs/test/checks/<uid>` from the screenshot).
3. **Hover** a dot → ephemeral tooltip with date / ms / "Click point
   for details" appears.
4. **Click** the dot →
   - Dot enlarges and gains the primary stroke.
   - The pinned box appears anchored above the dot with a brief
     "Loading details…" then the populated detail rows.
   - Hint reads "Click again to open full page".
   - The ephemeral hover tooltip no longer fires for this dot.
5. **Click the same dot again** → navigates to
   `/orgs/$org/checks/$checkUid/results/$resultUid`. Back-button
   returns to the chart with no pinned point (selection state lives
   in the chart only).
6. **Click another dot** → pinned box swaps content; old dot
   un-highlights.
7. **Click chart background** → selection cleared; no box.
8. **Click the box's ✕** → selection cleared; no box.
9. **Aggregate dot** (switch the chart period to "month" → some
   points are aggregates) → box shows min/max + availability% +
   period-type badge.
10. **Resize the window** while a point is pinned → box stays
    anchored to the same dot (does not jump to wrong coordinates).

### Playwright e2e

Add `web/dash0/e2e/check-chart-point-preview.spec.ts`:

- Login as test, create a check, wait until at least 2 results land
  (use the existing fixture / poll pattern from
  `web/dash0/e2e/availability.spec.ts`).
- Visit the check detail page.
- `await page.locator('circle[r="3.5"]').nth(0).click();`
- Assert: box visible (`getByText(/loading details|response/i)` then
  await populated content), URL **unchanged**.
- Click the same dot again → assert URL changed to
  `**/results/**`.
- Go back, click the second dot → box content swaps; URL still
  unchanged. Press ✕ → box gone.

### API (no change)

`useResult` already returns `OrgResultDetail` and is the single
source for both the full page and the new box — no curl smoke
needed; covered by the e2e.

## Implementation plan

1. Extract formatters into `web/dash0/src/lib/result-format.ts`;
   update `checks.$checkUid.results.$resultUid.tsx` to import
   them. Run `make build-dash0` — should be a no-op behaviorally.
2. Add the `chart.*` i18n keys (en + fr + de + es).
3. Build `PinnedResultBox` in isolation with hard-coded props,
   verify the layout under both raw and aggregate result shapes.
4. Wire it into `ResponseTimeChart`: state, click handler,
   tooltip suppression, dot styling, layout-effect for position
   capture, click-outside-to-dismiss.
5. Manual smoke per §Verification.
6. Add the Playwright e2e.
7. `make lint-dash` + `make build-dash0` clean.
8. Commit, archive the spec under `specs/done/2026/05/`, ship.

## Critical files

- `web/dash0/src/components/checks/response-time-chart.tsx`
  L1–80 (imports + props), L492–517 (hover tooltip), L538–588
  (Area + dot render + onClick).
- `web/dash0/src/api/hooks.ts` L489–499 — `useResult` (reused
  as-is).
- `web/dash0/src/routes/orgs/$org/checks.$checkUid.results.$resultUid.tsx`
  L22–60 — formatters to extract; L100–272 — full page that the
  box's content mirrors in condensed form.
- `web/dash0/src/locales/en/checks.json` — `resultDetail.*` keys
  the box reuses for field labels.
