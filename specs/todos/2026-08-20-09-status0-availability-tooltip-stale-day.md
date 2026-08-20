---
model: sonnet
effort: high
---

# status0 availability-bar tooltip often keeps showing the previous day when hovering an adjacent segment

## Problem

On the public status page (`http://localhost:4000/status0/default/status`), hovering
one day segment of an availability bar and then moving the pointer to another day
often leaves the tooltip stuck on the *first* day: the tooltip stays open (e.g.
"18 août — 100.00% de disponibilité") while the pointer is clearly over a different
segment. It reads as stale data — the user thinks they're looking at day N's uptime
when they're actually seeing day N−1's.

The bar is rendered by
[availability-bar.tsx](web/status0/src/components/shared/availability-bar.tsx):
every segment is a narrow SVG `<rect>` wrapped in its **own** Radix `Tooltip` root
([availability-bar.tsx:124](web/status0/src/components/shared/availability-bar.tsx:124)),
all sharing the app-level `TooltipProvider delayDuration={300}`
([main.tsx:109](web/status0/src/main.tsx:109)).

Likely mechanism (to be confirmed, not assumed): Radix tooltips have *hoverable
content* enabled by default (`disableHoverableContent` is never set — neither on the
provider nor in
[tooltip.tsx](web/status0/src/components/ui/tooltip.tsx)). Hoverable content keeps a
tooltip open while the pointer travels through the "grace area" polygon between the
trigger and the content. Here the trigger is a segment only a few px wide, while the
content ("18 août — 100.00% de disponibilité") is ~10× wider and floats 6px above the
bar — so the grace polygon fans out over many neighboring segments. Moving the
pointer along the bar (especially near its top edge) keeps the old tooltip's grace
area satisfied, so the old tooltip never closes and the neighbor's never takes over.
That would also explain the "often" (it depends on pointer path and speed).

Two secondary suspects worth ruling out while in there:

- Each segment gets `hover:scale-y-[1.18]`
  ([availability-bar.tsx:140](web/status0/src/components/shared/availability-bar.tsx:140)) —
  vertical only, so it shouldn't steal the neighbor's hover, but verify.
- The provider's skip-delay behavior (moving between triggers while a tooltip is
  open re-opens instantly) interacting badly with per-segment roots.

Scope: status0 only — dash0 does not use this segment/tooltip pattern (no
`AvailabilityBar` / `segmentGeometry` usage under `web/dash0/src`). The same bar is
used for both daily and hourly (`bucketUnit="hour"`) views, so the fix covers both.

## Proposal

1. **Reproduce first.** Drive it with Playwright (status0 already has
   `web/status0/e2e/availability-bar.spec.ts`): hover segment *i*, assert the
   tooltip shows date *i*; then move the pointer along the bar to segment *i+3*
   (use intermediate `mouse.move` steps so the pointer travels *through* the grace
   area rather than teleporting) and assert the tooltip text now matches segment
   *i+3*. This should fail before the fix — it's the regression test.

2. **Fix, preferring the smallest change that survives the repro test:**
   - First candidate: `disableHoverableContent` on the shared `TooltipProvider`
     (or per-`Tooltip` in the availability bar). Nothing in status0 needs the
     pointer to enter tooltip content — the tooltips are read-only labels — so
     dropping hoverable content should be safe globally there.
   - If that isn't enough (or degrades feel), restructure to **one Tooltip per
     bar**: a single Radix root whose trigger is the `<svg>`, with a
     `pointermove` handler deriving the hovered segment from `x / geometry.pitch`
     and re-rendering the content for that segment (position the content via a
     virtual anchor or `alignOffset`). One open/close lifecycle means stale-day
     bugs become structurally impossible, and it removes ~90 Tooltip roots per
     page as a bonus.

3. Keep the hover scale-grow behavior and the existing tooltip look (dot +
   date + percentage) unchanged; the E2E assertions from step 1 plus the existing
   `availability-bar.spec.ts` cases must stay green.

