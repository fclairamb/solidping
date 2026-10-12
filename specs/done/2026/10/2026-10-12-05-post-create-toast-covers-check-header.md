---
model: sonnet
effort: medium
---

# The post-create publication toast covers the check detail header actions

## Problem
After a check is created, `web/dash0/src/routes/orgs/$org/checks.new.tsx:257-285` fires a fire-and-forget `toast.info` ("Not on any status page yet." with a "Publish it" action, or "Published on X, in the Y section") once `fetchCheckPublications` resolves. That happens after the `navigate()` to the check detail page, so the toast lands on the detail page.

The app mounts `<Toaster position="top-right" />` (`web/dash0/src/routes/__root.tsx:32`, component in `web/dash0/src/components/ui/sonner.tsx`). In the top-right corner the toasts sit over the action buttons (Run now / Disable / Edit) of `data-testid="check-detail-header"` (`web/dash0/src/routes/orgs/$org/checks.$checkUid.index.tsx:1191`).

A user who goes for Edit hovers the toast. Sonner pauses auto-dismiss on hover, so the toast stays while the pointer is there and Edit cannot be clicked.

Found in CI run 38108234825: Playwright "TCP check form round-trips an escaped payload and a regex expectation" timed out clicking Edit, with two toasts covering the button. The workaround is test-side only (`makeToastsClickThrough`, `web/dash0/e2e/fixtures.ts:140`, on branch `chore/e2e-drop-networkidle`). Real users still hit the problem.

## Proposal
1. Drop the publication toast and show the hint inline on the detail page, in the page body below the header and never over it.
   - Reuse the existing publish entry point on the detail page (`publish-status-page-link`, `checks.$checkUid.index.tsx:~1449`, and `PublishOnStatusPageDialog`) instead of a second mechanism.
   - Remove the `fetchCheckPublications(...).then(toast...)` block in `checks.new.tsx:257-285` and the now-unused import.
   - Keep the `?publish=true` deep link working (`checks.$checkUid.index.tsx:203,240,710`).
2. Keep `toast.success(t("toast.created"))` as is, but make sure toasts never sit over the header: set `offset` on the `Toaster` (`__root.tsx:32` or `sonner.tsx`) so the stack starts below the page header on desktop, and check it on a 375px viewport.
3. Read the design reference page (`http://localhost:4000/d/orgs/default/design-reference`, source `web/dash0/src/routes/orgs/$org/design-reference.tsx`) first and reuse its alert/callout primitive for the inline hint. Add the pattern there if it is missing.
4. Add a `data-testid` on the inline hint (`check-publication-hint`) and on its action.
5. Translations: move/rename the `publish.postCreate*` keys (`web/dash0/src/locales/en/checks.json:1368-1370`) as needed and keep en/fr/de/es in sync.
6. Remove nothing from `makeToastsClickThrough` in this change. Decide in a follow-up whether the fixture workaround can go.

## Tests
- `web/dash0/e2e/checks.spec.ts`: after creating a check, the Edit button in `check-detail-header` is clickable with no workaround, even with the pointer resting where the toast used to appear.
- Same spec: a freshly created check not on any status page shows `check-publication-hint` with a working "Publish it" action that opens the publish dialog. A check that is on a page shows the page and section name instead (negative case: no hint "not on any status page").
- Unit test for `check-publication` (`web/dash0/src/lib/check-publication.test.ts`) stays green. Add a case if the lookup moves.
- Mobile: a 375px viewport check that the hint wraps and nothing overflows horizontally.

## To verify
- Whether the detail page already displays the publication state (what `publish-status-page-link` at `checks.$checkUid.index.tsx:~1449` shows). If it does, the new hint may be redundant and only the "just created" moment needs a nudge.
- Which toasts besides this one can land on the header after navigation (the demo-session `checkExpires` toast, `checks.new.tsx:~241`), so the `Toaster` offset covers them too.

## Resolved open questions

**Decision (user, 2026-10-12): adopt the recommended answer of every question below. Implement exactly those recommendations.**
- Inline hint on the detail page, or only move/offset the toaster? Recommended: inline hint plus a `Toaster` offset. The hint is the only toast that arrives late and asks for an action, and an inline element does not disappear before the user reads it.
