# Disabled checks show a grey status dot

## Context

A disabled check is not being monitored, but in the dash0 operator UI its **status
dot stays coloured by its last known status** (green/red/etc.). That's misleading: a
paused check that was last "up" still shows a green dot, reading as "healthy & live".

Make a disabled check (`enabled === false`) render a **grey dot** so its paused state
is obvious at a glance — on both:

- the check **detail** page — `/dash0/orgs/default/checks/$checkUid`
- the checks **listing** — `/dash0/orgs/default/checks`

Both surfaces already render a status dot; neither accounts for `enabled`. The dot just
needs to go grey when the check is disabled, with the disabled state taking precedence
over the live/last status colour.

---

## Current state (verified against source)

| Surface | Location | Today |
|---|---|---|
| Listing dot | `web/dash0/src/routes/orgs/$org/checks.index.tsx` — local `StatusDot` (lines 104–115), used in `CheckRow` at line 168 | hand-rolled `status → bg-*` map; **ignores `enabled`**. No other disabled indicator exists in the row. |
| Detail header dot | `web/dash0/src/routes/orgs/$org/checks.$checkUid.index.tsx` — inline `statusColor` (lines 440–447), rendered as the dot `<div>` at line 453, left of the `<h1>` title | same hand-rolled map; **ignores `enabled`**. |
| Canonical status colours | `web/dash0/src/lib/status-style.ts` — `statusStyle()` returns `.color` (e.g. `bg-green-500`) / `.dotColor` | its header comment declares it the **single source of truth** for status colours, yet **neither dot above uses it**. |
| Detail "Disabled" badge | `checks.$checkUid.index.tsx` lines 771–773 | already shows an outline `Badge` `t("checks:detail.disabled")` when `check.enabled === false`. Keep it. |
| `StatusBadge` | `web/dash0/src/components/shared/status-badge.tsx` | routes through `statusStyle()`. Shown in the detail header (line 770) and each list row (line 188). |
| Backend field | `server/internal/handlers/checks/service.go` — `CheckResponse.Enabled *bool` (line 493), set by `convertCheckToResponse` (line 1805) | `enabled` is present in **both** the list and detail JSON responses. TS `Check.enabled?: boolean` (`api/hooks.ts:46`). |

### The two hand-rolled dot maps also diverge from `statusStyle()`

Both inline maps are a stale subset of `statusStyle()` and can disagree with the
`StatusBadge` rendered right beside them:

- `timeout` → **yellow** in the dots, but **red** (down) in `statusStyle()`/the badge.
- `warning` / `degraded` → **not handled** by the dots (fall through to grey), but
  **yellow** in `statusStyle()`/the badge.

So today a `warning` check shows a grey dot next to a yellow "Warning" badge. Folding
both dots onto `statusStyle()` (below) fixes this as a beneficial side effect.

---

## Requirements

1. On the **listing** and the **detail header**, a check with `enabled === false`
   renders a **grey** status dot.
2. **Disabled overrides status:** the dot is grey regardless of the last/live status
   (a disabled-but-last-"up" check is grey, not green).
3. The grey dot must be **discoverable**: it carries an accessible label / tooltip
   ("Disabled"). This matters most on the **listing**, where the dot is otherwise the
   *only* disabled indicator in the row.
4. Enabled checks are unchanged in meaning (still coloured by status).
5. The detail page keeps its existing outline "Disabled" badge; the `StatusBadge`
   (last known status) is **unchanged** on both pages — only the dot conveys "paused".

---

## Recommended implementation — one shared `StatusDot`

