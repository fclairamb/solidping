# Status update card: badge and title on the same line

## Context

On the public status page (e.g. `https://solidping.k8xp.com/status0/org2/prod`), the
"Mises à jour récentes" / recent updates list renders each update via
[`web/status0/src/components/shared/status-update-card.tsx`](../../web/status0/src/components/shared/status-update-card.tsx).

Currently the kind badge (e.g. `Info`) sits on a header row next to the relative
timestamp, and the title (`cool`) is rendered on a separate line below it. This
wastes vertical space and visually separates the label from the title it
qualifies.

## Desired behavior

The kind badge (`Info`) and the update title should appear on the **same line**,
with the relative timestamp still right-aligned on that row.

Suggested layout for the header row:
- Left: `[Info badge] Title` together (badge first, then title inline).
- Right: relative timestamp (`2 hours ago`).

The body markdown and the optional "Read more →" link stay below, unchanged.

## Notes

- Keep the title's existing semantics (`<h3>`, `text-sm font-semibold`). Inlining
  it next to the badge should not change heading level or accessibility.
- Keep the row responsive: on narrow widths the badge + title should still wrap
  gracefully without overlapping the timestamp.
- No backend or API changes — this is purely a layout tweak in
  `status-update-card.tsx`.
