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