The cleanest fix (and the one that matches `status-style.ts`'s stated "single source of
truth" intent) is to **extract a shared dot component** and delete both inlined copies.

### 1. New `web/dash0/src/components/shared/status-dot.tsx`

```tsx
import { cn } from "@/lib/utils";
import { statusStyle } from "@/lib/status-style";

interface StatusDotProps {
  status?: string | null;
  /** When false, the check is paused → grey dot, overriding status colour. */
  enabled?: boolean;
  /** Size/extra classes; default size is h-2.5 w-2.5. */
  className?: string;
  /** Tooltip + accessible label (e.g. the localized "Disabled"). */
  title?: string;
}

export function StatusDot({ status, enabled, className, title }: StatusDotProps) {
  const disabled = enabled === false;
  const color = disabled ? "bg-muted-foreground" : statusStyle(status).color;
  return (
    <span
      data-testid="check-status-dot"
      data-disabled={disabled ? "true" : "false"}
      data-status={status ?? "unknown"}
      title={title}
      aria-label={title}
      className={cn(
        "inline-block h-2.5 w-2.5 shrink-0 rounded-full",
        color,
        disabled && "opacity-70",
        className,
      )}
    />
  );
}
```

- **Grey = `bg-muted-foreground`** — a theme token that reads grey in light *and* dark
  mode, and is the same neutral both current dots already fall back to for unknown
  status. `opacity-70` makes it read as "off" without inventing a new colour. (Avoid a
  raw `bg-gray-*` so dark mode stays correct.)
- Non-disabled colours come from `statusStyle().color`, so the dot now **matches the
  `StatusBadge`** beside it (this is the intended consistency fix for
  `timeout`/`warning`/`degraded` noted above).
- `data-*` attributes make E2E assert on state, not on brittle Tailwind classes.

### 2. Listing — `checks.index.tsx`

- Delete the local `StatusDot` (lines 104–115); import the shared one.
- At the call site (line 168) pass `enabled` + a localized title:

  ```tsx
  <StatusDot
    status={check.status ?? check.lastResult?.status}
    enabled={check.enabled}
    title={check.enabled === false ? t("checks:detail.disabled") : undefined}
  />
  ```

### 3. Detail header — `checks.$checkUid.index.tsx`

- Delete the inline `statusColor` (lines 440–447) and the dot `<div>` (line 453);
  replace with the shared component, preserving the larger `h-3 w-3` header size:

  ```tsx
  <StatusDot
    status={headerStatus}
    enabled={check.enabled}
    className="h-3 w-3"
    title={check.enabled === false ? t("checks:detail.disabled") : undefined}
  />
  ```

- Leave the existing outline "Disabled" badge (lines 771–773) and `StatusBadge` as-is.

### 4. Design reference (mandatory — repo `CLAUDE.md`)

Add `StatusDot` to `web/dash0/src/routes/orgs/$org/design-reference.tsx` with its import
line, showing the dot in each state (up / warning / down / unknown / **disabled**) so
the catalog stays canonical.

> **Acceptable minimal alternative** (if we want *zero* colour change for non-disabled
> checks): keep both inline maps and only prepend a `check.enabled === false →
> bg-muted-foreground` branch in each. This satisfies requirements 1–5 but keeps the
> duplication and the dot/badge colour mismatch for `timeout`/`warning`/`degraded`. The
> shared-component path above is preferred.

---

## i18n

The label already exists in all four locales (`checks:detail.disabled` = "Disabled" in
`web/dash0/src/locales/{en,fr,de,es}/checks.json`). Reuse it for the dot `title` — **no
new keys required**. Do not hardcode "Disabled" in the component; callers pass the
translated string.

---

## E2E — `web/dash0/e2e/`

Use `./fixtures` `authenticatedPage` (test mode: org `test`, `test@test.com` / `test`).
Assert on `data-testid="check-status-dot"` + `data-disabled`, not on colour classes.

- **`checks.spec.ts` (listing):** seed/create one disabled and one enabled check; assert
  the disabled row's dot has `data-disabled="true"` (and `title`/`aria-label` =
  "Disabled"), and the enabled row's dot has `data-disabled="false"`.
- **`check-detail.spec.ts`:** open a disabled check → header dot `data-disabled="true"`
  and the "Disabled" badge is visible; then toggle Enable (the detail page already has
  the enable/disable action) and assert the dot flips to `data-disabled="false"`.
- Screenshot each state into `test-results/screenshots/`. Treat any flake as a bug to
  root-cause, never re-run blindly ([[feedback_flaky_tests_are_bugs]]).

---

## Key files

| File | Change |
|---|---|
| `web/dash0/src/components/shared/status-dot.tsx` | **New** — shared `StatusDot` (status via `statusStyle()`, grey when `enabled === false`) |
| `web/dash0/src/routes/orgs/$org/checks.index.tsx` | **~** delete local `StatusDot`, use shared one with `enabled` + title |
| `web/dash0/src/routes/orgs/$org/checks.$checkUid.index.tsx` | **~** replace inline `statusColor`/dot with shared `StatusDot` (`h-3 w-3`); keep "Disabled" badge |
| `web/dash0/src/routes/orgs/$org/design-reference.tsx` | **+** register `StatusDot` with all states incl. disabled |
| `web/dash0/e2e/checks.spec.ts`, `web/dash0/e2e/check-detail.spec.ts` | **~** assert disabled→grey dot on both surfaces |

