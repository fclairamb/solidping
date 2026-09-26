---
model: sonnet
effort: medium
---

# `text-destructive-foreground` is used 19 times but the token does not exist, so every destructive confirm button renders with the wrong label color

## Problem

`web/dash0/src/index.css` defines `--destructive` (`:root` line 162, `.dark` line 248) and maps
`--color-destructive` in `@theme inline` (line 37). It defines no `--destructive-foreground` and no
`--color-destructive-foreground`. Tailwind v4 therefore generates **no CSS** for
`text-destructive-foreground`.

The class is used 19 times, always in the same string on an `AlertDialogAction`
(`grep -rn destructive-foreground web/dash0/src`):

```tsx
<AlertDialogAction
  onClick={handleDeleteCheck}
  className="bg-destructive text-destructive-foreground hover:bg-destructive/90"
>
```

Call sites:

- `src/components/shared/status-page-tv-card.tsx:228`
- `src/components/incidents/incident-publications-panel.tsx:245`
- `src/routes/orgs/$org/status-updates.index.tsx:476`
- `src/routes/orgs/$org/status-pages.index.tsx:418`
- `src/routes/orgs/$org/status-pages.$statusPageUid.index.tsx:1159`, `:1464`
- `src/routes/orgs/$org/maintenance-windows.index.tsx:395`
- `src/routes/orgs/$org/maintenance-windows.$maintenanceWindowUid.index.tsx:246`
- `src/routes/orgs/$org/incidents.$incidentUid.tsx:600`
- `src/routes/orgs/$org/integrations.$integrationUid.tsx:412`
- `src/routes/orgs/$org/test.reset.tsx:102`
- `src/routes/orgs/$org/organization.parameters.tsx:293`
- `src/routes/orgs/$org/organization.report-schedules.index.tsx:265`
- `src/routes/orgs/$org/checks.index.tsx:1873`
- `src/routes/orgs/$org/checks.$checkUid.index.tsx:1395`
- `src/routes/orgs/$org/account.sessions.tsx:229`, `:250`
- `src/routes/orgs/$org/account.tokens.tsx:291`
- `src/routes/orgs/$org/check-groups.$uid.edit.tsx:138`

Every one sits on a red `bg-destructive` fill inside a `bg-card` dialog
(`src/components/ui/alert-dialog.tsx:60`). None is on a non-red surface.

The dead class does worse than nothing. `AlertDialogAction` is
`cn(buttonVariants(), className)` (`alert-dialog.tsx:128`), and `tailwind-merge` treats
`text-destructive-foreground` as a text color, so it **drops** the default variant's working
`text-gradient-foreground` (verified: `twMerge("… text-gradient-foreground", "bg-destructive
text-destructive-foreground …")` keeps only the dead class). The label then inherits the body's
`text-foreground` (`index.css:356`): dark navy on red in light mode, near-white in dark mode.

### Adding a white token alone does not reach 4.5:1

Measured with the same oklch → clipped 8-bit sRGB → WCAG pipeline as `src/theme-tokens.test.ts`,
over the dialog's `bg-card` (light `oklch(1 0 0)`, dark `oklch(0.185 0.026 262)`):

| Label on | Light | Dark |
|---|---|---|
| Today: inherited `--foreground` on `--destructive` | 4.26 | 3.16 |
| White on `--destructive` | 4.41 | 3.56 |
| White on hover `bg-destructive/90` | 3.97 | 4.22 |

The label is `text-sm font-medium`, so it is normal-size text and needs 4.5:1. White fails in both
themes. `Button variant="destructive"` (`src/components/ui/button.tsx:24-25`, `text-white`) has the
same shortfall today; it is the same pair.

The two themes need different fixes:

- **Light**: darkening `--destructive` fixes the fill and also improves `text-destructive` text
  on the light page, which is at 4.17 today. At `oklch(0.57 0.22 25)`: white 5.01, hover/90 4.52,
  `text-destructive` on `--background` 4.73, on `--card` 5.01.
- **Dark**: `--destructive` is also the color of ~197 `text-destructive` usages on dark surfaces
  (5.57 on `--background`, 5.24 on `--card` today). Darkening the token far enough for white
  (≤ 0.59) drops that text to 4.36 / 4.10. The token must stay at 0.65, and only the **fill** gets
  darker: white on `destructive/80` over `--card` is 5.05, on `/70` 6.05.

## Proposal

### 1. The token

In `src/index.css`:

- `:root` and `.dark`: `--destructive-foreground: oklch(1 0 0);` next to `--destructive`. Pure white,
  matching `--gradient-foreground` (lines 201 and 270, whose comment explains why 1 beats 0.99).
