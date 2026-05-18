# Check detail — status badges use proper Badge variants via a shared StatusBadge

## Context

On the check detail page
(`web/dash0/src/routes/orgs/$org/checks.$checkUid.index.tsx`) status is
rendered with two inline-styled `Badge` components:

- **Header badge** (lines 689–704): `variant="secondary"` plus ad-hoc className
  `bg-green-500/10 text-green-500` (up), `bg-red-500/10 text-red-500`
  (down/error), `bg-yellow-500/10 text-yellow-500` (validating).
- **Results-table row** (lines 873–886): same pattern, adds
  `bg-blue-500/10 text-blue-500` for `created`.

Both use a tinted-secondary approach that produces barely-visible pastel badges.
The `Badge` component (`web/dash0/src/components/ui/badge.tsx`) already ships
`success` (solid green + white text), `destructive` (solid red + white), and
`warning` (solid yellow + white) variants. The design-reference page at
`src/routes/orgs/$org/design-reference.tsx:732–736` even has a local
`StatusBadge` helper showing the canonical mapping — but it is not exported or
shared.

The same mapping will be needed in more places as the UI grows (dependencies
card, incidents list, status pages). Centralising it now avoids per-file drift.

## Goal

1. Extract a shared `StatusBadge` component that maps check/result status
   strings to the correct `Badge` variant.
2. Replace the two ad-hoc tinted badges in `checks.$checkUid.index.tsx` with
   `StatusBadge`.
3. Update the design-reference page to import the shared component instead of
   its local helper.

## Scope

### New file: `web/dash0/src/components/shared/status-badge.tsx`

```tsx
import { useTranslation } from "react-i18next";
import { Badge } from "@/components/ui/badge";

type Status = "up" | "down" | "error" | "validating" | "created" | string;

interface StatusBadgeProps {
  status: Status | undefined | null;
  className?: string;
}

export function StatusBadge({ status, className }: StatusBadgeProps) {
  const { t } = useTranslation("checks");
  if (!status) return null;

  if (status === "up") {
    return <Badge variant="success" className={className}>{t("status.up", "up")}</Badge>;
  }
  if (status === "down" || status === "error") {
    return <Badge variant="destructive" className={className}>{t("status.down", status)}</Badge>;
  }
  if (status === "validating") {
    return <Badge variant="warning" className={className}>{t("status.validating", "validating")}</Badge>;
  }
  // created, unknown, or any future value
  return (
    <Badge variant="secondary" className={className}>
      {status}
    </Badge>
  );
}
```

Reuses `t("checks:status.*")` keys that already exist in the locale files
(confirmed present for `up`, `down`, `validating`).

### `web/dash0/src/routes/orgs/$org/checks.$checkUid.index.tsx`

- **Lines 689–704**: Replace the `<Badge variant="secondary" className={…}>` block
  with `<StatusBadge status={headerStatus} />`. Remove the conditional className
  logic for green/red/yellow.
- **Lines 873–886**: Replace the `<Badge variant="secondary" className={…}>` block
  in the results-table status cell with `<StatusBadge status={result.status} />`.

### `web/dash0/src/routes/orgs/$org/design-reference.tsx`

- At lines 732–736 replace the inline local `StatusBadge` function with an
  import from `@/components/shared/status-badge`.
- Keep the design-reference showcase row intact so the component stays
  catalogued.

### Sweep for remaining ad-hoc tints in `web/dash0/src`

After the above changes, run:

```bash
grep -r "bg-green-500/10\|bg-red-500/10\|bg-yellow-500/10\|bg-blue-500/10" web/dash0/src
```

Any remaining hits in dash0 components related to check/result status should
be migrated to `StatusBadge` within this spec. Hits in unrelated contexts
(e.g. decorative backgrounds) are out of scope. `web/status0` is out of scope
entirely — different app.

## Acceptance criteria

- [ ] `web/dash0/src/components/shared/status-badge.tsx` exists and exports `StatusBadge`.
- [ ] The header status badge on the check detail page renders with a **solid** green/red/yellow background (not a faint tint), matching the `success`/`destructive`/`warning` Badge variants.
- [ ] Each status cell in the recent-results table renders the same way.
- [ ] The design-reference page still showcases `StatusBadge` and uses the shared import.
- [ ] `grep -r "bg-green-500/10" web/dash0/src` returns no check-status-related hits.
- [ ] `make lint` clean; `make build` (or `make build-dash0`) clean.
