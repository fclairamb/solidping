---
model: opus
effort: high
---

# The "Getting started" card looks flat and dated — give it a modern, flashy treatment

## Problem

The dashboard's getting-started checklist
([onboarding-checklist.tsx](web/dash0/src/components/dashboard/onboarding-checklist.tsx))
has had two polish passes (specs 2026-08-30-03 and 2026-09-01-05: a
primary-tinted card, whole-row click targets, explicit row backgrounds, a
faint emerald wash on done rows). The report on the result, verbatim: *The
whole "Getting started" block rendering looks a little meh. Find a way to make
it more modern / flashy.*

Looking at it in both themes (screenshots attached to the request, 5-of-5
state), the "meh" comes from a few concrete things:

- **Everything is a flat rectangle.** The card is a `Card` with
  `border-primary/30 bg-primary/5`
  ([onboarding-checklist.tsx:250-251](web/dash0/src/components/dashboard/onboarding-checklist.tsx:250)),
  containing five identical `rounded-md border p-3` rows
  ([onboarding-checklist.tsx:331](web/dash0/src/components/dashboard/onboarding-checklist.tsx:331)).
  No depth, no hierarchy, no focal point — the one card on the dashboard whose
  job is to *pull the user forward* has the least visual energy of any card on
  the page.
- **Progress is a line of text.** "5 of 5 done" is a `CardDescription`
  ([onboarding-checklist.tsx:256](web/dash0/src/components/dashboard/onboarding-checklist.tsx:256)).
  There is no bar, ring, or percentage; nothing communicates "almost there"
  at a glance.
- **Done rows are struck through.** `line-through` on completed titles
  ([onboarding-checklist.tsx:357](web/dash0/src/components/dashboard/onboarding-checklist.tsx:357))
  reads like a crossed-out to-do list from a text editor, and five struck
  rows in a row (the all-set state) look like a wall of deletions rather than
  an achievement.
- **The all-set state is a grey-green box.** The banner at
  [onboarding-checklist.tsx:277](web/dash0/src/components/dashboard/onboarding-checklist.tsx:277)
  is the only reward for finishing every step, and it is a flat
  `bg-emerald-500/10` rectangle with two lines of text.
- **Nothing moves.** The rows appear all at once, the tick just is; there is no
  reveal, no state transition, no hover lift. `tw-animate-css` is already
  imported in [index.css:2](web/dash0/src/index.css:2) and used by every
  dialog/popover, but this card uses none of it.

## Proposal

Redesign the *presentation* of `OnboardingChecklistCard`
([onboarding-checklist.tsx:237](web/dash0/src/components/dashboard/onboarding-checklist.tsx:237))
and its rows so the block reads as a modern, energetic onboarding surface,
without changing what the container (`OnboardingChecklist`) computes or
stores. The implementer has design latitude — the goal is "noticeably more
polished and alive in both themes", not a pixel spec — but the direction
below is what "modern / flashy" should mean here.

### 1. Card chrome: depth and an accent, not a flat tint

- Replace the flat `bg-primary/5` wash with a treatment that has depth:
  e.g. a soft primary→brand gradient border/edge (a 1px gradient ring via a
  padded gradient wrapper, or a top accent bar), the existing
  `--shadow-primary` depth token ([index.css:59](web/dash0/src/index.css:59))
  for ambient lift, and one or two low-alpha, heavily blurred colour blobs
  (primary + `chart-5` violet, à la `AuroraPanel`) clipped inside the card at
  an alpha that works on *both* the light and the dark surface.
- **Do not** wrap the card in `AuroraPanel` itself: it is always-dark and the
  design reference explicitly reserves it (and `glass`) for marketing surfaces
  ([design-reference.tsx:4415](web/dash0/src/routes/orgs/$org/design-reference.tsx:4415)).
  Borrow its *idea* (blurred brand blobs) as a theme-aware, in-card accent.
- Keep the semantic colour rule from spec 2026-08-30-03: card chrome must not
  use green/amber/red, which mean up/degraded/down elsewhere on the dashboard.
  Emerald stays reserved for *done* state inside the card.

### 2. Header: visible progress

- Add a real progress indicator next to the title: the shipped `Progress`
  primitive ([progress.tsx](web/dash0/src/components/ui/progress.tsx)) as a
  slim bar, or a small SVG ring with the done count inside it. Either way,
  animate the fill on change (`transition-all` is already on the bar).
- **Gotcha:** `Progress` defaults to `destructiveWhenFull`, so 5/5 would turn
  *red*. Pass `destructiveWhenFull={false}` and give the full state an emerald
  `indicatorClassName`.
- Keep the `onboarding-progress` test id on the element that carries the
  "N of M done" text — the e2e suite reads it.

### 3. Step rows: hierarchy, a "next up" focal point, and a hover lift

- **Drop the strikethrough.** Done rows: emerald *filled* tick (circle with
  white check, not the outline `CheckCircle2` at
  [onboarding-checklist.tsx:348](web/dash0/src/components/dashboard/onboarding-checklist.tsx:348)),
  muted title, description hidden, CTA outlined — compact and calm.
