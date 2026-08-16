---
model: opus
effort: high
---

# Form controls are the exact same color as the page, and the segmented control's active segment is the darker one

## Problem

### 1. Controls have zero surface contrast with the page

Every interactive form surface in dash0 renders `border border-input
bg-background` — the *same token* the page shell uses. `SidebarInset` is
`bg-background` (`web/dash0/src/components/ui/sidebar.tsx:304`) and
`--background` is `oklch(0.978 0.005 250)`
(`web/dash0/src/index.css:88`), so a control and the page behind it are
pixel-identical. The only thing separating them is a 1px `--input`
hairline at `oklch(0.91 0.009 250)`.

Affected primitives:

- `web/dash0/src/components/ui/input.tsx:10`
- `web/dash0/src/components/ui/textarea.tsx:11`
- `web/dash0/src/components/ui/code-textarea.tsx:24`
- `web/dash0/src/components/ui/select.tsx:19` (`SelectTrigger`)
- `web/dash0/src/components/ui/button.tsx:17` (`outline` variant)

Plus three components that hand-roll the identical class string instead of
using the primitives — they must be kept in sync by hand:

- `web/dash0/src/components/CommandMenu.tsx:291` (the ⌘K trigger)
- `web/dash0/src/components/shared/token-chips-input.tsx:132`
- `web/dash0/src/components/shared/label-input.tsx:199`

Meanwhile `--card` is pure white `oklch(1 0 0)`
(`web/dash0/src/index.css:92`). **Elevation is therefore inverted**: the
passive table/card is brighter than the interactive controls sitting above
it. The thing you cannot touch glows; the thing you type into does not.

The current state is also neither of the two coherent designs. Recessed
inputs (Linear/GitHub) require the control to be *visibly darker* than the
page; raised inputs (shadcn/Vercel) require it to be *visibly lighter*.
Identical-color-plus-a-hairline is the worst of both.

Note the naming trap for whoever fixes this: **`--input` is the border
color** (consumed as `border-input`), not a fill. Do not add
`--input-background` next to it.

### 2. Segmented controls render the active segment as the darker one

The Group by (Groups/Host) and jobs org-scope toggles use `variant="secondary"`
for the **selected** segment and `ghost` for the unselected one. In light mode
`--secondary` is `oklch(0.95 0.01 250)` — *darker* than the `0.978` page — while
`ghost` is transparent, i.e. the page color. So the active segment reads as a
recessed grey chip and the inactive one as the bright surface, backwards from
the iOS/shadcn convention of a raised white pill on a recessed track.

Three copies of the same markup:

- `web/dash0/src/routes/orgs/$org/checks.index.tsx:1446`
- `web/dash0/src/routes/orgs/$org/jobs.index.tsx:204`
- `web/dash0/src/routes/orgs/$org/design-reference.tsx:3083` — which documents
  it as *the* canonical pattern, so the catalog is currently teaching the bug.

### Notes from a prototype

A first pass has already been applied by hand (uncommitted, and repeatedly
wiped by a concurrent automation on the shared batch branch — assume none of it
survives): all 8 control surfaces swapped from `bg-background` to `bg-card`.

Two measured findings from that prototype that should shape the decision:

- **In light the effect is marginal.** `0.978 → 1.0` is a 2.2% lightness step.
  Any fix that leaves `--background` where it is has this as its ceiling.
- **In dark it is the larger change, and arguably the wrong direction.** Dark
  `--card` is `0.18` over a `0.14` page, so `bg-card` makes dark inputs
  *raised*, whereas recessed inputs are the convention in dark UIs (and are why
  dark mode does not currently look broken).

## Proposal

### A. Decide the control-surface model

Pick one and apply it to all 8 sites above. Options, in increasing order of
investment:

1. **`bg-card` everywhere.** Simplest; one token, no new indirection. Accepts a
   2.2% step in light and flips dark inputs to raised.
2. **`bg-card dark:bg-background`.** Keeps white inputs in light and leaves dark
   exactly as it is today. Cheap, and correct in both themes, but expresses the
   light/dark divergence as a utility override rather than as a token.
