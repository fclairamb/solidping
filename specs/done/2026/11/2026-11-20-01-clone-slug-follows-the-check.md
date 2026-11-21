---
model: sonnet
effort: medium
---

# A cloned check keeps a stacked `-copy-copy-…` slug that no longer matches it

GitHub Issue: https://github.com/fclairamb/solidping-business/issues/96

## Source

A user built several SSL checks by cloning one check, then cloning the clones.
The last clone's slug was `<source>-copy-copy-copy-copy-copy`. For each clone
they renamed it in the edit form, then went back to the detail page, opened the
inline slug editor and triple-clicked to select the long generated slug before
typing a new one (four rage clicks in the replay, all text selections). Nothing
errored; the flow makes the user fix by hand a slug that no longer matches the
check. The report asks for: no stacked suffixes, a way to pick the name/slug
when cloning, the slug following a rename, and the inline editor selecting its
content.

## Problem

- `server/internal/handlers/checks/service.go:5399` (`cloneResolveSlug`): with
  no slug in the request the clone slug is `sourceSlug + "-copy"`. Cloning a
  clone appends again, so the suffixes stack. `ensureUniqueSlug`
  (`service.go:3059`) already numbers collisions, so the stacking buys nothing.
- The clone buttons (`checks.$checkUid.index.tsx:768`, `checks.$checkUid.edit.tsx`)
  call `useCloneCheck` with no body and then navigate to the new check's edit
  page. `CloneCheckRequest` (`service.go:5216`) already accepts `name` and
  `slug`; the dashboard never sends them.
- The edit page has no slug field (`checks.$checkUid.edit.tsx`, the form
  sections under `components/checks/form/`), and renaming leaves the slug as it
  was. The slug can only be changed in the detail header's inline editor
  (`checks.$checkUid.index.tsx:1067-1101`), which focuses the input without
  selecting it (`startEditingSlug`, `:719-723`).

The report is right on all four points. Its option "open the edit form of the
new check right away" already happens; the gap is that the edit form cannot
change the slug.

## Why a spec

- A product/UX decision the reporter did not settle: a clone dialog vs. a slug
  field on the edit form vs. re-deriving the slug from the name on rename.
- The full change spans the backend clone path, the edit form and the detail
  header (over ~4 source files).

## Proposal

1. **Backend, no stacking.** In `cloneResolveSlug`, strip a trailing
   `-copy` or `-copy-<n>` from the source slug before appending `-copy`, then
   let `ensureUniqueSlug` number it: cloning `acme` gives `acme-copy`, cloning
   `acme-copy` gives `acme-copy-2`, cloning `acme-copy-2` gives `acme-copy-3`
   (or the next free number). Same rule for the clone's default name if it
   stacks a "(copy)" suffix (check `cloneResolveName` or its equivalent). Go
   table test in the checks handler package.
2. **Inline slug editor.** `startEditingSlug` calls `select()` after `focus()`
   so one click replaces the slug. Playwright: open the editor, type, the old
   slug is gone.
3. **Slug on the edit form** (per the open question below): a slug input in the
   general section of `checks.$checkUid.edit.tsx`, sent in the PATCH, with the
   same validation and error display as the inline editor. While the slug is
   still the auto-generated clone slug (matches `<derived>-copy(-n)?` of the
   source), renaming the check offers to update the slug from the new name
   (pre-filled, not silent). `data-testid` on the field and the offer.
4. Docs: the check-clone behaviour in `web/docs/` if it describes the slug.

## Open questions

1. **Where does the user choose the clone's slug?** Recommended: a slug field
   on the edit form (proposal 3), since clone already lands there; it also
   fixes renaming any check, not only clones. Trade-off: one more field on a
   long form. Alternative: a small clone dialog asking name + slug before
   creating; it adds a modal step to every clone (and the repo rule says
   editing goes to a dedicated route, not a dialog).
2. **Rename re-derives the slug?** Recommended: only offer it (a one-click
   "Update slug to `acme-new`" hint) while the slug is still auto-generated;
   never change a hand-set slug, since slugs appear in URLs, badges and the
   API. Trade-off: one extra click.

## Resolved open questions

1. Add a slug field on the edit form (proposal 3); no clone dialog. (autopilot: recommended answer)
2. On rename, never change the slug automatically: offer a one-click "Update slug to `<derived>`" hint only while the slug is still the auto-generated one; never touch a hand-set slug. (autopilot: recommended answer)

## Closing the issue

This spec closes fclairamb/solidping-business#96. The implementing PR body
**must** carry a `Closes fclairamb/solidping-business#96` line. When the spec is
archived to `specs/done/`, verify the issue is closed and, if the merge did not
close it, close it by hand:

    gh issue close 96 --repo fclairamb/solidping-business --comment "Implemented by <PR or commit>; spec: <archived spec path>"
