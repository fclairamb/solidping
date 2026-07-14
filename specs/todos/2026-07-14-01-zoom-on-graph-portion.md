---
model: opus
effort: high
---

# Allow zooming into a selected portion of a response-time graph

## Problem

The response-time / results charts in dash0 show the full time range only. There
is no way to focus on a shorter window of interest — you cannot select a section
of the chart to zoom into it. Investigating a spike or a short outage means
squinting at a compressed full-range view.

Source issue: [#125 — Zoom on a graph portion](https://github.com/fclairamb/solidping/issues/125).
The issue attaches a screenshot of a response-time chart and asks for the ability
to **select a section from the top or from the bottom** of the graph to zoom into
that portion.

The main chart lives in the check detail route
[`web/dash0/src/routes/orgs/$org/checks.$checkUid.index.tsx`](web/dash0/src/routes/orgs/$org/checks.$checkUid.index.tsx)
(there is already a `graphFull` search param that expands the chart), with
related per-region chart components under `web/dash0/src/components/`.

## Proposal

Add drag-to-select **X-axis (time) zoom** on the response-time chart, driven by
the URL so the zoomed view is a shareable link that fetches only the selected
window:

1. **X (time) axis only.** Let the user click-drag a horizontal range across the
   chart to select a time sub-window; on release, the view zooms to
   `[start, end]` on the time axis. The Y (latency) axis always auto-scales to
   the data in that window — there is no Y-axis / latency-band zoom. Recharts
   supports the drag via a `<ReferenceArea>` driven by
   `onMouseDown` / `onMouseMove` / `onMouseUp` handlers that record the selected
   X bounds only.
2. **The zoom window lives in the URL and drives the fetch.** On release, write
   the selected `[start, end]` to URL search params (e.g. `from` / `to`, as
   epoch-ms or ISO), following the repo's URL-search-state convention (seed local
   state from the URL on mount + write-through — see
   [reference_dash0_url_search_state] and existing route patterns like
   `incidents.index.tsx` / `jobs.index.tsx`). The results query **must key off
   these params and request only that window from the server**, not re-scale an
   already-loaded full-range series client-side. The results endpoint already
   supports a time-window filter — `periodStartAfter` / `periodEndBefore`
   (RFC3339), handled in
   [`results/handler.go`](server/internal/handlers/results/handler.go:63) and
   already exposed by the dash0 results hook (`periodStartAfter` /
   `periodEndBefore` in `web/dash0/src/api/hooks.ts`) — so the zoom fetch **maps
   the URL `from`/`to` onto those existing params**; no new backend filter is
   needed. A shared link therefore fetches just the zoomed portion, and picks up
   higher-resolution (less-aggregated) rows for the narrower range for free.
   Absent params → full default range, as today.
3. **The selected element is part of the URL too.** When a user selects a point /
   region on the chart (the currently highlighted result and its details),
   persist that selection in the URL (e.g. a `selected` / `resultUid` param)
   alongside the zoom window. Deep-linking to that URL must re-open the view with
   that element shown as selected and its detail panel/tooltip populated — so the
   shared link reproduces both the zoomed window **and** the highlighted element.
4. **Reset.** Provide an obvious way to clear the zoom back to the full range (a
   "Reset zoom" button that appears while zoomed, and/or double-click to reset),
   which clears the `from`/`to` (and `selected`) params from the URL.
5. **Touch.** Ensure the interaction works on touch as well (CLAUDE.md: all pages
   must be fully usable on mobile) — support touch drag or offer an equivalent
   control.
6. **Reuse primitives.** Reuse existing chart primitives; check the design
   reference (`http://localhost:4000/dash0/orgs/default/design-reference`) before
   adding any new UI, and add any new primitive there if introduced.

## Notes

- The check detail route already has a `graphFull` search param; the new `from` /
  `to` / `selected` params sit alongside it in the same `validateSearch`. Make
  sure a cold deep-link (no prior client state) reconstructs the zoom + selection
  from the URL — [reference_dash0_url_search_state] documents that a plain
  `validateSearch` under a layout route can drop on cold deep-link, so seed from
  the URL on mount and write through.
- The narrowed fetch reuses the existing `periodStartAfter` / `periodEndBefore`
  results filter (see Proposal §2) — no new backend endpoint or query param is
  required; the work is wiring the URL window onto those params.