3. **A dedicated `--control` token** — light `oklch(1 0 0)`, dark
   `oklch(0.16 0.018 250)` (below `--card`, above `--background`) — wired
   through `@theme inline` as `--color-control: var(--control)` so `bg-control`
   exists. This is the only option that lets the two themes diverge
   *deliberately and by name*, and it makes the 8 sites impossible to confuse
   with the 3 legitimate `bg-background` users (the page shell at
   `sidebar.tsx:304`, the sidebar rail at `sidebar.tsx:447`, and the switch
   thumb at `switch.tsx:19`, which must NOT move).

   A token only earns its keep under this option — aliasing it to `var(--card)`
   would be indirection for nothing.

**Recommendation: option 3**, with option 2 as the acceptable cheap version.

Whichever is chosen, the three hand-rolled call sites should stop hand-rolling
and either use the primitive or the shared token.

### B. Optionally deepen the light ramp

Option A alone barely moves light mode. If the goal is for elevated surfaces to
actually read as surfaces, `--background` has to drop. A prototyped, coherent
re-spacing:

| token | now | proposed |
|---|---|---|
| `--background` | `0.978 0.005` | `0.945 0.008` |
| `--secondary` | `0.95 0.01` | `0.925 0.012` |
| `--muted` | `0.95 0.01` | `0.918 0.012` |
| `--accent` | `0.93 0.025` | `0.90 0.03` |
| `--border` / `--input` | `0.91 0.009` | `0.878 0.011` |

**Hard constraint: `--muted` must stay below `--background`**, because it is the
recessed track behind segmented controls and table headers (`bg-muted/30`). If
it rises above the page, those tracks invert.

This is a whole-app visual change and should be a deliberate call, not a side
effect of fixing the inputs. It can ship as a follow-up.

### C. Fix the segmented control (independent of A and B)

Track becomes the recessed surface, active segment becomes the raised pill:

- track: `inline-flex rounded-lg border bg-muted p-0.5 dark:bg-background`
- active segment: `variant="ghost"` plus `bg-card shadow-sm hover:bg-card`
- inactive segment: plain `variant="ghost"`

The track **must** flip token in dark: dark `--muted` (`0.22`) is *lighter* than
dark `--card` (`0.18`), so reusing `bg-muted` there would re-invert the pill.
The invariant to hold in both themes is *pill lighter than its track*.

Because the same markup is copy-pasted three times, prefer extracting a
`SegmentedControl` primitive into `web/dash0/src/components/ui/` and pointing
all three call sites (including the design reference) at it.

E2E is safe: `checks-index-host-view.spec.ts:89,136` and `jobs.spec.ts:99`
assert on `aria-pressed` and `data-testid` only, never on the variant classes.
Preserve both attributes.

### D. Update the design reference (mandatory)

Per `web/dash0/CLAUDE.md`, `web/dash0/src/routes/orgs/$org/design-reference.tsx`
is the canonical catalog and must be updated in the same change:

- Add `--control` (if option 3) to `COLOR_TOKENS` (~`:874`), and add `--input`
  with a description that says plainly it is a **border** color, so nobody
  reaches for `--input-background` later.
- Rewrite the segmented-control section (~`:3075`) so the documented pattern is
  the corrected one — selected = raised pill, track = recessed — and state the
  dark-mode token flip and the reason for it.

### Verification

- `bg-background` must remain on exactly three elements: page shell, sidebar
  rail, switch thumb. Grep to confirm nothing else regains it.
- Check the computed ordering in **both** themes, e.g. via
  `getComputedStyle`: page < control ≤ card in light, and track < pill in both.
- Dropping `--background` (option B) dulls the switch thumb, which is
  `bg-background` at `switch.tsx:19`; if B ships, repoint the thumb at
  `bg-card` so it stays white on its track.
- `bun run lint` on dash0 is red on base with pre-existing `react-hooks` errors
  (`CommandMenu.tsx:109,111,113`, `design-reference.tsx:2865`) — hold to
  *no new* errors rather than a clean run.

