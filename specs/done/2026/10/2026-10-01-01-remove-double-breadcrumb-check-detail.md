---
model: sonnet
effort: low
---

# Check detail page shows the breadcrumb twice

## Problem
`/d/orgs/<org>/checks/<checkUid>` renders two breadcrumbs: the global one in the top bar ("Checks > <check name>", built in `web/dash0/src/routes/orgs/$org.tsx:397-460`) and a second in-page one at the top of the content ("< Checks > <check name>", `web/dash0/src/routes/orgs/$org/checks.$checkUid.index.tsx:1169-1186`). Both show the same trail. Seen on a TLS cert check (screenshot in the thread).

The check detail page is the only route that renders an in-page `<Breadcrumb>` (the other user is the design reference page).

## Proposal
1. Remove the in-page `<Breadcrumb>` block (`checks.$checkUid.index.tsx:1169-1186`) and the imports that become unused (`Breadcrumb*`, `breadcrumbLinkClassName` at lines 109-114, `ArrowLeft` at line 11 if not used elsewhere in the file). Keep the `check-detail-header` wrapper and the title row.
2. Drop the `checks:detail.breadcrumb` and `checks:detail.backToChecks` keys from `web/dash0/src/locales/{en,fr,es,de}/checks.json` if nothing else references them.
3. Keep the top-bar breadcrumb as is. It already links "Checks" back to the list.
4. Leave `components/ui/breadcrumb.tsx` and the design reference page untouched (the primitive is still documented there).

## Tests
- `web/dash0/e2e/` (new case in the existing check detail spec): open a check detail page, assert exactly one breadcrumb nav for the page (`getByRole("navigation", { name: /breadcrumb/i })` count is 1) and that `data-testid="check-detail-back"` no longer exists.
- Same spec, positive control: the top-bar breadcrumb still has a "Checks" link that navigates to `/orgs/<org>/checks`.
- `bun run test:unit` and `bun run build` (unused-key / unused-import checks) stay green.

## To verify
- Whether the top-bar breadcrumb hides on mobile. If it does, removing the in-page one leaves no back link on small screens, so keep it there (hidden from `sm:` up) instead of deleting it.
- Nothing else uses the two locale keys (`grep -rn "detail.breadcrumb\|backToChecks" web/dash0/src`).