- **Highlight the first pending step as "next up"**: a stronger primary ring /
  gradient edge, a subtle primary glow, its CTA in the `default` variant, the
  remaining pending rows quieter. A tiny "Next" pill is optional; if added it
  is a new locale key (see constraints).
- Give each row a soft *lift* on hover (`hover:-translate-y-px` +
  `hover:shadow-primary`, `transition`), replacing the current
  background-only hover, so the whole-row click target is obviously alive.
- Replace the bare `Circle` outline on pending rows
  ([onboarding-checklist.tsx:350](web/dash0/src/components/dashboard/onboarding-checklist.tsx:350))
  with a step number badge or a ringed circle that visibly turns into the
  emerald tick — the state change should be *seen*.
- Consider a slightly larger, tinted icon well for the step icon (the
  `ListChecks` / `Bell` / … glyphs are currently 14px inline with the title).

### 4. All-set state: a reward, not a notice

- Turn the banner at
  [onboarding-checklist.tsx:277](web/dash0/src/components/dashboard/onboarding-checklist.tsx:277)
  into a celebratory strip: emerald→primary gradient wash, a `Sparkles` /
  `PartyPopper` glyph, the title in a stronger weight, and an `animate-in
  fade-in zoom-in-95` reveal. Keep the copy and the `onboarding-all-set` test
  id.
- With the strikethrough gone, five done rows under this strip should look
  like a completed set, not a deleted list.

### 5. Motion, with a reduced-motion guard

- On first mount, stagger the rows in (`animate-in fade-in
  slide-in-from-bottom-2`, ~50 ms delay per row, ≤300 ms total). Animate the
  tick when a step flips to done and the progress fill when the count moves.
- **Every animation must respect `prefers-reduced-motion`.** Nothing in
  `web/dash0/src` uses `motion-safe:` / `motion-reduce:` today, so this is the
  first surface to set the pattern — use Tailwind's `motion-safe:` prefix on
  the animation classes (or a `@media (prefers-reduced-motion: reduce)` rule
  in `index.css`), and note the convention in the design-reference section.
- Keep durations short so Playwright's actionability checks never wait on a
  moving element; no infinite animations on the rows (the `radar-ping`
  utility is fine on a single dot if used at all).

### 6. Constraints (hard)

- **Behaviour is untouched.** The container's derivation, self-dismissal,
  manual dismissal, and the test-alert flow stay exactly as they are; only
  `OnboardingChecklistCard`, `OnboardingStepRow` and `OnboardingStepLink`
  change (plus CSS / design reference / locales).
- **All test ids and data attributes survive unchanged**: `onboarding-checklist`,
  `onboarding-progress`, `onboarding-dismiss`, `onboarding-all-set`,
  `onboarding-step-<id>` with `data-done`, `onboarding-step-<id>-status` with
  `data-done`, `onboarding-step-<id>-cta`, `onboarding-test-alert`. Both
  [onboarding-checklist.spec.ts](web/dash0/e2e/onboarding-checklist.spec.ts)
  and [magic-wand.spec.ts](web/dash0/e2e/magic-wand.spec.ts) read them.
- **The stretched-link click target stays.** The row is `relative`, the CTA
  link carries `after:absolute after:inset-0`
  ([onboarding-checklist.tsx:451](web/dash0/src/components/dashboard/onboarding-checklist.tsx:451)),
  and the test-alert button sits on `relative z-10`
  ([onboarding-checklist.tsx:376](web/dash0/src/components/dashboard/onboarding-checklist.tsx:376)).
  A gradient-border wrapper or a blob layer must not become a new positioned
  ancestor that shrinks that target or intercepts clicks (`pointer-events-none`
  on every decorative layer). The e2e tests "clicking a step row's body …
  navigates" and "clicking the test-alert button … without navigating away"
  are the regression guard.
- **Both themes, verified by eye** in the running app (`make dev-test`,
  `test@test.com` / `test`), and in the design reference. Use theme tokens and
  alpha tints (`primary/…`, `emerald-500/…`, `--shadow-primary`), never raw
  light-only colours.
- **Mobile**: rows still stack (`flex-col` → `sm:flex-row`); the "stays usable
  at mobile width" e2e test must pass; touch targets do not shrink.
- **Design reference is mandatory**: update the section at
  [design-reference.tsx:5486](web/dash0/src/routes/orgs/$org/design-reference.tsx:5486)
  — its description text still documents the old `bg-card` / `emerald-500/10`
  row scheme — and make sure the fixture shows the "next up" row, a done row,
  and (a second fixture) the all-set state. If a new primitive falls out of
  this (e.g. a `GradientBorderCard` or a progress ring), catalogue it there.
- **Locales**: any new visible copy (e.g. a "Next" pill, a "{{pct}}%" label)
  goes into all four locales (`de`, `en`, `es`, `fr`); `bun run test:unit`
  enforces key parity.
- **Lint**: dash0 `eslint` is red on base for unrelated `react-hooks` debt —
  the bar is *no new* errors, not a clean run.

### Verification

- `bun run test:unit` (locale parity + the checklist unit tests) green.
- `onboarding-checklist.spec.ts`, `magic-wand.spec.ts` and
  `empty-state-onboarding.spec.ts` green against a `make dev-test` server.
