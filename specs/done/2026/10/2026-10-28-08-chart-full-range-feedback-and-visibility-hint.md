---
model: sonnet
effort: medium
---

# The chart "Full range" switch often has no visible effect, and the status page visibility hint looks clickable

GitHub Issue: https://github.com/fclairamb/solidping-business/issues/34

## Source

A session-replay review flagged two small frustration signals in the dashboard:

1. Rage clicks on the "Full range" switch of the response-time chart, on a check detail
   page with the hour period selected. The report guesses at a missing loading state, or a
   double toggle caused by the `<label>` wrapping the switch.
2. Repeated clicks on the muted hint under the visibility field of the status page editor,
   with the "password" visibility selected. The report assumes the hint sits next to a toggle
   and suggests making it part of the label's click target.

Both guesses are partly wrong (see below); neither finding had a confirmed cause.

## Problem

### 1. Full-range switch

- No refetch happens: `fullRange` is purely client-side. `updateFullRange`
  (`web/dash0/src/components/checks/response-time-chart.tsx:478`) sets state and calls
  `onSettingsChange`. The query key of `useChartWindowResults` (`:524`) does not include it.
  Only the series build changes (`buildSeriesForRange`, `:251`), so a loading state cannot
  be what is missing.
- The double toggle is unlikely. The Radix `Switch` renders a `<button>`, and a click on an
  interactive descendant of a `<label>` triggers no label activation. Outside a `<form>`,
  Radix renders no hidden bubble input. A click on the label text is forwarded once to the
  button. A test should still pin this down (below).
- **Likely real cause:** the switch only pins the x-domain to the requested window
  (`:257-286`). When the check has results across the whole window (the common case with
  the hour period and a check running every minute), the data's own min/max already match
  the window bounds, so toggling changes nothing on screen. The user toggles, sees nothing,
  and toggles again.
- Aggravating factors: the switch is drawn at `scale-75` (`:992`), and its text label is
  `hidden sm:inline` (`:995`). Below `sm`, it is a tiny unlabeled switch with no tooltip, so
  the user cannot tell what it is meant to do.

### 2. Status page visibility hint

The hint is not next to a toggle. It is the `<p class="text-xs text-muted-foreground">`
under the visibility `Select` (`web/dash0/src/components/shared/status-page-form.tsx:356-362`).
With "password" selected, the password input appears right below it (`:370`). Nothing in the
hint is interactive, and nothing is broken. The clicks are probably the user looking for
where to set the password, or trying to reopen the select through its text.

## Why a spec

- A product/UX decision the reporter didn't settle: what feedback the full-range switch
  should give when it has no visible effect (disable it, explain it, or drop it for short
  periods), and whether the hint should become a click target at all.
- No confirmed root cause for either finding: a speculative "fix" would be a guess.

## Proposal

1. **Full-range switch, feedback.** Compute `fullRangeHasEffect`: true when the data's
   min/max differ from the window bounds by more than one expected interval (or when there
   are gaps at the edges). Then:
   - always show a tooltip on the switch explaining it ("Stretch the chart over the whole
     selected period, even where there is no data") (i18n in every `checks.json` locale);
   - when `fullRangeHasEffect` is false, keep the switch usable but show the tooltip with a
     second line ("The data already covers the whole period");
   - give the switch an `aria-label` so it stays labeled when the text is hidden below `sm`.
2. **Full-range switch, single toggle.** Add a Vitest/RTL test (or Playwright on the check
   detail page): one click on the switch, then one click on its label text, each flips
   `data-state` exactly once.
3. **Visibility hint.** Link the hint to the select with `aria-describedby` on the
   `SelectTrigger`, and give it an `id` and a `data-testid` (`status-page-visibility-hint`).
   With "password" selected, end the hint with a pointer to the field below ("Set it
   below."). Don't make the paragraph a click target (see Open questions).
4. Playwright: on the status page editor, select "password" and assert that the hint and the
   password input are both visible.

## Open questions

1. **Full-range switch when it has no visible effect: keep it enabled with an explanatory
   tooltip, or disable it?** Recommended: keep it enabled, with the tooltip. Disabling it
   would flicker on every refetch near the boundaries and hide a setting the URL persists
   (`graphFull`). The trade-off is that the switch still does "nothing" visible in that case.
2. **Make the visibility hint open the select on click?** Recommended: no. A paragraph that
   opens a dropdown is surprising, and Radix's select trigger does not open reliably from a
   forwarded label click. The trade-off is that a user who clicks the text still gets no
   response, only a clearer pointer.

## Closing the issue

This spec closes fclairamb/solidping-business#34. The implementing PR body **must** carry a
`Closes fclairamb/solidping-business#34` line. When the spec is archived to `specs/done/`,
verify the issue is closed and, if the merge did not close it, close it by hand:

    gh issue close 34 --repo fclairamb/solidping-business --comment "Implemented by <PR or commit>; spec: <archived spec path>"

## Resolved open questions

1. Keep the switch enabled with an explanatory tooltip (autopilot: recommended answer).
2. Do not make the hint open the select on click (autopilot: recommended answer).
