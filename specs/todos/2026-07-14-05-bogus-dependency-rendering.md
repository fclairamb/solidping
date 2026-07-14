---
model: sonnet
effort: medium
---

# dash0: fix bogus dependency rendering

## Problem

The check-dependency UI can render in a broken/"bogus" state. The source issue
shows a small screenshot of a malformed dependency display but gives no textual
detail beyond that it looks wrong.

Source issue: [#129 — dash0: bogus dependency rendering](https://github.com/fclairamb/solidping/issues/129)
(the screenshot is the only description; see the issue for the image).

The dependency rendering lives in dash0 under:
- [`web/dash0/src/components/checks/dependencies-card.tsx`](web/dash0/src/components/checks/dependencies-card.tsx)
- [`web/dash0/src/components/checks/form/sections/dependencies.tsx`](web/dash0/src/components/checks/form/sections/dependencies.tsx)
- [`web/dash0/src/components/shared/dependency-cycle-path.tsx`](web/dash0/src/components/shared/dependency-cycle-path.tsx)
- locale strings in `web/dash0/src/locales/*/dependencies.json`

## Proposal

1. **Reproduce first.** Recreate the state in the screenshot — the likely
   candidates are: an empty / single-item dependency list rendering a stray
   connector or label, a missing dependency name falling back to a raw
   uid/placeholder, a broken cycle-path rendering
   (`dependency-cycle-path.tsx`), or a layout overflow. Compare against the
   image on the issue to identify the exact bogus element.
2. Fix the rendering in the responsible component so the dependency display is
   correct for the reproduced case (and degrades cleanly for empty / single /
   cyclic / missing-name cases). Reuse design-reference primitives; keep it
   responsive.
3. Add a focused test (component/Playwright) covering the previously-bogus case
   so it can't regress.

## Open questions

- The issue has no text, only an image — the first implementation step is to open
  the screenshot on [#129](https://github.com/fclairamb/solidping/issues/129),
  pin down exactly which element is malformed and under what data shape, and note
  the reproduction in the spec before coding. If it can't be reproduced from the
  image, capture what was tried and ask for a concrete repro rather than guessing.
