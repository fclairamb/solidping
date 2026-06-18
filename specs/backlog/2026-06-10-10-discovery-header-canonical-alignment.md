# Discovery detail: align header with the canonical detail-page pattern

## Context
The 2026-06-10 batch added the canonical **detail-page header** pattern to the
design reference (spec `2026-06-10-08-…`, `design-reference.tsx`):

- row: `flex items-start justify-between gap-3`,
- left: title block with `min-w-0 flex-1`,
- right: `flex gap-2 shrink-0` cluster whose first child is the icon-only ghost
  back button.

The status-pages detail page (`-02`) matches this exactly. The **discovery
detail** page does **not**: spec `-05` deliberately scoped itself to moving the
back arrow + labelling refresh and left the wrapper as
`flex flex-wrap items-start justify-between gap-4` (spec `-05` §A: "The header
wrapper stays …gap-4"). As shipped, `discovery.$jobUid.index.tsx:177-191` has:

- wrapper `flex flex-wrap items-start justify-between gap-4` (canonical: `gap-3`,
  no `flex-wrap`),
- a vestigial left wrapper `<div className="flex items-center gap-3">` that now
  wraps a single child (left over from when the back button was its sibling) —
  it should collapse to the title block with `min-w-0 flex-1`,
- right cluster `flex items-center gap-2` (canonical adds `shrink-0`).

So the design reference now claims to be the single source of truth for the
detail-header pattern while one shipped page diverges — the exact "drift" spec
`-08` set out to eliminate.

## Goal
Bring the discovery detail header into line with the canonical detail-page
header pattern so the design reference and the shipped routes agree.

## Behaviour
- Header wrapper → `flex items-start justify-between gap-3` (drop `flex-wrap`;
  the title is short ("Scan details") and the 3-button cluster fits beside it,
  with `min-w-0`/`shrink-0` handling any overflow gracefully).
- Collapse the redundant `flex items-center gap-3` left wrapper to the title
  block and give it `min-w-0 flex-1`.
- Right cluster → add `shrink-0` (keep the existing button order: back, Stop,
  Refresh).
- No change to button behaviour (back nav, Stop confirm, labelled-on-desktop
  Refresh + spin).

## Note / decision needed
This intentionally revisits spec `-05`'s explicit "wrapper stays gap-4". That
phrasing read as scope-limiting ("don't touch the wrapper in `-05`") rather than
a design veto, and spec `-08`'s context expected discovery to adopt the
canonical pattern — but because it contradicts an archived spec's literal text,
confirm before shipping. Verify on a real scan detail at desktop + mobile that
nothing wraps badly.

## Longer-term follow-up (from spec `-08` §C)
Documentation alone is what drifted before. Extracting shared `ListPageHeader`
and `DetailPageHeader` components — consumed by both the design-reference page
and the real routes — would enforce these patterns by construction instead of by
copy-paste. Bigger change; track separately.

## Testing
- `e2e/discovery.spec.ts` already asserts back-arrow placement (right cluster,
  leftmost) and the responsive refresh label — these must stay green.
- Manual: `make dev-test`, open a scan detail, check desktop + mobile across the
  `sm` breakpoint, light + dark.