## Resolved open questions

Answered 2026-08-16. These are directives — implement them as written.

> ### A. Decide the control-surface model — Pick one and apply it to all 8
> sites above.

**Decision: option 3 — a dedicated `--control` token.** Light `oklch(1 0 0)`,
dark `oklch(0.16 0.018 250)` (below `--card`, above `--background`), wired
through `@theme inline` as `--color-control: var(--control)` so `bg-control`
exists. Apply it to all 8 control surfaces.

Do **not** alias `--control` to `var(--card)` — the spec is explicit that the
token only earns its keep by letting the two themes diverge deliberately and
by name. The three hand-rolled call sites (`CommandMenu.tsx:291`,
`token-chips-input.tsx:132`, `label-input.tsx:199`) must stop hand-rolling the
class string and use the primitive or the shared token.

> ### B. Optionally deepen the light ramp

**Decision: DEFER — do not ship §B in this change.** Implement §A, §C and §D
only. The re-spaced ramp is a whole-app visual change affecting every page and
the spec itself says it can ship as a follow-up; keeping it out keeps this
change reviewable.

Consequences of deferring, both of which apply:

- Light mode's improvement is capped at the 2.2% step the spec measures. That
  is accepted and is not a reason to reach for §B mid-implementation.
- The switch thumb (`switch.tsx:19`) stays `bg-background` and must **not** be
  repointed — that repoint is only needed if §B ships. Leave it alone, along
  with the page shell (`sidebar.tsx:304`) and the sidebar rail
  (`sidebar.tsx:447`). The verification step still applies: `bg-background`
  must remain on exactly those three elements.

§C (segmented control) and §D (design reference) are unchanged and remain in
scope, including extracting a `SegmentedControl` primitive and pointing all
three call sites at it.

## Implementation Plan

1. **Token (§A).** Add `--control` to `web/dash0/src/index.css`: light
   `oklch(1 0 0)` next to `--card`, dark `oklch(0.16 0.018 250)` in the `.dark`
   block (between `--background` 0.14 and `--card` 0.18). Expose it via
   `@theme inline` as `--color-control: var(--control)` so `bg-control` exists.
   No `--input-background`; `--input` stays the border color.
2. **Apply `bg-control` to the 5 primitives**: `input.tsx`, `textarea.tsx`,
   `code-textarea.tsx`, `select.tsx` (`SelectTrigger`), `button.tsx` (`outline`).
3. **De-hand-roll the 3 copies**: `label-input.tsx` switches to the `Input`
   primitive; `CommandMenu.tsx` (⌘K trigger) and `token-chips-input.tsx` are
   wrapper elements that no primitive fits, so they take `bg-control` directly.
4. **`SegmentedControl` primitive (§C)** in `web/dash0/src/components/ui/`:
   track `inline-flex rounded-lg border bg-muted p-0.5 dark:bg-background`,
   selected segment `variant="ghost"` + `bg-card shadow-sm hover:bg-card`,
   unselected plain `ghost`. Options carry `label`, optional `tooltip` and
   `data-testid`; `aria-pressed` and `data-testid` are preserved verbatim
   (E2E asserts on them). Point `checks.index.tsx`, `jobs.index.tsx` and
   `design-reference.tsx` at it.
5. **Design reference (§D)**: add `--control` and `--input` (documented as a
   *border* color) to `COLOR_TOKENS`; rewrite the segmented-toggle section to
   teach raised-pill-on-recessed-track and the dark `bg-muted → bg-background`
   flip and why (dark `--muted` 0.22 > dark `--card` 0.18).
6. **Verification**: Playwright spec reading `getComputedStyle` background in
   light and dark, asserting page < control ≤ card and track < pill; grep that
   `bg-background` left the 8 control sites; `make build-dash0` + dash0 lint.
7. **Out of scope**: §B light-ramp re-spacing is deferred; the switch thumb
   stays `bg-background`.