- Light and dark screenshots of the dashboard with the card in the 1-of-5,
  3-of-5 and 5-of-5 states, plus the design-reference section, attached to
  the closing commit or PR.

## Open questions

- **How far is "flashy"?** Default reading: bold but tasteful — gradient
  accent, depth, progress, one celebratory moment, short reveal motion. Not
  confetti, not a full-bleed dark hero on an operator dashboard. If the
  implementer is torn between two directions, the restrained one wins;
  the previous passes erred on *too* little, not too much.
- **Done-row compaction.** Collapsing done rows to a single line (or a
  horizontal "completed" strip) would make the pending steps the clear focus,
  but changes the vertical rhythm the e2e mobile test exercises. Fine to do
  if the row test ids and click targets are preserved.
- The neighbouring `OverallStatusBanner` ("All systems operational") already
  has its own tinted treatment; the new card should sit *above* it as the
  more energetic of the two without clashing — check the pair together, not
  the card in isolation.

## Implementation Plan

Presentation-only rework of `OnboardingChecklistCard` / `OnboardingStepRow` /
`OnboardingStepLink` in
`web/dash0/src/components/dashboard/onboarding-checklist.tsx`. The container
(`OnboardingChecklist`), its derivation, its dismissal writes and the
test-alert mutation are not touched.

1. **Card chrome.** Keep `Card` + `data-testid="onboarding-checklist"`, swap
   `bg-primary/5` for `relative overflow-hidden bg-card shadow-primary` with a
   `border-primary/30`. Add one decorative, `aria-hidden`,
   `pointer-events-none` layer holding: a 1px top accent bar
   (`bg-gradient-to-r from-primary via-chart-5 to-primary/40`) and two
   heavily-blurred low-alpha blobs (`bg-primary/10`, `bg-chart-5/10`, both
   `blur-3xl`). No green/amber/red in the chrome; emerald stays reserved for
   *done*. `AuroraPanel`/`glass` are NOT used (marketing-only). `CardHeader`
   and `CardContent` get `relative` so they paint above the blob layer.

2. **Header progress.** Gradient icon tile (`Rocket`) beside the title; the
   `onboarding-progress` test id stays on the `CardDescription` carrying
   "N of M done"; below it a slim `Progress` bar with
   `destructiveWhenFull={false}` (its default TRUE would turn 5/5 red) and an
   emerald `indicatorClassName` once complete, primary→violet gradient
   otherwise.

3. **Step rows.** Compute `nextUpId` = first pending step in the card and pass
   `isNext` / `index` down.
   - Status well: done → filled emerald disc with a white `Check`; pending →
     a numbered ringed badge (primary-tinted when next-up, muted otherwise).
     The outer `<span>` keeps `onboarding-step-<id>-status` + `data-done` +
     the aria-label, so the force-click e2e keeps its target.
   - Strikethrough removed. Done title goes `text-muted-foreground`.
   - Next-up row gets a primary ring + `shadow-primary` + a "Next" pill (new
     locale key `onboarding.nextUp`); other pending rows are quieter; done
     rows keep the faint emerald wash.
   - Hover lift: `motion-safe:hover:-translate-y-px` +
     `hover:shadow-primary`/`hover:shadow-card`, `transition-all duration-200`.
   - CTA variant: `default` only on the next-up row that has no test-alert
     button; `outline` everywhere else.

4. **All-set banner.** Emerald→primary gradient strip, `Sparkles` glyph,
   stronger title weight, `motion-safe:animate-in fade-in zoom-in-95` reveal.
   Copy and `onboarding-all-set` test id unchanged.

5. **Motion + reduced-motion guard.** Rows stagger in with
   `motion-safe:animate-in motion-safe:fade-in
   motion-safe:slide-in-from-bottom-2 motion-safe:fill-mode-both` and an
   inline `animationDelay` of `index * 50ms` (≤200ms), 300ms duration. Every
   animation class carries the `motion-safe:` prefix — this is the first
   surface in dash0 to set that convention, so it is documented in the design
   reference.

6. **Locales.** `onboarding.nextUp` added to `de`, `en`, `es`, `fr`
   `dashboard.json`, and to `DASHBOARD_KEYS` in
   `onboarding-checklist.test.ts` so parity is enforced.

7. **Design reference.** Rewrite the `OnboardingChecklistSection` description
   (it still documents the old `bg-card` / `emerald-500/10` scheme), keep the
   mixed-state fixture (which now shows a done row and a next-up row) and add
   a second fixture in the 5/5 `allSet` state. Note the `motion-safe:`
   convention there.

8. **QA.** `make build-dash0`, `cd web/dash0 && bun run lint` (no NEW errors
   over the 42-error baseline), `bun run test:unit`, and the three named
   Playwright files if a `SP_RUNMODE=test` server is reachable.

### Hard constraints re-checked at the end

Every test id preserved; every decorative layer `pointer-events-none`; the
stretched `after:absolute after:inset-0` link and the `relative z-10`
test-alert button untouched; rows still `flex-col` → `sm:flex-row`.
