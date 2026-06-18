# Give the destructive button the same tinted elevation/hover lift as the save button

## Context

The primary (save) `Button` carries a deliberate "premium" elevation effect: a
**tinted drop shadow** at rest that **grows on hover**. The destructive (delete) button
does not — it only gets a flat `shadow-sm` and a background darken on hover, so it sits
visually flatter than the save button next to it (most visible when both render side by
side, e.g. the "Action buttons" row of the design reference and any detail-page header).

The effect lives in two places:

- **Shadow tokens** — [`web/dash0/src/index.css:48-58`](../../web/dash0/src/index.css)
  defines tinted elevation tokens inside `@theme inline`. They use `color-mix` against
  `var(--primary)` so the shadow carries the primary hue and adapts automatically in dark
  mode:

  ```css
  --shadow-primary: 0 8px 16px -4px color-mix(in oklab, var(--primary) 15%, transparent),
    0 4px 8px -4px color-mix(in oklab, var(--primary) 11%, transparent);
  --shadow-primary-hover: 0 12px 24px -6px color-mix(in oklab, var(--primary) 22%, transparent),
    0 6px 12px -6px color-mix(in oklab, var(--primary) 14%, transparent);
  ```

- **The variant** — the `default` variant in
  [`web/dash0/src/components/ui/button.tsx:12-13`](../../web/dash0/src/components/ui/button.tsx)
  wires those tokens in:

  ```tsx
  default:
    "bg-primary text-primary-foreground shadow-primary hover:bg-primary/90 hover:shadow-primary-hover",
  ```

  The `destructive` variant
  ([`button.tsx:14-15`](../../web/dash0/src/components/ui/button.tsx)) has no tinted shadow:

  ```tsx
  destructive:
    "bg-destructive text-white shadow-sm hover:bg-destructive/90",
  ```

`--destructive` is already a first-class theme color (defined for both light
[`index.css:114`](../../web/dash0/src/index.css) and dark
[`index.css:171`](../../web/dash0/src/index.css)), so a `--shadow-destructive` token built
the same way will track it automatically.

## Decision

Mirror the save button's effect on the destructive button by adding a **destructive-tinted**
pair of elevation tokens (`--shadow-destructive`, `--shadow-destructive-hover`) and wiring
them into the `destructive` variant — same shape/intensity as the primary pair, only swapping
the hue from `--primary` to `--destructive`. Rationale:

- It reuses the existing, proven tinted-elevation mechanism rather than inventing a new
  hover treatment, so the two buttons feel like one family.
- Tinting with `--destructive` (red) instead of `--primary` (blue) keeps the delete button
  reading as destructive — a blue shadow on a red button would look wrong and break the
  "destructive red is reserved for destructive actions" convention.
- It is a token + one-line-variant change; every destructive button in the app inherits it,
  and the design reference (which renders the live variant) shows it automatically.

## Goals

- The `destructive` button shows a **red-tinted drop shadow at rest** and a **larger
  red-tinted shadow on hover**, matching the *magnitude* and feel of the save button's
  `shadow-primary` → `shadow-primary-hover` lift.
- The existing background darken on hover (`hover:bg-destructive/90`) is **preserved** — the
  new shadow is additive, not a replacement.
- The effect adapts automatically in light and dark mode (because it color-mixes against the
  per-theme `--destructive`).
- No other variant (`default`, `outline`, `secondary`, `ghost`, `link`) changes.

## Out of scope

- Touching the `default`/save button or its tokens.
- New hover treatments beyond the tinted shadow (no scale/translate/ring changes).
- Changing the destructive color, the `Trash2` icon convention, or any "delete is red" rule.
- Adding shadows to `outline`/`secondary`/`ghost` destructive usages or to the
  `text-destructive` icon buttons / dropdown items (those are intentionally flat).

## Implementation

### 1. Add the destructive elevation tokens (`index.css`, in the `@theme inline` block)

Right after the `--shadow-primary-hover` definition
([`index.css:53-54`](../../web/dash0/src/index.css)), add a destructive-tinted pair with the
**same numbers** as the primary pair, swapping `var(--primary)` → `var(--destructive)`:

