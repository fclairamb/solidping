---
model: sonnet
effort: medium
---

# The incident "First failure" block blends into its card (background on background)

## Problem

On the incident detail page, the **Failure details** card renders the first
failure (and, when present, the "Latest relapse") through
`FailureSnapshotBlock`
([incidents.$incidentUid.tsx:1153](../../web/dash0/src/routes/orgs/$org/incidents.$incidentUid.tsx)):

```tsx
<div className="space-y-3 rounded-lg border p-4">
```

The block has **no background of its own** — it sits directly on the card's
`bg-card` surface, so the only thing separating it from its parent is a 1px
border. Combined with a `text-muted-foreground` section title (line 1155) and
plain-colored values, the most important diagnostic content on the page (what
actually failed, where, when) reads as flat and bland: background on
background.

Neighboring UI already does better: the incident timeline entries use a
tinted sub-surface (`rounded-md border bg-muted/30 p-3`,
[incidents.$incidentUid.tsx:568](../../web/dash0/src/routes/orgs/$org/incidents.$incidentUid.tsx)),
so the snapshot blocks are visibly the odd ones out.

## Proposal

Make the failure snapshot blocks visually distinct from the card surface, in
both light and dark mode. Start from the design reference
([design-reference.tsx](../../web/dash0/src/routes/orgs/$org/design-reference.tsx))
— it already ships `Alert` variants (line 2233) and the shadow/tint tokens.
Suggested treatment (final call is the implementer's, guided by the design
reference):

- Give the block a subtle tinted surface instead of bare `border`:
  - either the neutral precedent already used by the timeline
    (`bg-muted/30`),
  - or, since this is failure content, a soft destructive tint
    (e.g. `border-destructive/30 bg-destructive/5`) so the block reads as
    "this is the error" at a glance. Use tokens only, so dark mode stays
    correct.
- Consider rendering the raw error text (the `font-mono` line at
  [incidents.$incidentUid.tsx:1157](../../web/dash0/src/routes/orgs/$org/incidents.$incidentUid.tsx))
  in `text-destructive` so the actual failure message stands out from the
  metadata grid.
- Apply the same treatment to **both** blocks ("First failure" and
  "Latest relapse") — they share `FailureSnapshotBlock`, so this should be a
  single-site change.
- If the chosen treatment introduces a new pattern (e.g. a tinted
  "failure snapshot" panel), add it to the design reference page as part of
  the change, per the repo convention.

## Acceptance

- The first-failure block is clearly distinguishable from the card behind it
  in **both** light and dark themes (verify visually, e.g. via the existing
  Playwright setup or a quick screenshot at
  `/dash0/orgs/test/incidents/<uid>` under `make dev-test`).
- No raw hex colors — only theme tokens (`muted`, `destructive`, …).
- `bun run lint` introduces no new errors (dash0 has known pre-existing
  eslint debt; scope is no-new-errors).