- `@theme inline`: `--color-destructive-foreground: var(--destructive-foreground);` next to
  `--color-destructive`.

### 2. Make the pair readable

- Light `:root`: `--destructive: oklch(0.6 0.22 25)` → `oklch(0.57 0.22 25)`. Leave `--status-error`
  and `--chart-4` alone: they share the old value today but are separate tokens with their own
  consumers, and are out of scope.
- Dark: keep `--destructive` at `oklch(0.65 0.2 25)`. Darken the fill in the destructive **button
  variant** instead: `dark:bg-destructive/80`, and make the dark hover go darker, not lighter
  (`dark:hover:bg-destructive/70`). Check that `cn()` (`src/lib/utils.ts`) keeps the `dark:` and
  `dark:hover:` classes after merging with the default variant.
- `src/components/ui/button.tsx` destructive variant: `text-white` → `text-destructive-foreground`,
  plus the dark fill classes above. This fixes every `Button variant="destructive"` too.

### 3. Stop hand-rolling the destructive confirm button

Give `AlertDialogAction` a `variant` prop forwarded to `buttonVariants({ variant })` (default
unchanged), then replace all 19 `className="bg-destructive text-destructive-foreground
hover:bg-destructive/90"` with `variant="destructive"`. The contrast fix then lives in one place
(`button.tsx`), and no call site can drift from it. After the change,
`grep -rn 'bg-destructive text-destructive-foreground' web/dash0/src` returns nothing.

### 4. Design reference

In `src/routes/orgs/$org/design-reference.tsx`:

- `COLOR_TOKENS` (line 1514): add `{ name: "destructive-foreground", varName:
  "--destructive-foreground", description: "Label on a destructive fill" }` after `destructive`.
- The AlertDialog example (line ~4599) shows `<AlertDialogAction>Delete</AlertDialogAction>`, a
  blue Delete button, which breaks the "delete is always red" rule in the root `CLAUDE.md`. Change it
  to `<AlertDialogAction variant="destructive">` and update its `importLine`.

### 5. Tests

Extend `src/theme-tokens.test.ts` (it already parses `index.css` and has `contrast()`):

- `--destructive-foreground` is defined in both `:root` and `.dark`.
- `@theme inline` contains `--color-destructive-foreground: var(--destructive-foreground);`.
- Contrast ≥ 4.5 for the label on every fill it can sit on: light `--destructive`, light
  `destructive/90` over `--card`, dark `destructive/80` and `destructive/70` over `--card`. This needs a
  small alpha-blend helper (blend in sRGB, as the browser composites).
- Update the "leaves the tokens the spec does not list at their pre-spec values" pin
  (line 161): light `destructive` is now `oklch(0.57 0.22 25)`. Dark stays `oklch(0.65 0.2 25)`.

Add a guard so this bug class cannot come back: scan `src/**/*.tsx` for `(text|bg|border|ring|fill|stroke)-<name>-foreground`
classes (opacity suffix stripped), and assert each `<name>-foreground` has a
`--color-<name>-foreground` in `@theme inline`. `src/raw-blue-classes.test.ts` is the precedent
for a source-scanning test. Include a positive control: the test must fail on a fixture string
using a made-up `text-nope-foreground`.

### Acceptance criteria

- `text-destructive-foreground` produces CSS, and the built stylesheet contains
  `--color-destructive-foreground`.
- Every destructive confirm button and every `Button variant="destructive"` label is ≥ 4.5:1 on
  its fill, at rest and on hover, in both themes (asserted by the tests above).
- `text-destructive` on the dark page is unchanged (dark `--destructive` not modified).
- No `className="bg-destructive text-destructive-foreground …"` remains.
- Design reference shows the new swatch and a red Delete in the AlertDialog example.

### QA

- `make build-dash0`
- `cd web/dash0 && bun run lint`: no NEW errors (the pre-existing react-hooks errors are known
  debt, ~45 at last count).
- `cd web/dash0 && bun run test:unit`
- Look at one confirm dialog (e.g. delete a check) in both themes in the browser.

### Delivery

Per the request that filed this spec: branch from `main` with a `fix/` prefix
(e.g. `fix/dash0-destructive-foreground`). Do not do this work on a `batch/*` branch.

## Open questions

- Dark-mode approach. The proposal darkens the fill with `dark:bg-destructive/80`. The alternative,
  used for `--primary` in dark mode (`index.css:207-211`: light token, dark `--primary-foreground`),
  is a dark `--destructive-foreground` in `.dark` (`oklch(0.16 0.03 262)` gives 5.47 at rest, 4.60 on
  hover). That drops the dark-mode class juggling but puts dark text on a red button, which the
  request wanted to avoid. Going with the white label unless told otherwise.