---

## Verification

```bash
make dev-test   # backend + dash0, SP_RUNMODE=test, port 4000
# Listing  (/dash0/orgs/test/checks): a disabled check shows a grey dot; hovering reads "Disabled".
# Detail   (/dash0/orgs/test/checks/$uid): disabled → grey header dot + "Disabled" badge.
#   Toggle Enable/Disable on the detail page → the dot colour flips live.
# Confirm a disabled-but-last-"up" check is grey, not green.
# Resize to mobile width — dot still visible and aligned.
make test-dash  # Playwright E2E
make lint       # no NEW dash0 lint errors ([[project_dash0_eslint_debt]])
```

---

## Out of scope

- Changing the `StatusBadge` to read "Disabled" (the dot + existing badge already convey
  it). Listing rows still show the last-status badge.
- Any backend change — `enabled` is already serialized on list and detail responses.
- A dedicated disabled state in `statusStyle()` itself (disabled is a check-level flag,
  not a result status; handled by the `enabled` prop in `StatusDot`).

---

## Implementation Plan

Following the **recommended** path (shared component), not the minimal alternative,
so both inline maps are deleted and the dot/badge colour mismatch is fixed.

1. **New shared `StatusDot`** — `web/dash0/src/components/shared/status-dot.tsx`.
   - Props: `status?: string | null`, `enabled?: boolean`, `className?: string`,
     `title?: string`.
   - Colour: `enabled === false → bg-muted-foreground` (grey, theme token, correct in
     dark mode) with `opacity-70`; otherwise `statusStyle(status).color` so the dot
     matches the `StatusBadge` beside it (fixes `timeout`/`warning`/`degraded`).
   - Renders a `<span>` with `data-testid="check-status-dot"`,
     `data-disabled="true|false"`, `data-status`, plus `title`/`aria-label` for
     discoverability. Default size `h-2.5 w-2.5`, overridable via `className`.

2. **Listing wiring** — `web/dash0/src/routes/orgs/$org/checks.index.tsx`.
   - Delete the local `StatusDot` (lines 104–115); import the shared one.
   - Call site (line 168): pass `enabled={check.enabled}` and a localized
     `title` (`t("checks:detail.disabled")` when disabled, else `undefined`).

3. **Detail-header wiring** — `web/dash0/src/routes/orgs/$org/checks.$checkUid.index.tsx`.
   - Delete the inline `statusColor` (lines 440–447) and the dot `<div>` (line 453);
     replace with `<StatusDot ... className="h-3 w-3" />` preserving the larger size.
   - Keep the existing outline "Disabled" badge and `StatusBadge` untouched.

4. **Design reference** — register `StatusDot` in the "Buttons & badges" section of
   `web/dash0/src/routes/orgs/$org/design-reference.tsx`, showing up / warning / down /
   unknown / **disabled** states with its import line, so the catalog stays canonical.

5. **i18n** — no new keys; reuse `checks:detail.disabled` (present in en/fr/de/es) for
   the dot `title`. The component never hardcodes "Disabled"; callers pass the string.

6. **E2E** — `web/dash0/e2e/`.
   - `checks.spec.ts` (listing): create a disabled and an enabled check; assert the
     disabled row's dot has `data-disabled="true"` + `title`/`aria-label` "Disabled",
     and the enabled row's dot has `data-disabled="false"`.
   - `check-detail.spec.ts`: open a disabled check → header dot `data-disabled="true"`
     and the "Disabled" badge visible; toggle Enable → dot flips to
     `data-disabled="false"`. Also assert a disabled-but-last-up check is grey.
   - Assert on `data-testid`/`data-disabled`, never on Tailwind classes. Screenshot each
     state. (Local `:4000` devloop is usually not in `SP_RUNMODE=test`; if E2E can't run
     locally, author it anyway.)

7. **QA** — `make build-dash0`; `cd web/dash0 && bun run lint` (no NEW errors in touched
   files). No Go changes, so no backend build/test needed.