```css
--shadow-destructive: 0 8px 16px -4px color-mix(in oklab, var(--destructive) 15%, transparent),
  0 4px 8px -4px color-mix(in oklab, var(--destructive) 11%, transparent);
--shadow-destructive-hover: 0 12px 24px -6px color-mix(in oklab, var(--destructive) 22%, transparent),
  0 6px 12px -6px color-mix(in oklab, var(--destructive) 14%, transparent);
```

This makes `shadow-destructive` and `shadow-destructive-hover` available as Tailwind v4
utilities (mirroring how `shadow-primary` / `shadow-primary-hover` resolve today).

### 2. Wire them into the `destructive` variant (`button.tsx:14-15`)

```tsx
// before
destructive:
  "bg-destructive text-white shadow-sm hover:bg-destructive/90",
// after
destructive:
  "bg-destructive text-white shadow-destructive hover:bg-destructive/90 hover:shadow-destructive-hover",
```

Swap the flat `shadow-sm` for the tinted `shadow-destructive`, and add
`hover:shadow-destructive-hover` so the lift grows on hover — exactly the structure the
`default` variant uses.

### 3. Nothing else

The `transition` already on the base button class
([`button.tsx:8`](../../web/dash0/src/components/ui/button.tsx)) animates `box-shadow`, so the
hover lift eases in just like the save button. No component call sites change.

## Design reference

The design reference renders the live `destructive` variant in two spots — the "Button
variants" row ([`design-reference.tsx:734`](../../web/dash0/src/routes/orgs/$org/design-reference.tsx))
and the "Action buttons" row next to the Save button
([`design-reference.tsx:783-786`](../../web/dash0/src/routes/orgs/$org/design-reference.tsx)).
Because both pull the variant straight from `button.tsx`, the new tinted shadow appears
there automatically with **no edit needed** — the save and delete buttons in that row should
now show matching elevation at rest and on hover. Use that row as the canonical visual check.
(No new primitive is introduced, so the catalog stays correct without a code change to it.)

## Verification

1. `make dev-test` (or `bun run dev` in `web/dash0`), open
   `http://localhost:4000/dash0/orgs/default/design-reference`.
2. In the "Action buttons" row, confirm the **Delete** button now has a soft red-tinted drop
   shadow that **grows on hover**, with the same visual weight as the **Save** button's blue
   lift beside it. The background still darkens slightly on hover.
3. Toggle dark mode on the design reference; the destructive shadow stays red-tinted and
   legible (it tracks the dark `--destructive`), and is not washed out or pure black.
4. Spot-check a real destructive button in context (e.g. a check/incident detail header) to
   confirm the lift looks right against page chrome, not just on the reference.
5. `bun run lint` and `bun run build` (tsc included) pass in `web/dash0`.

## Tests

This is a visual/token change with no behavioural surface, so automated coverage is thin and
deterministic rather than pixel-based. Add a light assertion to the existing dash0 Playwright
suite (the design reference is already loaded by listing-pages-style / style specs in
`web/dash0/e2e/`):

- Load the design reference, locate the destructive variant button, and assert its computed
  `box-shadow` is **not** `none` and **changes on hover** (capture `box-shadow` before and
  after `hover()` and assert they differ) — proving the at-rest tint and the hover lift are
  both wired. Avoid asserting exact `color-mix`/rgb values (resolved differently per browser);
  compare the two computed strings for inequality instead.

If the suite has no natural home for a design-reference visual assertion, it's acceptable to
rely on the manual design-reference check above plus `lint`/`build`, and note that in the PR.

## Files referenced

- `web/dash0/src/index.css:48-58` — tinted elevation tokens; add the destructive pair here.
- `web/dash0/src/components/ui/button.tsx:14-15` — the `destructive` variant to rewire.
- `web/dash0/src/routes/orgs/$org/design-reference.tsx:734,783-786` — live destructive
  button previews; auto-update, used for visual verification.

## Implementation Plan

1. Add `--shadow-destructive` / `--shadow-destructive-hover` to the `@theme inline` block in
   `index.css`, mirroring the primary pair with `var(--destructive)`.
2. Change the `destructive` variant in `button.tsx` from
   `shadow-sm` → `shadow-destructive hover:shadow-destructive-hover` (keep
   `hover:bg-destructive/90`).
3. Visually confirm on the design reference (light + dark) that Delete matches Save's lift.
4. Add the light Playwright box-shadow-changes-on-hover assertion (or document the manual
   check if no suitable spec exists).
5. QA: `bun run lint` + `bun run build` in `web/dash0`.