Open question: if the root cause turns out to be something else entirely (e.g. an
SVG-trigger pointer-event quirk in Radix), document what was found in the commit and
pick the fix accordingly — the single-tooltip-per-bar structure remains the fallback
that sidesteps any per-segment-root misbehavior.

## Implementation Plan

1. **Confirm the mechanism by reading source, not just guessing.** Read
   `node_modules/@radix-ui/react-tooltip/dist/index.mjs` directly (installed
   version `^1.2.10`). Root cause is worse than "grace polygon stays satisfied":
   `TooltipContentHoverable` calls `providerContext.onPointerInTransitChange(true)`
   on `pointerleave` of the trigger, which sets a ref
   (`isPointerInTransitRef`) that lives on the **shared `TooltipProvider`
   context**, not on the individual `Tooltip` root. `TooltipTrigger`'s
   `onPointerMove` handler is a complete no-op whenever that shared ref is
   `true`:
   ```js
   onPointerMove: ... (event) => {
     if (!hasPointerMoveOpenedRef.current && !providerContext.isPointerInTransitRef.current) {
       context.onTriggerEnter();
       ...
     }
   }
   ```
   So while segment *i*'s grace polygon is active, segment *i+3*'s trigger
   never even calls `onTriggerEnter()` — it isn't that the new tooltip loses a
   race, it's that it's never asked to open. This matches "often" (depends on
   whether the pointer path stays inside the polygon long enough) and explains
   why the *old* content is what's stuck.
2. **Regression test first.** Extend
   `web/status0/e2e/availability-bar.spec.ts` with a new test: hover segment 2,
   assert tooltip text for that day; `mouse.move` in several intermediate
   steps along the top of the bar to segment 5; assert the tooltip now shows
   segment 5's text (and no longer segment 2's). Run it against the unfixed
   code and confirm it fails.
3. **Fix — scoped `disableHoverableContent` on the availability bar's
   `Tooltip` roots**, not the app-level `TooltipProvider`. Passing
   `disableHoverableContent` directly to each `<Tooltip>` in
   `availability-bar.tsx` makes `Tooltip.tsx`'s own
   `disableHoverableContent = disableHoverableContentProp ?? providerContext.disableHoverableContent`
   resolve to `true` for just those roots, so `TooltipContent` renders via
   plain `TooltipContentImpl` (no grace area, no `onPointerInTransitChange`
   call ever fires) while leaving `status-page-view.tsx`'s single status-dot
   tooltip and the app-level provider untouched. This is the smaller blast
   radius than flipping the provider default, and status0's own reasoning
   ("nothing needs the pointer to enter tooltip content") still applies to
   every Radix tooltip in status0, so either fix would be behaviorally safe —
   scoping it to the bar is just tighter.
4. **Secondary suspects**, checked on the record:
   - `hover:scale-y-[1.18]` — CSS-only, vertical transform in `fill-box`,
     doesn't touch pointer-event geometry or Radix state. Not implicated;
     confirmed by reading the class and by the fix working without touching
     it.
   - Provider skip-delay (`skipDelayDuration`) — orthogonal: it only affects
     whether the *next* open is instant or goes through `delayDuration`
     again. It does not gate whether `onTriggerEnter()` fires at all. Not the
     cause, though it does mean the fix still opens the new tooltip
     instantly once hoverable content stops suppressing it.
   - No SVG/pointer-events quirk found — `<rect>` triggers wrapped via
     `asChild` receive pointer events normally; the repro test confirms the
     mechanism is exactly the grace-area/transit-ref one above.
5. Run existing `availability-bar.spec.ts` cases (segment spacing tests) plus
   the new regression test, both daily (`bucketUnit` unset) and hourly
   (`bucketUnit="hour"`) — same component/props path, so one code change
   covers both.
6. QA gate: `make build-status0`, `bun run lint` in `web/status0`, full
   Playwright run for `availability-bar.spec.ts` against the already-running
   dev server on `:4000` (per repo `CLAUDE.md`: apply directly and hot-reload
   rather than standing up a side-car, since it's already up).
